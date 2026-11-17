// Package events defines shared event types and interfaces used across
// internal packages (engine, scheduler, api) to decouple event emission
// from event transport (WebSocket hub, future: message queue, etc.).
//
// This package has NO dependencies on other internal packages, making
// it safe to import from any layer without creating circular deps.
package events

// Broadcaster is the interface for emitting domain events to interested
// subscribers (e.g. WebSocket hub, message queue, audit logger).
//
// Implementations must be safe for concurrent use.
// The payload map is free-form; callers document the schema per event type.
//
// Event types used in this project:
//   - pipeline_started, pipeline_completed, pipeline_failed, pipeline_paused
//     (emitted by scheduler)
//   - draft_created, draft_approved, draft_rejected
//     (emitted by engine and api/drafts handlers)
type Broadcaster interface {
	Broadcast(eventType string, payload map[string]any)
}

// NoopBroadcaster is a broadcaster that discards all events.
// Useful as a default value to avoid nil checks throughout the codebase.
type NoopBroadcaster struct{}

// Broadcast implements Broadcaster by silently discarding the event.
func (NoopBroadcaster) Broadcast(string, map[string]any) {}
