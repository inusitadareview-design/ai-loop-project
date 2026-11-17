package memory

// Tests for the FFI wrapper (CompressEvents) and content extraction.
// Compression ORCHESTRATION tests live in store_test.go (TestCompress_*):
// snapshot creation, backup, bounded reads, G3.1 preservation, P2 format.

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCompressEvents_ValidInput(t *testing.T) {
	events := []Event{
		{ID: "e1", Timestamp: 1000, Data: map[string]any{"msg": "hello"}},
		{ID: "e2", Timestamp: 2000, Data: map[string]any{"msg": "world"}},
	}

	result, err := CompressEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("CompressEvents failed: %v", err)
	}

	if result.EventCount != 2 {
		t.Errorf("EventCount = %d, want 2", result.EventCount)
	}

	if result.Snapshot == "" {
		t.Error("Snapshot is empty")
	}

	if result.OriginalSize <= 0 {
		t.Errorf("OriginalSize = %d, want > 0", result.OriginalSize)
	}

	if result.CompressedSize <= 0 {
		t.Errorf("CompressedSize = %d, want > 0", result.CompressedSize)
	}
}

func TestCompressEvents_EmptyInput(t *testing.T) {
	_, err := CompressEvents(context.Background(), []Event{})
	if err == nil {
		t.Error("CompressEvents with empty slice should fail")
	}
}

func TestCompressEvents_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := []Event{
		{ID: "e1", Timestamp: 1000, Data: map[string]any{"msg": "test"}},
	}

	_, err := CompressEvents(ctx, events)
	if err == nil {
		t.Error("CompressEvents with canceled context should fail")
	}
}

func TestEventContent_NilData(t *testing.T) {
	ev := Event{ID: "e1", Timestamp: 1000, Data: nil}
	content := eventContent(ev)
	if content != "" {
		t.Errorf("eventContent with nil Data = %q, want empty string", content)
	}
}

func TestEventContent_ValidData(t *testing.T) {
	ev := Event{
		ID:        "e1",
		Timestamp: 1000,
		Data:      map[string]any{"key": "value"},
	}
	content := eventContent(ev)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Errorf("eventContent output is not valid JSON: %v", err)
	}

	if parsed["key"] != "value" {
		t.Errorf("parsed[key] = %v, want 'value'", parsed["key"])
	}
}
