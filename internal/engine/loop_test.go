package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SEU_USUARIO/ai-loop-project/internal/events"
	"github.com/SEU_USUARIO/ai-loop-project/internal/memory"
	"github.com/SEU_USUARIO/ai-loop-project/internal/omniroute"
	"github.com/SEU_USUARIO/ai-loop-project/internal/router"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config" // NOVO
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
	// This test validates the behavior of stubHTTPPost when require_review is true.
	// It should save a draft file to data/outputs/ instead of returning "published_stub".

	p := &Pipeline{
		Name: "http_test",
		Limits: Limits{
			RequireReview: true, // G3.2: requires human approval
		},
		Stages: []Stage{
			{
				ID:        "generate",
				Action:    "generate",
				OutputKey: "content",
			},
			{
				ID:          "publish",
				Action:      "http_post",
				EndpointEnv: "TEST_WEBHOOK",
				DependsOn:   []string{"generate"},
				OutputKey:   "published",
			},
		},
	}

	engine := NewEngine()
	ctx := context.Background()
	result, err := engine.Run(ctx, p)

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %v, want %v", result.Status, StatusCompleted)
	}

	// Verify the output — when require_review is true, draft is saved
	publishedValue, exists := result.Outputs["published"]
	if !exists {
		t.Fatal("published output not found in result.Outputs")
	}

	if !strings.HasPrefix(publishedValue, "draft_saved:") {
		t.Errorf("published = %q, want prefix draft_saved:", publishedValue)
	}

	// Verify draft file was created
	draftPath := strings.TrimPrefix(publishedValue, "draft_saved:")
	if _, err := os.Stat(draftPath); err != nil {
		t.Errorf("draft file %q does not exist: %v", draftPath, err)
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

// newTestEngineWithRealRouter creates an Engine wired to a real router.Client
// and a Resolver that returns the test server URL for the "test-provider" name.
// Used for integration tests of realGenerate.
func newTestEngineWithRealRouter(t *testing.T, serverURL string) (*Engine, *router.Client) {
	t.Helper()

	// Build a minimal config with one provider in the fallback chain.
	cfg := &config.Config{
		Providers: config.Providers{
			Default: "test-provider",
		},
		Routing: config.Routing{},
	}

	// Client with nil tracer is acceptable for tests (no X-Ray observability,
	// but the test doesn't assert on logs — only on HTTP behavior).
	client := router.NewClient(nil)

	// Resolver that returns serverURL when asked for "test-provider".
	resolver := router.NewResolver(cfg, nil)
	resolver.SetGetenv(func(key string) string {
		// AILOOP_TEST_PROVIDER_URL → serverURL (env var convention: uppercase + _).
		if key == "AILOOP_TEST_PROVIDER_URL" {
			return serverURL
		}
		return ""
	})

	eng := NewEngineWithDeps(client, resolver)
	eng.SetConfig(cfg)
	eng.SetBroadcaster(events.NoopBroadcaster{})

	return eng, client
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

		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/completions" {
			t.Errorf("path = %q, want /v1/completions", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}

		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req["model"] != "omniroute/test-model" {
			t.Errorf("model = %v, want omniroute/test-model", req["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "hello from ai"}},
		})
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
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

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
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

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
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

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	p := makePipelineForGenerate("ai_malformed", "plan", "plan_out")

	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(err.Error(), "unmarshal response") {
		t.Errorf("error = %q, want mention of 'unmarshal response'", err)
	}
}

func TestRealGenerate_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{},
		})
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	p := makePipelineForGenerate("ai_empty", "plan", "plan_out")

	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error = %q, want mention of 'no choices'", err)
	}
}

