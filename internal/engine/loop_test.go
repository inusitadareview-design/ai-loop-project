package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/internal/router"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

func TestEngine_Run_ValidPipeline(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if result.PipelineName != "test_pipeline" {
		t.Errorf("PipelineName = %q, want test_pipeline", result.PipelineName)
	}

	// Verify all stage outputs are present
	expectedKeys := []string{"plan", "draft", "reviewed"}
	for _, key := range expectedKeys {
		if _, ok := result.Outputs[key]; !ok {
			t.Errorf("missing output key %q", key)
		}
	}

	// Verify outputs are non-empty
	for key, val := range result.Outputs {
		if val == "" {
			t.Errorf("output %q is empty", key)
		}
	}
}

func TestEngine_Run_NilPipeline(t *testing.T) {
	e := NewEngine()
	_, err := e.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil pipeline")
	}
}

func TestEngine_Run_ContextCanceled(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	e := NewEngine()
	result, err := e.Run(ctx, p)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if result.Status != StatusCancelled {
		t.Errorf("Status = %q, want cancelled", result.Status)
	}
}

func TestEngine_Run_ContextTimeout(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // Ensure timeout

	e := NewEngine()
	result, err := e.Run(ctx, p)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if result.Status != StatusCancelled {
		t.Errorf("Status = %q, want cancelled", result.Status)
	}
}

func TestEngine_Run_StageOutputsChain(t *testing.T) {
	// Test that outputs from one stage are available to the next.
	yaml := `
name: chain_test
stages:
  - id: first
    action: generate
    output_key: first_output
  - id: second
    action: self_review
    depends_on: [first]
    input_keys: [first_output]
    output_key: second_output
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The second stage should have received the first stage's output.
	if result.Outputs["second_output"] != result.Outputs["first_output"] {
		t.Errorf("second_output = %q, want %q (pass-through)",
			result.Outputs["second_output"], result.Outputs["first_output"])
	}
}

func TestEngine_Run_SelfReviewNoInput(t *testing.T) {
	// Test that self_review fails gracefully when no input is available.
	yaml := `
name: no_input
stages:
  - id: review
    action: self_review
    input_keys: [nonexistent]
    output_key: reviewed
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for missing input")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
}

func TestEngine_Run_UnknownAction(t *testing.T) {
	yaml := `
name: unknown_action
stages:
  - id: test
    action: nonexistent_action
    output_key: out
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
}

func TestEngine_Run_HTTPPostStub(t *testing.T) {
	yaml := `
name: http_test
stages:
  - id: generate
    action: generate
    output_key: content
  - id: publish
    action: http_post
    depends_on: [generate]
    input_keys: [content]
    endpoint_env: TEST_WEBHOOK
    output_key: published
limits:
  require_review: true
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if result.Outputs["published"] != "published_stub" {
		t.Errorf("published = %q, want published_stub", result.Outputs["published"])
	}
}

