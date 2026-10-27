// Package router — client.go is the HTTP executor that implements the
// Intelligent Retry policy (invariant I3, decision tree D1). It makes
// actual HTTP calls to AI providers, applies backoff on 429, refuses
// blind retry on 4xx, and walks the fallback chain on 5xx/network
// failures. It integrates with tracer.go for X-Ray observability (I2).
//
// This file does NOT know about providers by name — the caller
// (typically the engine) is responsible for resolving provider URLs
// via discovery.go and passing them in CallRequest.URLs. This keeps
// the client generic and testable.
package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
)

// ErrAllProvidersFailed is returned by Call when every URL in the
// fallback chain has been exhausted without success. Callers can use
// errors.Is(err, ErrAllProvidersFailed) to detect this condition and
// pause the pipeline (decision tree D1: "status=blocked").
var ErrAllProvidersFailed = errors.New("router: all providers in fallback chain failed")

// ClientError represents a 4xx HTTP response. This error type signals
// that the request failed due to client-side issues (bad payload, auth,
// etc.) and that retrying with a different provider is futile. The Call
// method uses errors.As to detect this and stops the fallback chain
// immediately (decision tree D1: "4xx — fail task with explicit error").
type ClientError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("router: client error %d on %s: %s", e.StatusCode, e.URL, e.Body)
}

// Retry budget constants. These implement decision tree D1 from
// docs/DECISIONS.md. Changing these values changes the system's
// resilience vs. latency tradeoff — do so deliberately.
const (
	maxRetries5xx     = 3
	maxRetries429     = 5
	maxRetriesNet     = 3
	maxRetriesTimeout = 1 // D1: "1st time: retry 1x with same provider"
	baseDelay         = 2 * time.Second
	maxDelay          = 30 * time.Second
)

// CallRequest describes an outbound HTTP call to an AI provider.
// URLs is the fallback chain: first element is the primary provider,
// subsequent elements are tried in order if the primary fails per D1.
// AuthHeader is set verbatim on the Authorization header; the tracer
// (I2) masks it in logs so secrets never leak (G2.1).
type CallRequest struct {
	// PipelineID is propagated to the tracer for log correlation.
	PipelineID string
	// Method is the HTTP method (GET, POST, ...). Required.
	Method string
	// URLs is the fallback chain. At least one URL is required.
	URLs []string
	// Body is the request payload (typically JSON). May be nil.
	Body []byte
	// AuthHeader is the value for the Authorization header. May be empty.
	AuthHeader string
	// Timeout is the per-attempt timeout. Zero means no timeout.
	Timeout time.Duration
}

// CallResponse holds the result of a successful HTTP call.
// ProviderUsed is the URL from the fallback chain that succeeded,
// so the caller can record which provider served the request (useful
// for metrics and for the dashboard).
type CallResponse struct {
	StatusCode   int
	Body         []byte
	LatencyMs    int64
	ProviderUsed string
}

// httpResult is the internal shape of a single HTTP attempt, before
// retry/fallback logic is applied. Body is already read and closed.
type httpResult struct {
	StatusCode int
	Body       []byte
}

// Client executes HTTP calls with intelligent retry per D1. It is
// safe for concurrent use. Build one per process via NewClient.
type Client struct {
	tracer    *observability.Tracer
	rand      *rand.Rand
	backoffFn func(attempt int) time.Duration // nil = use default; tests override
}

