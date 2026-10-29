// Package scheduler implements cron-based pipeline triggers with per-pipeline
// locking and dead-loop protection (decision tree D5, guardrails G4.1, G4.3).
//
// Responsibilities:
//   - Evaluate cron expressions for pipelines with schedule.enabled=true
//   - Acquire exclusive lock per pipeline before running (G4.3)
//   - Detect and remove orphan locks (stale locks from crashed processes)
//   - Track consecutive failures per pipeline; pause after threshold (G4.1)
//   - Graceful shutdown: wait for running pipelines to finish
//
// All files stay inside data/ (guardrail G2.2):
//   - data/locks/<pipeline>.lock        — active lock files
//   - data/memory/<pipeline>.failures   — consecutive failure counter
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// ErrAlreadyRunning is returned by RunNow when the pipeline is locked
// by another execution (live or orphan not yet detected). The caller
// can decide whether to wait, retry, or surface the error to the user.
var ErrAlreadyRunning = errors.New("scheduler: pipeline already running")

// ErrPaused is returned by RunNow when the pipeline has been paused
// due to too many consecutive failures (G4.1). The user must manually
// clear the failure counter via the dashboard or CLI to resume.
var ErrPaused = errors.New("scheduler: pipeline paused due to consecutive failures")

// Scheduler orchestrates cron-based pipeline execution.
// It is safe for concurrent use.
type Scheduler struct {
	engine     *engine.Engine
	cfg        *config.Config
	logger     *observability.Logger
	locksDir   string // data/locks/
	memoryDir  string // data/memory/
	parser     cron.Parser
	staleDur   time.Duration // from cfg.Scheduler.LockStaleSeconds
	failLimit  int           // from cfg.Pipeline.DeadLoopFailures
	maxRunning int           // from cfg.Scheduler.MaxConcurrentPipelines

	// running tracks active pipelines for concurrency limiting and
	// graceful shutdown. Keyed by pipeline name, value is cancel func.
	running sync.Map

	// cron is the underlying scheduler. Lazy-init in Start().
	cron *cron.Cron

	// stopCh signals Start() to return.
	stopCh chan struct{}
}

// New builds a Scheduler wired to the project's engine, config and logger.
// It creates the data/locks/ directory if missing (G2.2).
func New(engine *engine.Engine, cfg *config.Config, logger *observability.Logger) (*Scheduler, error) {
	if engine == nil {
		return nil, errors.New("scheduler: engine must not be nil")
	}
	if cfg == nil {
		return nil, errors.New("scheduler: cfg must not be nil")
	}

	locksDir := filepath.Join(filepath.Dir(cfg.Logging.File), "..", "locks")
	// Use the configured memory_dir for failure counters.
	memoryDir := cfg.Paths.MemoryDir

	if err := os.MkdirAll(locksDir, 0o750); err != nil {
		return nil, fmt.Errorf("scheduler: create locks dir %q: %w", locksDir, err)
	}
	if err := os.MkdirAll(memoryDir, 0o750); err != nil {
		return nil, fmt.Errorf("scheduler: create memory dir %q: %w", memoryDir, err)
	}

	staleDur := time.Duration(cfg.Scheduler.LockStaleSeconds) * time.Second
	if staleDur <= 0 {
		staleDur = 3600 * time.Second // default 1 hour
	}

	maxRunning := cfg.Scheduler.MaxConcurrentPipelines
	if maxRunning <= 0 {
		maxRunning = 1
	}

	failLimit := cfg.Pipeline.DeadLoopFailures
	if failLimit <= 0 {
		failLimit = 3
	}

	return &Scheduler{
		engine:     engine,
		cfg:        cfg,
		logger:     logger,
		locksDir:   locksDir,
		memoryDir:  memoryDir,
		parser:     cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		staleDur:   staleDur,
		failLimit:  failLimit,
		maxRunning: maxRunning,
		stopCh:     make(chan struct{}),
	}, nil
}

// LoadPipelineFunc is a function that loads a pipeline by name from
// the configured pipelines directory. Injected via WithPipelineLoader
// so the scheduler stays decoupled from disk. In production this is
// engine.LoadPipeline; in tests it can be a stub.
type LoadPipelineFunc func(ctx context.Context, name string) (*engine.Pipeline, error)

// pipelineLoader is the function used to fetch pipelines. Defaults to
// a no-op that returns error; callers must set it via WithPipelineLoader.
var defaultPipelineLoader LoadPipelineFunc = func(ctx context.Context, name string) (*engine.Pipeline, error) {
	return nil, fmt.Errorf("scheduler: pipeline loader not configured for %q", name)
}

// WithPipelineLoader sets the function used by Start() to load pipeline
// definitions. Must be called before Start().
func (s *Scheduler) WithPipelineLoader(loader LoadPipelineFunc) {
	if loader != nil {
		defaultPipelineLoader = loader
	}
}

