package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testTracer returns a Tracer that writes structured JSON lines to buf.
// Each test gets its own buffer so logs do not leak across tests.
func testTracer(t *testing.T) (*Tracer, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	lg := newLoggerForTest(buf)
	return NewTracer(lg), buf
}

// parseLogLines splits the buffer into one map per JSON log line.
// Fails the test on any malformed line — structured logs must always
// be valid JSON.
func parseLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range bytes.Split(buf.Bytes(), []byte{'\n'}) {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("malformed log line: %v\nline: %s", err, raw)
		}
		out = append(out, m)
	}
	return out
}

// findEvent returns the first log line whose "event" field matches.
// Fails the test if none is found.
func findEvent(t *testing.T, lines []map[string]any, event string) map[string]any {
	t.Helper()
	for _, m := range lines {
		if m["event"] == event {
			return m
		}
	}
	t.Fatalf("event %q not found in log lines: %+v", event, lines)
	return nil
}

// --- Authorization masking (G2.1) --------------------------------------

func TestTracer_MasksAuthorizationHeader(t *testing.T) {
	tracer, buf := testTracer(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "test-provider")}

	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat", strings.NewReader(`{"prompt":"hello"}`))
	req.Header.Set("Authorization", "Bearer sk-proj-abc123def456ghi789jkl012mno345pqr678")
	req.Header.Set("X-Api-Key", "sk-legacy-abcdefghijklmnopqrstuvwxyz01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)
	reqLog := findEvent(t, lines, "http_request")

	headers, ok := reqLog["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers field missing or not a map: %+v", reqLog)
	}

	// Sensitive headers must be masked.
	if got, _ := headers["Authorization"].(string); got != redactedPlaceholder {
		t.Fatalf("Authorization = %q, want %q", got, redactedPlaceholder)
	}
	if got, _ := headers["X-Api-Key"].(string); got != redactedPlaceholder {
		t.Fatalf("X-Api-Key = %q, want %q", got, redactedPlaceholder)
	}

	// Non-sensitive headers must pass through verbatim.
	if got, _ := headers["Content-Type"].(string); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	// Defense in depth: even if a masking failure occurred, the raw
	// secret must not appear anywhere in the log.
	fullLog := buf.String()
	if strings.Contains(fullLog, "sk-proj-abc123") {
		t.Fatalf("secret leaked into log: %s", fullLog)
	}
	if strings.Contains(fullLog, "sk-legacy-abcdef") {
		t.Fatalf("legacy secret leaked into log: %s", fullLog)
	}
}

func TestTracer_MasksCookieHeader(t *testing.T) {
	tracer, buf := testTracer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Cookie", "session=secret-session-token-1234567890")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)
	headers := findEvent(t, lines, "http_request")["headers"].(map[string]any)
	if got, _ := headers["Cookie"].(string); got != redactedPlaceholder {
		t.Fatalf("Cookie = %q, want %q", got, redactedPlaceholder)
	}
}

// --- Request + Response pairing (I2) -----------------------------------

func TestTracer_LogsRequestAndResponseWithRequiredFields(t *testing.T) {
	tracer, buf := testTracer(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"answer":42}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "omniroute")}
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"prompt":"hello world"}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)

	// Request event.
	reqLog := findEvent(t, lines, "http_request")
	for _, required := range []string{"provider", "method", "url", "request_tokens_est", "headers"} {
		if _, ok := reqLog[required]; !ok {
			t.Errorf("http_request missing field %q: %+v", required, reqLog)
		}
	}
	if reqLog["provider"] != "omniroute" {
		t.Fatalf("provider = %v, want omniroute", reqLog["provider"])
	}

	// Response event.
	respLog := findEvent(t, lines, "http_response")
	for _, required := range []string{
		"provider", "method", "url", "response_status",
		"response_latency_ms", "response_body", "response_body_truncated",
	} {
		if _, ok := respLog[required]; !ok {
			t.Errorf("http_response missing field %q: %+v", required, respLog)
		}
	}
	if got, _ := respLog["response_status"].(float64); int(got) != 200 {
		t.Fatalf("response_status = %v, want 200", respLog["response_status"])
	}
	if got, _ := respLog["response_body"].(string); got != `{"answer":42}` {
		t.Fatalf("response_body = %q, want {\"answer\":42}", got)
	}
	if got, _ := respLog["response_latency_ms"].(float64); got < 0 {
		t.Fatalf("response_latency_ms = %v, want >= 0", got)
	}
}