func TestStubHTTPPost_SavesDraftWhenRequireReview(t *testing.T) {
	tmp := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	eng := NewEngine()
	eng.SetOutputsDir(filepath.Join(tmp, "data", "outputs")) // injetar caminho absoluto

	pipeline := &Pipeline{
		Name: "news_blog",
		Limits: Limits{
			RequireReview: true,
		},
		Stages: []Stage{
			{
				ID:          "publish",
				Action:      "http_post",
				EndpointEnv: "AILOOP_BLOG_WEBHOOK",
				InputKeys:   []string{"reviewed"},
			},
		},
	}

	stage := pipeline.Stages[0]
	inputs := map[string]string{"reviewed": "Final reviewed draft content"}

	result, err := eng.stubHTTPPost(context.Background(), pipeline, stage, inputs)
	if err != nil {
		t.Fatalf("stubHTTPPost: %v", err)
	}

	if !strings.HasPrefix(result, "draft_saved:") {
		t.Fatalf("result = %q, want prefix draft_saved:", result)
	}
	path := strings.TrimPrefix(result, "draft_saved:")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}

	var draft Draft
	if err := json.Unmarshal(data, &draft); err != nil {
		t.Fatalf("unmarshal draft: %v", err)
	}

	if draft.Pipeline != "news_blog" {
		t.Errorf("draft.Pipeline = %q, want news_blog", draft.Pipeline)
	}
	if draft.StageID != "publish" {
		t.Errorf("draft.StageID = %q, want publish", draft.StageID)
	}
	if draft.EndpointEnv != "AILOOP_BLOG_WEBHOOK" {
		t.Errorf("draft.EndpointEnv = %q, want AILOOP_BLOG_WEBHOOK", draft.EndpointEnv)
	}
	if draft.Inputs["reviewed"] != "Final reviewed draft content" {
		t.Errorf("draft.Inputs[reviewed] = %q, want 'Final reviewed draft content'", draft.Inputs["reviewed"])
	}
	if draft.CreatedAt.IsZero() {
		t.Error("draft.CreatedAt is zero")
	}

	if !strings.Contains(path, filepath.Join("data", "outputs")) {
		t.Errorf("draft path %q is not under data/outputs/", path)
	}
}

func TestRealHTTPPost_NoReviewMakesActualPOST(t *testing.T) {
	// This test verifies that when require_review=false, stubHTTPPost
	// delegates to realHTTPPost and makes an actual HTTP POST to the webhook.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	// Set env var to test server URL
	t.Setenv("TEST_WEBHOOK_NO_REVIEW", srv.URL)

	tmp := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	eng := NewEngine()
	eng.SetOutputsDir(filepath.Join(tmp, "data", "outputs"))

	pipeline := &Pipeline{
		Name:   "no_review_pipeline",
		Limits: Limits{RequireReview: false}, // no draft, makes real POST
	}
	stage := Stage{
		ID:          "publish",
		Action:      "http_post",
		EndpointEnv: "TEST_WEBHOOK_NO_REVIEW",
	}

	result, err := eng.stubHTTPPost(context.Background(), pipeline, stage, map[string]string{"content": "test"})
	if err != nil {
		t.Fatalf("stubHTTPPost: %v", err)
	}
	if !strings.HasPrefix(result, "published:") {
		t.Errorf("result = %q, want prefix 'published:'", result)
	}

	// Verify webhook was called exactly once (no retry on 2xx).
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("webhook calls = %d, want 1", got)
	}

	// Verify no draft file was created (require_review=false).
	if _, err := os.Stat(filepath.Join("data", "outputs")); !os.IsNotExist(err) {
		t.Errorf("data/outputs should not exist when require_review is false")
	}
}

