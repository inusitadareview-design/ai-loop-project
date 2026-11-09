// templates.go provides template loading and variable substitution for
// pipeline stage prompts. Sprint 14 (Template Loading from Files).
//
// Templates are .md files stored in internal/pipeline/templates/ or
// config/pipelines/ (whitelist enforced per G2.2). Variables are
// substituted using Go's text/template package.
//
// Supported variables:
// - {{.StageID}} - current stage ID
// - {{.Pipeline}} - pipeline name
// - {{.Model}} - preferred model
// - {{.Input.<key>}} - input values from previous stages (e.g., {{.Input.plan}})
//
// Guardrails enforced:
// - G2.2: only read from whitelisted paths (internal/pipeline/templates/, config/pipelines/)
// - I1: token budget validation happens AFTER template rendering (on final prompt)
// - I2: log template loading for observability
// - G5.1: wrapped errors with context
package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/rs/zerolog/log"
)

// templateData holds the variables available to stage templates.
type templateData struct {
	StageID  string
	Pipeline string
	Model    string
	Input    map[string]string
}

// loadAndRenderTemplate reads a template file from disk, substitutes
// variables, and returns the rendered prompt string.
//
// Decision tree:
// - promptFile is empty → returns ("", nil) [caller uses fallback]
// - file not found → returns ("", error)
// - path not in whitelist → returns ("", error) (G2.2)
// - template parse error → returns ("", error)
// - template render error → returns ("", error)
// - success → returns (rendered_prompt, nil)
//
// Whitelist: only allows paths under:
// - internal/pipeline/templates/
// - config/pipelines/
// This prevents reading arbitrary files from disk (G2.2).
func loadAndRenderTemplate(
	ctx context.Context,
	promptFile string,
	data templateData,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("template: %w", err)
	}

	// Empty promptFile → caller uses fallback.
	if promptFile == "" {
		return "", nil
	}

	// G2.2: validate path is in whitelist.
	if !isTemplatePathAllowed(promptFile) {
		return "", fmt.Errorf("template: path %q not in whitelist (G2.2)", promptFile)
	}

	// Read template file.
	templateBytes, err := os.ReadFile(promptFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("template: file %q not found", promptFile)
		}
		return "", fmt.Errorf("template: read %q: %w", promptFile, err)
	}

	// Parse template.
	tmpl, err := template.New("stage_prompt").Parse(string(templateBytes))
	if err != nil {
		return "", fmt.Errorf("template: parse %q: %w", promptFile, err)
	}

	// Render template with data.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template: render %q: %w", promptFile, err)
	}

	rendered := buf.String()

	// I2: observability — log template loaded.
	log.Debug().
		Str("template_file", promptFile).
		Str("stage_id", data.StageID).
		Str("pipeline", data.Pipeline).
		Int("rendered_bytes", len(rendered)).
		Msg("engine: template loaded and rendered")

	return rendered, nil
}

// isTemplatePathAllowed checks if the given path is in the whitelist of
// allowed template directories (G2.2: no runtime reads outside project).
//
// Whitelist:
// - internal/pipeline/templates/
// - config/pipelines/
func isTemplatePathAllowed(path string) bool {
	// Normalize path to absolute for comparison.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	// Get current working directory.
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}

	// Build allowed paths.
	allowed := []string{
		filepath.Join(cwd, "internal", "pipeline", "templates"),
		filepath.Join(cwd, "config", "pipelines"),
	}

	// Check if path starts with any allowed directory.
	for _, allowedDir := range allowed {
		if strings.HasPrefix(absPath, allowedDir) {
			return true
		}
	}

	return false
}

// buildTemplateData constructs the templateData struct from pipeline and
// stage metadata plus the inputs map.
func buildTemplateData(p *Pipeline, stage Stage, inputs map[string]string) templateData {
	return templateData{
		StageID:  stage.ID,
		Pipeline: p.Name,
		Model:    p.Model.Preferred,
		Input:    inputs,
	}
}
