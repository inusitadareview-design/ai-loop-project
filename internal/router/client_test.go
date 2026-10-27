package router

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// testClient builds a Client wired to a real Tracer+Logger so that
// integration with tracer.go is exercised in every test. The logger
// is closed via t.Cleanup.
func testClient(t *testing.T) *Client {
	t.Helper()
	cfg := &config.Config{
		Logging: config.Logging{
			Level:      "error",
			File:       t.TempDir() + "/test.log",
			MaxSizeMB:  1,
			MaxBackups: 1,
			JSON:       true,
		},
	}
	lg, err := observability.New(cfg)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })

	tracer := observability.NewTracer(lg)
	c := NewClient(tracer)
	// Override backoff for fast tests — 1ms instead of seconds.
	c.backoffFn = func(attempt int) time.Duration { return 1 * time.Millisecond }
	return c
}

// --- Happy path --------------------------------------------------------

func TestCall_HappyPath_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test-key-12345" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"answer":42}`))
	}))
	defer srv.Close()

	c := testClient(t)
	resp, err := c.Call(context.Background(), CallRequest{
		PipelineID: "news_blog",
		Method:     "POST",
		URLs:       []string{srv.URL},
		Body:       []byte(`{"prompt":"hello"}`),
		AuthHeader: "Bearer sk-test-key-12345",
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if string(resp.Body) != `{"answer":42}` {
		t.Fatalf("Body = %q, want {\"answer\":42}", string(resp.Body))
	}
	if resp.ProviderUsed != srv.URL {
		t.Fatalf("ProviderUsed = %q, want %q", resp.ProviderUsed, srv.URL)
	}
	if resp.LatencyMs < 0 {
		t.Fatalf("LatencyMs = %d, want >= 0", resp.LatencyMs)
	}
}

// --- 4xx: fail-fast, no blind retry (I3) --------------------------------

func TestCall_4xx_NoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad payload"}`))
	}))
	defer srv.Close()

	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if !strings.Contains(err.Error(), "client error 400") {
		t.Fatalf("error = %v, want client error 400", err)
	}
	if !strings.Contains(err.Error(), "bad payload") {
		t.Fatalf("error should contain body, got: %v", err)
	}
	// Critical: 4xx must NOT trigger any retry (anti-ban, I3).
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 (4xx must not retry)", got)
	}
}

func TestCall_404_NoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 (4xx must not retry)", got)
	}
}

// --- 5xx: retry then success -------------------------------------------

