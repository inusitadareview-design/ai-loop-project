// Package engine implements the pipeline orchestrator (Maestro) that
// reads pipeline YAML definitions and executes stages sequentially.
// This is Sprint 4's core deliverable per ARCHITECTURE.md 4.6.
//
// pipeline.go handles YAML parsing and validation. loop.go handles
// execution. State persistence and router integration come in later
// increments (Sprint 5+).
package engine

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Pipeline represents a named workflow loaded from YAML.
// It is immutable after loading — treat as read-only.
type Pipeline struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Schedule    Schedule `yaml:"schedule"`
	Model       Model    `yaml:"model"`
	Stages      []Stage  `yaml:"stages"`
	Limits      Limits   `yaml:"limits"`
	Cache       Cache    `yaml:"cache"`
	Memory      Memory   `yaml:"memory"`
}

// Schedule defines when the pipeline should be triggered by the scheduler.
type Schedule struct {
	Enabled bool   `yaml:"enabled"`
	Cron    string `yaml:"cron"`
}

// Model specifies the AI model configuration for generation stages.
type Model struct {
	Preferred   string   `yaml:"preferred"`
	Fallbacks   []string `yaml:"fallbacks"`
	MaxTokens   int      `yaml:"max_tokens"`
	Temperature float64  `yaml:"temperature"`
}

// Stage represents a single step in the pipeline (plan, execute, review, publish).
type Stage struct {
	ID          string   `yaml:"id"`
	Action      string   `yaml:"action"` // generate | self_review | http_post
	DependsOn   []string `yaml:"depends_on"`
	PromptFile  string   `yaml:"prompt_file"`
	InputKeys   []string `yaml:"input_keys"`
	OutputKey   string   `yaml:"output_key"`
	Criteria    []string `yaml:"criteria"`
	EndpointEnv string   `yaml:"endpoint_env"`
}

// Limits enforces guardrails G1 (budget), G3.2 (require_review), G4.1 (dead-loop).
type Limits struct {
	RequireReview      bool    `yaml:"require_review"`
	MaxCostPerRunUSD   float64 `yaml:"max_cost_per_run_usd"`
	MaxRetriesPerStage int     `yaml:"max_retries_per_stage"`
	DeadLoopFailures   int     `yaml:"dead_loop_failures"`
}

// Cache configures response caching (D3 decision tree).
type Cache struct {
	Enabled    bool   `yaml:"enabled"`
	TTLMinutes int    `yaml:"ttl_minutes"`
	Dir        string `yaml:"dir"`
}

// Memory configures persistent memory (I5, G3.1).
type Memory struct {
	Enabled           bool `yaml:"enabled"`
	StoreOutputs      bool `yaml:"store_outputs"`
	CompressAfterRuns int  `yaml:"compress_after_runs"`
}

// LoadPipeline reads and validates a pipeline YAML file.
// Returns an error if the file is missing, malformed, or structurally invalid.
func LoadPipeline(ctx context.Context, path string) (*Pipeline, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("engine: read pipeline %q: %w", path, err)
	}

	var p Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("engine: parse pipeline %q: %w", path, err)
	}

	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("engine: validate pipeline %q: %w", path, err)
	}

	return &p, nil
}

// Validate checks pipeline structural integrity:
// - Name is required
// - At least one stage
// - No duplicate stage IDs
// - All depends_on references point to existing stages
func (p *Pipeline) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("pipeline name is required")
	}
	if len(p.Stages) == 0 {
		return fmt.Errorf("pipeline %q has no stages", p.Name)
	}

	stageIDs := make(map[string]bool, len(p.Stages))
	for _, s := range p.Stages {
		if s.ID == "" {
			return fmt.Errorf("pipeline %q: stage ID is required", p.Name)
		}
		if stageIDs[s.ID] {
			return fmt.Errorf("pipeline %q: duplicate stage ID %q", p.Name, s.ID)
		}
		stageIDs[s.ID] = true
	}

	// Validate depends_on references exist.
	for _, s := range p.Stages {
		for _, dep := range s.DependsOn {
			if !stageIDs[dep] {
				return fmt.Errorf("pipeline %q: stage %q depends on unknown stage %q",
					p.Name, s.ID, dep)
			}
		}
	}

	return nil
}

// StageByID returns the stage with the given ID, or nil if not found.
func (p *Pipeline) StageByID(id string) *Stage {
	for i := range p.Stages {
		if p.Stages[i].ID == id {
			return &p.Stages[i]
		}
	}
	return nil
}
