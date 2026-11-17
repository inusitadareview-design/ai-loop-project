// loop.go implements the pipeline executor (Maestro).
// Sprint 4 implementation: sequential execution with real AI generation
// via router.Client. Memory integration comes in Sprint 5.
//
// Guardrails enforced:
// - G4.1: Dead-loop protection (stub — full implementation in Sprint 5)
// - G4.2: State persisted after each stage (stub — in-memory only for now)
// - G3.2: require_review respected (stub — logs instead of saving draft)
// - G2.1: Secrets masked by router.Client's tracer
// - I1: Token budget enforced by router.Client
package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath" // NOVO
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/SEU_USUARIO/ai-loop-project/internal/events"
	"github.com/SEU_USUARIO/ai-loop-project/internal/memory"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/internal/omniroute"
	"github.com/SEU_USUARIO/ai-loop-project/internal/router"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// DraftPublish describes a draft to be published via webhook.
// Used by the API layer after human approval (G3.2).
type DraftPublish struct {
	PipelineName string
	StageID      string
	EndpointEnv  string
	Inputs       map[string]string
}

// PipelineStatus represents the final state of a pipeline run.
type PipelineStatus string

const (
	StatusCompleted PipelineStatus = "completed"
	StatusFailed    PipelineStatus = "failed"
	StatusCancelled PipelineStatus = "cancelled"
	StatusPaused    PipelineStatus = "paused" // G4.1: dead-loop protection
)

// PipelineResult holds the final state of a pipeline run.
type PipelineResult struct {
	PipelineName string
	Status       PipelineStatus
	Outputs      map[string]string
	Error        error
}

// Draft represents a pipeline output waiting for human approval (G3.2).
// Serialized to JSON in data/outputs/<pipeline>.<timestamp>.draft.json.
type Draft struct {
	ID              string            `json:"id"`
	Pipeline        string            `json:"pipeline"`
	CreatedAt       time.Time         `json:"created_at"`
	StageID         string            `json:"stage_id"`
	EndpointEnv     string            `json:"endpoint_env,omitempty"`
	Inputs          map[string]string `json:"inputs"`
	AllOutputs      map[string]string `json:"all_outputs"`
	ReviewCriteria  []string          `json:"review_criteria,omitempty"`  // NEW: Sprint 15 15d
	ReviewReasoning string            `json:"review_reasoning,omitempty"` // NEW: Sprint 15 15d (placeholder for 15d-p2)
}

// Engine orchestrates pipeline execution.
// It can operate in two modes:
// - Stub mode: no router.Client, generate actions return placeholders
// - Real mode: router.Client provided, generate actions make HTTP calls
type Engine struct {
	router      *router.Client
	resolver    *router.Resolver
	outputsDir  string // absolute path where drafts are saved (injected by main)
	broadcaster events.Broadcaster
	cfg         *config.Config
	store       *memory.Store // persists pipeline events (I5, G4.2)
}

// SetConfig injects the runtime configuration for provider fallback chain
// and other settings needed by realGenerate. Must be called before Run().
func (e *Engine) SetConfig(cfg *config.Config) {
	e.cfg = cfg
}

// NewEngine creates a pipeline engine in stub mode (no real AI calls).
// Use NewEngineWithDeps for production use with real router.Client.
func NewEngine() *Engine {
	return &Engine{
		router:      nil,
		resolver:    nil,
		broadcaster: events.NoopBroadcaster{},
		store:       nil, // injected via SetStore; nil means no memory persistence
	}
}

// SetOutputsDir sets the absolute path where drafts will be saved.
// Must be called before Run() if you want draft persistence.
// If empty, drafts will be saved to "data/outputs" relative to cwd.
func (e *Engine) SetOutputsDir(path string) {
	e.outputsDir = path
}

// NewEngineWithDeps creates a pipeline engine with real dependencies.
// If router or resolver is nil, the engine falls back to stub mode
// for generate actions.
func NewEngineWithDeps(router *router.Client, resolver *router.Resolver) *Engine {
	return &Engine{
		router:      router,
		resolver:    resolver,
		broadcaster: events.NoopBroadcaster{},
		store:       nil, // injected via SetStore; nil means no memory persistence
	}
}

// SetBroadcaster sets the event broadcaster used to emit draft_created
// events when a pipeline with require_review=true saves a draft (G3.2).
// Optional: if nil, a NoopBroadcaster is used.
func (e *Engine) SetBroadcaster(b events.Broadcaster) {
	if b == nil {
		e.broadcaster = events.NoopBroadcaster{}
		return
	}
	e.broadcaster = b
}

// SetStore sets the memory store used to persist pipeline events (I5, G4.2).
// Optional: if nil, no events are persisted (engine still runs normally).
// Failures writing to the store are logged as warnings but do NOT fail
// the pipeline — observability must not cause outage (I2).
func (e *Engine) SetStore(store *memory.Store) {
	e.store = store
}

// recordEvent appends an event to the memory store if one is configured.
// Failures are logged but never propagated — the pipeline's success
// must not depend on observability infrastructure (I2).
func (e *Engine) recordEvent(ctx context.Context, ev memory.Event) {
	if e.store == nil {
		return
	}
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().Unix()
	}
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	if err := e.store.Append(ctx, ev.PipelineID, ev); err != nil {
		log.Warn().
			Err(err).
			Str("event_id", ev.ID).
			Str("event_type", ev.Type).
			Msg("engine: failed to record event in memory (non-fatal)")
	}
}

// Run executes a pipeline once, sequentially through all stages.
// It respects context cancellation and returns a PipelineResult
// with the final status and all stage outputs.
//
// Sprint 4 implementation:
// - generate: calls router.Client if available, otherwise returns stub
// - self_review: passes through input (real validation in future sprint)
// - http_post: logs instead of sending (real HTTP in future sprint)
//
// Sprint 5+ will add:
// - State persistence to disk
// - Dead-loop protection across runs
// - Memory integration
func (e *Engine) Run(ctx context.Context, p *Pipeline) (*PipelineResult, error) {
	if p == nil {
		return nil, fmt.Errorf("engine: nil pipeline")
	}

	result := &PipelineResult{
		PipelineName: p.Name,
		Status:       StatusCompleted,
		Outputs:      make(map[string]string),
	}

	log.Info().
		Str("pipeline", p.Name).
		Int("stages", len(p.Stages)).
		Bool("real_mode", e.router != nil).
		Msg("engine: starting pipeline run")

	// Record pipeline start in memory (I5, G4.2).
	e.recordEvent(ctx, memory.Event{
		ID:         fmt.Sprintf("%s-start-%d", p.Name, time.Now().UnixNano()),
		PipelineID: p.Name,
		Type:       "pipeline_started",
		Timestamp:  time.Now().Unix(),
		Data: map[string]interface{}{
			"stages_count": len(p.Stages),
			"real_mode":    e.router != nil,
		},
	})

	for _, stage := range p.Stages {
		// Check context before each stage (G5.1: explicit error handling).
		if err := ctx.Err(); err != nil {
			result.Status = StatusCancelled
			result.Error = fmt.Errorf("engine: pipeline %q cancelled: %w", p.Name, err)
			log.Warn().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Msg("engine: pipeline cancelled")

			// Record cancellation in memory (I5).
			e.recordEvent(ctx, memory.Event{
				ID:         fmt.Sprintf("%s-cancel-%d", p.Name, time.Now().UnixNano()),
				PipelineID: p.Name,
				Type:       "pipeline_cancelled",
				Timestamp:  time.Now().Unix(),
				Data: map[string]interface{}{
					"error": result.Error.Error(),
				},
			})

			return result, result.Error
		}

		output, err := e.executeStage(ctx, p, stage, result.Outputs)
		if err != nil {
			result.Status = StatusFailed
			result.Error = fmt.Errorf("engine: stage %q failed: %w", stage.ID, err)

			log.Error().
				Err(err).
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Msg("engine: stage failed")

			// Record stage failure in memory (I5).
			e.recordEvent(ctx, memory.Event{
				ID:         fmt.Sprintf("%s-%s-fail-%d", p.Name, stage.ID, time.Now().UnixNano()),
				PipelineID: p.Name,
				StageID:    stage.ID,
				Type:       "stage_failed",
				Timestamp:  time.Now().Unix(),
				Data: map[string]interface{}{
					"error": result.Error.Error(),
				},
			})

			return result, result.Error
		}

		// Store output by the stage's output_key.
		if stage.OutputKey != "" {
			result.Outputs[stage.OutputKey] = output
		}

		log.Info().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Str("output_key", stage.OutputKey).
			Msg("engine: stage completed")

		// Record stage completion in memory (I5).
		e.recordEvent(ctx, memory.Event{
			ID:         fmt.Sprintf("%s-%s-ok-%d", p.Name, stage.ID, time.Now().UnixNano()),
			PipelineID: p.Name,
			StageID:    stage.ID,
			Type:       "stage_completed",
			Timestamp:  time.Now().Unix(),
			Data: map[string]interface{}{
				"output_key": stage.OutputKey,
			},
		})
	}

	log.Info().
		Str("pipeline", p.Name).
		Msg("engine: pipeline completed successfully")

	// Record pipeline completion in memory (I5, G4.2).
	e.recordEvent(ctx, memory.Event{
		ID:         fmt.Sprintf("%s-end-%d", p.Name, time.Now().UnixNano()),
		PipelineID: p.Name,
		Type:       "pipeline_completed",
		Timestamp:  time.Now().Unix(),
		Data: map[string]interface{}{
			"status": result.Status,
		},
	})

	return result, nil
}

