package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// testCfg builds a minimal Config rooted in a temp dir.
func testCfg(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		Logging: config.Logging{
			Level:      "error",
			File:       filepath.Join(dir, "logs", "ai_loop.log"),
			MaxSizeMB:  1,
			MaxBackups: 1,
			JSON:       true,
		},
		Paths: config.Paths{
			MemoryDir:    filepath.Join(dir, "memory"),
			PipelinesDir: filepath.Join(dir, "pipelines"),
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
	}
}

// testLogger builds a logger rooted in the same temp dir.
func testLogger(t *testing.T, cfg *config.Config) *observability.Logger {
	t.Helper()
	lg, err := observability.New(cfg)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	return lg
}

// newTestScheduler builds a Scheduler wired to temp dirs.
func newTestScheduler(t *testing.T) (*Scheduler, *config.Config) {
	t.Helper()
	cfg := testCfg(t)
	lg := testLogger(t, cfg)
	eng := engine.NewEngine() // stub mode
	s, err := New(eng, cfg, lg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, cfg
}

// mkPipeline builds a minimal valid Pipeline for tests.
func mkPipeline(name string) *engine.Pipeline {
	return &engine.Pipeline{
		Name: name,
		Stages: []engine.Stage{
			{ID: "plan", Action: "generate", OutputKey: "plan"},
		},
		Limits: engine.Limits{RequireReview: true},
	}
}

// --- New ---------------------------------------------------------------

func TestNew_NilEngine(t *testing.T) {
	_, err := New(nil, &config.Config{}, nil)
	if err == nil {
		t.Fatal("expected error for nil engine")
	}
}

func TestNew_NilConfig(t *testing.T) {
	_, err := New(engine.NewEngine(), nil, nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestNew_CreatesLocksDir(t *testing.T) {
	s, cfg := newTestScheduler(t)
	info, err := os.Stat(s.locksDir)
	if err != nil {
		t.Fatalf("locks dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("locks dir is not a directory")
	}
	_ = cfg
}

// --- acquireLock / release --------------------------------------------

func TestAcquireLock_CreatesAndReleases(t *testing.T) {
	s, _ := newTestScheduler(t)
	release, err := s.acquireLock("news_blog")
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}

	// Lock file exists.
	lockPath := s.lockPath("news_blog")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatal("lock file not created")
	}

	// Release removes the file.
	release()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("lock file not removed after release")
	}
}

func TestAcquireLock_SecondCallReturnsErrAlreadyRunning(t *testing.T) {
	s, _ := newTestScheduler(t)
	release, err := s.acquireLock("news_blog")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	_, err = s.acquireLock("news_blog")
	if err == nil {
		t.Fatal("expected error for second acquire")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestAcquireLock_OrphanLockRemoved(t *testing.T) {
	s, _ := newTestScheduler(t)
	// Reduce staleDur for the test so we don't have to wait an hour.
	s.staleDur = 1 * time.Millisecond

	// Create a lock file with a very old mtime.
	lockPath := s.lockPath("news_blog")
	if err := os.WriteFile(lockPath, []byte("pid=99999\ntime=0\n"), 0o640); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Acquire should remove the orphan and succeed.
	release, err := s.acquireLock("news_blog")
	if err != nil {
		t.Fatalf("acquireLock after orphan: %v", err)
	}
	defer release()
}

// --- RunNow ------------------------------------------------------------

func TestRunNow_Success(t *testing.T) {
	s, _ := newTestScheduler(t)
	p := mkPipeline("news_blog")

	result, err := s.RunNow(context.Background(), p)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if result.Status != engine.StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}

	// Lock should be released after run.
	if _, err := os.Stat(s.lockPath("news_blog")); !os.IsNotExist(err) {
		t.Fatal("lock not released after RunNow")
	}
}

func TestRunNow_RefusesWhenAlreadyRunning(t *testing.T) {
	s, _ := newTestScheduler(t)
	p := mkPipeline("news_blog")

	// Hold the lock manually.
	release, err := s.acquireLock("news_blog")
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	defer release()

	_, err = s.RunNow(context.Background(), p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestRunNow_RefusesWhenPaused(t *testing.T) {
	s, _ := newTestScheduler(t)
	p := mkPipeline("news_blog")

	// Write failure count = failLimit.
	failPath := s.failurePath("news_blog")
	if err := os.WriteFile(failPath, []byte("3"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := s.RunNow(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for paused pipeline")
	}
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
}

func TestRunNow_NilPipeline(t *testing.T) {
	s, _ := newTestScheduler(t)
	_, err := s.RunNow(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil pipeline")
	}
}

// --- Failure tracking (G4.1) ------------------------------------------

func TestRecordFailure_IncrementsCounter(t *testing.T) {
	s, _ := newTestScheduler(t)
	for i := 1; i <= 3; i++ {
		if err := s.recordFailure("news_blog"); err != nil {
			t.Fatalf("recordFailure[%d]: %v", i, err)
		}
		count, _ := s.readFailureCount("news_blog")
		if count != i {
			t.Errorf("count = %d, want %d", count, i)
		}
	}

	if !s.isPaused("news_blog") {
		t.Error("pipeline should be paused after 3 failures")
	}
}

func TestRecordSuccess_ResetsCounter(t *testing.T) {
	s, _ := newTestScheduler(t)
	// Simulate 2 failures.
	_ = s.recordFailure("news_blog")
	_ = s.recordFailure("news_blog")

	// Record success → counter reset.
	if err := s.recordSuccess("news_blog"); err != nil {
		t.Fatalf("recordSuccess: %v", err)
	}
	count, _ := s.readFailureCount("news_blog")
	if count != 0 {
		t.Errorf("count = %d, want 0 after success", count)
	}
	if s.isPaused("news_blog") {
		t.Error("pipeline should not be paused after success")
	}
}

func TestIsPaused_NonexistentFile(t *testing.T) {
	s, _ := newTestScheduler(t)
	if s.isPaused("never_run") {
		t.Error("new pipeline should not be paused")
	}
}

// --- Start disabled ----------------------------------------------------

func TestStart_Disabled(t *testing.T) {
	s, cfg := newTestScheduler(t)
	cfg.Scheduler.Enabled = false

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := s.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
}

// --- Context cancellation ---------------------------------------------

func TestRunNow_ContextCanceled(t *testing.T) {
	s, _ := newTestScheduler(t)
	p := mkPipeline("news_blog")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := s.RunNow(ctx, p)
	// Engine.Run returns an error on canceled context; RunNow wraps it.
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

// --- Path sanitization ------------------------------------------------

func TestLockPath_SanitizesName(t *testing.T) {
	s, _ := newTestScheduler(t)
	// Path traversal attempts must not escape locksDir.
	cases := []string{
		"../../etc/passwd",
		"/etc/passwd",
		"..\\windows",
	}
	for _, name := range cases {
		path := s.lockPath(name)
		if !filepath.IsAbs(s.locksDir) {
			// Relative paths need normalization.
			abs1, _ := filepath.Abs(s.locksDir)
			abs2, _ := filepath.Abs(path)
			if !hasPrefix(abs2, abs1) {
				t.Errorf("lockPath(%q) = %q escaped %q", name, abs2, abs1)
			}
		}
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// --- Concurrency limiting --------------------------------------------

func TestReserveSlot_RespectsMax(t *testing.T) {
	s, _ := newTestScheduler(t)
	s.maxRunning = 1

	if !s.reserveSlot("a") {
		t.Fatal("first reserve should succeed")
	}
	if s.reserveSlot("b") {
		t.Fatal("second reserve should fail (max=1)")
	}
	s.releaseSlot("a")
	if !s.reserveSlot("b") {
		t.Fatal("reserve after release should succeed")
	}
}

func TestResetFailures_ResetsCounter(t *testing.T) {
	s, _ := newTestScheduler(t)

	// Simulate 3 failures (pauses the pipeline).
	for i := 0; i < 3; i++ {
		_ = s.recordFailure("news_blog")
	}

	if !s.isPaused("news_blog") {
		t.Fatal("pipeline should be paused after 3 failures")
	}

	// Reset failures.
	resumed, err := s.ResetFailures("news_blog")
	if err != nil {
		t.Fatalf("ResetFailures: %v", err)
	}
	if !resumed {
		t.Error("resumed should be true (was paused)")
	}

	// Verify counter is reset.
	if s.isPaused("news_blog") {
		t.Error("pipeline should not be paused after reset")
	}
}

func TestResetFailures_AlreadyAtZero(t *testing.T) {
	s, _ := newTestScheduler(t)

	// No failures recorded.
	resumed, err := s.ResetFailures("news_blog")
	if err != nil {
		t.Fatalf("ResetFailures: %v", err)
	}
	if resumed {
		t.Error("resumed should be false (was not paused)")
	}
}

func TestResetFailures_NilScheduler(t *testing.T) {
	var s *Scheduler
	_, err := s.ResetFailures("test")
	if err == nil {
		t.Fatal("expected error for nil scheduler")
	}
}

func TestResetFailures_EmptyName(t *testing.T) {
	s, _ := newTestScheduler(t)
	_, err := s.ResetFailures("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}
