package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeTestPipeline creates a temporary YAML file for testing.
func writeTestPipeline(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test_pipeline.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test pipeline: %v", err)
	}
	return path
}

const validPipelineYAML = `
name: test_pipeline
description: Test pipeline for unit tests

schedule:
  enabled: false
  cron: "0 8 * * *"

model:
  preferred: omniroute/test-model
  fallbacks:
    - openrouter/fallback-model
  max_tokens: 1024
  temperature: 0.5

stages:
  - id: plan
    action: generate
    prompt_file: templates/plan.md
    output_key: plan

  - id: execute
    action: generate
    depends_on: [plan]
    prompt_file: templates/execute.md
    input_keys: [plan]
    output_key: draft

  - id: review
    action: self_review
    depends_on: [execute]
    criteria:
      - no_prompt_leak
    output_key: reviewed

  - id: publish
    action: http_post
    depends_on: [review]
    endpoint_env: TEST_WEBHOOK
    input_keys: [reviewed]

limits:
  require_review: true
  max_cost_per_run_usd: 0.0
  max_retries_per_stage: 3
  dead_loop_failures: 3

cache:
  enabled: true
  ttl_minutes: 60
  dir: data/cache

memory:
  enabled: true
  store_outputs: true
  compress_after_runs: 5
`

func TestLoadPipeline_Valid(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)

	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	// Verify basic fields
	if p.Name != "test_pipeline" {
		t.Errorf("Name = %q, want test_pipeline", p.Name)
	}
	if p.Description != "Test pipeline for unit tests" {
		t.Errorf("Description = %q", p.Description)
	}

	// Verify schedule
	if p.Schedule.Enabled != false {
		t.Errorf("Schedule.Enabled = %v, want false", p.Schedule.Enabled)
	}
	if p.Schedule.Cron != "0 8 * * *" {
		t.Errorf("Schedule.Cron = %q", p.Schedule.Cron)
	}

	// Verify model
	if p.Model.Preferred != "omniroute/test-model" {
		t.Errorf("Model.Preferred = %q", p.Model.Preferred)
	}
	if len(p.Model.Fallbacks) != 1 {
		t.Errorf("Model.Fallbacks len = %d, want 1", len(p.Model.Fallbacks))
	}
	if p.Model.MaxTokens != 1024 {
		t.Errorf("Model.MaxTokens = %d, want 1024", p.Model.MaxTokens)
	}

	// Verify stages
	if len(p.Stages) != 4 {
		t.Fatalf("Stages len = %d, want 4", len(p.Stages))
	}

	// Verify stage order and fields
	expectedIDs := []string{"plan", "execute", "review", "publish"}
	for i, expected := range expectedIDs {
		if p.Stages[i].ID != expected {
			t.Errorf("Stage[%d].ID = %q, want %q", i, p.Stages[i].ID, expected)
		}
	}

	// Verify depends_on
	executeStage := p.StageByID("execute")
	if executeStage == nil {
		t.Fatal("StageByID(execute) returned nil")
	}
	if len(executeStage.DependsOn) != 1 || executeStage.DependsOn[0] != "plan" {
		t.Errorf("execute.DependsOn = %v, want [plan]", executeStage.DependsOn)
	}

	// Verify limits
	if p.Limits.RequireReview != true {
		t.Errorf("Limits.RequireReview = %v, want true", p.Limits.RequireReview)
	}
	if p.Limits.DeadLoopFailures != 3 {
		t.Errorf("Limits.DeadLoopFailures = %d, want 3", p.Limits.DeadLoopFailures)
	}
}

func TestLoadPipeline_MissingFile(t *testing.T) {
	_, err := LoadPipeline(context.Background(), "/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadPipeline_MalformedYAML(t *testing.T) {
	path := writeTestPipeline(t, "invalid: [yaml: content")
	_, err := LoadPipeline(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestLoadPipeline_MissingName(t *testing.T) {
	yaml := `
description: No name
stages:
  - id: test
    action: generate
`
	path := writeTestPipeline(t, yaml)
	_, err := LoadPipeline(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestLoadPipeline_NoStages(t *testing.T) {
	yaml := `
name: empty
stages: []
`
	path := writeTestPipeline(t, yaml)
	_, err := LoadPipeline(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for empty stages")
	}
}

func TestLoadPipeline_DuplicateStageID(t *testing.T) {
	yaml := `
name: dup
stages:
  - id: same
    action: generate
  - id: same
    action: generate
`
	path := writeTestPipeline(t, yaml)
	_, err := LoadPipeline(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for duplicate stage ID")
	}
}

func TestLoadPipeline_UnknownDependency(t *testing.T) {
	yaml := `
name: bad_dep
stages:
  - id: stage1
    action: generate
    depends_on: [nonexistent]
`
	path := writeTestPipeline(t, yaml)
	_, err := LoadPipeline(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestLoadPipeline_ContextCanceled(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := LoadPipeline(ctx, path)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestPipeline_StageByID(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	// Existing stage
	stage := p.StageByID("review")
	if stage == nil {
		t.Fatal("StageByID(review) returned nil")
	}
	if stage.Action != "self_review" {
		t.Errorf("review.Action = %q, want self_review", stage.Action)
	}

	// Non-existing stage
	stage = p.StageByID("nonexistent")
	if stage != nil {
		t.Errorf("StageByID(nonexistent) = %v, want nil", stage)
	}
}