// NewClient builds a Client wired to the project's Tracer. The tracer
// may be nil (no observability), but this is strongly discouraged —
// without a tracer the system violates invariant I2.
func NewClient(tracer *observability.Tracer) *Client {
	return &Client{
		tracer: tracer,
		rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Call executes the HTTP request following the D1 decision tree.
// It walks req.URLs in order; for each URL it applies the per-URL
// retry policy (backoff on 429, fail-fast on 4xx, retry on 5xx).
// Returns ErrAllProvidersFailed (wrapped) when every URL has been
// exhausted. Context cancellation is honored between attempts.
func (c *Client) Call(ctx context.Context, req CallRequest) (*CallResponse, error) {
	if len(req.URLs) == 0 {
		return nil, errors.New("router: CallRequest.URLs must not be empty")
	}
	if req.Method == "" {
		return nil, errors.New("router: CallRequest.Method must not be empty")
	}
	if c == nil {
		return nil, errors.New("router: nil Client")
	}

	var lastErr error
	for _, url := range req.URLs {
		// Check context before each URL attempt.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("router: %w", err)
		}

		resp, err := c.callURL(ctx, req, url)
		if err == nil {
			return resp, nil
		}

		// 4xx errors are terminal — do NOT try the next provider.
		// The problem is in the client's payload/auth, not the server.
		var clientErr *ClientError
		if errors.As(err, &clientErr) {
			return nil, err
		}

		lastErr = err
		// Per-URL retry exhausted (5xx, timeout, network) — try next URL.
	}

	return nil, fmt.Errorf("%w: last error: %v", ErrAllProvidersFailed, lastErr)
}

// callURL applies the per-URL retry policy of D1 to a single URL.
// It loops until: success, 4xx (fail-fast), retry budget exhausted,
// or context canceled.
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
				// D1: "Timeout — 1st time: retry 1x with same provider"
				if attemptTimeout < maxRetriesTimeout {
					attemptTimeout++
					continue // immediate retry, no backoff
				}
				return nil, fmt.Errorf("router: timeout on %s after %d attempts: %w",
					url, attemptTimeout+1, err)
			}
			// Generic network error: retry up to maxRetriesNet with backoff.
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

		// --- Got HTTP response: dispatch by status code ---
		switch {
		case result.StatusCode >= 200 && result.StatusCode < 300:
			// Success.
			return &CallResponse{
				StatusCode:   result.StatusCode,
				Body:         result.Body,
				LatencyMs:    latency.Milliseconds(),
				ProviderUsed: url,
			}, nil

		case result.StatusCode == http.StatusTooManyRequests:
			// D1: "429 — exponential backoff, then fallback"
			if attempt429 >= maxRetries429 {
				return nil, fmt.Errorf("router: 429 rate limit on %s exhausted after %d attempts: body=%s",
					url, attempt429+1, truncate(result.Body, 200))
			}
			attempt429++
			if !c.sleep(ctx, c.backoff(attempt429)) {
				return nil, fmt.Errorf("router: %w", ctx.Err())
			}
			continue

		case result.StatusCode >= 400 && result.StatusCode < 500:
			// D1: "4xx — fail task with explicit error (no blind retry)"
			// This is the anti-ban clause. We NEVER retry 4xx, and we
			// return a ClientError so Call stops the fallback chain.
			return nil, &ClientError{
				StatusCode: result.StatusCode,
				URL:        url,
				Body:       truncate(result.Body, 500),
			}

		case result.StatusCode >= 500:
			// D1: "5xx — up to 3 retries with backoff; then next provider"
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
			// 1xx / 3xx: return as-is. Redirects are not followed
			// automatically — the caller decides what to do.
			return &CallResponse{
				StatusCode:   result.StatusCode,
				Body:         result.Body,
				LatencyMs:    latency.Milliseconds(),
				ProviderUsed: url,
			}, nil
		}
	}
}

// doOnce performs a single HTTP attempt. It builds the request, wraps
// the transport with the tracer (if present), and reads the response
// body into memory. The response body is closed before return.
func (c *Client) doOnce(ctx context.Context, req CallRequest, url string) (*httpResult, time.Duration, error) {
	// Apply per-attempt timeout via context.
	reqCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	var bodyReader io.Reader
	if req.Body != nil {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, req.Method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}

	if req.AuthHeader != "" {
		httpReq.Header.Set("Authorization", req.AuthHeader)
	}
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Accept", "application/json")

	// Wrap transport with tracer for X-Ray observability (I2).
	var transport http.RoundTripper = http.DefaultTransport
	if c.tracer != nil {
		transport = c.tracer.WrapTransport(transport, url)
	}

	client := &http.Client{Transport: transport}

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

	return &httpResult{
		StatusCode: resp.StatusCode,
		Body:       body,
	}, latency, nil
}

// backoff returns the delay before the next retry attempt. The delay
// is exponential (baseDelay * 2^(attempt-1)) capped at maxDelay, plus
// a random jitter in [0, baseDelay/2) to prevent thundering-herd when
// multiple clients retry simultaneously (G1.2: respect rate limits).
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

// sleep blocks for duration d or until ctx is canceled. Returns true
// if the full sleep completed, false if ctx was canceled (caller
// should propagate ctx.Err()).
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

// isTimeout returns true if err represents a request timeout. It
// detects both context.DeadlineExceeded and net.Error.Timeout().
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	type timeoutErr interface {
		Timeout() bool
	}
	var te timeoutErr
	if errors.As(err, &te) {
		return te.Timeout()
	}
	return false
}

// truncate returns at most max bytes of b as a string, appending "..."
// if truncated. Used to keep error messages readable without bloating
// them with huge response bodies.
func truncate(b []byte, max int) string {
	if len(b) == 0 {
		return "<empty>"
	}
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
