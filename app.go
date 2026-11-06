// app.go defines the App struct that Wails exposes to the frontend.
// This is the bridge between Go backend and React frontend.
//
// Sprint 8 increment 7a: stub implementation — methods return placeholder
// data. Real implementations will be added in increments 7b-7e as we
// migrate each API endpoint to Wails bindings.
//
// Architecture:
//   - Methods exported (capitalized) are callable from frontend as
//     window.go.main.App.MethodName()
//   - Context is automatically injected by Wails for frontend calls
//   - Return values are serialized to JSON automatically
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the main application struct exposed to the frontend.
// Wails generates bindings for all exported methods.
type App struct {
	ctx     context.Context
	version string
}

// NewApp creates a new App instance. Called once at startup by Wails.
func NewApp(version string) *App {
	return &App{
		version: version,
	}
}

// startup is called when the app starts. The context is saved for
// later use (e.g., emitting events to the frontend).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// --- Stub methods (real implementations in increments 7b-7e) ---

// GetVersion returns the application version.
// Frontend calls: window.go.main.App.GetVersion()
func (a *App) GetVersion() string {
	return a.version
}

// ListPipelines returns all pipelines (stub for now).
// Real implementation in increment 7b.
func (a *App) ListPipelines() ([]map[string]any, error) {
	// Stub: return empty list
	// TODO (increment 7b): integrate with engine.LoadPipeline
	return []map[string]any{}, nil
}

// RunPipeline executes a pipeline by name (stub for now).
// Real implementation in increment 7b.
func (a *App) RunPipeline(name string) (map[string]any, error) {
	// Stub: return error
	// TODO (increment 7b): integrate with scheduler.RunNow
	return nil, fmt.Errorf("not implemented yet (increment 7b)")
}

// GetDrafts returns all pending drafts from data/outputs/*.draft.json
func (a *App) GetDrafts() ([]map[string]any, error) {
	outputsDir := "data/outputs"

	// Ensure directory exists
	if err := os.MkdirAll(outputsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create outputs dir: %w", err)
	}

	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		return nil, fmt.Errorf("read outputs dir: %w", err)
	}

	var drafts []map[string]any
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".draft.json") {
			continue
		}

		path := filepath.Join(outputsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn().Err(err).Str("path", path).Msg("failed to read draft file")
			continue
		}

		var draft map[string]any
		if err := json.Unmarshal(data, &draft); err != nil {
			log.Warn().Err(err).Str("path", path).Msg("failed to parse draft file")
			continue
		}

		drafts = append(drafts, draft)
	}

	return drafts, nil
}

// ApproveDraft renames a draft file to .approved.json and emits an event
func (a *App) ApproveDraft(id string) error {
	outputsDir := "data/outputs"

	// Extract timestamp from ID (format: "pipeline-timestamp")
	parts := strings.Split(id, "-")
	if len(parts) < 2 {
		return fmt.Errorf("invalid draft ID format: %q", id)
	}
	timestamp := parts[len(parts)-1]

	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		return fmt.Errorf("read outputs dir: %w", err)
	}

	// Find file containing the timestamp
	for _, entry := range entries {
		if !strings.Contains(entry.Name(), timestamp) || !strings.HasSuffix(entry.Name(), ".draft.json") {
			continue
		}

		draftPath := filepath.Join(outputsDir, entry.Name())
		approvedPath := strings.Replace(draftPath, ".draft.json", ".approved.json", 1)

		if err := os.Rename(draftPath, approvedPath); err != nil {
			return fmt.Errorf("rename draft: %w", err)
		}

		// Emit event to frontend
		pipeline := extractPipelineFromFilename(entry.Name())
		a.emitEvent("draft_approved", map[string]any{
			"draft_id": id,
			"pipeline": pipeline,
		})

		log.Info().Str("draft_id", id).Str("pipeline", pipeline).Msg("draft approved")
		return nil
	}

	return fmt.Errorf("draft %q not found", id)
}

// RejectDraft renames a draft file to .rejected.json and emits an event
func (a *App) RejectDraft(id, reason string) error {
	outputsDir := "data/outputs"

	// Extract timestamp from ID (format: "pipeline-timestamp")
	parts := strings.Split(id, "-")
	if len(parts) < 2 {
		return fmt.Errorf("invalid draft ID format: %q", id)
	}
	timestamp := parts[len(parts)-1]

	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		return fmt.Errorf("read outputs dir: %w", err)
	}

	// Find file containing the timestamp
	for _, entry := range entries {
		if !strings.Contains(entry.Name(), timestamp) || !strings.HasSuffix(entry.Name(), ".draft.json") {
			continue
		}

		draftPath := filepath.Join(outputsDir, entry.Name())
		rejectedPath := strings.Replace(draftPath, ".draft.json", ".rejected.json", 1)

		if err := os.Rename(draftPath, rejectedPath); err != nil {
			return fmt.Errorf("rename draft: %w", err)
		}

		// Emit event to frontend
		pipeline := extractPipelineFromFilename(entry.Name())
		a.emitEvent("draft_rejected", map[string]any{
			"draft_id": id,
			"pipeline": pipeline,
			"reason":   reason,
		})

		log.Info().Str("draft_id", id).Str("pipeline", pipeline).Str("reason", reason).Msg("draft rejected")
		return nil
	}

	return fmt.Errorf("draft %q not found", id)
}

// extractPipelineFromFilename extracts pipeline name from "pipeline.timestamp.draft.json"
func extractPipelineFromFilename(filename string) string {
	parts := strings.Split(filename, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

// GetPipelineMemory returns memory events for a pipeline (stub for now).
// Real implementation in increment 7d.
func (a *App) GetPipelineMemory(name string, limit int) ([]map[string]any, error) {
	// Stub: return empty list
	// TODO (increment 7d): integrate with memory.Store.LastN
	return []map[string]any{}, nil
}

// --- Event emission helpers (for future increments) ---

// emitEvent sends a custom event to the frontend.
// Frontend listens via runtime.EventsOn(eventName, callback).
func (a *App) emitEvent(eventName string, data ...interface{}) {
	runtime.EventsEmit(a.ctx, eventName, data...)
}

// Example usage in future increments:
// a.emitEvent("draft_created", map[string]any{
//     "pipeline": "news_blog",
//     "draft_id": "news_blog-123",
// })