// executeStage runs a single stage based on its action type.
// It makes ALL previous stage outputs available to the stage action,
// regardless of whether `input_keys` is declared — this matches the
// `news_blog.yaml` convention where `review` uses `depends_on: [execute]`
// without explicitly declaring `input_keys: [draft]`.
func (e *Engine) executeStage(
	ctx context.Context,
	p *Pipeline,
	stage Stage,
	allOutputs map[string]string,
) (string, error) {
	// Build the effective input map for this stage.
	// Strategy: if stage.InputKeys is non-empty, use only those keys
	// (explicit contract). If empty, pass all available outputs from
	// stages that this stage depends on (implicit contract via depends_on).
	inputs := make(map[string]string)
	if len(stage.InputKeys) > 0 {
		// Explicit: only the declared keys.
		for _, key := range stage.InputKeys {
			if val, ok := allOutputs[key]; ok {
				inputs[key] = val
			}
		}
	} else {
		// Implicit: gather outputs from all depended-on stages.
		for _, depID := range stage.DependsOn {
			depStage := p.StageByID(depID)
			if depStage == nil {
				continue
			}
			if depStage.OutputKey != "" {
				if val, ok := allOutputs[depStage.OutputKey]; ok {
					inputs[depStage.OutputKey] = val
				}
			}
		}
	}

	switch stage.Action {
	case "generate":
		if e.router != nil && e.resolver != nil {
			return e.realGenerate(ctx, p, stage, inputs)
		}
		return e.stubGenerate(ctx, p, stage)
	case "self_review":
		if e.router != nil && e.resolver != nil {
			return e.realSelfReview(ctx, p, stage, inputs)
		}
		return e.stubSelfReview(ctx, stage, inputs)
	case "http_post":
		return e.stubHTTPPost(ctx, p, stage, inputs)
	case "git_publish":
		return e.gitPublish(ctx, p, stage, inputs)
	case "web_fetch": // ← NOVO
		return e.webFetch(ctx, p, stage, inputs) // ← NOVO
	default:
		return "", fmt.Errorf("unknown action %q", stage.Action)
	}
}

