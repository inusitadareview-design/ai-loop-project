// tracer.go is the X-Ray observability layer for every outbound HTTP
// call (invariant I2, ARCHITECTURE.md 4.3). It wraps an http.RoundTripper
// and logs, in structured JSON, exactly what is sent to the provider
// and what is received back — solving the "blind logs" pain point of
// SwarmClaw.
//
// What is logged on every request:
//   - http_request event: method, url, sanitized headers, token estimate,
//     provider, trace_id, pipeline_id.
//   - http_response event (on success): status, latency_ms, raw body
//     (truncated to maxLogBodyBytes), provider, trace_id, pipeline_id.
//   - http_error event (on network failure): error body, latency_ms,
//     provider, trace_id, pipeline_id.
//
// Guardrails enforced by this file:
//   - I2   — X-Ray Observability: structured logs of request + response.
//   - G2.1 — Secret masking: Authorization, X-Api-Key and Cookie
//     headers are replaced with "***REDACTED***" before logging.
//     Any remaining secret-shaped strings in the body are caught
//     by the logger's maskingWriter downstream.
//   - G5.1 — Errors are first-class: network errors are logged with
//     full context and returned to the caller; they are never
//     swallowed.
package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// maxLogBodyBytes caps how much of a response body is recorded in the
// log. Bodies larger than this are truncated in the log (with the
// `response_body_truncated` flag set) but the full body is always
// delivered to the caller — truncation is for log hygiene, not for
// data loss. 4 KiB is enough to capture most error payloads and small
// JSON responses without bloating the log file.
const maxLogBodyBytes = 4096

// pipelineIDKey is the context key used to propagate the current
// pipeline id through request-scoped code. Using the same contextKey
// type as traceIDKey prevents collisions with keys defined elsewhere.
const pipelineIDContextKey contextKey = "pipeline_id"

// sensitiveHeaderKeys lists the HTTP header names (lowercased) whose
// values are ALWAYS replaced with "***REDACTED***" in logs. Any header
// not in this list is logged as-is (secret-shaped values in non-listed
// headers are still caught by the maskingWriter at the sink layer,
// defense in depth — G2.1).
var sensitiveHeaderKeys = map[string]bool{
	"authorization":             true,
	"x-api-key":                 true,
	"proxy-authorization":       true,
	"cookie":                    true,
	"set-cookie":                true,
	"x-forwarded-authorization": true,
}

// Tracer is the factory for tracing HTTP transports. Build one per
// process (sharing the project's Logger) and use WrapTransport to
// instrument each HTTP client that talks to an AI provider.
type Tracer struct {
	logger *Logger
}

// NewTracer builds a Tracer wired to the project's Logger. The logger
// must not be nil — a nil logger is a configuration error, not a
// runtime failure.
func NewTracer(logger *Logger) *Tracer {
	return &Tracer{logger: logger}
}

// TracingTransport is an http.RoundTripper that logs every request and
// response. It never modifies the request/response semantics seen by
// the caller — it only observes. Body is buffered so both the logger
// and the caller can read it.
type TracingTransport struct {
	base     http.RoundTripper
	provider string
	logger   *Logger
}

// WrapTransport returns an http.RoundTripper that delegates to `base`
// but logs every request/response pair. If `base` is nil,
// http.DefaultTransport is used. `provider` is a free-form identifier
// (e.g., "omniroute", "openrouter") that appears in every log line for
// that transport, making it easy to filter logs by provider.
func (t *Tracer) WrapTransport(base http.RoundTripper, provider string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if t == nil || t.logger == nil {
		// Defensive: a mis-built Tracer should not crash the caller.
		// Return the base transport unchanged — no tracing, no panic.
		return base
	}
	return &TracingTransport{
		base:     base,
		provider: provider,
		logger:   t.logger,
	}
}

// RoundTrip implements http.RoundTripper. It:
//  1. Buffers the request body (if any) so both logger and callee read it.
//  2. Logs the http_request event with sanitized headers and token estimate.
//  3. Delegates to the base transport.
//  4. On success: buffers the response body, logs http_response, restores
//     the body for the caller, and returns the response.
//  5. On network error: logs http_error and returns the error.
//
// RoundTrip never panics and never swallows errors from the base
// transport. Context cancellation is honored by the base transport.
func (tr *TracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("tracer: nil request")
	}

	// 1. Buffer request body so logger and callee can both read it.
	var reqBodyBytes []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("tracer: read request body: %w", err)
		}
		reqBodyBytes = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}

	// 2. Extract context fields BEFORE any I/O (context may be canceled).
	ctx := req.Context()
	traceID := traceIDFromContext(ctx)
	pipelineID := pipelineIDFromContext(ctx)

	// 3. Log the request.
	tr.logRequest(req, reqBodyBytes, traceID, pipelineID)

	// 4. Execute the underlying transport.
	start := time.Now()
	resp, err := tr.base.RoundTrip(req)
	latency := time.Since(start)

	if err != nil {
		tr.logError(req, err, latency, traceID, pipelineID)
		return nil, err
	}

	// 5. Buffer the response body so logger and caller both read it.
	var respBodyBytes []byte
	if resp.Body != nil {
		b, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			// We cannot silently drop the read error — surface it as a
			// placeholder body so the log records what happened, but
			// the caller still gets the response with a failed body.
			respBodyBytes = []byte(fmt.Sprintf("[tracer: failed to read response body: %v]", readErr))
		} else {
			respBodyBytes = b
		}
		resp.Body = io.NopCloser(bytes.NewReader(b))
	}

	tr.logResponse(req, resp, respBodyBytes, latency, traceID, pipelineID)
	return resp, nil
}

