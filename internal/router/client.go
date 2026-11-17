// Package router — client.go is the HTTP executor that implements the
// Intelligent Retry policy (invariant I3, decision tree D1).
//
// This file does NOT know about providers by name — the caller resolves
// URLs and passes them in CallRequest. Keeps the client generic/testable.
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
)

// ErrAllProvidersFailed is returned when every URL in the chain failed.
var ErrAllProvidersFailed = errors.New("router: all providers in fallback chain failed")

// ClientError represents a 4xx caused by the CLIENT (bad payload, invalid
// auth, unknown model). Retrying elsewhere is futile — Call aborts (D1).
type ClientError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("router: client error %d on %s: %s", e.StatusCode, e.URL, e.Body)
}

// SoftError represents an HTTP 200 whose body carries a provider-side
// failure (e.g. Pollinations "reached its budget"). Quota does not recover
// by retrying the same provider, so Call skips to the next one immediately.
type SoftError struct {
	URL  string
	Body string
}

func (e *SoftError) Error() string {
	return fmt.Sprintf("router: soft error (HTTP 200 with error body) on %s: %s",
		e.URL, truncate([]byte(e.Body), 200))
}

// Retry budget constants (decision tree D1). Change deliberately.
const (
	maxRetries5xx     = 3
	maxRetries429     = 5
	maxRetriesNet     = 3
	maxRetriesTimeout = 1
	baseDelay         = 2 * time.Second
	maxDelay          = 30 * time.Second
)

// CallRequest describes an outbound HTTP call to an AI provider.
type CallRequest struct {
	PipelineID string
	Method     string
	URLs       []string
	Body       []byte
	AuthHeader string

	// AuthHeaders maps each URL to its OWN Authorization header.
	// REQUIRED: provider A's key must never be sent to provider B.
	AuthHeaders map[string]string

	// Bodies maps each URL to its OWN payload. REQUIRED because model IDs
	// AND payload shapes are provider-specific (G1.2).
	Bodies map[string][]byte

	// Timeout is the per-attempt timeout, clamped to the remaining parent
	// deadline so an attempt never outlives the request envelope.
	Timeout time.Duration
}

// CallResponse holds the result of a successful HTTP call.
type CallResponse struct {
	StatusCode   int
	Body         []byte
	LatencyMs    int64
	ProviderUsed string
}

type httpResult struct {
	StatusCode int
	Body       []byte
}

// Client executes HTTP calls with intelligent retry per D1.
// Safe for concurrent use. The underlying transport is shared so
// connections are pooled and reused across attempts and providers.
type Client struct {
	tracer    *observability.Tracer
	rand      *rand.Rand
	backoffFn func(attempt int) time.Duration

	baseTransport *http.Transport
	transports    sync.Map // url -> tracer-wrapped RoundTripper (cached)
}