// realGenerate makes an actual HTTP call to the AI provider via router.Client.
// It resolves the provider URL, builds the payload JSON, and extracts the
// generated text from the response.
func (e *Engine) realGenerate(ctx context.Context, p *Pipeline, stage Stage, inputs map[string]string) (string, error) {
	if e.cfg == nil {
		return "", fmt.Errorf("engine: config not set (call SetConfig before Run)")
	}

	// Sprint 17e-p4 (grounding): when this stage receives both the raw news
	// feed and the editor plan, deterministically narrow the news to the
	// items selected by the plan (URL match, no LLM). The writer must never
	// see discarded items — prevents cross-contamination and fits I1.
	if newsJSON, hasNews := inputs["news"]; hasNews {
		if planJSON, hasPlan := inputs["plan"]; hasPlan {
			grounded, selectedCount, gErr := selectNewsByPlan(newsJSON, planJSON)
			if gErr != nil {
				return "", fmt.Errorf("stage %q: %w", stage.ID, gErr)
			}
			inputs["news"] = grounded
			log.Info().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int("selected_items", selectedCount).
				Msg("engine: news grounded to plan selection (deterministic)")
		}
	}

	// 1. Build fallback chain with provider names.
	endpoints, err := router.BuildFallbackChain(ctx, e.resolver, e.cfg)
	if err != nil {
		return "", fmt.Errorf("build fallback chain: %w", err)
	}

	// 3. Build prompt and payload.
	// Sprint 14: load template from stage.PromptFile if specified.
	var prompt string
	if stage.PromptFile != "" {
		// Load and render template with stage data and inputs.
		templateData := buildTemplateData(p, stage, inputs)
		renderedPrompt, err := loadAndRenderTemplate(ctx, stage.PromptFile, templateData)
		if err != nil {
			return "", fmt.Errorf("load template for stage %q: %w", stage.ID, err)
		}
		prompt = renderedPrompt

		// Sprint 17d: debug log para investigar alucinação
		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Str("prompt_file", stage.PromptFile).
			Int("input_keys_count", len(inputs)).
			Strs("input_keys", func() []string {
				keys := make([]string, 0, len(inputs))
				for k := range inputs {
					keys = append(keys, k)
				}
				return keys
			}()).
			Int("prompt_length_chars", len(prompt)).
			Msg("engine: template rendered (debug)")

		// Log do input (primeiros 500 chars para ver se tem notícias reais)
		for key, val := range inputs {
			log.Debug().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Str("input_key", key).
				Int("value_length", len(val)).
				Str("value_preview", truncate(val, 500)).
				Msg("engine: input preview (debug)")
		}
	} else {
		// Fallback: hardcoded prompt (backward compatibility).
		prompt = fmt.Sprintf("Stage: %s\n", stage.ID)
		for key, val := range inputs {
			prompt += fmt.Sprintf("Input %s: %s\n", key, val)
		}
		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Msg("engine: using hardcoded prompt (no template)")
	}

	payload := map[string]interface{}{
		"model":       p.Model.Preferred,
		"prompt":      prompt,
		"max_tokens":  p.Model.MaxTokens,
		"temperature": p.Model.Temperature,
	}

	bodyBytes, err := json.Marshal(payload)

	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	// 3.1 Resolve per-provider targets: endpoint path + API key + model ID +
	// payload shape. Needs the rendered prompt, so it runs after step 3.
	names := make([]string, len(endpoints))
	baseURLs := make([]string, len(endpoints))
	for i, ep := range endpoints {
		names[i], baseURLs[i] = ep.Name, ep.URL
	}
	tg, err := buildProviderTargets(names, baseURLs, p, prompt, p.Model.Temperature)
	if err != nil {
		return "", fmt.Errorf("build provider targets: %w", err)
	}

	// 3.3 (NEW) Cache lookup (D3: cache decision tree).
	// If cache is enabled and we have a valid cache dir, try to serve from cache.
	// Cache hit = cost $0 (bypasses token budget validation entirely, I1).
	if p.Cache.Enabled && e.cfg != nil {
		cacheDir := e.cfg.Paths.CacheDir
		if cacheDir != "" {
			// Build deterministic cache key from request parameters.
			// Must include all fields that affect the response.
			cacheKeyPayload := map[string]interface{}{
				"model":       payload["model"],
				"prompt":      payload["prompt"],
				"max_tokens":  payload["max_tokens"],
				"temperature": payload["temperature"],
			}
			cacheKeyJSON, err := json.Marshal(cacheKeyPayload)
			if err == nil {
				cacheHit, err := omniroute.CacheLookup(ctx, cacheDir, string(cacheKeyJSON))
				if err == nil && cacheHit != nil {
					// D3: valid hit — serve from cache, cost = $0.
					log.Info().
						Str("pipeline", p.Name).
						Str("stage", stage.ID).
						Int64("remaining_ttl_secs", cacheHit.RemainingTTLSecs).
						Msg("engine: cache hit, serving from cache (D3, cost=$0)")

					// Record cache hit event in memory store (I2 observability).
					if e.store != nil {
						e.recordEvent(ctx, memory.Event{
							ID:         fmt.Sprintf("%s-%s-cache-hit-%d", p.Name, stage.ID, time.Now().UnixNano()),
							PipelineID: p.Name,
							StageID:    stage.ID,
							Type:       "cache_hit",
							Timestamp:  time.Now().Unix(),
							Data: map[string]interface{}{
								"remaining_ttl_secs": cacheHit.RemainingTTLSecs,
								"cost_usd":           0.0, // cache hit = $0
							},
						})
					}

					return cacheHit.Value, nil
				}
				// Cache miss or expired — continue to provider call (D3).
				if err == nil {
					log.Debug().
						Str("pipeline", p.Name).
						Str("stage", stage.ID).
						Msg("engine: cache miss or expired, calling provider (D3)")
				}
			}
		}
	}

	// 3.4 (NEW) Read spent_usd from memory store for budget enforcement (G1.1).
	var spentUSD float64
	if e.store != nil {
		var err error
		spentUSD, err = e.store.GetSpentToday(ctx)
		if err != nil {
			log.Warn().Err(err).Str("pipeline", p.Name).Msg("engine: failed to read spent_usd (using 0.0)")
			spentUSD = 0.0
		}
	}

	// 3.5 (I1 — Token Pre-Validation). NO HTTP request touches the network
	// without this check. Uses REAL spent_usd from memory store (G1.1).
	// Log budget parameters for debugging
	log.Debug().
		Str("pipeline", p.Name).
		Float64("daily_limit_usd", e.cfg.Budget.DailyLimitUSD).
		Float64("spent_usd", spentUSD).
		Float64("cost_per_1k_input_tokens", e.cfg.Budget.CostPer1kInputTokens).
		Int("max_tokens", p.Model.MaxTokens).
		Msg("engine: calling ValidateTokenBudget with parameters")

	budgetResult, err := omniroute.ValidateTokenBudget(
		prompt,
		e.cfg.Budget.DailyLimitUSD,
		spentUSD, // REAL spent_usd from memory store (G1.1)
		e.cfg.Budget.CostPer1kInputTokens,
		p.Model.MaxTokens,
	)
	if err != nil {
		// FFI failure — log and fail the stage (D7 — FFI decision tree).
		log.Error().Err(err).Str("pipeline", p.Name).Msg("engine: token-budget FFI failed")
		return "", fmt.Errorf("validate token budget: %w", err)
	}
	if !budgetResult.Fits {
		// I1: prompt exceeds budget — fail explicitly (no blind retry).
		log.Warn().
			Str("pipeline", p.Name).
			Int("estimated_tokens", budgetResult.EstimatedTokens).
			Float64("spent_usd", spentUSD).
			Float64("projected_cost_usd", budgetResult.ProjectedCostUSD).
			Msg("engine: prompt exceeds token budget")
		return "", fmt.Errorf("prompt exceeds token budget: estimated=%d, spent=%.4f, projected=%.4f",
			budgetResult.EstimatedTokens, spentUSD, budgetResult.ProjectedCostUSD)
	}

	log.Debug().
		Str("pipeline", p.Name).
		Int("estimated_tokens", budgetResult.EstimatedTokens).
		Float64("spent_usd", spentUSD).
		Float64("projected_cost_usd", budgetResult.ProjectedCostUSD).
		Msg("engine: token budget validated (I1)")

	// 4. Call router with full fallback chain.
	// Add pipeline_id to context for tracer observability (I2).
	callCtx := observability.WithPipelineID(ctx, p.Name)

	// Debug: mostrar deadline do contexto pai
	if deadline, ok := ctx.Deadline(); ok {
		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Time("ctx_deadline", deadline).
			Dur("ctx_remaining", time.Until(deadline)).
			Int("req_timeout_seconds", e.cfg.Providers.TimeoutSeconds).
			Msg("engine: context has deadline (may limit effective timeout)")
	} else {
		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Int("req_timeout_seconds", e.cfg.Providers.TimeoutSeconds).
			Msg("engine: context has no deadline (req.Timeout will apply)")
	}

	resp, err := e.router.Call(callCtx, router.CallRequest{
		PipelineID:  p.Name,
		Method:      "POST",
		URLs:        tg.URLs,
		AuthHeaders: tg.AuthHeaders,
		Bodies:      tg.Bodies,
		Body:        bodyBytes,
		AuthHeader:  tg.LegacyAuth,
		Timeout:     time.Duration(e.cfg.Providers.TimeoutSeconds) * time.Second,
	})

	if err != nil {
		return "", fmt.Errorf("router call: %w", err)
	}

	// 5. Parse response via the shared tolerant extractor (all shapes).
	responseText, finishReason, completionTokens, err := extractProviderText(resp.Body, resp.ProviderUsed)
	if err != nil {
		return "", err
	}

	log.Info().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Str("provider_used", resp.ProviderUsed).
		Str("finish_reason", finishReason).
		Int("completion_tokens", completionTokens).
		Int("extracted_chars", len(responseText)).
		Int("extracted_words", len(strings.Fields(responseText))).
		Msg("engine: provider response parsed")

	// A truncated reasoning block can make sanitizeOutput return "".
	// That is provider truncation, NOT hallucination (G5.1, I2).
	rawLen := len(responseText)
	responseText = sanitizeOutput(responseText)
	if responseText == "" {
		return "", fmt.Errorf("provider %s returned no usable content after sanitization (raw_len=%d, finish_reason=%q): probable max_tokens truncation inside reasoning block",
			resp.ProviderUsed, rawLen, finishReason)
	}

	// Sprint 17e-p2b: a truncated reasoning block (unclosed thinking tag) can
	// make sanitizeOutput return "". That is a provider truncation failure,
	// NOT a hallucination — report it precisely instead of letting URL
	// validation misdiagnose it (G5.1, I2).
	sanitized := sanitizeOutput(responseText)
	if sanitized == "" {
		return "", fmt.Errorf("provider %s returned no usable content after sanitization (raw_len=%d): probable max_tokens truncation inside reasoning block",
			resp.ProviderUsed, len(responseText))
	}

	responseText = sanitized

	// Prepare allInputs for URL validation (used in initial and refinement passes)
	var allInputs string
	for _, v := range inputs {
		allInputs += v + "\n"
	}

	// Sprint 17d: validate URLs to prevent hallucination (I1 enforcement)
	// Only validate if this stage has input AND generates markdown articles.
	// Planner stages (plan_sections) generate JSON structures, not articles,
	// so URL validation is skipped for them.
	shouldValidateURLs := len(inputs) > 0 && stage.ID != "plan_sections"

	if shouldValidateURLs {
		// Sprint 17d: Step 1 — ensure model cited at least one input URL.
		// Catches cases where model ignores input entirely (PR #26).
		if err := validateURLPresence(allInputs, responseText); err != nil {
			log.Warn().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Err(err).
				Msg("engine: URL presence validation failed (model ignored input)")
			return "", fmt.Errorf("URL presence validation failed: %w", err)
		}

		// Sprint 17d: Step 2 — ensure no hallucinated URLs were invented.
		if err := validateURLs(allInputs, responseText); err != nil {
			log.Warn().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Err(err).
				Msg("engine: URL validation failed (hallucination detected)")
			return "", fmt.Errorf("URL validation failed: %w", err)
		}

		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Msg("engine: URL validation passed (presence + no hallucination)")
	} else if len(inputs) > 0 {
		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Msg("engine: URL validation skipped (planner stage generates JSON, not article)")
	}

	// 5.5 (NEW) Record cost in memory store for budget tracking (G1.1, I5).
	// Only record on successful call — failed calls don't cost money.
	if e.store != nil && budgetResult.ProjectedCostUSD > 0 {
		providerName := ""
		if len(endpoints) > 0 {
			providerName = endpoints[0].Name
		}
		costEvent := memory.Event{
			ID:         fmt.Sprintf("%s-%s-cost-%d", p.Name, stage.ID, time.Now().UnixNano()),
			PipelineID: p.Name,
			StageID:    stage.ID,
			Type:       "http_call_cost",
			Timestamp:  time.Now().Unix(),
			Data: map[string]interface{}{
				"cost_usd":         budgetResult.ProjectedCostUSD,
				"estimated_tokens": budgetResult.EstimatedTokens,
				"provider":         providerName,
				"stage_id":         stage.ID,
			},
		}
		e.recordEvent(ctx, costEvent)

		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Float64("cost_usd", budgetResult.ProjectedCostUSD).
			Int("estimated_tokens", budgetResult.EstimatedTokens).
			Str("provider", providerName).
			Msg("engine: cost recorded in memory (G1.1)")
	}

	log.Debug().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Bool("config_is_nil", stage.Config == nil).
		Int("config_keys_count", len(stage.Config)).
		Strs("config_keys", func() []string {
			keys := make([]string, 0, len(stage.Config))
			for k := range stage.Config {
				keys = append(keys, k)
			}
			return keys
		}()).
		Msg("engine: pre-contract check (debug)")

	// --- OUTPUT CONTRACT REFINEMENT LOOP (Alavanca B) ---
	// Sprint 17e-p3: deterministic quality gate. If the output violates the
	// contract, send a corrective prompt to the model (I3 refinement cycle).
	contract, contractEnabled, contractErr := parseOutputContract(stage.Config)
	if contractErr != nil {
		return "", fmt.Errorf("parse output contract: %w", contractErr)
	}

	if contractEnabled {
		maxRetries := p.Limits.MaxRetriesPerStage
		if maxRetries <= 0 {
			maxRetries = 2 // sensible default if not configured
		}

		for attempt := 1; attempt <= maxRetries; attempt++ {
			violations := contract.validate(responseText)
			if len(violations) == 0 {
				if attempt > 1 {
					log.Info().
						Str("pipeline", p.Name).
						Str("stage", stage.ID).
						Int("attempt", attempt-1).
						Msg("engine: output contract met after refinement")
				}
				break // Contract met, exit loop
			}

			if attempt == maxRetries {
				log.Warn().
					Str("pipeline", p.Name).
					Str("stage", stage.ID).
					Strs("violations", violations).
					Msg("engine: output contract violations persist after max retries")
				return "", fmt.Errorf("output contract failed after %d retries: %v", maxRetries, violations)
			}

			// Record refinement event in memory (I5)
			e.recordEvent(ctx, memory.Event{
				ID:         fmt.Sprintf("%s-%s-refine-%d-%d", p.Name, stage.ID, attempt, time.Now().UnixNano()),
				PipelineID: p.Name,
				StageID:    stage.ID,
				Type:       "output_refinement",
				Timestamp:  time.Now().Unix(),
				Data: map[string]interface{}{
					"attempt":    attempt,
					"violations": violations,
				},
			})

			correctivePrompt := buildCorrectivePrompt(responseText, violations)

			// Re-validate token budget for the corrective prompt (I1)
			var currentSpentUSD float64
			if e.store != nil {
				currentSpentUSD, _ = e.store.GetSpentToday(ctx)
			}
			refineBudgetResult, err := omniroute.ValidateTokenBudget(
				correctivePrompt,
				e.cfg.Budget.DailyLimitUSD,
				currentSpentUSD,
				e.cfg.Budget.CostPer1kInputTokens,
				p.Model.MaxTokens,
			)
			if err != nil {
				return "", fmt.Errorf("validate token budget for refinement: %w", err)
			}
			if !refineBudgetResult.Fits {
				return "", fmt.Errorf("refinement prompt exceeds token budget: estimated=%d", refineBudgetResult.EstimatedTokens)
			}

			// Build provider targets for the corrective prompt
			refineTG, err := buildProviderTargets(names, baseURLs, p, correctivePrompt, p.Model.Temperature)
			if err != nil {
				return "", fmt.Errorf("build provider targets for refinement: %w", err)
			}

			refinePayload := map[string]interface{}{
				"model":       p.Model.Preferred,
				"prompt":      correctivePrompt,
				"max_tokens":  p.Model.MaxTokens,
				"temperature": p.Model.Temperature,
			}
			refineBodyBytes, err := json.Marshal(refinePayload)
			if err != nil {
				return "", fmt.Errorf("marshal refinement payload: %w", err)
			}

			log.Info().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int("attempt", attempt).
				Strs("violations", violations).
				Msg("engine: sending corrective refinement prompt")

			// Call router for refinement
			refineResp, err := e.router.Call(callCtx, router.CallRequest{
				PipelineID:  p.Name,
				Method:      "POST",
				URLs:        refineTG.URLs,
				AuthHeaders: refineTG.AuthHeaders,
				Bodies:      refineTG.Bodies,
				Body:        refineBodyBytes,
				AuthHeader:  refineTG.LegacyAuth,
				Timeout:     time.Duration(e.cfg.Providers.TimeoutSeconds) * time.Second,
			})
			if err != nil {
				return "", fmt.Errorf("router call for refinement: %w", err)
			}

			// Parse refinement response
			refineText, refineFinish, refineTokens, err := extractProviderText(refineResp.Body, refineResp.ProviderUsed)
			if err != nil {
				return "", fmt.Errorf("extract refinement text: %w", err)
			}

			log.Info().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int("attempt", attempt).
				Str("finish_reason", refineFinish).
				Int("completion_tokens", refineTokens).
				Msg("engine: refinement response parsed")

			rawRefineLen := len(refineText)
			refineText = sanitizeOutput(refineText)
			if refineText == "" {
				return "", fmt.Errorf("refinement: provider returned no usable content (raw_len=%d)", rawRefineLen)
			}

			// Re-validate URLs on refined text to prevent hallucination during correction
			if shouldValidateURLs {
				if err := validateURLPresence(allInputs, refineText); err != nil {
					return "", fmt.Errorf("refinement URL presence validation failed: %w", err)
				}
				if err := validateURLs(allInputs, refineText); err != nil {
					return "", fmt.Errorf("refinement URL validation failed: %w", err)
				}
			}

			// Update responseText for next iteration or final return
			responseText = refineText

			// Record refinement cost (G1.1)
			if e.store != nil && refineBudgetResult.ProjectedCostUSD > 0 {
				providerName := ""
				if len(endpoints) > 0 {
					providerName = endpoints[0].Name
				}
				e.recordEvent(ctx, memory.Event{
					ID:         fmt.Sprintf("%s-%s-refine-cost-%d-%d", p.Name, stage.ID, attempt, time.Now().UnixNano()),
					PipelineID: p.Name,
					StageID:    stage.ID,
					Type:       "http_call_cost",
					Timestamp:  time.Now().Unix(),
					Data: map[string]interface{}{
						"cost_usd":         refineBudgetResult.ProjectedCostUSD,
						"estimated_tokens": refineBudgetResult.EstimatedTokens,
						"provider":         providerName,
						"stage_id":         stage.ID,
						"action":           "refinement",
					},
				})
			}
		}
	}
	// --- END OUTPUT CONTRACT REFINEMENT LOOP ---

	// 5.7 (NEW) Cache store AFTER refinement (Sprint 18a P1 fix).
	// Cache write failure is NON-FATAL — pipeline succeeds even if cache fails.
	// CRITICAL: must happen AFTER refinement loop to ensure the cached value
	// is the contract-compliant output, not the pre-refinement draft.
	if p.Cache.Enabled && e.cfg != nil && responseText != "" {
		cacheDir := e.cfg.Paths.CacheDir
		if cacheDir != "" {
			cacheKeyPayload := map[string]interface{}{
				"model":       payload["model"],
				"prompt":      payload["prompt"],
				"max_tokens":  payload["max_tokens"],
				"temperature": payload["temperature"],
			}
			cacheKeyJSON, err := json.Marshal(cacheKeyPayload)
			if err == nil {
				ttlSecs := int64(p.Cache.TTLMinutes) * 60
				if err := omniroute.CacheStore(ctx, cacheDir, string(cacheKeyJSON), responseText, ttlSecs); err != nil {
					log.Warn().
						Err(err).
						Str("pipeline", p.Name).
						Str("stage", stage.ID).
						Msg("engine: failed to cache response (non-fatal, D3)")
				} else {
					log.Debug().
						Str("pipeline", p.Name).
						Str("stage", stage.ID).
						Int64("ttl_secs", ttlSecs).
						Msg("engine: response cached (D3, post-refinement)")

					// 5.8 (NEW) Cache eviction — enforce bounded growth (G3.4).
					// Defaults: 200 entries / 50 MB when pipeline YAML does not specify.
					maxEntries := p.Cache.MaxEntries
					if maxEntries <= 0 {
						maxEntries = 200
					}
					maxBytes := p.Cache.MaxBytes
					if maxBytes <= 0 {
						maxBytes = 50 * 1024 * 1024 // 50 MB
					}
					evicted, evictErr := omniroute.CacheEvict(ctx, cacheDir, maxEntries, maxBytes)
					if evictErr != nil {
						log.Warn().Err(evictErr).Msg("engine: cache eviction failed (non-fatal, G3.4)")
					} else if evicted > 0 {
						log.Info().
							Str("pipeline", p.Name).
							Str("stage", stage.ID).
							Int("evicted", evicted).
							Int("max_entries", maxEntries).
							Int64("max_bytes", maxBytes).
							Msg("engine: cache eviction completed (G3.4)")
					}
				}
			}
		}
	}

	return responseText, nil
}