func TestRealGenerate_TokenBudgetExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should NOT be called when budget exceeded (I1)")
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	// Inject config with restrictive token limit
	engine.SetConfig(&config.Config{
		Budget: config.Budget{
			DailyLimitUSD:        0.0, // zero-cost mode (G1.1)
			CostPer1kInputTokens: 0.0, // free-tier
		},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	// Pipeline with max_tokens=1 (very restrictive)
	p := &Pipeline{
		Name: "ai_budget_test",
		Model: Model{
			Preferred: "test-model",
			MaxTokens: 1, // Only 1 token allowed
		},
		Stages: []Stage{
			{
				ID:         "plan",
				Action:     "generate",
				OutputKey:  "plan_out",
				PromptFile: "templates/test.md",
			},
		},
	}

	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for budget exceeded (I1: pre-validation must block)")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	// Error must mention budget — proves I1 was enforced BEFORE network call
	if !strings.Contains(err.Error(), "budget") && !strings.Contains(err.Error(), "token") {
		t.Errorf("error = %q, want mention of budget/token", err)
	}
}

func TestRealGenerate_RecordsCostInMemory(t *testing.T) {
	// Mock server that returns success
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "success"}},
		})
	}))
	defer srv.Close()

	// Create temp dir for memory store
	tmp := t.TempDir()
	store, err := memory.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetStore(store)
	engine.SetConfig(&config.Config{
		Budget: config.Budget{
			DailyLimitUSD:        0.04, // spent (0.05) > daily_limit (0.04), remaining = 0, any projected_cost fails
			CostPer1kInputTokens: 0.002,
		},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name: "cost_test",
		Model: Model{
			Preferred: "test-model",
			MaxTokens: 100,
		},
		Stages: []Stage{
			{
				ID:         "plan",
				Action:     "generate",
				OutputKey:  "plan_out",
				PromptFile: "templates/test.md",
			},
		},
	}

	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}

	// Verify cost event was recorded
	events, err := store.ReadAll(context.Background(), "cost_test")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	var costEvent *memory.Event
	for i := range events {
		if events[i].Type == "http_call_cost" {
			costEvent = &events[i]
			break
		}
	}
	if costEvent == nil {
		t.Fatal("expected http_call_cost event to be recorded")
	}

	// Verify cost_usd field
	costUSD, ok := costEvent.Data["cost_usd"].(float64)
	if !ok {
		t.Fatal("cost_usd field missing or not float64")
	}
	if costUSD <= 0 {
		t.Errorf("cost_usd = %v, want > 0", costUSD)
	}

	// Verify estimated_tokens field
	estimatedTokens, ok := costEvent.Data["estimated_tokens"].(float64)
	if !ok {
		t.Fatal("estimated_tokens field missing or not float64")
	}
	if estimatedTokens <= 0 {
		t.Errorf("estimated_tokens = %v, want > 0", estimatedTokens)
	}
}

func TestRealGenerate_ReadsSpentUSDFromMemory(t *testing.T) {
	// Mock server that returns success
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "success"}},
		})
	}))
	defer srv.Close()

	// Create temp dir for memory store
	tmp := t.TempDir()
	store, err := memory.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Pre-populate with spent_usd
	now := time.Now().Unix()
	spentEvent := memory.Event{
		ID:         "spent-1",
		PipelineID: "spent_test",
		Type:       "http_call_cost",
		Timestamp:  now,
		Data: map[string]interface{}{
			"cost_usd": 0.05,
		},
	}
	if err := store.Append(context.Background(), "spent_test", spentEvent); err != nil {
		t.Fatalf("Append: %v", err)
	}

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetStore(store)
	engine.SetConfig(&config.Config{
		Budget: config.Budget{
			DailyLimitUSD:        0.050001, // remaining (0.000001) < projected (0.000006) → FAILS
			CostPer1kInputTokens: 0.002,
		},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name: "spent_test",
		Model: Model{
			Preferred: "test-model",
			MaxTokens: 1000, // Large prompt to exceed budget
		},
		Stages: []Stage{
			{
				ID:         "plan",
				Action:     "generate",
				OutputKey:  "plan_out",
				PromptFile: "templates/test.md",
			},
		},
	}

	// Should fail because spent_usd (0.05) + projected cost > daily_limit (0.06)
	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for budget exceeded")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(err.Error(), "exceeds token budget") {
		t.Errorf("error = %q, want mention of 'exceeds token budget'", err)
	}
}