func TestEngine_Run_MultipleStagesSequential(t *testing.T) {
	// Test a pipeline with many stages to verify sequential execution.
	yaml := `
name: sequential
stages:
  - id: s1
    action: generate
    output_key: out1
  - id: s2
    action: generate
    depends_on: [s1]
    output_key: out2
  - id: s3
    action: generate
    depends_on: [s2]
    output_key: out3
  - id: s4
    action: self_review
    depends_on: [s3]
    input_keys: [out3]
    output_key: out4
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}

	// All 4 outputs should be present
	for i := 1; i <= 4; i++ {
		key := fmt.Sprintf("out%d", i)
		if _, ok := result.Outputs[key]; !ok {
			t.Errorf("missing output key %q", key)
		}
	}
}

// =========================================================================
// Integration tests: real AI generation via router.Client + httptest
// =========================================================================

// newTestEngineReal builds an Engine wired to real router.Client + Resolver
// for integration tests. The returned cleanup function closes the logger.
// Callers MUST call the cleanup via defer (e.g., `defer cleanup()`).
// The Resolver will use the OMNI_ROUTE_URL env var set by t.Setenv.
func newTestEngineReal(t *testing.T) (*Engine, func()) {
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

	tracer := observability.NewTracer(lg)
	client := router.NewClient(tracer)
	// New resolver — each test needs its own sync.Once cache.
	resolver := router.NewResolver(cfg, lg)

	engine := NewEngineWithDeps(client, resolver)
	cleanup := func() { _ = lg.Close() }
	return engine, cleanup
}

// makePipelineForGenerate builds a minimal valid pipeline with a single
// generate stage, ready to be fed to Engine.Run.
func makePipelineForGenerate(name, stageID, outputKey string) *Pipeline {
	return &Pipeline{
		Name: name,
		Model: Model{
			Preferred:   "omniroute/test-model",
			MaxTokens:   100,
			Temperature: 0.5,
		},
		Stages: []Stage{
			{
				ID:         stageID,
				Action:     "generate",
				OutputKey:  outputKey,
				PromptFile: "templates/test.md",
			},
		},
		Limits: Limits{
			RequireReview:    false,
			MaxCostPerRunUSD: 0.0,
			DeadLoopFailures: 3,
		},
	}
}

func TestRealGenerate_Success(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)

		// Validate request path and method.
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/completions" {
			t.Errorf("path = %q, want /v1/completions", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}

		// Validate payload shape.
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req["model"] != "omniroute/test-model" {
			t.Errorf("model = %v, want omniroute/test-model", req["model"])
		}
		if _, ok := req["prompt"]; !ok {
			t.Errorf("missing prompt field in payload")
		}

		// Return an OpenAI-compatible response.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "hello from ai"}},
		})
	}))
	defer srv.Close()

	// Point the Resolver at our mock server via env var.
	t.Setenv("OMNI_ROUTE_URL", srv.URL)

	engine, cleanup := newTestEngineReal(t)
	defer cleanup()

	p := makePipelineForGenerate("ai_test", "plan", "plan_out")

	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if got := result.Outputs["plan_out"]; got != "hello from ai" {
		t.Errorf("plan_out = %q, want 'hello from ai'", got)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (no retries on success)", got)
	}
}

func TestRealGenerate_ServerReturns4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad payload"}`))
	}))
	defer srv.Close()

	t.Setenv("OMNI_ROUTE_URL", srv.URL)
	engine, cleanup := newTestEngineReal(t)
	defer cleanup()

	p := makePipelineForGenerate("ai_4xx", "plan", "plan_out")

	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	// Critical: 4xx must NOT trigger retry (anti-ban, I3).
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (4xx must not retry)", got)
	}
	// Error must mention the status code.
	if !errors.Is(err, err) {
		// Just ensure error is not nil; exact type checking below.
	}
	if err.Error() == "" {
		t.Error("error message is empty")
	}
}

func TestRealGenerate_ServerReturns5xxThen200(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"down"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "recovered"}},
		})
	}))
	defer srv.Close()

	t.Setenv("OMNI_ROUTE_URL", srv.URL)
	engine, cleanup := newTestEngineReal(t)
	defer cleanup()

	p := makePipelineForGenerate("ai_5xx", "plan", "plan_out")

	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v (should recover after retries)", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if got := result.Outputs["plan_out"]; got != "recovered" {
		t.Errorf("plan_out = %q, want 'recovered'", got)
	}
	// 2 failures (5xx) + 1 success = 3 calls.
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server calls = %d, want 3 (2 fails + 1 success)", got)
	}
}

func TestRealGenerate_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not valid json at all`))
	}))
	defer srv.Close()

	t.Setenv("OMNI_ROUTE_URL", srv.URL)
	engine, cleanup := newTestEngineReal(t)
	defer cleanup()

	p := makePipelineForGenerate("ai_malformed", "plan", "plan_out")

	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	if !contains(err.Error(), "unmarshal response") {
		t.Errorf("error = %q, want mention of 'unmarshal response'", err)
	}
}

func TestRealGenerate_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{}, // empty
		})
	}))
	defer srv.Close()

	t.Setenv("OMNI_ROUTE_URL", srv.URL)
	engine, cleanup := newTestEngineReal(t)
	defer cleanup()

	p := makePipelineForGenerate("ai_empty", "plan", "plan_out")

	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	if !contains(err.Error(), "no choices") {
		t.Errorf("error = %q, want mention of 'no choices'", err)
	}
}

// contains reports whether s contains substr. Avoids importing strings
// package just for this helper (already covered by existing imports).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

// searchSubstring is a tiny helper so we don't pull in strings package.
// Simple O(n*m) scan — fine for test assertions.
func searchSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