func TestCall_5xx_RetryThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"down"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(t)
	resp, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestCall_5xx_ExhaustedRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"down"}`))
	}))
	defer srv.Close()

	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error after exhausting 5xx retries")
	}
	if !strings.Contains(err.Error(), "server error 500") {
		t.Fatalf("error = %v, want server error 500", err)
	}
	// 1 initial + maxRetries5xx (3) retries = 4 total.
	if got := atomic.LoadInt32(&calls); got != 1+maxRetries5xx {
		t.Fatalf("calls = %d, want %d", got, 1+maxRetries5xx)
	}
}

// --- 429: backoff then success ----------------------------------------

func TestCall_429_BackoffThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(t)
	resp, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestCall_429_ExhaustedRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error after exhausting 429 retries")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want 429 mention", err)
	}
	// 1 initial + maxRetries429 (5) retries = 6 total.
	if got := atomic.LoadInt32(&calls); got != 1+maxRetries429 {
		t.Fatalf("calls = %d, want %d", got, 1+maxRetries429)
	}
}

// --- Fallback chain ----------------------------------------------------

func TestCall_FallbackChain_FirstFailsSecondSucceeds(t *testing.T) {
	var calls1, calls2 int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls1, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls2, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv2.Close()

	c := testClient(t)
	resp, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv1.URL, srv2.URL},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.ProviderUsed != srv2.URL {
		t.Fatalf("ProviderUsed = %q, want %q", resp.ProviderUsed, srv2.URL)
	}
	// srv1: 1 initial + 3 retries = 4 attempts, all fail → fallback
	if got := atomic.LoadInt32(&calls1); got != 1+maxRetries5xx {
		t.Fatalf("srv1 calls = %d, want %d", got, 1+maxRetries5xx)
	}
	// srv2: 1 attempt, success
	if got := atomic.LoadInt32(&calls2); got != 1 {
		t.Fatalf("srv2 calls = %d, want 1", got)
	}
}

func TestCall_FallbackChain_4xxStopsChain(t *testing.T) {
	// 4xx on first URL must fail fast — do NOT try the next URL.
	// This is the anti-ban clause (I3).
	var calls1, calls2 int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls1, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls2, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv1.URL, srv2.URL},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// srv1 tried exactly once (4xx fail-fast).
	if got := atomic.LoadInt32(&calls1); got != 1 {
		t.Fatalf("srv1 calls = %d, want 1", got)
	}
	// srv2 must NOT have been tried.
	if got := atomic.LoadInt32(&calls2); got != 0 {
		t.Fatalf("srv2 calls = %d, want 0 (4xx must not trigger fallback)", got)
	}
}

func TestCall_AllProvidersFailed(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv2.Close()

	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv1.URL, srv2.URL},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAllProvidersFailed) {
		t.Fatalf("error = %v, want ErrAllProvidersFailed", err)
	}
}

// --- Context cancellation ----------------------------------------------

func TestCall_ContextCanceledBeforeStart(t *testing.T) {
	c := testClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Call(ctx, CallRequest{
		Method:  "POST",
		URLs:    []string{"http://example.com"},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCall_ContextCanceledDuringBackoff(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := testClient(t)
	// Use real backoff (seconds) so we have time to cancel.
	c.backoffFn = nil

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := c.Call(ctx, CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// Must not have exhausted all retries — canceled mid-backoff.
	if got := atomic.LoadInt32(&calls); got >= 1+maxRetries5xx {
		t.Fatalf("calls = %d, expected fewer due to cancellation", got)
	}
}

// --- Input validation --------------------------------------------------

func TestCall_EmptyURLs_ReturnsError(t *testing.T) {
	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method: "POST",
		URLs:   []string{},
	})
	if err == nil {
		t.Fatal("expected error for empty URLs")
	}
	if !strings.Contains(err.Error(), "URLs must not be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestCall_NilURLs_ReturnsError(t *testing.T) {
	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method: "POST",
		URLs:   nil,
	})
	if err == nil {
		t.Fatal("expected error for nil URLs")
	}
}

func TestCall_EmptyMethod_ReturnsError(t *testing.T) {
	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method: "",
		URLs:   []string{"http://example.com"},
	})
	if err == nil {
		t.Fatal("expected error for empty method")
	}
	if !strings.Contains(err.Error(), "Method must not be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestCall_NilClient_ReturnsError(t *testing.T) {
	var c *Client
	_, err := c.Call(context.Background(), CallRequest{
		Method: "POST",
		URLs:   []string{"http://example.com"},
	})
	if err == nil {
		t.Fatal("expected error for nil client")
	}
}

// --- Timeout handling --------------------------------------------------

func TestCall_Timeout_RetriesOnce(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// First attempt: sleep longer than timeout → client times out.
			time.Sleep(150 * time.Millisecond)
		}
		// Second attempt (and any further): respond quickly.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(t)
	resp, err := c.Call(context.Background(), CallRequest{
		Method:  "POST",
		URLs:    []string{srv.URL},
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	// D1: timeout → 1 immediate retry with same provider → success.
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (1 timeout + 1 success)", got)
	}
}

// --- Backoff logic -----------------------------------------------------

func TestBackoff_ExponentialGrowth(t *testing.T) {
	c := &Client{rand: rand.New(rand.NewSource(42))}

	// attempt=1: baseDelay * 2^0 = 2s
	d1 := c.backoff(1)
	if d1 < baseDelay || d1 > baseDelay+baseDelay/2 {
		t.Fatalf("backoff(1) = %v, want [%v, %v]", d1, baseDelay, baseDelay+baseDelay/2)
	}

	// attempt=2: baseDelay * 2^1 = 4s
	d2 := c.backoff(2)
	if d2 < 2*baseDelay || d2 > 2*baseDelay+baseDelay/2 {
		t.Fatalf("backoff(2) = %v, want [%v, %v]", d2, 2*baseDelay, 2*baseDelay+baseDelay/2)
	}

	// attempt=3: baseDelay * 2^2 = 8s
	d3 := c.backoff(3)
	if d3 < 4*baseDelay || d3 > 4*baseDelay+baseDelay/2 {
		t.Fatalf("backoff(3) = %v, want [%v, %v]", d3, 4*baseDelay, 4*baseDelay+baseDelay/2)
	}

	// Monotonicity (on average — jitter can make d_n < d_{n-1} only
	// if exp growth is small; at these scales exp dominates jitter).
	if d2 < d1 {
		t.Fatalf("backoff not growing: d1=%v, d2=%v", d1, d2)
	}
	if d3 < d2 {
		t.Fatalf("backoff not growing: d2=%v, d3=%v", d2, d3)
	}
}

func TestBackoff_CappedAtMaxDelay(t *testing.T) {
	c := &Client{rand: rand.New(rand.NewSource(42))}
	// attempt=10: baseDelay * 2^9 = 1024s, must be capped at maxDelay (30s).
	d := c.backoff(10)
	if d > maxDelay+baseDelay/2 {
		t.Fatalf("backoff(10) = %v, want <= %v + jitter", d, maxDelay)
	}
}

func TestBackoff_ZeroAttemptTreatedAsOne(t *testing.T) {
	c := &Client{rand: rand.New(rand.NewSource(42))}
	d := c.backoff(0)
	// Should behave like attempt=1: baseDelay + jitter.
	if d < baseDelay || d > baseDelay+baseDelay/2 {
		t.Fatalf("backoff(0) = %v, want [%v, %v]", d, baseDelay, baseDelay+baseDelay/2)
	}
}

// --- isTimeout ---------------------------------------------------------

func TestIsTimeout_NilError(t *testing.T) {
	if isTimeout(nil) {
		t.Fatal("isTimeout(nil) = true, want false")
	}
}

func TestIsTimeout_ContextDeadlineExceeded(t *testing.T) {
	if !isTimeout(context.DeadlineExceeded) {
		t.Fatal("isTimeout(DeadlineExceeded) = false, want true")
	}
}

func TestIsTimeout_WrappedDeadlineExceeded(t *testing.T) {
	wrapped := errors.Join(errors.New("outer"), context.DeadlineExceeded)
	if !isTimeout(wrapped) {
		t.Fatal("isTimeout(wrapped DeadlineExceeded) = false, want true")
	}
}

func TestIsTimeout_GenericError(t *testing.T) {
	if isTimeout(errors.New("some other error")) {
		t.Fatal("isTimeout(generic) = true, want false")
	}
}

// --- truncate ----------------------------------------------------------

func TestTruncate_ShortString(t *testing.T) {
	if got := truncate([]byte("hello"), 10); got != "hello" {
		t.Fatalf("truncate = %q, want hello", got)
	}
}

func TestTruncate_LongString(t *testing.T) {
	got := truncate([]byte("hello world"), 5)
	if got != "hello..." {
		t.Fatalf("truncate = %q, want hello...", got)
	}
}

func TestTruncate_Empty(t *testing.T) {
	if got := truncate([]byte{}, 10); got != "<empty>" {
		t.Fatalf("truncate = %q, want <empty>", got)
	}
}

// --- Integration: tracer is wired -------------------------------------

func TestCall_AuthHeaderIsPassedToProvider(t *testing.T) {
	// Verify the auth header actually reaches the provider (not just
	// that it doesn't crash). The tracer's masking is tested separately
	// in tracer_test.go; here we only check the header is set.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := testClient(t)
	_, err := c.Call(context.Background(), CallRequest{
		Method:     "POST",
		URLs:       []string{srv.URL},
		AuthHeader: "Bearer sk-test-key-xyz",
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotAuth != "Bearer sk-test-key-xyz" {
		t.Fatalf("provider received auth = %q, want Bearer sk-test-key-xyz", gotAuth)
	}
}