func TestRealHTTPPost_Success(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req["pipeline"] != "webhook_test" {
			t.Errorf("pipeline = %v, want webhook_test", req["pipeline"])
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	// Set env var to test server URL
	t.Setenv("TEST_WEBHOOK_URL", srv.URL)

	eng := NewEngine()
	p := &Pipeline{
		Name: "webhook_test",
		Stages: []Stage{
			{
				ID:          "publish",
				Action:      "http_post",
				EndpointEnv: "TEST_WEBHOOK_URL",
			},
		},
		Limits: Limits{RequireReview: false}, // bypass draft
	}

	result, err := eng.realHTTPPost(context.Background(), p, p.Stages[0], map[string]string{
		"content": "Hello from AI Loop",
	})
	if err != nil {
		t.Fatalf("realHTTPPost: %v", err)
	}
	if !strings.HasPrefix(result, "published:") {
		t.Errorf("result = %q, want prefix 'published:'", result)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (no retry on 2xx)", got)
	}
}

func TestRealHTTPPost_4xxFailsPermanently(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid payload"}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_WEBHOOK_URL", srv.URL)

	eng := NewEngine()
	p := &Pipeline{
		Name:   "webhook_4xx",
		Stages: []Stage{{ID: "publish", EndpointEnv: "TEST_WEBHOOK_URL"}},
		Limits: Limits{RequireReview: false},
	}

	_, err := eng.realHTTPPost(context.Background(), p, p.Stages[0], map[string]string{"x": "y"})
	if err == nil {
		t.Fatal("expected error for 400 response (D6: fail-fast)")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, want mention of 400", err)
	}
	// D6: 4xx must NOT retry.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (4xx must not retry per D6)", got)
	}
}

func TestRealHTTPPost_5xxRetriesAndFails(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server down"}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_WEBHOOK_URL", srv.URL)

	eng := NewEngine()
	p := &Pipeline{
		Name:   "webhook_5xx",
		Stages: []Stage{{ID: "publish", EndpointEnv: "TEST_WEBHOOK_URL"}},
		Limits: Limits{RequireReview: false},
	}

	start := time.Now()
	_, err := eng.realHTTPPost(context.Background(), p, p.Stages[0], map[string]string{"x": "y"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error after 3 retry attempts (D6)")
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("error = %q, want mention of '3 attempts'", err)
	}
	// D6: 5xx must retry 3 times with backoff (1s + 2s + 4s = 7s min).
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server calls = %d, want 3 (D6: retry 5xx)", got)
	}
	// Backoff should have added ~3 seconds (1s + 2s between attempts).
	if elapsed < 2*time.Second {
		t.Errorf("elapsed = %v, want >= 2s (backoff applied)", elapsed)
	}
}

