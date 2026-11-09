package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndRenderTemplate_EmptyPromptFile(t *testing.T) {
	ctx := context.Background()
	data := templateData{
		StageID:  "plan",
		Pipeline: "test_pipeline",
		Model:    "test-model",
		Input:    map[string]string{},
	}

	rendered, err := loadAndRenderTemplate(ctx, "", data)
	if err != nil {
		t.Fatalf("loadAndRenderTemplate: %v", err)
	}
	if rendered != "" {
		t.Errorf("rendered = %q, want empty string for empty promptFile", rendered)
	}
}

func TestLoadAndRenderTemplate_FileNotFound(t *testing.T) {
	ctx := context.Background()
	data := templateData{
		StageID:  "plan",
		Pipeline: "test_pipeline",
		Model:    "test-model",
		Input:    map[string]string{},
	}

	_, err := loadAndRenderTemplate(ctx, "internal/pipeline/templates/nonexistent.md", data)
	if err == nil {
		t.Fatal("expected error for missing template file")
	}
	if !contains(err.Error(), "not found") {
		t.Errorf("error = %q, want mention of 'not found'", err)
	}
}

func TestLoadAndRenderTemplate_PathNotInWhitelist(t *testing.T) {
	ctx := context.Background()
	data := templateData{
		StageID:  "plan",
		Pipeline: "test_pipeline",
		Model:    "test-model",
		Input:    map[string]string{},
	}

	// Try to read from /etc/passwd (outside whitelist).
	_, err := loadAndRenderTemplate(ctx, "/etc/passwd", data)
	if err == nil {
		t.Fatal("expected error for path outside whitelist (G2.2)")
	}
	if !contains(err.Error(), "whitelist") {
		t.Errorf("error = %q, want mention of 'whitelist'", err)
	}
}

func TestLoadAndRenderTemplate_SuccessWithVariables(t *testing.T) {
	// Create temp template file in allowed directory.
	tmp := t.TempDir()
	templateDir := filepath.Join(tmp, "internal", "pipeline", "templates")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	templateContent := `Stage: {{.StageID}}
Pipeline: {{.Pipeline}}
Model: {{.Model}}
Input plan: {{.Input.plan}}`

	templatePath := filepath.Join(templateDir, "test.md")
	if err := os.WriteFile(templatePath, []byte(templateContent), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	// Change to temp dir so whitelist check passes.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	ctx := context.Background()
	data := templateData{
		StageID:  "plan",
		Pipeline: "test_pipeline",
		Model:    "test-model",
		Input:    map[string]string{"plan": "planned content"},
	}

	rendered, err := loadAndRenderTemplate(ctx, "internal/pipeline/templates/test.md", data)
	if err != nil {
		t.Fatalf("loadAndRenderTemplate: %v", err)
	}

	expected := `Stage: plan
Pipeline: test_pipeline
Model: test-model
Input plan: planned content`
	if rendered != expected {
		t.Errorf("rendered = %q, want %q", rendered, expected)
	}
}

func TestIsTemplatePathAllowed(t *testing.T) {
	tests := []struct {
		path    string
		allowed bool
	}{
		{"internal/pipeline/templates/test.md", true},
		{"config/pipelines/test.yaml", true},
		{"/etc/passwd", false},
		{"/tmp/malicious.md", false},
		{"../outside.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isTemplatePathAllowed(tt.path); got != tt.allowed {
				t.Errorf("isTemplatePathAllowed(%q) = %v, want %v", tt.path, got, tt.allowed)
			}
		})
	}
}

func TestBuildTemplateData(t *testing.T) {
	p := &Pipeline{
		Name:  "test_pipeline",
		Model: Model{Preferred: "test-model"},
	}
	stage := Stage{ID: "plan"}
	inputs := map[string]string{"key1": "value1", "key2": "value2"}

	data := buildTemplateData(p, stage, inputs)

	if data.StageID != "plan" {
		t.Errorf("StageID = %q, want 'plan'", data.StageID)
	}
	if data.Pipeline != "test_pipeline" {
		t.Errorf("Pipeline = %q, want 'test_pipeline'", data.Pipeline)
	}
	if data.Model != "test-model" {
		t.Errorf("Model = %q, want 'test-model'", data.Model)
	}
	if data.Input["key1"] != "value1" {
		t.Errorf("Input[key1] = %q, want 'value1'", data.Input["key1"])
	}
}

// contains is a helper to check if a string contains a substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
