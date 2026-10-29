package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/SEU_USUARIO/ai-loop-project/internal/memory"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/internal/scheduler"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// testEnv bundles everything needed to exercise handlers in tests.
type testEnv struct {
	cfg       *config.Config
	logger    *observability.Logger
	scheduler *scheduler.Scheduler
	memory    *memory.Store
	engine    *engine.Engine
	server    *Server
	dir       string
}

// newTestEnv builds a complete test environment in a fresh temp dir.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()

	cfg := &config.Config{
		App: config.App{
			Name:        "ai-loop-test",
			Version:     "test",
			Environment: "test",
		},
		Paths: config.Paths{
			MemoryDir:    filepath.Join(dir, "memory"),
			PipelinesDir: filepath.Join(dir, "pipelines"),
		},
		Logging: config.Logging{
			Level:      "error",
			File:       filepath.Join(dir, "logs", "ai_loop.log"),
			MaxSizeMB:  1,
			MaxBackups: 1,
			JSON:       true,
		},
		Scheduler: config.Scheduler{
			Enabled:                true,
			Timezone:               "UTC",
			MaxConcurrentPipelines: 1,
			LockStaleSeconds:       3600,
		},
		Pipeline: config.Pipeline{
			RequireReview:    true,
			DeadLoopFailures: 3,
		},
		Server: config.Server{
			Host: "127.0.0.1",
			Port: 0,
		},
	}

	for _, p := range []string{
		cfg.Paths.MemoryDir,
		cfg.Paths.PipelinesDir,
		filepath.Dir(cfg.Logging.File),
		filepath.Join(filepath.Dir(cfg.Logging.File), "..", "locks"),
	} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	lg, err := observability.New(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })

	eng := engine.NewEngine()

	sched, err := scheduler.New(eng, cfg, lg)
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	mem, err := memory.NewStore(cfg.Paths.MemoryDir)
	if err != nil {
		t.Fatalf("memory: %v", err)
	}

	srv, err := NewServer(cfg, lg, sched, mem, eng, "test-version")
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	return &testEnv{
		cfg:       cfg,
		logger:    lg,
		scheduler: sched,
		memory:    mem,
		engine:    eng,
		server:    srv,
		dir:       dir,
	}
}

// --- Health & Version -------------------------------------------------

func TestHealth_Returns200(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
}

func TestVersion_ReturnsConfiguredVersion(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest("GET", "/version", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["version"] != "test-version" {
		t.Errorf("version = %q, want test-version", body["version"])
	}
}

// --- List pipelines ---------------------------------------------------

const testPipelineYAML = `
name: news_blog
description: Test pipeline
schedule:
  enabled: true
  cron: "0 8 * * *"
model:
  preferred: test-model
  max_tokens: 100
  temperature: 0.5
stages:
  - id: plan
    action: generate
    output_key: plan
limits:
  require_review: true
  dead_loop_failures: 3
`

func TestListPipelines_Empty(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest("GET", "/api/v1/pipelines", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []pipelineSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0", len(body))
	}
}

func TestListPipelines_ReturnsAllYAMLs(t *testing.T) {
	e := newTestEnv(t)

	// Write two pipeline YAMLs.
	for _, name := range []string{"news_blog", "summarize"} {
		yaml := `name: ` + name + `
description: Test
schedule:
  enabled: true
  cron: "0 8 * * *"
model:
  preferred: test-model
  max_tokens: 100
  temperature: 0.5
stages:
  - id: plan
    action: generate
    output_key: plan
limits:
  require_review: true
  dead_loop_failures: 3
`
		path := filepath.Join(e.cfg.Paths.PipelinesDir, name+".yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/pipelines", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []pipelineSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("len = %d, want 2", len(body))
	}
}

// --- Get pipeline -----------------------------------------------------

func TestGetPipeline_Found(t *testing.T) {
	e := newTestEnv(t)
	path := filepath.Join(e.cfg.Paths.PipelinesDir, "news_blog.yaml")
	if err := os.WriteFile(path, []byte(testPipelineYAML), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/pipelines/news_blog", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestGetPipeline_NotFound(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest("GET", "/api/v1/pipelines/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// --- CORS -------------------------------------------------------------

func TestCORS_HeadersPresent(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest("OPTIONS", "/api/v1/pipelines", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Error("CORS Allow-Origin header missing")
	}
}

// --- NewServer --------------------------------------------------------

func TestNewServer_NilDependencies(t *testing.T) {
	_, err := NewServer(nil, nil, nil, nil, nil, "")
	if err == nil {
		t.Error("expected error for nil dependencies")
	}
}

func TestResetPipelineFailures_Success(t *testing.T) {
	e := newTestEnv(t)

	// Write a pipeline YAML.
	path := filepath.Join(e.cfg.Paths.PipelinesDir, "news_blog.yaml")
	if err := os.WriteFile(path, []byte(testPipelineYAML), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Directly write the failure file to simulate 3 consecutive failures.
	// This pauses the pipeline (G4.1).
	failPath := filepath.Join(e.cfg.Paths.MemoryDir, "news_blog.failures")
	if err := os.WriteFile(failPath, []byte("3"), 0o640); err != nil {
		t.Fatalf("write failure file: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/pipelines/news_blog/failures", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["resumed"] != true {
		t.Errorf("resumed = %v, want true", body["resumed"])
	}
}

func TestResetPipelineFailures_NotPaused(t *testing.T) {
	e := newTestEnv(t)

	// Write a pipeline YAML (no failures).
	path := filepath.Join(e.cfg.Paths.PipelinesDir, "news_blog.yaml")
	if err := os.WriteFile(path, []byte(testPipelineYAML), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/pipelines/news_blog/failures", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["resumed"] != false {
		t.Errorf("resumed = %v, want false", body["resumed"])
	}
}

func TestResetPipelineFailures_NotFound(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest("DELETE", "/api/v1/pipelines/nonexistent/failures", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