func TestRealHTTPPost_MissingEnvVar(t *testing.T) {
	eng := NewEngine()
	p := &Pipeline{
		Name:   "webhook_noenv",
		Stages: []Stage{{ID: "publish", EndpointEnv: "NONEXISTENT_WEBHOOK_URL"}},
		Limits: Limits{RequireReview: false},
	}

	_, err := eng.realHTTPPost(context.Background(), p, p.Stages[0], map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
	if !strings.Contains(err.Error(), "NONEXISTENT_WEBHOOK_URL") {
		t.Errorf("error = %q, want mention of env var name", err)
	}
	if !strings.Contains(err.Error(), "not set") {
		t.Errorf("error = %q, want mention of 'not set'", err)
	}
}

func TestPublishDraft_Success(t *testing.T) {
	var received map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Payload map[string]string `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		received = body.Payload
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_PUBLISH_WEBHOOK", srv.URL)

	eng := NewEngine()
	err := eng.PublishDraft(context.Background(), DraftPublish{
		PipelineName: "news_blog",
		StageID:      "publish",
		EndpointEnv:  "TEST_PUBLISH_WEBHOOK",
		Inputs:       map[string]string{"content": "Hello world"},
	})
	if err != nil {
		t.Fatalf("PublishDraft: %v", err)
	}
	if received["content"] != "Hello world" {
		t.Errorf("webhook payload = %v, want content='Hello world'", received)
	}
}

func TestPublishDraft_FailsOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid"}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_PUBLISH_WEBHOOK", srv.URL)

	eng := NewEngine()
	err := eng.PublishDraft(context.Background(), DraftPublish{
		PipelineName: "news_blog",
		StageID:      "publish",
		EndpointEnv:  "TEST_PUBLISH_WEBHOOK",
		Inputs:       map[string]string{"x": "y"},
	})
	if err == nil {
		t.Fatal("expected error on 4xx")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, want mention of 400", err)
	}
}

func TestPublishDraft_EmptyEndpointEnv(t *testing.T) {
	eng := NewEngine()
	err := eng.PublishDraft(context.Background(), DraftPublish{
		PipelineName: "news_blog",
		StageID:      "publish",
		EndpointEnv:  "", // empty
		Inputs:       map[string]string{"x": "y"},
	})
	if err == nil {
		t.Fatal("expected error for empty EndpointEnv")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want mention of 'empty'", err)
	}
}

func TestRealHTTPPost_WithWebhookSecret_AddsSignatureHeader(t *testing.T) {
	var receivedSignature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_WEBHOOK_URL", srv.URL)
	t.Setenv("TEST_WEBHOOK_SECRET", "super-secret-key-123")

	eng := NewEngine()
	// Inject config with webhook secret
	eng.SetConfig(&config.Config{
		Webhooks:  config.Webhooks{SecretEnvVar: "TEST_WEBHOOK_SECRET"},
		Providers: config.Providers{Default: "test"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:   "signed_test",
		Stages: []Stage{{ID: "publish", EndpointEnv: "TEST_WEBHOOK_URL"}},
		Limits: Limits{RequireReview: false},
	}

	_, err := eng.realHTTPPost(context.Background(), p, p.Stages[0], map[string]string{"x": "y"})
	if err != nil {
		t.Fatalf("realHTTPPost: %v", err)
	}

	if receivedSignature == "" {
		t.Fatal("X-Webhook-Signature header not set")
	}
	if !strings.HasPrefix(receivedSignature, "sha256=") {
		t.Errorf("signature = %q, want prefix 'sha256='", receivedSignature)
	}
	// SHA256 hex digest is 64 chars + "sha256=" prefix = 71 chars total
	if len(receivedSignature) != 71 {
		t.Errorf("signature length = %d, want 71 (sha256= + 64 hex chars)", len(receivedSignature))
	}
}

func TestRealHTTPPost_WithoutWebhookSecret_NoSignatureHeader(t *testing.T) {
	var receivedSignature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_WEBHOOK_URL", srv.URL)
	// DO NOT set any webhook secret env var

	eng := NewEngine()
	// Config with empty webhooks (backwards compatible)
	eng.SetConfig(&config.Config{
		Webhooks:  config.Webhooks{SecretEnvVar: ""},
		Providers: config.Providers{Default: "test"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:   "unsigned_test",
		Stages: []Stage{{ID: "publish", EndpointEnv: "TEST_WEBHOOK_URL"}},
		Limits: Limits{RequireReview: false},
	}

	_, err := eng.realHTTPPost(context.Background(), p, p.Stages[0], map[string]string{"x": "y"})
	if err != nil {
		t.Fatalf("realHTTPPost: %v", err)
	}

	if receivedSignature != "" {
		t.Errorf("signature = %q, want empty (no secret configured)", receivedSignature)
	}
}

func TestRealHTTPPost_WithWebhookSecret_MissingEnvVar_NoSignature(t *testing.T) {
	var receivedSignature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_WEBHOOK_URL", srv.URL)
	// DO NOT set NONEXISTENT_SECRET env var (simulates misconfig)

	eng := NewEngine()
	eng.SetConfig(&config.Config{
		Webhooks:  config.Webhooks{SecretEnvVar: "NONEXISTENT_SECRET"},
		Providers: config.Providers{Default: "test"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:   "misconfig_test",
		Stages: []Stage{{ID: "publish", EndpointEnv: "TEST_WEBHOOK_URL"}},
		Limits: Limits{RequireReview: false},
	}

	// I2: graceful degradation — should NOT fail, just log warning
	_, err := eng.realHTTPPost(context.Background(), p, p.Stages[0], map[string]string{"x": "y"})
	if err != nil {
		t.Fatalf("realHTTPPost: %v (should degrade gracefully per I2)", err)
	}

	if receivedSignature != "" {
		t.Errorf("signature = %q, want empty (env var not set)", receivedSignature)
	}
}

// --- Sprint 12: Cache Integration Tests (D3) ---

func TestRealGenerate_CacheHit_ReturnsCachedValue(t *testing.T) {
	// Mock server that should NOT be called (cache hit = no HTTP).
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		t.Error("server should NOT be called when cache hit (D3: cost=$0)")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"text":"should not be used"}]}`))
	}))
	defer srv.Close()

	// Create temp dir for cache.
	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Paths: config.Paths{CacheDir: cacheDir},
		Budget: config.Budget{
			DailyLimitUSD:        10.0,
			CostPer1kInputTokens: 0.002,
		},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name: "cache_hit_test",
		Model: Model{
			Preferred:   "test-model",
			MaxTokens:   100,
			Temperature: 0.5,
		},
		Stages: []Stage{
			{
				ID:         "plan",
				Action:     "generate",
				OutputKey:  "plan_out",
				PromptFile: "templates/test.md",
			},
		},
		Cache: Cache{
			Enabled:    true,
			TTLMinutes: 60,
		},
	}

	// Pre-populate cache with a valid response.
	cacheKeyPayload := map[string]interface{}{
		"model":       "test-model",
		"prompt":      "Stage: plan\n",
		"max_tokens":  100,
		"temperature": 0.5,
	}
	cacheKeyJSON, _ := json.Marshal(cacheKeyPayload)
	cachedResponse := "cached response from previous run"
	if err := omniroute.CacheStore(context.Background(), cacheDir, string(cacheKeyJSON), cachedResponse, 3600); err != nil {
		t.Fatalf("CacheStore: %v", err)
	}

	// Run pipeline — should hit cache, NOT call server.
	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if got := result.Outputs["plan_out"]; got != cachedResponse {
		t.Errorf("plan_out = %q, want %q (cached value)", got, cachedResponse)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("server calls = %d, want 0 (cache hit should bypass HTTP, D3)", got)
	}
}