// NewClient builds a Client wired to the project's Tracer.
func NewClient(tracer *observability.Tracer) *Client {
	return &Client{
		tracer: tracer,
		rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
		baseTransport: &http.Transport{
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// transportFor returns a cached, tracer-wrapped transport for the URL.
// The connection pool lives in baseTransport, so wrapping per URL does
// not defeat pooling. Falls back to http.DefaultTransport when the Client
// was built as a zero value (unit tests of pure helpers).
func (c *Client) transportFor(url string) http.RoundTripper {
	if v, ok := c.transports.Load(url); ok {
		return v.(http.RoundTripper)
	}
	var base http.RoundTripper = c.baseTransport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped := base
	if c.tracer != nil {
		wrapped = c.tracer.WrapTransport(base, url)
	}
	actual, _ := c.transports.LoadOrStore(url, wrapped)
	return actual.(http.RoundTripper)
}

// Call walks req.URLs in order applying the per-URL retry policy.
// ClientError (4xx) aborts the chain. SoftError skips immediately.
func (c *Client) Call(ctx context.Context, req CallRequest) (*CallResponse, error) {
	if c == nil {
		return nil, errors.New("router: nil Client")
	}
	if len(req.URLs) == 0 {
		return nil, errors.New("router: CallRequest.URLs must not be empty")
	}
	if req.Method == "" {
		return nil, errors.New("router: CallRequest.Method must not be empty")
	}

	var lastErr error
	for _, url := range req.URLs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("router: %w", err)
		}

		resp, err := c.callURL(ctx, req, url)
		if err == nil {
			return resp, nil
		}

		// 4xx: client-side problem — do NOT try the next provider.
		var clientErr *ClientError
		if errors.As(err, &clientErr) {
			return nil, err
		}

		// SoftError: provider quota — skip immediately, no backoff.
		var softErr *SoftError
		if errors.As(err, &softErr) {
			lastErr = err
			continue
		}

		lastErr = err
	}

	return nil, fmt.Errorf("%w: last error: %v", ErrAllProvidersFailed, lastErr)
}

// callURL applies the per-URL retry policy of D1 to a single URL.
func (c *Client) callURL(ctx context.Context, req CallRequest, url string) (*CallResponse, error) {
	var (
		attempt429     int
		attempt5xx     int
		attemptNet     int
		attemptTimeout int
	)

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("router: %w", err)
		}

		result, latency, err := c.doOnce(ctx, req, url)

		// --- Network / timeout errors ---
		if err != nil {
			if isTimeout(err) {
				if attemptTimeout < maxRetriesTimeout {
					attemptTimeout++
					continue
				}
				return nil, fmt.Errorf("router: timeout on %s after %d attempts: %w",
					url, attemptTimeout+1, err)
			}
			if attemptNet < maxRetriesNet {
				attemptNet++
				if !c.sleep(ctx, c.backoff(attemptNet)) {
					return nil, fmt.Errorf("router: %w", ctx.Err())
				}
				continue
			}
			return nil, fmt.Errorf("router: network error on %s after %d attempts: %w",
				url, attemptNet+1, err)
		}

		switch {
		case result.StatusCode >= 200 && result.StatusCode < 300:
			// HTTP 200 can still carry a provider failure in the body.
			// Quota does not recover on retry → skip provider now.
			if isSoftError(result.Body) {
				return nil, &SoftError{URL: url, Body: string(result.Body)}
			}
			return &CallResponse{
				StatusCode:   result.StatusCode,
				Body:         result.Body,
				LatencyMs:    latency.Milliseconds(),
				ProviderUsed: url,
			}, nil

		case result.StatusCode == http.StatusTooManyRequests:
			if attempt429 >= maxRetries429 {
				return nil, fmt.Errorf("router: 429 rate limit on %s exhausted after %d attempts: body=%s",
					url, attempt429+1, truncate(result.Body, 200))
			}
			attempt429++
			if !c.sleep(ctx, c.backoff(attempt429)) {
				return nil, fmt.Errorf("router: %w", ctx.Err())
			}
			continue

		case result.StatusCode == http.StatusPaymentRequired,
			result.StatusCode == http.StatusRequestTimeout:
			// 402 = quota/budget exhausted (KEY_BUDGET_EXHAUSTED).
			// 408 = server-side request timeout.
			// Both are PROVIDER failures, not client payload errors:
			// retry with backoff, then fall back to the next provider.
			if attempt5xx >= maxRetries5xx {
				return nil, fmt.Errorf("router: quota/timeout %d on %s after %d attempts: body=%s",
					result.StatusCode, url, attempt5xx+1, truncate(result.Body, 200))
			}
			attempt5xx++
			if !c.sleep(ctx, c.backoff(attempt5xx)) {
				return nil, fmt.Errorf("router: %w", ctx.Err())
			}
			continue

		case result.StatusCode >= 400 && result.StatusCode < 500:
			// Anti-ban clause: NEVER retry 4xx, abort the whole chain.
			return nil, &ClientError{
				StatusCode: result.StatusCode,
				URL:        url,
				Body:       truncate(result.Body, 500),
			}

		case result.StatusCode >= 500:
			if attempt5xx >= maxRetries5xx {
				return nil, fmt.Errorf("router: server error %d on %s after %d attempts: body=%s",
					result.StatusCode, url, attempt5xx+1, truncate(result.Body, 200))
			}
			attempt5xx++
			if !c.sleep(ctx, c.backoff(attempt5xx)) {
				return nil, fmt.Errorf("router: %w", ctx.Err())
			}
			continue

		default:
			return &CallResponse{
				StatusCode:   result.StatusCode,
				Body:         result.Body,
				LatencyMs:    latency.Milliseconds(),
				ProviderUsed: url,
			}, nil
		}
	}
}