// buildCorrectivePrompt constructs a prompt to fix output contract violations.
// It instructs the model to rewrite the article fixing all listed violations
// while maintaining factual accuracy and preventing new hallucinations.
func buildCorrectivePrompt(currentOutput string, violations []string) string {
	var sb strings.Builder
	sb.WriteString("You are an editorial AI assistant. Your previous output failed to meet the required quality contract.\n\n")
	sb.WriteString("VIOLATIONS DETECTED:\n")
	for i, v := range violations {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, v))
	}
	sb.WriteString("\nYOUR PREVIOUS OUTPUT:\n")
	sb.WriteString(currentOutput)
	sb.WriteString("\n\nINSTRUCTIONS:\n")
	sb.WriteString("Rewrite the article to fix ALL the violations listed above. ")
	sb.WriteString("Maintain the original frontmatter structure, the journalistic tone, and the factual accuracy. ")
	sb.WriteString("Do NOT invent new facts. Do NOT add new URLs. ")
	sb.WriteString("Output ONLY the complete corrected markdown article, starting directly with the frontmatter (---).\n")
	return sb.String()
}

// extractURLs extracts all URLs from text using regex.
// Matches http:// and https:// URLs.
func extractURLs(text string) []string {
	re := regexp.MustCompile(`https?://[^\s\)\]"]+`)
	matches := re.FindAllString(text, -1)

	// Deduplicate
	seen := make(map[string]bool)
	var urls []string
	for _, url := range matches {
		// Clean trailing punctuation
		url = strings.TrimRight(url, ".,;:!?")
		if !seen[url] {
			seen[url] = true
			urls = append(urls, url)
		}
	}
	return urls
}

// validateURLs checks that all URLs in output exist in input.
// Returns error if hallucinated URLs are detected.
func validateURLs(input, output string) error {
	inputURLs := extractURLs(input)
	outputURLs := extractURLs(output)

	// Build set of allowed URLs
	allowed := make(map[string]bool)
	for _, url := range inputURLs {
		allowed[url] = true
	}

	// Check each output URL
	var hallucinated []string
	for _, url := range outputURLs {
		if !allowed[url] {
			hallucinated = append(hallucinated, url)
		}
	}

	if len(hallucinated) > 0 {
		return fmt.Errorf("hallucinated URLs detected: %v (allowed: %v)",
			hallucinated, inputURLs)
	}

	return nil
}