func TestRealGenerate_CacheMiss_CallsProviderAndStores(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "fresh response from provider"}},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Paths: config.Paths{CacheDir: cacheDir},
		Budget: config.Budget{
			DailyLimitUSD:        10.0,
			CostPer1kInputTokens: 0.002,
		},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name: "cache_miss_test",
		Model: Model{
			Preferred:   "test-model",
			MaxTokens:   100,
			Temperature: 0.5,
		},
		Stages: []Stage{
			{
				ID:         "plan",
				Action:     "generate",
				OutputKey:  "plan_out",
				PromptFile: "templates/test.md",
			},
		},
		Cache: Cache{
			Enabled:    true,
			TTLMinutes: 60,
		},
	}

	// Run pipeline — cache miss, should call provider.
	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if got := result.Outputs["plan_out"]; got != "fresh response from provider" {
		t.Errorf("plan_out = %q, want 'fresh response from provider'", got)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (cache miss should call provider)", got)
	}

	// Verify response was cached.
	cacheKeyPayload := map[string]interface{}{
		"model":       "test-model",
		"prompt":      "Stage: plan\n",
		"max_tokens":  100,
		"temperature": 0.5,
	}
	cacheKeyJSON, _ := json.Marshal(cacheKeyPayload)
	cacheHit, err := omniroute.CacheLookup(context.Background(), cacheDir, string(cacheKeyJSON))
	if err != nil {
		t.Fatalf("CacheLookup: %v", err)
	}
	if cacheHit == nil {
		t.Fatal("expected cache hit after successful store (D3)")
	}
	if cacheHit.Value != "fresh response from provider" {
		t.Errorf("cached value = %q, want 'fresh response from provider'", cacheHit.Value)
	}
}