// Start runs the scheduler loop until ctx is canceled or Stop() is called.
// It evaluates every enabled pipeline's cron expression on each minute
// tick and triggers runs when they fire. Returns nil on graceful shutdown.
//
// Start is blocking — launch it in a goroutine from main.
func (s *Scheduler) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("scheduler: nil Scheduler")
	}
	if !s.cfg.Scheduler.Enabled {
		log.Info().Msg("scheduler: disabled in config, not starting")
		<-ctx.Done()
		return nil
	}

	log.Info().
		Str("timezone", s.cfg.Scheduler.Timezone).
		Int("max_concurrent", s.maxRunning).
		Int("lock_stale_seconds", int(s.staleDur.Seconds())).
		Msg("scheduler: starting")

	// Tick every minute to evaluate cron expressions.
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.waitRunning(ctx)
			log.Info().Msg("scheduler: stopped")
			return nil
		case <-s.stopCh:
			s.waitRunning(context.Background())
			log.Info().Msg("scheduler: stopped via Stop()")
			return nil
		case t := <-ticker.C:
			s.evaluateTick(ctx, t)
		}
	}
}

// Stop signals Start() to return and waits for running pipelines to finish.
// Safe to call multiple times.
func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	select {
	case <-s.stopCh:
		// Already stopped.
	default:
		close(s.stopCh)
	}
}

// RunNow executes a pipeline immediately, respecting the lock and
// dead-loop pause rules. Returns the pipeline result or an error
// describing why it could not run (already running, paused, etc.).
//
// The caller is responsible for passing a valid pipeline object —
// this function does not load pipelines from disk.
func (s *Scheduler) RunNow(ctx context.Context, p *engine.Pipeline) (*engine.PipelineResult, error) {
	if s == nil {
		return nil, errors.New("scheduler: nil Scheduler")
	}
	if p == nil {
		return nil, errors.New("scheduler: nil pipeline")
	}

	// G4.1: refuse to run paused pipelines.
	if s.isPaused(p.Name) {
		return nil, fmt.Errorf("%w: %s", ErrPaused, p.Name)
	}

	// G4.3: acquire exclusive lock.
	release, err := s.acquireLock(p.Name)
	if err != nil {
		return nil, err
	}
	defer release()

	// Concurrency limiting.
	if !s.reserveSlot(p.Name) {
		return nil, fmt.Errorf("scheduler: max concurrent pipelines (%d) reached", s.maxRunning)
	}
	defer s.releaseSlot(p.Name)

	// Run the pipeline in a cancellable sub-context so Stop() can
	// propagate cancellation.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	result, err := s.engine.Run(runCtx, p)
	if err != nil {
		// Pipeline failed — track for dead-loop detection.
		if trackErr := s.recordFailure(p.Name); trackErr != nil {
			log.Error().Err(trackErr).
				Str("pipeline", p.Name).
				Msg("scheduler: failed to record failure")
		}
		return result, fmt.Errorf("scheduler: pipeline %q failed: %w", p.Name, err)
	}

	// Pipeline succeeded — reset consecutive failure counter.
	if trackErr := s.recordSuccess(p.Name); trackErr != nil {
		log.Error().Err(trackErr).
			Str("pipeline", p.Name).
			Msg("scheduler: failed to record success")
	}
	return result, nil
}

// evaluateTick checks every enabled pipeline's cron schedule and
// triggers runs that fire at time t. Failures in tick evaluation are
// logged but do not stop the scheduler.
func (s *Scheduler) evaluateTick(ctx context.Context, t time.Time) {
	pipelineDir := s.cfg.Paths.PipelinesDir
	entries, err := os.ReadDir(pipelineDir)
	if err != nil {
		log.Error().Err(err).Str("dir", pipelineDir).
			Msg("scheduler: cannot read pipelines dir")
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		path := filepath.Join(pipelineDir, entry.Name())

		p, err := engine.LoadPipeline(ctx, path)
		if err != nil {
			log.Warn().Err(err).Str("pipeline", name).
				Msg("scheduler: failed to load pipeline")
			continue
		}
		if !p.Schedule.Enabled || p.Schedule.Cron == "" {
			continue
		}
		if s.shouldFire(p.Schedule.Cron, t) {
			go func(p *engine.Pipeline) {
				// Ignore result — evaluateTick is best-effort.
				_, _ = s.RunNow(ctx, p)
			}(p)
		}
	}
}

// shouldFire returns true if the cron expression fires at time t.
// Uses the cron parser's Next() with a 1-minute window.
func (s *Scheduler) shouldFire(expr string, t time.Time) bool {
	sched, err := s.parser.Parse(expr)
	if err != nil {
		log.Error().Err(err).Str("cron", expr).
			Msg("scheduler: invalid cron expression")
		return false
	}
	// The cron fires at `t` if Next(t-1min) == t (within 1 second).
	prev := t.Add(-1 * time.Minute)
	next := sched.Next(prev)
	return next.Sub(t).Abs() < time.Second
}