func TestTracer_LogsHTTP500AsResponseNotError(t *testing.T) {
	tracer, buf := testTracer(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}
	req, _ := http.NewRequest("POST", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)

	// 5xx is an HTTP response, not a network error — log it as such.
	respLog := findEvent(t, lines, "http_response")
	if got, _ := respLog["response_status"].(float64); int(got) != 500 {
		t.Fatalf("response_status = %v, want 500", respLog["response_status"])
	}
	if got, _ := respLog["response_body"].(string); got != `{"error":"upstream down"}` {
		t.Fatalf("response_body = %q", got)
	}

	// There must be no http_error event for a successful HTTP exchange.
	for _, m := range lines {
		if m["event"] == "http_error" {
			t.Fatalf("unexpected http_error event for 5xx response: %+v", m)
		}
	}
}

// --- Network errors ----------------------------------------------------

func TestTracer_LogsNetworkError(t *testing.T) {
	tracer, buf := testTracer(t)

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "broken")}
	req, _ := http.NewRequest("GET", "http://this-host-does-not-exist-12345.invalid/x", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected network error, got nil")
	}

	lines := parseLogLines(t, buf)
	errLog := findEvent(t, lines, "http_error")
	for _, required := range []string{"provider", "method", "url", "response_latency_ms", "error_body"} {
		if _, ok := errLog[required]; !ok {
			t.Errorf("http_error missing field %q: %+v", required, errLog)
		}
	}
	if got, _ := errLog["provider"].(string); got != "broken" {
		t.Fatalf("provider = %q, want broken", got)
	}
	if got, _ := errLog["error_body"].(string); got == "" {
		t.Fatal("error_body is empty")
	}
}

// --- Token estimation --------------------------------------------------

func TestEstimateTokens_MatchesRustHeuristic(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("a", 40), 10},
		{strings.Repeat("a", 41), 11},
		// Unicode: count runes, not bytes.
		{"héllo→", 2}, // 6 runes -> ceil(6/4) = 2
		{"🚀🚀🚀🚀", 1},   // 4 runes -> ceil(4/4) = 1
	}
	for _, tt := range tests {
		got := estimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestTracer_LogsTokenEstimateForPayload(t *testing.T) {
	tracer, buf := testTracer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}
	// 40 chars = 10 tokens under the heuristic.
	body := strings.Repeat("a", 40)
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(body))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)
	reqLog := findEvent(t, lines, "http_request")
	if got, _ := reqLog["request_tokens_est"].(float64); int(got) != 10 {
		t.Fatalf("request_tokens_est = %v, want 10", reqLog["request_tokens_est"])
	}
}

// --- Context propagation ----------------------------------------------

func TestTracer_PropagatesTraceIDAndPipelineID(t *testing.T) {
	tracer, buf := testTracer(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}

	ctx := context.Background()
	ctx = WithTraceID(ctx, "trace-xyz-789")
	ctx = WithPipelineID(ctx, "news_blog")

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)
	for _, event := range []string{"http_request", "http_response"} {
		log := findEvent(t, lines, event)
		if got, _ := log["trace_id"].(string); got != "trace-xyz-789" {
			t.Errorf("%s: trace_id = %q, want trace-xyz-789", event, got)
		}
		if got, _ := log["pipeline_id"].(string); got != "news_blog" {
			t.Errorf("%s: pipeline_id = %q, want news_blog", event, got)
		}
	}
}

func TestTracer_OmitsTraceIDAndPipelineIDWhenAbsent(t *testing.T) {
	tracer, buf := testTracer(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)
	reqLog := findEvent(t, lines, "http_request")
	if _, ok := reqLog["trace_id"]; ok {
		t.Fatalf("trace_id should be omitted when absent: %+v", reqLog)
	}
	if _, ok := reqLog["pipeline_id"]; ok {
		t.Fatalf("pipeline_id should be omitted when absent: %+v", reqLog)
	}
}

func TestWithPipelineID(t *testing.T) {
	ctx := WithPipelineID(context.Background(), "news_blog")
	if got := pipelineIDFromContext(ctx); got != "news_blog" {
		t.Fatalf("got %q, want news_blog", got)
	}

	// Nil context must not panic and must return "".
	if got := pipelineIDFromContext(nil); got != "" {
		t.Fatalf("pipelineIDFromContext(nil) = %q, want \"\"", got)
	}

	// WithPipelineID on nil ctx must still work.
	ctx2 := WithPipelineID(nil, "foo")
	if got := pipelineIDFromContext(ctx2); got != "foo" {
		t.Fatalf("got %q, want foo", got)
	}
}

// --- Body handling -----------------------------------------------------

func TestTracer_ResponseBodyStillReadableByCaller(t *testing.T) {
	tracer, _ := testTracer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != `{"hello":"world"}` {
		t.Fatalf("body = %q, want original", string(body))
	}
}