func TestRealGenerate_CacheWriteFailure_IsNonFatal(t *testing.T) {
	// Mock server that returns success.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "response"}},
		})
	}))
	defer srv.Close()

	// Use a non-existent cache dir to force write failure.
	nonExistentCacheDir := filepath.Join(t.TempDir(), "nonexistent", "cache")

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Paths: config.Paths{CacheDir: nonExistentCacheDir}, // dir does not exist
		Budget: config.Budget{
			DailyLimitUSD:        10.0,
			CostPer1kInputTokens: 0.002,
		},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name: "cache_write_fail_test",
		Model: Model{
			Preferred:   "test-model",
			MaxTokens:   100,
			Temperature: 0.5,
		},
		Stages: []Stage{
			{
				ID:         "plan",
				Action:     "generate",
				OutputKey:  "plan_out",
				PromptFile: "templates/test.md",
			},
		},
		Cache: Cache{
			Enabled:    true,
			TTLMinutes: 60,
		},
	}

	// D3: cache write failure is non-fatal — pipeline should succeed.
	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v (D3: cache write failure should be non-fatal)", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed (D3: non-fatal)", result.Status)
	}
	if got := result.Outputs["plan_out"]; got != "response" {
		t.Errorf("plan_out = %q, want 'response'", got)
	}
}

func TestRealGenerate_CacheDisabled_DoesNotUseCache(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{"text": "response"}},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Paths: config.Paths{CacheDir: cacheDir},
		Budget: config.Budget{
			DailyLimitUSD:        10.0,
			CostPer1kInputTokens: 0.002,
		},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name: "cache_disabled_test",
		Model: Model{
			Preferred:   "test-model",
			MaxTokens:   100,
			Temperature: 0.5,
		},
		Stages: []Stage{
			{
				ID:         "plan",
				Action:     "generate",
				OutputKey:  "plan_out",
				PromptFile: "templates/test.md",
			},
		},
		Cache: Cache{
			Enabled:    false, // G3.4: cache disabled
			TTLMinutes: 60,
		},
	}

	// Run twice — should call provider both times (no cache).
	for i := 0; i < 2; i++ {
		result, err := engine.Run(context.Background(), p)
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		if result.Status != StatusCompleted {
			t.Errorf("Run %d: Status = %q, want completed", i, result.Status)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server calls = %d, want 2 (cache disabled, G3.4)", got)
	}
}

// --- Sprint 13: Real self-review Tests (G3.2 quality gate) ---

func TestRealSelfReview_Pass_PropagatesInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{
				"text": `{"decision":"PASS","reasoning":"All criteria met"}`,
			}},
		})
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Budget:    config.Budget{DailyLimitUSD: 10.0, CostPer1kInputTokens: 0.002},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:  "review_pass_test",
		Model: Model{Preferred: "test-model", MaxTokens: 100, Temperature: 0.5},
		Stages: []Stage{
			{ID: "generate", Action: "generate", OutputKey: "draft"},
			{
				ID:        "review",
				Action:    "self_review",
				DependsOn: []string{"generate"},
				InputKeys: []string{"draft"},
				OutputKey: "reviewed",
				Criteria:  []string{"factual_sources_cited", "no_prompt_leak"},
			},
		},
	}

	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	// PASS propagates the input unchanged.
	if got := result.Outputs["reviewed"]; got != result.Outputs["draft"] {
		t.Errorf("reviewed = %q, want %q (input propagated on PASS)", got, result.Outputs["draft"])
	}
}

func TestRealSelfReview_Fail_StageErrorsWithReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{
				"text": `{"decision":"FAIL","reasoning":"Prompt leaked in output"}`,
			}},
		})
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Budget:    config.Budget{DailyLimitUSD: 10.0, CostPer1kInputTokens: 0.002},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:  "review_fail_test",
		Model: Model{Preferred: "test-model", MaxTokens: 100, Temperature: 0.5},
		Stages: []Stage{
			{ID: "generate", Action: "generate", OutputKey: "draft"},
			{
				ID:        "review",
				Action:    "self_review",
				DependsOn: []string{"generate"},
				InputKeys: []string{"draft"},
				OutputKey: "reviewed",
				Criteria:  []string{"no_prompt_leak"},
			},
		},
	}

	result, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error when self_review returns FAIL")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(err.Error(), "self_review FAILED") {
		t.Errorf("error = %q, want mention of 'self_review FAILED'", err)
	}
	if !strings.Contains(err.Error(), "Prompt leaked") {
		t.Errorf("error = %q, want LLM reasoning included", err)
	}
}

