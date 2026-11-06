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
	"fmt"

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

// GetDrafts returns pending drafts (stub for now).
// Real implementation in increment 7c.
func (a *App) GetDrafts() ([]map[string]any, error) {
	// Stub: return empty list
	// TODO (increment 7c): read from data/outputs/*.draft.json
	return []map[string]any{}, nil
}

// ApproveDraft approves a draft by ID (stub for now).
// Real implementation in increment 7c.
func (a *App) ApproveDraft(id string) error {
	// Stub: return error
	// TODO (increment 7c): rename file to .approved.json + emit event
	return fmt.Errorf("not implemented yet (increment 7c)")
}

// RejectDraft rejects a draft by ID (stub for now).
// Real implementation in increment 7c.
func (a *App) RejectDraft(id, reason string) error {
	// Stub: return error
	// TODO (increment 7c): rename file to .rejected.json + emit event
	return fmt.Errorf("not implemented yet (increment 7c)")
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