func TestTracer_TruncatesLargeResponseBodyInLog(t *testing.T) {
	tracer, buf := testTracer(t)
	bigBody := strings.Repeat("x", 10_000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	// Caller must still get the full body.
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != bigBody {
		t.Fatalf("caller body corrupted: got %d bytes, want %d", len(got), len(bigBody))
	}

	// Log must NOT contain the full 10k body.
	lines := parseLogLines(t, buf)
	respLog := findEvent(t, lines, "http_response")
	bodyLogged, _ := respLog["response_body"].(string)
	if len(bodyLogged) > maxLogBodyBytes {
		t.Fatalf("log body is %d bytes, exceeds max %d", len(bodyLogged), maxLogBodyBytes)
	}
	truncated, _ := respLog["response_body_truncated"].(bool)
	if !truncated {
		t.Fatalf("response_body_truncated should be true")
	}
}

func TestTracer_NilRequestBodyDoesNotPanic(t *testing.T) {
	tracer, buf := testTracer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracer.WrapTransport(http.DefaultTransport, "p")}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	lines := parseLogLines(t, buf)
	reqLog := findEvent(t, lines, "http_request")
	if got, _ := reqLog["request_tokens_est"].(float64); int(got) != 0 {
		t.Fatalf("request_tokens_est = %v, want 0 for empty body", got)
	}
}

// --- Edge cases --------------------------------------------------------

func TestTracer_WrapTransport_NilBaseDefaultsToDefault(t *testing.T) {
	tracer, _ := testTracer(t)
	wrapped := tracer.WrapTransport(nil, "p")
	tt, ok := wrapped.(*TracingTransport)
	if !ok {
		t.Fatalf("want *TracingTransport, got %T", wrapped)
	}
	if tt.base != http.DefaultTransport {
		t.Fatalf("base = %v, want http.DefaultTransport", tt.base)
	}
}

func TestTracer_WrapTransport_NilTracerReturnsBaseUnchanged(t *testing.T) {
	// A nil Tracer must not crash; the base transport is returned as-is.
	var tr *Tracer
	base := http.DefaultTransport
	wrapped := tr.WrapTransport(base, "p")
	if wrapped != base {
		t.Fatalf("nil Tracer should pass through base transport")
	}
}

func TestTracer_RoundTrip_NilRequestReturnsError(t *testing.T) {
	tracer, _ := testTracer(t)
	wrapped := tracer.WrapTransport(http.DefaultTransport, "p").(*TracingTransport)
	_, err := wrapped.RoundTrip(nil)
	if err == nil {
		t.Fatal("nil request must return error")
	}
}

// --- sanitizeHeaders unit tests ----------------------------------------

func TestSanitizeHeaders_MasksSensitiveKeys(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-xxx")
	h.Set("X-Api-Key", "secret")        // canonical form — http.Header canonizes to this
	h.Set("cookie", "session=abc")      // lowercase → canonical "Cookie"
	h.Set("Content-Type", "text/plain") // safe
	h.Set("Accept", "application/json") // safe

	out := sanitizeHeaders(h)

	// Keys in the returned map use the canonical form produced by http.Header.
	if out["Authorization"] != redactedPlaceholder {
		t.Fatalf("Authorization not masked: %q", out["Authorization"])
	}
	if out["X-Api-Key"] != redactedPlaceholder {
		t.Fatalf("X-Api-Key not masked: %q", out["X-Api-Key"])
	}
	if out["Cookie"] != redactedPlaceholder {
		t.Fatalf("Cookie not masked: %q", out["Cookie"])
	}
	if out["Content-Type"] != "text/plain" {
		t.Fatalf("Content-Type mangled: %q", out["Content-Type"])
	}
	if out["Accept"] != "application/json" {
		t.Fatalf("Accept mangled: %q", out["Accept"])
	}

	// Defense in depth: regardless of the form used by the caller,
	// all case variants must be masked. Use ToLower lookup to prove
	// the sanitization is case-insensitive.
	for _, key := range []string{"x-api-key", "X-API-KEY", "X-Api-Key"} {
		// Find the matching key in `out` (which uses canonical form).
		var found bool
		for k, v := range out {
			if strings.EqualFold(k, key) {
				if v != redactedPlaceholder {
					t.Fatalf("variant %q of x-api-key not masked: %q", key, v)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no variant of %q found in output: %v", key, out)
		}
	}
}

func TestSanitizeHeaders_JoinsMultiValuedHeaders(t *testing.T) {
	h := http.Header{}
	h.Add("Accept", "text/html")
	h.Add("Accept", "application/json")
	out := sanitizeHeaders(h)
	if out["Accept"] != "text/html, application/json" {
		t.Fatalf("got %q, want joined value", out["Accept"])
	}
}