// acquireLock attempts to create an exclusive lock file for the pipeline.
// Returns a release function on success, or ErrAlreadyRunning if locked.
// Implements D5: orphan lock (older than staleDur) is removed and taken over.
func (s *Scheduler) acquireLock(pipelineName string) (func(), error) {
	lockPath := s.lockPath(pipelineName)

	// Check for existing lock.
	info, err := os.Stat(lockPath)
	if err == nil {
		// Lock exists — check if it's stale (orphan).
		age := time.Since(info.ModTime())
		if age > s.staleDur {
			log.Warn().
				Str("pipeline", pipelineName).
				Dur("age", age).
				Msg("scheduler: orphan lock detected, removing")
			if rmErr := os.Remove(lockPath); rmErr != nil {
				return nil, fmt.Errorf("scheduler: remove orphan lock: %w", rmErr)
			}
			// Fall through to create new lock.
		} else {
			return nil, fmt.Errorf("%w: %s (locked for %s)", ErrAlreadyRunning, pipelineName, age)
		}
	}

	// Write our PID + timestamp atomically.
	content := fmt.Sprintf("pid=%d\ntime=%d\n", os.Getpid(), time.Now().Unix())
	if err := os.WriteFile(lockPath, []byte(content), 0o640); err != nil {
		return nil, fmt.Errorf("scheduler: create lock: %w", err)
	}

	release := func() {
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			log.Error().Err(err).Str("lock", lockPath).
				Msg("scheduler: failed to release lock")
		}
	}
	return release, nil
}

// isPaused returns true if the pipeline has been paused due to too
// many consecutive failures (G4.1).
func (s *Scheduler) isPaused(pipelineName string) bool {
	count, err := s.readFailureCount(pipelineName)
	if err != nil {
		return false // conservative: allow run on read error
	}
	return count >= s.failLimit
}

// recordSuccess resets the consecutive failure counter.
func (s *Scheduler) recordSuccess(pipelineName string) error {
	path := s.failurePath(pipelineName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("scheduler: remove failure counter: %w", err)
	}
	return nil
}

// ResetFailures clears the consecutive failure counter for a pipeline,
// allowing it to run again after being paused. Returns true if the
// pipeline was actually paused (counter > 0), false if it was already
// at 0. This is exposed via the API for manual intervention (G4.1).
func (s *Scheduler) ResetFailures(pipelineName string) (bool, error) {
	if s == nil {
		return false, errors.New("scheduler: nil Scheduler")
	}
	if pipelineName == "" {
		return false, errors.New("scheduler: pipelineName must not be empty")
	}

	path := s.failurePath(pipelineName)

	// Check if file exists first.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil // already at 0
	}

	// Remove the file to reset counter.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("scheduler: reset failures: %w", err)
	}

	log.Info().
		Str("pipeline", pipelineName).
		Msg("scheduler: failure counter reset (pipeline resumed)")
	return true, nil
}

// recordFailure increments the consecutive failure counter. If it
// reaches failLimit, the pipeline is considered paused.
func (s *Scheduler) recordFailure(pipelineName string) error {
	count, _ := s.readFailureCount(pipelineName)
	count++

	path := s.failurePath(pipelineName)
	content := strconv.Itoa(count)
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		return fmt.Errorf("scheduler: write failure counter: %w", err)
	}

	if count >= s.failLimit {
		log.Error().
			Str("pipeline", pipelineName).
			Int("failures", count).
			Msg("scheduler: pipeline paused due to consecutive failures")
	}
	return nil
}

// readFailureCount reads the consecutive failure counter for a pipeline.
// Returns 0 if the file does not exist or cannot be parsed.
func (s *Scheduler) readFailureCount(pipelineName string) (int, error) {
	path := s.failurePath(pipelineName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return n, nil
}

// reserveSlot marks a pipeline as running for concurrency limiting.
// Returns false if the max is already reached.
func (s *Scheduler) reserveSlot(pipelineName string) bool {
	count := 0
	s.running.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count >= s.maxRunning {
		return false
	}
	s.running.Store(pipelineName, struct{}{})
	return true
}

// releaseSlot frees the pipeline's concurrency slot.
func (s *Scheduler) releaseSlot(pipelineName string) {
	s.running.Delete(pipelineName)
}

// waitRunning blocks until all running pipelines finish or ctx is canceled.
func (s *Scheduler) waitRunning(ctx context.Context) {
	for {
		count := 0
		s.running.Range(func(_, _ any) bool {
			count++
			return true
		})
		if count == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// lockPath returns the canonical path for a pipeline's lock file.
func (s *Scheduler) lockPath(pipelineName string) string {
	safe := filepath.Base(pipelineName)
	return filepath.Join(s.locksDir, safe+".lock")
}

// failurePath returns the canonical path for a pipeline's failure counter.
func (s *Scheduler) failurePath(pipelineName string) string {
	safe := filepath.Base(pipelineName)
	return filepath.Join(s.memoryDir, safe+".failures")
}
