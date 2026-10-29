package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/SEU_USUARIO/ai-loop-project/internal/memory"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/internal/scheduler"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// handlers groups all HTTP handlers with shared dependencies.
type handlers struct {
	cfg       *config.Config
	logger    *observability.Logger
	scheduler *scheduler.Scheduler
	memory    *memory.Store
	engine    *engine.Engine
	version   string
}

// --- Health & version -------------------------------------------------

// health returns 200 OK. Used by load balancers and uptime monitors.
func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// versionHandler returns the application version.
func (h *handlers) versionHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":     h.version,
		"environment": h.cfg.App.Environment,
	})
}

// --- Pipelines --------------------------------------------------------

// pipelineSummary is the compact representation returned in list endpoints.
type pipelineSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scheduled   bool   `json:"scheduled"`
	Cron        string `json:"cron,omitempty"`
	StagesCount int    `json:"stages_count"`
	Locked      bool   `json:"locked"`
	Paused      bool   `json:"paused"`
}

// listPipelines reads every YAML in config/pipelines/ and returns a
// list of summaries. Errors loading individual pipelines are logged
// but do not fail the request — partial results are returned.
func (h *handlers) listPipelines(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dir := h.cfg.Paths.PipelinesDir

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []pipelineSummary{})
			return
		}
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read pipelines dir: %w", err))
		return
	}

	var result []pipelineSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		path := filepath.Join(dir, entry.Name())

		p, err := engine.LoadPipeline(ctx, path)
		if err != nil {
			log.Warn().Err(err).Str("pipeline", name).
				Msg("api: failed to load pipeline")
			continue
		}

		sum := pipelineSummary{
			Name:        p.Name,
			Description: p.Description,
			Scheduled:   p.Schedule.Enabled,
			Cron:        p.Schedule.Cron,
			StagesCount: len(p.Stages),
			Locked:      h.isLocked(name),
			Paused:      h.isPaused(name),
		}
		result = append(result, sum)
	}

	writeJSON(w, http.StatusOK, result)
}

// pipelineDetail is the full representation returned in detail endpoints.
// Reuses engine.Pipeline directly (all fields are exported).
type pipelineDetail struct {
	*engine.Pipeline
	Locked bool `json:"locked"`
	Paused bool `json:"paused"`
}

func (h *handlers) getPipeline(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest,
			errors.New("api: pipeline name is required"))
		return
	}

	path := filepath.Join(h.cfg.Paths.PipelinesDir, name+".yaml")
	p, err := engine.LoadPipeline(r.Context(), path)
	if err != nil {
		// Check if it's a "file not found" error.
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			writeError(w, http.StatusNotFound,
				fmt.Errorf("api: pipeline %q not found", name))
			return
		}
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: load pipeline: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, pipelineDetail{
		Pipeline: p,
		Locked:   h.isLocked(name),
		Paused:   h.isPaused(name),
	})
}

// --- Pipeline execution ---------------------------------------------

// runPipeline triggers an immediate execution of the named pipeline
// via scheduler.RunNow. Returns the pipeline result on success.
func (h *handlers) runPipeline(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest,
			errors.New("api: pipeline name is required"))
		return
	}

	path := filepath.Join(h.cfg.Paths.PipelinesDir, name+".yaml")
	p, err := engine.LoadPipeline(r.Context(), path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound,
				fmt.Errorf("api: pipeline %q not found", name))
			return
		}
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: load pipeline: %w", err))
		return
	}

	result, err := h.scheduler.RunNow(r.Context(), p)
	if err != nil {
		// Map well-known scheduler errors to appropriate HTTP codes.
		switch {
		case errors.Is(err, scheduler.ErrAlreadyRunning):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, scheduler.ErrPaused):
			writeError(w, http.StatusForbidden, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   string(result.Status),
		"pipeline": result.PipelineName,
		"outputs":  result.Outputs,
	})
}

// --- Pipeline memory --------------------------------------------------

// getPipelineMemory returns the last N events for a pipeline.
// The `limit` query param controls N (default 50, max 500).
func (h *handlers) getPipelineMemory(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest,
			errors.New("api: pipeline name is required"))
		return
	}

	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest,
				fmt.Errorf("api: invalid limit %q", q))
			return
		}
		if n > 500 {
			n = 500
		}
		limit = n
	}

	events, err := h.memory.LastN(r.Context(), name, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read memory: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pipeline": name,
		"count":    len(events),
		"events":   events,
	})
}

// --- Helpers ----------------------------------------------------------

// isLocked returns true if a lock file exists for the pipeline.
func (h *handlers) isLocked(pipelineName string) bool {
	lockPath := filepath.Join(h.locksDir(), pipelineName+".lock")
	_, err := os.Stat(lockPath)
	return err == nil
}

// isPaused returns true if the pipeline has too many consecutive failures.
func (h *handlers) isPaused(pipelineName string) bool {
	failPath := filepath.Join(h.cfg.Paths.MemoryDir, pipelineName+".failures")
	data, err := os.ReadFile(failPath)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	return n >= h.cfg.Pipeline.DeadLoopFailures
}

// locksDir returns the canonical locks directory (same as scheduler uses).
func (h *handlers) locksDir() string {
	// Mirror scheduler's logic: data/locks/
	return filepath.Join(filepath.Dir(h.cfg.Logging.File), "..", "locks")
}

// writeJSON marshals v to JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("api: failed to encode response")
	}
}

// writeError writes a structured error response.
// Guardrail G2.1: error messages are sanitized — no secrets leak.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{
		"error": err.Error(),
	})
}
