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
	ID          string            `json:"id"`
	Pipeline    string            `json:"pipeline"`
	CreatedAt   time.Time         `json:"created_at"`
	StageID     string            `json:"stage_id"`
	EndpointEnv string            `json:"endpoint_env,omitempty"`
	Inputs      map[string]string `json:"inputs"`
	AllOutputs  map[string]string `json:"all_outputs"`
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
		return e.stubSelfReview(ctx, stage, inputs)
	case "http_post":
		return e.stubHTTPPost(ctx, p, stage, inputs)
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

	// 1. Build fallback chain with provider names.
	endpoints, err := router.BuildFallbackChain(ctx, e.resolver, e.cfg)
	if err != nil {
		return "", fmt.Errorf("build fallback chain: %w", err)
	}

	// 2. Build URLs with path suffix and find first valid auth.
	urls := make([]string, 0, len(endpoints))
	var authHeader string
	for _, ep := range endpoints {
		// Build the completions endpoint URL with /v1 prefix (OpenAI standard)
		completionsURL := ep.URL
		if !strings.HasSuffix(completionsURL, "/v1") {
			completionsURL = strings.TrimSuffix(completionsURL, "/") + "/v1"
		}
		completionsURL += "/completions"
		urls = append(urls, completionsURL)

		// Read auth from env var AILOOP_<NAME>_API_KEY (uppercase).
		if authHeader == "" {
			envKey := "AILOOP_" + strings.ToUpper(strings.ReplaceAll(ep.Name, "-", "_")) + "_API_KEY"
			if key := os.Getenv(envKey); key != "" {
				authHeader = "Bearer " + key
				log.Debug().Str("provider", ep.Name).Msg("engine: auth key loaded from env")
			}
		}
	}

	// 3. Build prompt and payload.
	prompt := fmt.Sprintf("Stage: %s\n", stage.ID)
	for key, val := range inputs {
		prompt += fmt.Sprintf("Input %s: %s\n", key, val)
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
	resp, err := e.router.Call(callCtx, router.CallRequest{
		PipelineID: p.Name,
		Method:     "POST",
		URLs:       urls,
		Body:       bodyBytes,
		AuthHeader: authHeader,
		Timeout:    time.Duration(e.cfg.Providers.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("router call: %w", err)
	}

	// 5. Parse response.
	var result struct {
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
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

	// 5.6 (NEW) Cache store (D3: cache write on successful response).
	// Cache write failure is NON-FATAL — pipeline succeeds even if cache fails.
	responseText := result.Choices[0].Text
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
					// D3: cache write failure is non-fatal — log warning but continue.
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
						Msg("engine: response cached (D3)")
				}
			}
		}
	}

	return responseText, nil
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

// stubSelfReview is a placeholder for self-review.
// It passes through the input unchanged. Respects both explicit
// contracts (stage.InputKeys declared) and implicit contracts
// (only depends_on declared, as in news_blog.yaml).
func (e *Engine) stubSelfReview(ctx context.Context, stage Stage, inputs map[string]string) (string, error) {
	log.Info().
		Str("stage", stage.ID).
		Strs("criteria", stage.Criteria).
		Msg("engine: stub self_review (pass-through)")

	// Explicit contract: InputKeys declared in YAML — pick the first
	// available in order.
	if len(stage.InputKeys) > 0 {
		for _, key := range stage.InputKeys {
			if val, ok := inputs[key]; ok {
				return val, nil
			}
		}
		return "", fmt.Errorf(
			"self_review stage %q: no input found for declared keys %v",
			stage.ID, stage.InputKeys,
		)
	}

	// Implicit contract: no InputKeys, rely on inputs gathered from
	// depends_on by executeStage. Pick the first non-empty value.
	for _, val := range inputs {
		if val != "" {
			return val, nil
		}
	}

	return "", fmt.Errorf("self_review stage %q: no input found", stage.ID)
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
		draftPath, err := e.saveDraft(p, stage, inputs)
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
) (string, error) {
	now := time.Now()

	// Build the full outputs map from inputs (what this stage sees).
	allOutputs := make(map[string]string)
	for k, v := range inputs {
		allOutputs[k] = v
	}

	draft := Draft{
		ID:          fmt.Sprintf("%s-%d", p.Name, now.UnixNano()),
		Pipeline:    p.Name,
		CreatedAt:   now,
		StageID:     stage.ID,
		EndpointEnv: stage.EndpointEnv,
		Inputs:      inputs,
		AllOutputs:  allOutputs,
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
		var Version string
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
