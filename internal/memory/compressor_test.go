package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestCompressPipelineMemory_EndToEnd(t *testing.T) {
	// Create a temporary directory for the store
	tempDir, err := os.MkdirTemp("", "memory-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create store
	store, err := NewStore(tempDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	// Append some events
	for i := 0; i < 5; i++ {
		err := store.Append(ctx, "test_pipeline", Event{
			ID:        fmt.Sprintf("e%d", i),
			Timestamp: int64(1000 + i),
			Data:      map[string]any{"msg": fmt.Sprintf("message %d", i)},
		})
		if err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	// Compress
	err = CompressPipelineMemory(ctx, store, "test_pipeline")
	if err != nil {
		t.Fatalf("CompressPipelineMemory failed: %v", err)
	}

	// Verify snapshot was created
	snapshotPath := filepath.Join(tempDir, "test_pipeline.snapshot.txt")
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		t.Error("Snapshot file was not created")
	}

	// Verify backup was created
	backupPath := filepath.Join(tempDir, "test_pipeline.events.jsonl.bak")
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("Backup file was not created")
	}

	// Verify original events file still exists (G3.1: append-only)
	eventsPath := filepath.Join(tempDir, "test_pipeline.events.jsonl")
	if _, err := os.Stat(eventsPath); os.IsNotExist(err) {
		t.Error("Original events file was deleted (G3.1 violation)")
	}
}

func TestCompressIfNeeded_BelowThreshold(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "memory-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewStore(tempDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	// Append only 3 events (below threshold of 10)
	for i := 0; i < 3; i++ {
		store.Append(ctx, "test_pipeline", Event{
			ID:        fmt.Sprintf("e%d", i),
			Timestamp: int64(1000 + i),
		})
	}

	// Should not compress (below threshold)
	err = CompressIfNeeded(ctx, store, "test_pipeline", 10)
	if err != nil {
		t.Errorf("CompressIfNeeded failed: %v", err)
	}

	// Verify snapshot was NOT created
	snapshotPath := filepath.Join(tempDir, "test_pipeline.snapshot.txt")
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Error("Snapshot should not exist when below threshold")
	}
}

func TestCompressIfNeeded_AboveThreshold(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "memory-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewStore(tempDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	// Append 15 events (above threshold of 10)
	for i := 0; i < 15; i++ {
		store.Append(ctx, "test_pipeline", Event{
			ID:        fmt.Sprintf("e%d", i),
			Timestamp: int64(1000 + i),
		})
	}

	// Should compress (above threshold)
	err = CompressIfNeeded(ctx, store, "test_pipeline", 10)
	if err != nil {
		t.Errorf("CompressIfNeeded failed: %v", err)
	}

	// Verify snapshot was created
	snapshotPath := filepath.Join(tempDir, "test_pipeline.snapshot.txt")
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		t.Error("Snapshot should exist when above threshold")
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