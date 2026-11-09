// pipelines.go implements CRUD operations for pipeline YAML files.
// Sprint 15 increment 15e: Pipeline & Cron Job Visual Editor (backend).
//
// Endpoints:
// - POST /api/v1/pipelines — create new pipeline from JSON
// - PUT /api/v1/pipelines/{name} — update existing pipeline
// - DELETE /api/v1/pipelines/{name} — delete pipeline file
// - GET /api/v1/templates — list available prompt templates
//
// Guardrails enforced:
// - G2.2: pipeline name validation (no path traversal)
// - G5.1: wrapped errors with context
// - I2: log all CRUD operations
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
)

// pipelineNameRegex validates pipeline names: lowercase alphanumeric + underscore/hyphen.
// Prevents path traversal attacks (G2.2).
var pipelineNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// pipelineCreateRequest is the JSON body for POST /api/v1/pipelines.
type pipelineCreateRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schedule    scheduleRequest `json:"schedule"`
	Model       modelRequest    `json:"model"`
	Stages      []stageRequest  `json:"stages"`
	Limits      limitsRequest   `json:"limits"`
	Cache       cacheRequest    `json:"cache"`
}

type scheduleRequest struct {
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
}

type modelRequest struct {
	Preferred   string   `json:"preferred"`
	Fallbacks   []string `json:"fallbacks"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature float64  `json:"temperature"`
}

type stageRequest struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"`
	DependsOn   []string `json:"depends_on"`
	PromptFile  string   `json:"prompt_file"`
	InputKeys   []string `json:"input_keys"`
	OutputKey   string   `json:"output_key"`
	Criteria    []string `json:"criteria"`
	EndpointEnv string   `json:"endpoint_env"`
}

type limitsRequest struct {
	RequireReview      bool    `json:"require_review"`
	MaxCostPerRunUSD   float64 `json:"max_cost_per_run_usd"`
	MaxRetriesPerStage int     `json:"max_retries_per_stage"`
	DeadLoopFailures   int     `json:"dead_loop_failures"`
}

type cacheRequest struct {
	Enabled    bool `json:"enabled"`
	TTLMinutes int  `json:"ttl_minutes"`
}

// createPipeline creates a new pipeline YAML file from JSON request body.
// Validates name format (G2.2), checks for duplicates, and writes to disk.
func (h *handlers) createPipeline(w http.ResponseWriter, r *http.Request) {
	var req pipelineCreateRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: invalid JSON: %w", err))
		return
	}

	// G2.2: validate pipeline name format (prevent path traversal).
	if !pipelineNameRegex.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: invalid pipeline name %q (must match %s)",
				req.Name, pipelineNameRegex.String()))
		return
	}

	// Check if pipeline already exists.
	path := filepath.Join(h.cfg.Paths.PipelinesDir, req.Name+".yaml")
	if _, err := os.Stat(path); err == nil {
		writeError(w, http.StatusConflict,
			fmt.Errorf("api: pipeline %q already exists", req.Name))
		return
	}

	// Convert request to engine.Pipeline struct.
	p := &engine.Pipeline{
		Name:        req.Name,
		Description: req.Description,
		Schedule: engine.Schedule{
			Enabled: req.Schedule.Enabled,
			Cron:    req.Schedule.Cron,
		},
		Model: engine.Model{
			Preferred:   req.Model.Preferred,
			Fallbacks:   req.Model.Fallbacks,
			MaxTokens:   req.Model.MaxTokens,
			Temperature: req.Model.Temperature,
		},
		Limits: engine.Limits{
			RequireReview:      req.Limits.RequireReview,
			MaxCostPerRunUSD:   req.Limits.MaxCostPerRunUSD,
			MaxRetriesPerStage: req.Limits.MaxRetriesPerStage,
			DeadLoopFailures:   req.Limits.DeadLoopFailures,
		},
		Cache: engine.Cache{
			Enabled:    req.Cache.Enabled,
			TTLMinutes: req.Cache.TTLMinutes,
		},
	}

	// Convert stages.
	for _, s := range req.Stages {
		p.Stages = append(p.Stages, engine.Stage{
			ID:          s.ID,
			Action:      s.Action,
			DependsOn:   s.DependsOn,
			PromptFile:  s.PromptFile,
			InputKeys:   s.InputKeys,
			OutputKey:   s.OutputKey,
			Criteria:    s.Criteria,
			EndpointEnv: s.EndpointEnv,
		})
	}

	// Validate pipeline structure.
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: pipeline validation failed: %w", err))
		return
	}

	// Serialize to YAML.
	yamlBytes, err := yaml.Marshal(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: marshal pipeline to YAML: %w", err))
		return
	}

	// Write to disk.
	if err := os.WriteFile(path, yamlBytes, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: write pipeline file: %w", err))
		return
	}

	// I2: log creation.
	log.Info().
		Str("pipeline", req.Name).
		Str("path", path).
		Msg("api: pipeline created")

	// Return the created pipeline detail.
	writeJSON(w, http.StatusCreated, pipelineDetail{
		Pipeline: p,
		Locked:   false,
		Paused:   false,
	})
}

