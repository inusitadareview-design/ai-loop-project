package engine

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestEngine_Run_ValidPipeline(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if result.PipelineName != "test_pipeline" {
		t.Errorf("PipelineName = %q, want test_pipeline", result.PipelineName)
	}

	// Verify all stage outputs are present
	expectedKeys := []string{"plan", "draft", "reviewed"}
	for _, key := range expectedKeys {
		if _, ok := result.Outputs[key]; !ok {
			t.Errorf("missing output key %q", key)
		}
	}

	// Verify outputs are non-empty
	for key, val := range result.Outputs {
		if val == "" {
			t.Errorf("output %q is empty", key)
		}
	}
}

func TestEngine_Run_NilPipeline(t *testing.T) {
	e := NewEngine()
	_, err := e.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil pipeline")
	}
}

func TestEngine_Run_ContextCanceled(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	e := NewEngine()
	result, err := e.Run(ctx, p)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if result.Status != StatusCancelled {
		t.Errorf("Status = %q, want cancelled", result.Status)
	}
}

func TestEngine_Run_ContextTimeout(t *testing.T) {
	path := writeTestPipeline(t, validPipelineYAML)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // Ensure timeout

	e := NewEngine()
	result, err := e.Run(ctx, p)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if result.Status != StatusCancelled {
		t.Errorf("Status = %q, want cancelled", result.Status)
	}
}

func TestEngine_Run_StageOutputsChain(t *testing.T) {
	// Test that outputs from one stage are available to the next.
	yaml := `
name: chain_test
stages:
  - id: first
    action: generate
    output_key: first_output
  - id: second
    action: self_review
    depends_on: [first]
    input_keys: [first_output]
    output_key: second_output
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The second stage should have received the first stage's output.
	if result.Outputs["second_output"] != result.Outputs["first_output"] {
		t.Errorf("second_output = %q, want %q (pass-through)",
			result.Outputs["second_output"], result.Outputs["first_output"])
	}
}

func TestEngine_Run_SelfReviewNoInput(t *testing.T) {
	// Test that self_review fails gracefully when no input is available.
	yaml := `
name: no_input
stages:
  - id: review
    action: self_review
    input_keys: [nonexistent]
    output_key: reviewed
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for missing input")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
}

func TestEngine_Run_UnknownAction(t *testing.T) {
	yaml := `
name: unknown_action
stages:
  - id: test
    action: nonexistent_action
    output_key: out
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
}

func TestEngine_Run_HTTPPostStub(t *testing.T) {
	yaml := `
name: http_test
stages:
  - id: generate
    action: generate
    output_key: content
  - id: publish
    action: http_post
    depends_on: [generate]
    input_keys: [content]
    endpoint_env: TEST_WEBHOOK
    output_key: published
limits:
  require_review: true
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if result.Outputs["published"] != "published_stub" {
		t.Errorf("published = %q, want published_stub", result.Outputs["published"])
	}
}

func TestEngine_Run_MultipleStagesSequential(t *testing.T) {
	// Test a pipeline with many stages to verify sequential execution.
	yaml := `
name: sequential
stages:
  - id: s1
    action: generate
    output_key: out1
  - id: s2
    action: generate
    depends_on: [s1]
    output_key: out2
  - id: s3
    action: generate
    depends_on: [s2]
    output_key: out3
  - id: s4
    action: self_review
    depends_on: [s3]
    input_keys: [out3]
    output_key: out4
`
	path := writeTestPipeline(t, yaml)
	p, err := LoadPipeline(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}

	e := NewEngine()
	result, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", result.Status)
	}

	// All 4 outputs should be present
	for i := 1; i <= 4; i++ {
		key := fmt.Sprintf("out%d", i)
		if _, ok := result.Outputs[key]; !ok {
			t.Errorf("missing output key %q", key)
		}
	}
}