// doOnce performs a single HTTP attempt with per-provider auth/payload.
func (c *Client) doOnce(ctx context.Context, req CallRequest, url string) (*httpResult, time.Duration, error) {
	// Clamp the per-attempt timeout to the remaining parent deadline.
	// Without this, attempts × timeout can exceed the HTTP envelope
	// (chi 180s) and produce "context deadline exceeded".
	reqCtx := ctx
	if req.Timeout > 0 {
		timeout := req.Timeout
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining < timeout {
				timeout = remaining
			}
		}
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Per-provider payload: model IDs and shapes are provider-specific.
	payload := req.Body
	if b, ok := req.Bodies[url]; ok && len(b) > 0 {
		payload = b
	}

	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, req.Method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}

	// Per-provider auth: never reuse another provider's key (G2.1).
	auth := req.AuthHeader
	if h, ok := req.AuthHeaders[url]; ok && h != "" {
		auth = h
	}
	if auth != "" {
		httpReq.Header.Set("Authorization", auth)
	}
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: c.transportFor(url)}

	start := time.Now()
	resp, err := client.Do(httpReq)
	latency := time.Since(start)
	if err != nil {
		return nil, latency, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latency, fmt.Errorf("read response body: %w", err)
	}

	return &httpResult{StatusCode: resp.StatusCode, Body: body}, latency, nil
}

// backoff returns exponential delay capped at maxDelay, plus jitter.
func (c *Client) backoff(attempt int) time.Duration {
	if c.backoffFn != nil {
		return c.backoffFn(attempt)
	}
	if attempt < 1 {
		attempt = 1
	}
	exp := time.Duration(1 << uint(attempt-1))
	delay := baseDelay * exp
	if delay > maxDelay {
		delay = maxDelay
	}
	jitter := time.Duration(c.rand.Int63n(int64(baseDelay / 2)))
	return delay + jitter
}

// sleep blocks for d or until ctx is canceled.
func (c *Client) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// isTimeout detects context.DeadlineExceeded and net.Error.Timeout().
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	type timeoutErr interface{ Timeout() bool }
	var te timeoutErr
	if errors.As(err, &te) {
		return te.Timeout()
	}
	return false
}

// truncate returns at most max bytes of b, appending "..." if cut.
func truncate(b []byte, max int) string {
	if len(b) == 0 {
		return "<empty>"
	}
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}

// isSoftError detects HTTP 200 bodies carrying a provider failure.
// A response that produced output tokens is real content — even if the
// article text legitimately mentions "quota exceeded". This prevents
// false positives on generated articles about rate limits or pricing.
func isSoftError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var probe struct {
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &probe); err == nil && probe.Usage.CompletionTokens > 0 {
		return false
	}

	bodyStr := string(body)
	indicators := []string{
		"reached its budget",
		"quota exceeded",
		"rate limit exceeded",
		"insufficient funds",
		"payment required",
		"budget exceeded",
	}
	for _, ind := range indicators {
		if containsIgnoreCase(bodyStr, ind) {
			return true
		}
	}
	return false
}

func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