// updatePipeline updates an existing pipeline YAML file.
// Validates name exists, overwrites file with new content.
func (h *handlers) updatePipeline(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest,
			errors.New("api: pipeline name is required"))
		return
	}

	// G2.2: validate pipeline name format.
	if !pipelineNameRegex.MatchString(name) {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: invalid pipeline name %q", name))
		return
	}

	path := filepath.Join(h.cfg.Paths.PipelinesDir, name+".yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("api: pipeline %q not found", name))
		return
	}

	var req pipelineCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: invalid JSON: %w", err))
		return
	}

	// Ensure request name matches URL param.
	req.Name = name

	// Convert request to engine.Pipeline struct (same logic as createPipeline).
	p := &engine.Pipeline{
		Name:        req.Name,
		Description: req.Description,
		Schedule: engine.Schedule{
			Enabled: req.Schedule.Enabled,
			Cron:    req.Schedule.Cron,
		},
		Model: engine.Model{
			Preferred:   req.Model.Preferred,
			Fallbacks:   req.Model.Fallbacks,
			MaxTokens:   req.Model.MaxTokens,
			Temperature: req.Model.Temperature,
		},
		Limits: engine.Limits{
			RequireReview:      req.Limits.RequireReview,
			MaxCostPerRunUSD:   req.Limits.MaxCostPerRunUSD,
			MaxRetriesPerStage: req.Limits.MaxRetriesPerStage,
			DeadLoopFailures:   req.Limits.DeadLoopFailures,
		},
		Cache: engine.Cache{
			Enabled:    req.Cache.Enabled,
			TTLMinutes: req.Cache.TTLMinutes,
		},
	}

	for _, s := range req.Stages {
		p.Stages = append(p.Stages, engine.Stage{
			ID:          s.ID,
			Action:      s.Action,
			DependsOn:   s.DependsOn,
			PromptFile:  s.PromptFile,
			InputKeys:   s.InputKeys,
			OutputKey:   s.OutputKey,
			Criteria:    s.Criteria,
			EndpointEnv: s.EndpointEnv,
		})
	}

	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: pipeline validation failed: %w", err))
		return
	}

	yamlBytes, err := yaml.Marshal(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: marshal pipeline to YAML: %w", err))
		return
	}

	if err := os.WriteFile(path, yamlBytes, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: write pipeline file: %w", err))
		return
	}

	// I2: log update.
	log.Info().
		Str("pipeline", name).
		Str("path", path).
		Msg("api: pipeline updated")

	writeJSON(w, http.StatusOK, pipelineDetail{
		Pipeline: p,
		Locked:   h.isLocked(name),
		Paused:   h.isPaused(name),
	})
}

// deletePipeline deletes a pipeline YAML file.
func (h *handlers) deletePipeline(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest,
			errors.New("api: pipeline name is required"))
		return
	}

	// G2.2: validate pipeline name format.
	if !pipelineNameRegex.MatchString(name) {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: invalid pipeline name %q", name))
		return
	}

	path := filepath.Join(h.cfg.Paths.PipelinesDir, name+".yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("api: pipeline %q not found", name))
		return
	}

	// Check if pipeline is locked (cannot delete locked pipeline).
	if h.isLocked(name) {
		writeError(w, http.StatusConflict,
			fmt.Errorf("api: pipeline %q is locked and cannot be deleted", name))
		return
	}

	if err := os.Remove(path); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: delete pipeline file: %w", err))
		return
	}

	// I2: log deletion.
	log.Info().
		Str("pipeline", name).
		Str("path", path).
		Msg("api: pipeline deleted")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "deleted",
		"pipeline": name,
	})
}

// templateSummary is the response for GET /api/v1/templates.
type templateSummary struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int    `json:"size_bytes"`
}

// listTemplates returns all .md files in internal/pipeline/templates/.
func (h *handlers) listTemplates(w http.ResponseWriter, r *http.Request) {
	templateDir := filepath.Join("internal", "pipeline", "templates")

	entries, err := os.ReadDir(templateDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []templateSummary{})
			return
		}
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read templates dir: %w", err))
		return
	}

	var templates []templateSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(templateDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			log.Warn().Err(err).Str("path", path).
				Msg("api: failed to stat template file")
			continue
		}

		templates = append(templates, templateSummary{
			Name: strings.TrimSuffix(entry.Name(), ".md"),
			Path: path,
			Size: int(info.Size()),
		})
	}

	writeJSON(w, http.StatusOK, templates)
}

// templateContentResponse is the JSON response for GET /api/v1/templates/{name}/content.
type templateContentResponse struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// getTemplateContent reads and returns the content of a specific template file.
// Used by the TemplateBrowser UI to preview template content before selection.
// G2.2: validates that the requested path is in the whitelist (internal/pipeline/templates/).
func (h *handlers) getTemplateContent(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest,
			errors.New("api: template name is required"))
		return
	}

	// G2.2: validate template name (prevent path traversal).
	// Only allow alphanumeric, underscore, and hyphen.
	templateNameRegex := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
	if !templateNameRegex.MatchString(name) {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: invalid template name %q", name))
		return
	}

	templateDir := filepath.Join("internal", "pipeline", "templates")
	path := filepath.Join(templateDir, name+".md")

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("api: template %q not found", name))
		return
	}

	// Read content
	content, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read template content: %w", err))
		return
	}

	// I2: log the operation
	log.Debug().
		Str("template", name).
		Int("bytes", len(content)).
		Msg("api: template content served")

	writeJSON(w, http.StatusOK, templateContentResponse{
		Name:    name,
		Path:    path,
		Content: string(content),
	})
}