// validateURLPresence checks that at least one URL from input appears in output.
// This prevents the model from completely ignoring the input and inventing content.
// Returns error if no input URLs are found in output.
//
// Sprint 17d: added to catch cases where model ignores input entirely.
func validateURLPresence(input, output string) error {
	inputURLs := extractURLs(input)
	if len(inputURLs) == 0 {
		// No URLs in input (e.g., first stage) — skip validation
		return nil
	}

	outputURLs := extractURLs(output)

	// Check if ANY input URL appears in output
	found := false
	for _, inputURL := range inputURLs {
		for _, outputURL := range outputURLs {
			if inputURL == outputURL {
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		return fmt.Errorf("no input URLs found in output (model may have ignored input and invented content). Input had %d URLs, output has %d URLs",
			len(inputURLs), len(outputURLs))
	}

	return nil
}

// Thinking-tag literals are built by concatenation so they survive copy/paste
// through chat renderers that strip HTML-like sequences. Never write the raw
// tag literal in source shared via chat (lesson learned: Sprint 17e-p2b —
// empty literals made strings.Index return 0, wiping every response).
var (
	thinkOpen    = "<" + "think>"
	thinkClose   = "</" + "think>"
	thinkRe      = regexp.MustCompile("(?s)" + regexp.QuoteMeta(thinkOpen) + ".*?" + regexp.QuoteMeta(thinkClose))
	blankLinesRe = regexp.MustCompile(`\n{3,}`)
)

// sanitizeOutput removes reasoning blocks and normalizes whitespace.
// Handles both closed blocks and an unclosed opening tag (truncated response).
// NEVER returns empty for a non-empty input that has content outside the tags.
func sanitizeOutput(text string) string {
	if text == "" {
		return text
	}

	// 1. Remove complete reasoning blocks (non-greedy, multiline-safe).
	text = thinkRe.ReplaceAllString(text, "")

	// 2. Remove an unclosed opening tag: drop everything from it onward.
	// Guard against empty literal — Index(s, "") == 0 would wipe the text.
	if thinkOpen != "" {
		if idx := strings.Index(text, thinkOpen); idx >= 0 {
			text = text[:idx]
		}
	}

	// 3. Remove orphan tags (defensive).
	text = strings.ReplaceAll(text, thinkOpen, "")
	text = strings.ReplaceAll(text, thinkClose, "")

	// 4. Collapse 3+ consecutive blank lines into 2 (cosmetic).
	text = blankLinesRe.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

// stubGenerate is a placeholder for AI generation when router.Client is nil.
// It returns a deterministic placeholder so tests can validate the pipeline
// flow without making real HTTP calls.
func (e *Engine) stubGenerate(ctx context.Context, p *Pipeline, stage Stage) (string, error) {
	log.Info().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Str("model", p.Model.Preferred).
		Msg("engine: stub generate (no real AI call)")

	return fmt.Sprintf("Generated output for stage %s", stage.ID), nil
}

// stubSelfReview is the fallback when router.Client is nil (stub mode).
// It passes through the input unchanged. Respects both explicit
// contracts (stage.InputKeys declared) and implicit contracts
// (only depends_on declared, as in news_blog.yaml).
func (e *Engine) stubSelfReview(ctx context.Context, stage Stage, inputs map[string]string) (string, error) {
	log.Info().
		Str("stage", stage.ID).
		Strs("criteria", stage.Criteria).
		Msg("engine: stub self_review (pass-through, no real LLM)")

	return e.pickReviewInput(stage, inputs)
}

// realSelfReview uses an LLM to evaluate the stage input against the
// declared criteria. The LLM is prompted to respond with structured JSON:
// {"decision": "PASS" or "FAIL", "reasoning": "..."}.
//
// Decision tree:
// - JSON with decision=PASS → returns input unchanged (quality gate passed)
// - JSON with decision=FAIL → returns error with reasoning (stage fails)
// - Malformed JSON → treated as FAIL (fail-safe per G5.1)
// - Cache hit (D3) → uses cached decision at cost $0
// - Token budget exceeded (I1) → fails with clear error
//
// Respects G3.2 (real quality gate), I1 (token budget), D3 (cache),
// I2 (observability via router tracer), I3 (intelligent retry).
func (e *Engine) realSelfReview(
	ctx context.Context,
	p *Pipeline,
	stage Stage,
	inputs map[string]string,
) (string, error) {
	if e.cfg == nil {
		return "", fmt.Errorf("engine: config not set (call SetConfig before Run)")
	}

	// 1. Pick the input to review (same logic as stubSelfReview).
	inputText, err := e.pickReviewInput(stage, inputs)
	if err != nil {
		return "", fmt.Errorf("self_review stage %q: %w", stage.ID, err)
	}

	// Sprint 17e-p4 (P0 fix): the review window is budget-capped, but the
	// truncation applies ONLY to the prompt sent to the LLM. The stage
	// output is the ORIGINAL article — returning the truncated copy here
	// published posts with an internal marker and missing middle content
	// (regression found in PR #37 after articles grew past 5000 chars).
	reviewText := inputText
	const maxReviewInputChars = 5000
	if len(reviewText) > maxReviewInputChars {
		originalLen := len(reviewText)

		headEnd := 3500
		if idx := strings.LastIndex(reviewText[:headEnd], "\n\n"); idx > 1000 {
			headEnd = idx
		}

		tailStart := len(reviewText) - 1200
		if idx := strings.Index(reviewText[tailStart:], "\n\n"); idx >= 0 {
			tailStart += idx
		}

		reviewText = reviewText[:headEnd] +
			"\n\n[... trecho intermediario omitido para caber no orcamento do review ...]\n\n" +
			reviewText[tailStart:]

		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Int("original_length", originalLen).
			Int("truncated_length", len(reviewText)).
			Msg("engine: self_review prompt truncated at paragraph boundaries (stage output keeps full article)")
	}

	// 2. Build review prompt with criteria (uses the truncated COPY only).
	reviewPrompt := e.buildReviewPrompt(stage.Criteria, reviewText)

	// 3. Build fallback chain (reuse realGenerate pattern).
	endpoints, err := router.BuildFallbackChain(ctx, e.resolver, e.cfg)
	if err != nil {
		return "", fmt.Errorf("build fallback chain: %w", err)
	}

	// 4. Build payload for review call (temperature 0.1 for determinism).
	payload := map[string]interface{}{
		"model":       p.Model.Preferred,
		"prompt":      reviewPrompt,
		"max_tokens":  p.Model.MaxTokens,
		"temperature": 0.1,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal review payload: %w", err)
	}

	// 4.1 Resolve per-provider targets for the review prompt.
	names := make([]string, len(endpoints))
	baseURLs := make([]string, len(endpoints))
	for i, ep := range endpoints {
		names[i], baseURLs[i] = ep.Name, ep.URL
	}
	tg, err := buildProviderTargets(names, baseURLs, p, reviewPrompt, 0.1)
	if err != nil {
		return "", fmt.Errorf("build provider targets for review: %w", err)
	}

	// 5. Cache lookup (D3): same input+criteria = same decision.
	// Build a deterministic cache key combining criteria + input.
	cacheKeyPayload := map[string]interface{}{
		"action":   "self_review",
		"model":    payload["model"],
		"prompt":   reviewPrompt,
		"criteria": stage.Criteria,
	}
	if p.Cache.Enabled && e.cfg != nil && e.cfg.Paths.CacheDir != "" {
		cacheKeyJSON, _ := json.Marshal(cacheKeyPayload)
		cacheHit, err := omniroute.CacheLookup(ctx, e.cfg.Paths.CacheDir, string(cacheKeyJSON))
		if err == nil && cacheHit != nil {
			log.Info().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int64("remaining_ttl_secs", cacheHit.RemainingTTLSecs).
				Msg("engine: self_review cache hit (D3, cost=$0)")

			decision, reasoning, parseErr := e.parseReviewResponse(cacheHit.Value)
			if parseErr != nil {
				// Corrupt cache entry — treat as miss, continue to LLM call.
				log.Warn().Err(parseErr).
					Str("pipeline", p.Name).
					Msg("engine: corrupt cache entry in self_review, ignoring")
			} else {
				if decision == "FAIL" {
					return "", fmt.Errorf("self_review FAILED (cached): %s", reasoning)
				}
				// PASS — return input unchanged.
				return inputText, nil
			}
		}
	}

	// 6. Token budget validation (I1) — read spent_usd from memory.
	var spentUSD float64
	if e.store != nil {
		spentUSD, _ = e.store.GetSpentToday(ctx)
	}

	// Raised from 1500: a 5000-char review window is ~1250 tokens plus
	// ~250 of prompt overhead. 2000 keeps headroom without touching G1.
	const reviewInputTokenLimit = 2000
	budgetResult, err := omniroute.ValidateTokenBudget(
		reviewPrompt,
		e.cfg.Budget.DailyLimitUSD,
		spentUSD,
		e.cfg.Budget.CostPer1kInputTokens,
		reviewInputTokenLimit,
	)
	if err != nil {
		return "", fmt.Errorf("validate token budget for review: %w", err)
	}
	if !budgetResult.Fits {
		return "", fmt.Errorf("self_review exceeds token budget: estimated=%d, spent=%.4f, projected=%.4f",
			budgetResult.EstimatedTokens, spentUSD, budgetResult.ProjectedCostUSD)
	}

	// 7. Call router (I2: observability via tracer, I3: intelligent retry).
	callCtx := observability.WithPipelineID(ctx, p.Name)
	resp, err := e.router.Call(callCtx, router.CallRequest{
		PipelineID:  p.Name,
		Method:      "POST",
		URLs:        tg.URLs,
		AuthHeaders: tg.AuthHeaders,
		Bodies:      tg.Bodies,
		Body:        bodyBytes,
		AuthHeader:  tg.LegacyAuth,
		Timeout:     time.Duration(e.cfg.Providers.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("self_review router call: %w", err)
	}

	// 8. Parse response via the shared tolerant extractor.
	llmResponseText, reviewFinish, reviewTokens, err := extractProviderText(resp.Body, resp.ProviderUsed)
	if err != nil {
		return "", fmt.Errorf("self_review: %w", err)
	}

	log.Info().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Str("provider_used", resp.ProviderUsed).
		Str("finish_reason", reviewFinish).
		Int("completion_tokens", reviewTokens).
		Int("extracted_chars", len(llmResponseText)).
		Msg("engine: review response parsed")

	rawReviewLen := len(llmResponseText)
	llmResponseText = sanitizeOutput(llmResponseText)
	if llmResponseText == "" {
		return "", fmt.Errorf("self_review: provider %s returned no usable content after sanitization (raw_len=%d, finish_reason=%q)",
			resp.ProviderUsed, rawReviewLen, reviewFinish)
	}

	// 9. Parse structured LLM response: {"decision":"PASS|FAIL","reasoning":"..."}
	decision, reasoning, parseErr := e.parseReviewResponse(llmResponseText)
	if parseErr != nil {
		// G5.1: fail-safe — malformed LLM output is treated as FAIL.
		log.Warn().Err(parseErr).
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Str("raw_response", truncate(llmResponseText, 500)).
			Msg("engine: self_review LLM response malformed, treating as FAIL (G5.1)")
		return "", fmt.Errorf("self_review: malformed LLM response: %w (raw: %s)",
			parseErr, truncate(llmResponseText, 200))
	}

	// 10. Cache store (D3) — non-fatal on failure.
	if p.Cache.Enabled && e.cfg != nil && e.cfg.Paths.CacheDir != "" {
		cacheKeyJSON, _ := json.Marshal(cacheKeyPayload)
		ttlSecs := int64(p.Cache.TTLMinutes) * 60
		if err := omniroute.CacheStore(ctx, e.cfg.Paths.CacheDir, string(cacheKeyJSON), llmResponseText, ttlSecs); err != nil {
			log.Warn().Err(err).Msg("engine: failed to cache self_review response (non-fatal, D3)")
		} else {
			log.Debug().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Str("decision", decision).
				Msg("engine: self_review response cached (D3)")
		}
	}

	// 11. Record cost (G1.1) if any.
	if e.store != nil && budgetResult.ProjectedCostUSD > 0 {
		providerName := ""
		if len(endpoints) > 0 {
			providerName = endpoints[0].Name
		}
		e.recordEvent(ctx, memory.Event{
			ID:         fmt.Sprintf("%s-%s-cost-%d", p.Name, stage.ID, time.Now().UnixNano()),
			PipelineID: p.Name,
			StageID:    stage.ID,
			Type:       "http_call_cost",
			Timestamp:  time.Now().Unix(),
			Data: map[string]interface{}{
				"cost_usd":         budgetResult.ProjectedCostUSD,
				"estimated_tokens": budgetResult.EstimatedTokens,
				"provider":         providerName,
				"stage_id":         stage.ID,
				"action":           "self_review",
			},
		})
	}

	// 12. Apply decision.
	if decision == "FAIL" {
		log.Warn().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Str("reasoning", reasoning).
			Msg("engine: self_review FAILED")
		return "", fmt.Errorf("self_review FAILED: %s", reasoning)
	}

	log.Info().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Str("reasoning", reasoning).
		Msg("engine: self_review PASSED (G3.2 quality gate)")

	return inputText, nil
}

// providerResponse is the tolerant envelope for every known provider shape:
//
//	legacy /completions  → choices[0].text
//	/chat/completions    → choices[0].message.content
//	streaming aggregate  → choices[0].delta.content
//	Responses API        → output_text or output[0].content[0].text
type providerResponse struct {
	Choices []struct {
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	OutputText string `json:"output_text"`
	Output     []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// extractProviderText normalizes any provider envelope into plain text.
// Shared by realGenerate and realSelfReview so the two paths can never
// diverge again. NEVER indexes Choices without a length guard and NEVER
// panics — every failure mode returns an explicit, actionable error (G5.1).
func extractProviderText(body []byte, providerUsed string) (string, string, int, error) {
	var r providerResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", "", 0, fmt.Errorf("unmarshal response: %w", err)
	}
	tokens := r.Usage.CompletionTokens
	text := ""
	finish := ""

	if len(r.Choices) > 0 {
		c := r.Choices[0]
		finish = c.FinishReason
		switch {
		case c.Text != "":
			text = c.Text
		case c.Message.Content != "":
			text = c.Message.Content
		case c.Delta.Content != "":
			text = c.Delta.Content
		}
		// Reasoning-only is a FAILURE, not content: thinking is not the answer.
		if text == "" && (c.Message.ReasoningContent != "" || c.Message.Reasoning != "") {
			return "", finish, tokens, fmt.Errorf(
				"provider %s produced only reasoning, no answer (finish_reason=%q, completion_tokens=%d): raise max_tokens or use a non-reasoning model",
				providerUsed, finish, tokens)
		}
	}

	if text == "" && r.OutputText != "" {
		text = r.OutputText
	}
	if text == "" {
		for _, o := range r.Output {
			for _, ct := range o.Content {
				if ct.Text != "" {
					text = ct.Text
					break
				}
			}
			if text != "" {
				break
			}
		}
	}
	if text != "" {
		return text, finish, tokens, nil
	}

	// Empty envelope: keep the historical "no choices" wording so existing
	// tests and dashboards keep matching, and never index Choices[0].
	if len(r.Choices) == 0 && r.OutputText == "" && len(r.Output) == 0 {
		return "", finish, tokens, fmt.Errorf(
			"no choices in response (provider=%s, completion_tokens=%d, body_preview=%s)",
			providerUsed, tokens, truncate(string(body), 300))
	}
	if finish == "tool_calls" {
		return "", finish, tokens, fmt.Errorf(
			"provider %s emitted tool_calls instead of text (completion_tokens=%d): disable tools/function-calling for this model alias in the gateway",
			providerUsed, tokens)
	}
	return "", finish, tokens, fmt.Errorf(
		"empty response text (provider=%s, finish_reason=%q, completion_tokens=%d, body_preview=%s)",
		providerUsed, finish, tokens, truncate(string(body), 300))
}

// providerTargets holds per-provider request targeting for a fallback chain.
// Each provider needs its OWN endpoint path, API key, model ID and payload
// shape: an OmniRoute alias is invalid on OpenRouter, and OpenRouter exposes
// only /chat/completions (no legacy /completions). Sprint 17e-p2b (G1.2, I3).
type providerTargets struct {
	URLs        []string
	AuthHeaders map[string]string
	Bodies      map[string][]byte
	LegacyAuth  string // first key found; kept for backward compatibility
}

// buildProviderTargets resolves endpoint path, auth key and payload for every
// provider in the chain. Index 0 uses model.preferred; index i>0 uses
// model.fallbacks[i-1]. Every marshal error is wrapped (G5.1).
func buildProviderTargets(names, baseUrls []string, p *Pipeline, prompt string, temperature float64) (*providerTargets, error) {
	if len(names) != len(baseUrls) {
		return nil, fmt.Errorf("provider targets: names/baseUrls length mismatch (%d != %d)",
			len(names), len(baseUrls))
	}

	t := &providerTargets{
		URLs:        make([]string, 0, len(names)),
		AuthHeaders: make(map[string]string, len(names)),
		Bodies:      make(map[string][]byte, len(names)),
	}

	for i, name := range names {
		path, isChat := providerAPIStyle(name)
		base := baseUrls[i]
		if !strings.HasSuffix(base, "/v1") {
			base = strings.TrimSuffix(base, "/") + "/v1"
		}
		u := base + path
		t.URLs = append(t.URLs, u)

		// Resolve THIS provider's key — never reuse another provider's key.
		envVarNames := getEnvVarNames(name)
		for _, envKey := range envVarNames {
			if key := os.Getenv(envKey); key != "" {
				t.AuthHeaders[u] = "Bearer " + key
				if t.LegacyAuth == "" {
					t.LegacyAuth = "Bearer " + key
				}
				log.Info().
					Str("provider", name).
					Str("endpoint_path", path).
					Bool("chat_shape", isChat).
					Msg("engine: auth key loaded")
				break
			}
		}
		if _, ok := t.AuthHeaders[u]; !ok {
			log.Warn().
				Str("provider", name).
				Strs("checked_keys", envVarNames).
				Msg("engine: no auth key found for provider")
		}

		modelID := p.Model.Preferred
		if i > 0 && len(p.Model.Fallbacks) >= i {
			modelID = p.Model.Fallbacks[i-1]
		}
		b, err := json.Marshal(buildProviderPayload(isChat, modelID, prompt, p.Model.MaxTokens, temperature))
		if err != nil {
			return nil, fmt.Errorf("marshal payload for %s: %w", name, err)
		}
		t.Bodies[u] = b
	}

	return t, nil
}

// pickReviewInput selects the input text for review using the same logic
// as stubSelfReview: explicit InputKeys first, then implicit from depends_on.
func (e *Engine) pickReviewInput(stage Stage, inputs map[string]string) (string, error) {
	if len(stage.InputKeys) > 0 {
		for _, key := range stage.InputKeys {
			if val, ok := inputs[key]; ok {
				return val, nil
			}
		}
		return "", fmt.Errorf("no input found for declared keys %v", stage.InputKeys)
	}
	for _, val := range inputs {
		if val != "" {
			return val, nil
		}
	}
	return "", fmt.Errorf("no input found for review")
}

// buildReviewPrompt constructs a deterministic prompt asking the LLM to
// evaluate the input against the given criteria and respond with structured
// JSON: {"decision": "PASS" or "FAIL", "reasoning": "..."}.
//
// Sprint 17e-p1: criteria names are identifiers (ASCII), not prose to be
// spell-checked. The reviewer MUST evaluate the OUTPUT against the criteria,
// NOT critique the criteria names themselves.
func (e *Engine) buildReviewPrompt(criteria []string, inputText string) string {
	var sb strings.Builder
	sb.WriteString("You are a strict quality reviewer for a Brazilian tech blog.\n\n")
	sb.WriteString("Evaluate the OUTPUT below against the CRITERIA list.\n\n")
	sb.WriteString("IMPORTANT RULES:\n")
	sb.WriteString("- The CRITERIA names are IDENTIFIERS (ASCII), NOT text to be spell-checked.\n")
	sb.WriteString("- Do NOT comment on the criteria names themselves (e.g., do not flag 'coerencia' as a typo).\n")
	sb.WriteString("- Only evaluate the OUTPUT content against what each criterion represents.\n")
	sb.WriteString("- Criteria represent: YAML validity, Portuguese language, markdown structure, editorial coherence.\n")
	sb.WriteString("- editorial_coherence REQUIRES a single coherent topic. An article that mixes\n")
	sb.WriteString("  unrelated subjects (e.g. an enterprise cloud outage AND a game's playtime) FAILS\n")
	sb.WriteString("  this criterion, even if each part is well written. One focused story PASSES.\n\n")

	sb.WriteString("SEVERITY RULES (FAIL = BLOCKING defects only):\n")
	sb.WriteString("- valid_yaml_frontmatter: FAIL if frontmatter is missing, unclosed, or invalid YAML.\n")
	sb.WriteString("- brazilian_portuguese_language: FAIL only if the text is NOT written in Brazilian\n")
	sb.WriteString("  Portuguese (e.g., English paragraphs, wrong language). Isolated typos or\n")
	sb.WriteString("  orthographic slips (e.g., 'quesari' instead of 'quesito') are NON-BLOCKING:\n")
	sb.WriteString("  mention them in reasoning for the human editor, but do NOT FAIL this criterion.\n")
	sb.WriteString("- correct_markdown_structure: FAIL if required sections are missing or structure is broken.\n")
	sb.WriteString("- editorial_coherence: FAIL if unrelated subjects are mixed in one article.\n")
	sb.WriteString("- BUDGET TRUNCATION ARTIFACT: The text below may be truncated to fit the review\n")
	sb.WriteString("  budget. If you see the marker '[... trecho intermediario omitido ...]', it is\n")
	sb.WriteString("  an INTERNAL SYSTEM ARTIFACT indicating hidden middle content. You MUST IGNORE\n")
	sb.WriteString("  this marker completely. Do NOT fail the article because of it. Evaluate only\n")
	sb.WriteString("  the visible head and tail for coherence and structure.\n")
	sb.WriteString("- Minor style/typo issues never block the pipeline: the human approval at PR\n")
	sb.WriteString("  merge is the final quality gate (G3.2). Report them in reasoning instead.\n\n")

	sb.WriteString("CRITERIA TO CHECK:\n")
	for i, c := range criteria {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, c))
	}
	sb.WriteString("\nOUTPUT TO REVIEW:\n")
	sb.WriteString(inputText)
	sb.WriteString("\n\nRespond ONLY with valid JSON in this exact format:\n")
	sb.WriteString(`{"decision": "PASS" or "FAIL", "reasoning": "brief explanation"}`)
	sb.WriteString("\n- decision MUST be exactly \"PASS\" or \"FAIL\" (uppercase, in quotes).\n")
	sb.WriteString("- reasoning MUST explain which criteria failed (if FAIL) or confirm all passed (if PASS).\n")
	sb.WriteString("- If PASS with non-blocking issues, list them briefly in reasoning.\n")
	sb.WriteString("- Do NOT include any other text, markdown, or code fences.\n")
	sb.WriteString("- Do NOT critique the criteria names — only evaluate the OUTPUT.\n")
	return sb.String()
}

// parseReviewResponse parses the LLM's structured JSON response.
// Returns (decision, reasoning, error). Decision is "PASS" or "FAIL".
// If parsing fails, returns error (caller treats as FAIL per G5.1).
func (e *Engine) parseReviewResponse(text string) (string, string, error) {
	// Strip common markdown code fences if the LLM wrapped its response.
	cleaned := strings.TrimSpace(text)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var resp struct {
		Decision  string `json:"decision"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return "", "", fmt.Errorf("parse review JSON: %w", err)
	}
	if resp.Decision != "PASS" && resp.Decision != "FAIL" {
		return "", "", fmt.Errorf("invalid decision %q (must be PASS or FAIL)", resp.Decision)
	}
	return resp.Decision, resp.Reasoning, nil
}

// stubHTTPPost handles HTTP publishing with human review support (G3.2).
// - When require_review is true: saves a draft file for human approval
// - When require_review is false: calls realHTTPPost to POST to the webhook
func (e *Engine) stubHTTPPost(
	ctx context.Context,
	p *Pipeline,
	stage Stage,
	inputs map[string]string,
) (string, error) {
	log.Info().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Str("endpoint_env", stage.EndpointEnv).
		Bool("require_review", p.Limits.RequireReview).
		Msg("engine: http_post stage")

	// G3.2: if review required, save draft for human approval.
	if p.Limits.RequireReview {
		// Sprint 15 15d-p1: extract review criteria from the most recent self_review stage.
		// Reasoning will be populated in 15d-p2 when realSelfReview stores it in memory.
		var reviewCriteria []string
		for _, s := range p.Stages {
			if s.Action == "self_review" && len(s.Criteria) > 0 {
				reviewCriteria = s.Criteria
				break
			}
		}

		draftPath, err := e.saveDraft(p, stage, inputs, reviewCriteria, "")
		if err != nil {
			return "", fmt.Errorf("engine: save draft: %w", err)
		}
		log.Info().
			Str("pipeline", p.Name).
			Str("path", draftPath).
			Msg("engine: draft saved, awaiting human approval (G3.2)")
		return fmt.Sprintf("draft_saved:%s", draftPath), nil
	}

	// No review required: make real HTTP POST to webhook.
	return e.realHTTPPost(ctx, p, stage, inputs)
}

// PublishDraft sends the draft's inputs to the webhook URL defined by
// dp.EndpointEnv environment variable. It delegates to realHTTPPost
// which respects decision tree D6 (retry 5xx, fail-fast 4xx).
//
// Returns nil on success (HTTP 2xx). Returns error on 4xx, 5xx after
// retries, missing env var, or network failure.
//
// G3.2: this method is ONLY called after human approval; the API layer
// is responsible for enforcing the approval precondition.
func (e *Engine) PublishDraft(ctx context.Context, dp DraftPublish) error {
	if dp.EndpointEnv == "" {
		return fmt.Errorf("engine: DraftPublish.EndpointEnv is empty")
	}

	// Build a minimal Pipeline and Stage to satisfy realHTTPPost signature.
	// The only fields realHTTPPost actually reads are: p.Name, stage.ID,
	// stage.EndpointEnv, and inputs.
	syntheticPipeline := &Pipeline{
		Name: dp.PipelineName,
		Limits: Limits{
			RequireReview: false, // already approved (G3.2 precondition)
		},
	}
	syntheticStage := Stage{
		ID:          dp.StageID,
		Action:      "http_post",
		EndpointEnv: dp.EndpointEnv,
	}

	_, err := e.realHTTPPost(ctx, syntheticPipeline, syntheticStage, dp.Inputs)
	return err
}

// saveDraft persists a draft file to data/outputs/ for human approval.
// Returns the path of the created file.
func (e *Engine) saveDraft(
	p *Pipeline,
	stage Stage,
	inputs map[string]string,
	reviewCriteria []string,
	reviewReasoning string,
) (string, error) {
	now := time.Now()

	// Build the full outputs map from inputs (what this stage sees).
	allOutputs := make(map[string]string)
	for k, v := range inputs {
		allOutputs[k] = v
	}

	draft := Draft{
		ID:              fmt.Sprintf("%s-%d", p.Name, now.UnixNano()),
		Pipeline:        p.Name,
		CreatedAt:       now,
		StageID:         stage.ID,
		EndpointEnv:     stage.EndpointEnv,
		Inputs:          inputs,
		AllOutputs:      allOutputs,
		ReviewCriteria:  reviewCriteria,
		ReviewReasoning: reviewReasoning,
	}

	// Use injected absolute path, or fallback to relative for backward compat.
	outputsDir := e.outputsDir
	if outputsDir == "" {
		outputsDir = filepath.Join("data", "outputs")
	}

	if err := os.MkdirAll(outputsDir, 0o755); err != nil {
		return "", fmt.Errorf("create outputs dir %q: %w", outputsDir, err)
	}

	filename := fmt.Sprintf("%s.%d.draft.json", p.Name, now.UnixNano())
	path := filepath.Join(outputsDir, filename)

	data, err := json.MarshalIndent(draft, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal draft: %w", err)
	}

	// Atomic write: temp file then rename to avoid partial reads.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write temp draft: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename temp draft: %w", err)
	}

	log.Info().
		Str("path", path).
		Str("pipeline", p.Name).
		Msg("engine: draft saved, awaiting human approval (G3.2)")

		// Emit draft_created event so dashboard can refresh in real time (G3.2).
	e.broadcaster.Broadcast("draft_created", map[string]any{
		"pipeline":     p.Name,
		"draft_id":     draft.ID,
		"filename":     filename,
		"endpoint_env": stage.EndpointEnv,
	})

	return path, nil
}

// realHTTPPost makes an actual HTTP POST to the webhook URL defined by
// stage.EndpointEnv environment variable. Respects decision tree D6:
// - 2xx → success, return response body
// - 4xx → fail permanently (client error, no retry)
// - 5xx → retry with exponential backoff (up to 3 attempts)
// - timeout → retry with backoff
func (e *Engine) realHTTPPost(
	ctx context.Context,
	p *Pipeline,
	stage Stage,
	inputs map[string]string,
) (string, error) {
	// 1. Resolve webhook URL from environment variable.
	if stage.EndpointEnv == "" {
		return "", fmt.Errorf("engine: http_post stage %q has empty endpoint_env", stage.ID)
	}
	webhookURL := os.Getenv(stage.EndpointEnv)
	if webhookURL == "" {
		return "", fmt.Errorf("engine: env var %q not set (required for stage %q)",
			stage.EndpointEnv, stage.ID)
	}

	// 2. Build payload JSON with all inputs from the stage.
	payload := map[string]interface{}{
		"pipeline": p.Name,
		"stage":    stage.ID,
		"payload":  inputs,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal webhook payload: %w", err)
	}

	// 3. HTTP client with timeout.
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 4. Retry loop (D6: up to 3 attempts for 5xx/timeout).
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check context before each attempt.
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("context cancelled during webhook POST: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return "", fmt.Errorf("build webhook request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		const Version = "dev" // TODO: inject via ldflags in build
		req.Header.Set("User-Agent", "ai-loop/"+Version)

		// HMAC-SHA256 signature (G2.1: secret via env var).
		// Only signs if webhooks.secret_env_var is configured AND env var is set.
		// Graceful degradation (I2): if env var missing, log warn but proceed without signature.
		if e.cfg != nil && e.cfg.Webhooks.SecretEnvVar != "" {
			secret := os.Getenv(e.cfg.Webhooks.SecretEnvVar)
			if secret != "" {
				mac := hmac.New(sha256.New, []byte(secret))
				mac.Write(bodyBytes)
				signature := hex.EncodeToString(mac.Sum(nil))
				req.Header.Set("X-Webhook-Signature", "sha256="+signature)
				log.Debug().
					Str("pipeline", p.Name).
					Str("stage", stage.ID).
					Str("secret_env_var", e.cfg.Webhooks.SecretEnvVar).
					Msg("engine: webhook payload signed with HMAC-SHA256 (G2.1)")
			} else {
				log.Warn().
					Str("pipeline", p.Name).
					Str("stage", stage.ID).
					Str("secret_env_var", e.cfg.Webhooks.SecretEnvVar).
					Msg("engine: webhook secret env var not set, sending unsigned (I2 graceful degradation)")
			}
		}

		log.Debug().
			Str("pipeline", p.Name).
			Str("stage", stage.ID).
			Str("webhook_url", webhookURL).
			Int("attempt", attempt).
			Int("payload_bytes", len(bodyBytes)).
			Msg("engine: POST to webhook (D6)")

		resp, err := client.Do(req)
		if err != nil {
			// Network error or timeout — retry.
			lastErr = fmt.Errorf("webhook POST attempt %d: %w", attempt, err)
			log.Warn().Err(err).
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int("attempt", attempt).
				Msg("engine: webhook POST failed, will retry (D6)")
			time.Sleep(backoffDuration(attempt))
			continue
		}

		// Read body and close.
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			// Success — record event in memory store.
			if e.store != nil {
				e.recordEvent(ctx, memory.Event{
					ID:         fmt.Sprintf("%s-%s-published-%d", p.Name, stage.ID, time.Now().UnixNano()),
					PipelineID: p.Name,
					StageID:    stage.ID,
					Type:       "http_post_published",
					Timestamp:  time.Now().Unix(),
					Data: map[string]interface{}{
						"webhook_url":     webhookURL,
						"response_status": resp.StatusCode,
						"response_body":   truncate(string(respBody), 500),
						"payload_bytes":   len(bodyBytes),
					},
				})
			}

			log.Info().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int("status", resp.StatusCode).
				Msg("engine: webhook POST succeeded (D6)")

			return fmt.Sprintf("published:%d", resp.StatusCode), nil

		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			// Client error — fail permanently (D6: no retry on 4xx).
			log.Error().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int("status", resp.StatusCode).
				Str("response", truncate(string(respBody), 200)).
				Msg("engine: webhook POST returned 4xx (D6: fail-fast)")

			return "", fmt.Errorf("webhook returned %d: %s",
				resp.StatusCode, truncate(string(respBody), 200))

		case resp.StatusCode >= 500:
			// Server error — retry (D6).
			lastErr = fmt.Errorf("webhook attempt %d: %d %s",
				attempt, resp.StatusCode, truncate(string(respBody), 200))
			log.Warn().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Int("status", resp.StatusCode).
				Int("attempt", attempt).
				Msg("engine: webhook POST returned 5xx, will retry (D6)")
			time.Sleep(backoffDuration(attempt))
		}
	}

	// All retries exhausted.
	return "", fmt.Errorf("webhook POST failed after %d attempts: %w", maxAttempts, lastErr)
}

// backoffDuration returns exponential backoff: 1s, 2s, 4s.
func backoffDuration(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt-1)) * time.Second
}

// truncate returns the first n characters of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// providerAPIStyle returns the endpoint path and payload shape for a provider.
// OmniRoute accepts the legacy /completions shape (prompt-based).
// OpenRouter only exposes /chat/completions and /responses — there is NO
// legacy /completions endpoint, so it requires the messages-based shape.
// Sprint 17e-p2b: per-provider API style is required for real fallback (G1.2).
func providerAPIStyle(name string) (path string, chatShape bool) {
	switch strings.ToLower(name) {
	case "openrouter":
		return "/chat/completions", true
	default:
		return "/completions", false
	}
}

// buildProviderPayload builds the request body in the shape the provider expects.
func buildProviderPayload(chatShape bool, modelID, prompt string, maxTokens int, temperature float64) map[string]interface{} {
	if chatShape {
		return map[string]interface{}{
			"model":       modelID,
			"messages":    []map[string]string{{"role": "user", "content": prompt}},
			"max_tokens":  maxTokens,
			"temperature": temperature,
		}
	}
	return map[string]interface{}{
		"model":       modelID,
		"prompt":      prompt,
		"max_tokens":  maxTokens,
		"temperature": temperature,
	}
}

// getEnvVarNames returns all possible environment variable names for a provider's API key.
// This handles the mismatch between provider names in the fallback chain (e.g., "omniroute")
// and the actual env var names users configure (e.g., "OMNI_ROUTE_API_KEY" with underscore).
// Sprint 15 final hotfix-4: explicit alias mapping for compound provider names.
func getEnvVarNames(providerName string) []string {
	// Base: canonical AILOOP_<NAME>_API_KEY format
	base := strings.ToUpper(strings.ReplaceAll(providerName, "-", "_"))

	// Known aliases for providers with compound names
	aliases := map[string][]string{
		"omniroute":  {"OMNI_ROUTE"}, // user configures OMNI_ROUTE_API_KEY
		"openrouter": {"OPENROUTER"}, // user configures OPENROUTER_API_KEY (same)
		"openai":     {"OPENAI"},
		"anthropic":  {"ANTHROPIC"},
	}

	var names []string
	// 1. Canonical format with AILOOP_ prefix
	names = append(names, "AILOOP_"+base+"_API_KEY")

	// 2. Aliases (if any)
	if aliasList, ok := aliases[strings.ToLower(providerName)]; ok {
		for _, alias := range aliasList {
			names = append(names, alias+"_API_KEY")
		}
	} else {
		// No alias: use base name directly
		names = append(names, base+"_API_KEY")
	}

	return names
}