func TestRealSelfReview_MalformedResponse_TreatedAsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{
				"text": `This is not JSON at all`, // G5.1: fail-safe
			}},
		})
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Budget:    config.Budget{DailyLimitUSD: 10.0, CostPer1kInputTokens: 0.002},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:  "review_malformed_test",
		Model: Model{Preferred: "test-model", MaxTokens: 100, Temperature: 0.5},
		Stages: []Stage{
			{ID: "generate", Action: "generate", OutputKey: "draft"},
			{
				ID:        "review",
				Action:    "self_review",
				DependsOn: []string{"generate"},
				InputKeys: []string{"draft"},
				Criteria:  []string{"any_criterion"},
			},
		},
	}

	_, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for malformed LLM response (G5.1 fail-safe)")
	}
	if !strings.Contains(err.Error(), "malformed LLM response") {
		t.Errorf("error = %q, want mention of 'malformed LLM response'", err)
	}
}

func TestRealSelfReview_InvalidDecision_TreatedAsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{
				"text": `{"decision":"MAYBE","reasoning":"unsure"}`, // invalid decision
			}},
		})
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Budget:    config.Budget{DailyLimitUSD: 10.0, CostPer1kInputTokens: 0.002},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:  "review_invalid_decision_test",
		Model: Model{Preferred: "test-model", MaxTokens: 100, Temperature: 0.5},
		Stages: []Stage{
			{ID: "generate", Action: "generate", OutputKey: "draft"},
			{
				ID:        "review",
				Action:    "self_review",
				DependsOn: []string{"generate"},
				InputKeys: []string{"draft"},
				Criteria:  []string{"any_criterion"},
			},
		},
	}

	_, err := engine.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for invalid decision value")
	}
	if !strings.Contains(err.Error(), "invalid decision") {
		t.Errorf("error = %q, want mention of 'invalid decision'", err)
	}
}

func TestRealSelfReview_WithMarkdownCodeFences_ParsesSuccessfully(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// LLM wraps response in markdown code fence.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{
				"text": "```json\n{\"decision\":\"PASS\",\"reasoning\":\"ok\"}\n```",
			}},
		})
	}))
	defer srv.Close()

	engine, _ := newTestEngineWithRealRouter(t, srv.URL)
	engine.SetConfig(&config.Config{
		Budget:    config.Budget{DailyLimitUSD: 10.0, CostPer1kInputTokens: 0.002},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:  "review_fences_test",
		Model: Model{Preferred: "test-model", MaxTokens: 100, Temperature: 0.5},
		Stages: []Stage{
			{ID: "generate", Action: "generate", OutputKey: "draft"},
			{
				ID:        "review",
				Action:    "self_review",
				DependsOn: []string{"generate"},
				InputKeys: []string{"draft"},
				Criteria:  []string{"c1"},
			},
		},
	}

	result, err := engine.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v (should parse markdown-fenced JSON)", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
}

func TestStubSelfReview_UsedWhenRouterIsNil(t *testing.T) {
	// When router is nil (stub mode), executeStage must call stubSelfReview
	// (pass-through) instead of realSelfReview.
	p := &Pipeline{
		Name: "stub_review_test",
		Stages: []Stage{
			{ID: "generate", Action: "generate", OutputKey: "draft"},
			{
				ID:        "review",
				Action:    "self_review",
				DependsOn: []string{"generate"},
				InputKeys: []string{"draft"},
				OutputKey: "reviewed",
				Criteria:  []string{"any_criterion"},
			},
		},
	}

	eng := NewEngine() // stub mode: router == nil
	result, err := eng.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	// Stub pass-through: reviewed == draft.
	if result.Outputs["reviewed"] != result.Outputs["draft"] {
		t.Errorf("reviewed = %q, want %q (stub pass-through)",
			result.Outputs["reviewed"], result.Outputs["draft"])
	}
}
