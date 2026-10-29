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
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/SEU_USUARIO/ai-loop-project/internal/router"
)

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

// Engine orchestrates pipeline execution.
// It can operate in two modes:
// - Stub mode: no router.Client, generate actions return placeholders
// - Real mode: router.Client provided, generate actions make HTTP calls
type Engine struct {
	router   *router.Client
	resolver *router.Resolver
}

// NewEngine creates a pipeline engine in stub mode (no real AI calls).
// Use NewEngineWithDeps for production use with real router.Client.
func NewEngine() *Engine {
	return &Engine{
		router:   nil,
		resolver: nil,
	}
}

// NewEngineWithDeps creates a pipeline engine with real dependencies.
// If router or resolver is nil, the engine falls back to stub mode
// for generate actions.
func NewEngineWithDeps(router *router.Client, resolver *router.Resolver) *Engine {
	return &Engine{
		router:   router,
		resolver: resolver,
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

	for _, stage := range p.Stages {
		// Check context before each stage (G5.1: explicit error handling).
		if err := ctx.Err(); err != nil {
			result.Status = StatusCancelled
			result.Error = fmt.Errorf("engine: pipeline %q cancelled: %w", p.Name, err)
			log.Warn().
				Str("pipeline", p.Name).
				Str("stage", stage.ID).
				Msg("engine: pipeline cancelled")
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
	}

	log.Info().
		Str("pipeline", p.Name).
		Msg("engine: pipeline completed successfully")

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
func (e *Engine) realGenerate(
	ctx context.Context,
	p *Pipeline,
	stage Stage,
	inputs map[string]string,
) (string, error) {
	// Resolve provider URL via discovery.go
	baseURL, err := e.resolver.Resolve(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve provider URL: %w", err)
	}

	// Build the prompt from stage.PromptFile and inputs
	// For now, we construct a simple prompt; in future sprints this will
	// use the prompt-builder crate via FFI.
	prompt := fmt.Sprintf("Stage: %s\n", stage.ID)
	for key, val := range inputs {
		prompt += fmt.Sprintf("Input %s: %s\n", key, val)
	}

	// Build the OmniRoute API payload (assuming OpenAI-compatible format)
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

	// Build the URL for the completions endpoint
	completionsURL := baseURL + "/v1/completions"

	// Make the HTTP call via router.Client (applies I3: intelligent retry)
	resp, err := e.router.Call(ctx, router.CallRequest{
		PipelineID: p.Name,
		Method:     "POST",
		URLs:       []string{completionsURL},
		Body:       bodyBytes,
		Timeout:    60 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("router call: %w", err)
	}

	// Parse the response (assuming OpenAI-compatible format)
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

	return result.Choices[0].Text, nil
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

// stubHTTPPost is a placeholder for HTTP publishing.
// It respects G3.2 (require_review) by logging instead of sending.
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
		Msg("engine: stub http_post (not sending, G3.2 require_review)")

	// In a real implementation, this would:
	// 1. Check if require_review is true → save draft to data/outputs/
	// 2. If false, send HTTP POST to the endpoint
	// For now, just return a placeholder.
	return "published_stub", nil
}