// logRequest emits the http_request event. Headers are sanitized; the
// request body is only used to estimate token count (the body itself
// is never logged — it may contain user data).
func (tr *TracingTransport) logRequest(
	req *http.Request,
	reqBody []byte,
	traceID, pipelineID string,
) {
	l := tr.logger.Root()
	ev := l.Info().
		Str("event", "http_request").
		Str("provider", tr.provider).
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Int("request_tokens_est", estimateTokens(string(reqBody))).
		Int64("request_bytes", int64(len(reqBody))).
		Interface("headers", sanitizeHeaders(req.Header))

	if traceID != "" {
		ev = ev.Str("trace_id", traceID)
	}
	if pipelineID != "" {
		ev = ev.Str("pipeline_id", pipelineID)
	}

	ev.Msg("outgoing HTTP request")
}

// logResponse emits the http_response event. Body is truncated to
// maxLogBodyBytes for the log; the truncation flag is always set so
// consumers can tell when the logged body is partial.
func (tr *TracingTransport) logResponse(
	req *http.Request,
	resp *http.Response,
	body []byte,
	latency time.Duration,
	traceID, pipelineID string,
) {
	logBody := body
	truncated := false
	if len(body) > maxLogBodyBytes {
		logBody = body[:maxLogBodyBytes]
		truncated = true
	}

	l := tr.logger.Root()
	ev := l.Info().
		Str("event", "http_response").
		Str("provider", tr.provider).
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Int("response_status", resp.StatusCode).
		Int64("response_latency_ms", latency.Milliseconds()).
		Int64("response_bytes", int64(len(body))).
		Bool("response_body_truncated", truncated).
		Str("response_body", string(logBody))

	if traceID != "" {
		ev = ev.Str("trace_id", traceID)
	}
	if pipelineID != "" {
		ev = ev.Str("pipeline_id", pipelineID)
	}

	ev.Msg("HTTP response received")
}

// logError emits the http_error event for network-level failures
// (DNS, TLS, timeout, connection refused). These are NOT HTTP 4xx/5xx
// responses — those arrive as normal responses with a status code and
// are logged by logResponse.
func (tr *TracingTransport) logError(
	req *http.Request,
	err error,
	latency time.Duration,
	traceID, pipelineID string,
) {
	l := tr.logger.Root()
	ev := l.Error().
		Str("event", "http_error").
		Str("provider", tr.provider).
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Int64("response_latency_ms", latency.Milliseconds()).
		Str("error_body", err.Error())

	if traceID != "" {
		ev = ev.Str("trace_id", traceID)
	}
	if pipelineID != "" {
		ev = ev.Str("pipeline_id", pipelineID)
	}

	ev.Msg("HTTP request failed")
}

// sanitizeHeaders returns a copy of the headers with sensitive keys
// replaced by "***REDACTED***". Non-sensitive headers are joined into
// a single comma-separated string (matching HTTP semantics for
// multi-valued headers).
func sanitizeHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if sensitiveHeaderKeys[strings.ToLower(k)] {
			out[k] = redactedPlaceholder
		} else {
			out[k] = strings.Join(v, ", ")
		}
	}
	return out
}

// estimateTokens applies the same ~4 chars/token heuristic used by the
// Rust crates (token-budget, payload-optimizer, prompt-builder) so the
// Go layer agrees with the hot path across the FFI boundary. Counts
// Unicode code points (runes), not bytes, so multibyte characters
// behave consistently with the Rust side.
func estimateTokens(s string) int {
	return (utf8.RuneCountInString(s) + 3) / 4
}

// WithPipelineID returns a context carrying the given pipeline id,
// which the Tracer propagates into every log line for requests made
// under that context. Use alongside WithTraceID for full correlation.
func WithPipelineID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pipelineIDContextKey, id)
}

// traceIDFromContext extracts the trace_id set by WithTraceID. Returns
// "" if the context is nil or carries no trace id.
func traceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(traceIDKey).(string); ok {
		return id
	}
	return ""
}

// pipelineIDFromContext extracts the pipeline id set by WithPipelineID.
// Returns "" if the context is nil or carries no pipeline id.
func pipelineIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(pipelineIDContextKey).(string); ok {
		return id
	}
	return ""
}
