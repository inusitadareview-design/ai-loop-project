package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore creates a Store rooted in a fresh t.TempDir().
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// mkEvent builds a minimal valid Event for tests.
func mkEvent(id, pipelineID, evType string) Event {
	return Event{
		ID:         id,
		PipelineID: pipelineID,
		StageID:    "stage-" + id,
		Timestamp:  1730000000,
		Type:       evType,
		Data:       map[string]interface{}{"msg": "hello-" + id},
	}
}

// --- NewStore ----------------------------------------------------------

func TestNewStore_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "memory")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory, got file")
	}
	_ = s
}

func TestNewStore_EmptyPath(t *testing.T) {
	_, err := NewStore("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

// --- Append ------------------------------------------------------------

func TestAppend_WritesJSONLines(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ev := mkEvent(string(rune('a'+i)), "news_blog", "stage_output")
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	// Read the file directly and verify the JSONL format.
	path := s.eventsPath("news_blog")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
		if ev.PipelineID != "news_blog" {
			t.Errorf("line %d: pipeline_id = %q, want news_blog", i, ev.PipelineID)
		}
	}
}

func TestAppend_EmptyPipelineID(t *testing.T) {
	s := newTestStore(t)
	err := s.Append(context.Background(), "", mkEvent("e1", "", "x"))
	if err == nil {
		t.Fatal("expected error for empty pipelineID")
	}
}

func TestAppend_EmptyEventID(t *testing.T) {
	s := newTestStore(t)
	ev := mkEvent("e1", "news_blog", "x")
	ev.ID = ""
	err := s.Append(context.Background(), "news_blog", ev)
	if err == nil {
		t.Fatal("expected error for empty event.ID")
	}
}

func TestAppend_NilStore(t *testing.T) {
	var s *Store
	err := s.Append(context.Background(), "news_blog", mkEvent("e1", "news_blog", "x"))
	if err == nil {
		t.Fatal("expected error for nil Store")
	}
}

func TestAppend_ContextCanceled(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.Append(ctx, "news_blog", mkEvent("e1", "news_blog", "x"))
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestAppend_SetsTimestampIfZero(t *testing.T) {
	s := newTestStore(t)
	ev := mkEvent("e1", "news_blog", "x")
	ev.Timestamp = 0

	if err := s.Append(context.Background(), "news_blog", ev); err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, err := s.ReadAll(context.Background(), "news_blog")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Timestamp == 0 {
		t.Error("Timestamp was not auto-set")
	}
}

// --- ReadAll -----------------------------------------------------------

func TestReadAll_EmptyFile(t *testing.T) {
	s := newTestStore(t)
	events, err := s.ReadAll(context.Background(), "news_blog")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 for nonexistent file", len(events))
	}
}

func TestReadAll_ReturnsAllEventsInOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		ev := mkEvent(string(rune('a'+i)), "news_blog", "stage_output")
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	events, err := s.ReadAll(ctx, "news_blog")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}
	for i, ev := range events {
		wantID := string(rune('a' + i))
		if ev.ID != wantID {
			t.Errorf("events[%d].ID = %q, want %q", i, ev.ID, wantID)
		}
	}
}

func TestReadAll_DropsTruncatedLastRecord(t *testing.T) {
	// D4: "last record truncated? → discard only last, keep rest"
	s := newTestStore(t)
	ctx := context.Background()

	// Append two valid events.
	if err := s.Append(ctx, "news_blog", mkEvent("e1", "news_blog", "x")); err != nil {
		t.Fatalf("Append e1: %v", err)
	}
	if err := s.Append(ctx, "news_blog", mkEvent("e2", "news_blog", "x")); err != nil {
		t.Fatalf("Append e2: %v", err)
	}

	// Append a truncated third line (incomplete JSON).
	path := s.eventsPath("news_blog")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString(`{"id":"e3","pipeline_id":"news_blog","type":"x",` + "\n") // truncated
	_ = f.Close()

	events, err := s.ReadAll(ctx, "news_blog")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (truncated last should be dropped)", len(events))
	}
	if events[0].ID != "e1" || events[1].ID != "e2" {
		t.Errorf("events = [%q, %q], want [e1, e2]", events[0].ID, events[1].ID)
	}
}

func TestReadAll_FallsBackToBackup(t *testing.T) {
	// D4: "file unreadable? → use last backup (.bak) available"
	s := newTestStore(t)
	ctx := context.Background()

	// Append to primary, then back it up, then corrupt the primary.
	if err := s.Append(ctx, "news_blog", mkEvent("e1", "news_blog", "x")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Backup(ctx, "news_blog"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Corrupt primary: replace with garbage that is not valid JSONL.
	path := s.eventsPath("news_blog")
	if err := os.WriteFile(path, []byte("not jsonl at all\nand more garbage"), 0o640); err != nil {
		t.Fatalf("corrupt primary: %v", err)
	}

	events, err := s.ReadAll(ctx, "news_blog")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (from backup)", len(events))
	}
	if events[0].ID != "e1" {
		t.Errorf("events[0].ID = %q, want e1", events[0].ID)
	}
}

func TestReadAll_ReturnsEmptyWhenNoBackup(t *testing.T) {
	// D4: "no backup? → recreate empty, log ERROR"
	s := newTestStore(t)

	// Write garbage to primary, no .bak exists.
	path := s.eventsPath("news_blog")
	if err := os.WriteFile(path, []byte("not jsonl at all\nmore garbage"), 0o640); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	events, err := s.ReadAll(context.Background(), "news_blog")
	if err != nil {
		t.Fatalf("ReadAll should return empty, not error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 (empty fallback)", len(events))
	}
}

func TestReadAll_EmptyPipelineID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.ReadAll(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty pipelineID")
	}
}

func TestReadAll_NilStore(t *testing.T) {
	var s *Store
	_, err := s.ReadAll(context.Background(), "news_blog")
	if err == nil {
		t.Fatal("expected error for nil Store")
	}
}

// --- LastN -------------------------------------------------------------

func TestLastN_ReturnsLastN(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		ev := mkEvent(string(rune('a'+i)), "news_blog", "x")
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	last3, err := s.LastN(ctx, "news_blog", 3)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(last3) != 3 {
		t.Fatalf("got %d events, want 3", len(last3))
	}
	if last3[0].ID != "h" || last3[1].ID != "i" || last3[2].ID != "j" {
		t.Errorf("last3 IDs = [%q, %q, %q], want [h, i, j]",
			last3[0].ID, last3[1].ID, last3[2].ID)
	}
}

func TestLastN_NGreaterThanLen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Append(ctx, "news_blog", mkEvent("e1", "news_blog", "x")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	all, err := s.LastN(ctx, "news_blog", 100)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d events, want 1 (all available)", len(all))
	}
}

func TestLastN_NegativeN(t *testing.T) {
	s := newTestStore(t)
	_, err := s.LastN(context.Background(), "news_blog", -1)
	if err == nil {
		t.Fatal("expected error for negative N")
	}
}

// --- Backup ------------------------------------------------------------

func TestBackup_CreatesCopy(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Append(ctx, "news_blog", mkEvent("e1", "news_blog", "x")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Backup(ctx, "news_blog"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	primary := s.eventsPath("news_blog")
	backup := primary + ".bak"

	pData, err := os.ReadFile(primary)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	bData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(pData) != string(bData) {
		t.Errorf("primary and backup differ:\nprimary: %q\nbackup: %q", pData, bData)
	}
}

func TestBackup_NonexistentSource(t *testing.T) {
	s := newTestStore(t)
	// Backup on a pipeline with no events file should succeed (no-op).
	if err := s.Backup(context.Background(), "news_blog"); err != nil {
		t.Fatalf("Backup: %v (should be no-op for nonexistent source)", err)
	}
}

// --- Path traversal defense --------------------------------------------

func TestEventsPath_SanitizesPipelineID(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// A malicious pipelineID must NOT escape basePath.
	malicious := []string{
		"../../etc/passwd",
		"/etc/passwd",
		"..\\..\\windows\\system32",
	}
	for _, id := range malicious {
		path := s.eventsPath(id)
		if !strings.HasPrefix(path, s.basePath) {
			t.Errorf("eventsPath(%q) = %q escaped basePath %q", id, path, s.basePath)
		}
	}
}

// --- Concurrency -------------------------------------------------------

func TestAppend_ConcurrentSamePipeline(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const workers = 10
	const perWorker = 20
	errs := make(chan error, workers*perWorker)

	for w := 0; w < workers; w++ {
		w := w
		go func() {
			for i := 0; i < perWorker; i++ {
				id := string(rune('A'+w)) + "-" + string(rune('0'+i%10))
				ev := mkEvent(id, "news_blog", "stage_output")
				errs <- s.Append(ctx, "news_blog", ev)
			}
		}()
	}

	for i := 0; i < workers*perWorker; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}

	events, err := s.ReadAll(ctx, "news_blog")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := workers * perWorker
	if len(events) != want {
		t.Errorf("got %d events, want %d", len(events), want)
	}
}

// --- Errors are wrapped ------------------------------------------------

func TestAppend_ErrorIsWrapped(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Create a file where a directory is expected for the events path.
	path := s.eventsPath("news_blog")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err = s.Append(context.Background(), "news_blog", mkEvent("e1", "news_blog", "x"))
	if err == nil {
		t.Fatal("expected error")
	}
	// The error must mention "memory:" so callers can identify the source.
	if !strings.Contains(err.Error(), "memory:") {
		t.Errorf("error = %q, want wrapped with 'memory:'", err)
	}
}

// --- Unused but defensive: ensure no panics on edge cases --------------

func TestReadAll_ContextCanceledBeforeStart(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.ReadAll(ctx, "news_blog")
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// --- Sprint 18b: LastNStreaming (bounded reads, I5/G3.4) ---------------

func lastNEvents(t *testing.T, value any) []Event {
	t.Helper()
	events, ok := value.([]Event)
	if !ok {
		t.Fatalf("LastNStreaming returned %T, want []memory.Event", value)
	}
	return events
}

func TestLastNStreaming_ReturnsLastN(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Append 100 events.
	for i := 0; i < 100; i++ {
		ev := mkEvent(fmt.Sprintf("evt-%03d", i), "news_blog", "stage_output")
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	// LastNStreaming should return the last 10 without loading all 100.
	result, err := s.LastNStreaming(ctx, "news_blog", 10)
	if err != nil {
		t.Fatalf("LastNStreaming: %v", err)
	}
	last10 := lastNEvents(t, result)
	if len(last10) != 10 {
		t.Fatalf("got %d events, want 10", len(last10))
	}

	// Verify order (chronological: evt-090, evt-091, ..., evt-099).
	for i, ev := range last10 {
		wantID := fmt.Sprintf("evt-%03d", 90+i)
		if ev.ID != wantID {
			t.Errorf("last10[%d].ID = %q, want %q", i, ev.ID, wantID)
		}
	}
}

func TestLastNStreaming_NGreaterThanLen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Append 5 events.
	for i := 0; i < 5; i++ {
		ev := mkEvent(fmt.Sprintf("evt-%d", i), "news_blog", "x")
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Request 100, should return all 5.
	result, err := s.LastNStreaming(ctx, "news_blog", 100)
	if err != nil {
		t.Fatalf("LastNStreaming: %v", err)
	}
	all := lastNEvents(t, result)
	if len(all) != 5 {
		t.Errorf("got %d events, want 5 (all available)", len(all))
	}
}

func TestLastNStreaming_EmptyFile(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	result, err := s.LastNStreaming(ctx, "news_blog", 10)
	if err != nil {
		t.Fatalf("LastNStreaming: %v", err)
	}
	events := lastNEvents(t, result)
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 (empty file)", len(events))
	}
}

func TestLastNStreaming_HandlesTruncatedLastLine(t *testing.T) {
	// D4: truncated last record should be dropped.
	s := newTestStore(t)
	ctx := context.Background()

	// Append 3 valid events.
	for i := 0; i < 3; i++ {
		ev := mkEvent(fmt.Sprintf("evt-%d", i), "news_blog", "x")
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Append a truncated line.
	path := s.eventsPath("news_blog")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString(`{"id":"truncated","pipeline_id":"news_blog","type":"x",` + "\n")
	_ = f.Close()

	// LastNStreaming should skip the truncated line and return the last 2 valid events.
	result, err := s.LastNStreaming(ctx, "news_blog", 2)
	if err != nil {
		t.Fatalf("LastNStreaming: %v", err)
	}
	last2 := lastNEvents(t, result)
	if len(last2) != 2 {
		t.Fatalf("got %d events, want 2", len(last2))
	}
	if last2[0].ID != "evt-1" || last2[1].ID != "evt-2" {
		t.Errorf("last2 = [%q, %q], want [evt-1, evt-2]", last2[0].ID, last2[1].ID)
	}
}

// --- Sprint 18b: Compress (async memory-compressor, I5) ---------------

func TestCompress_SmallBatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Append 10 events with varied content.
	for i := 0; i < 10; i++ {
		ev := Event{
			ID:         fmt.Sprintf("evt-%d", i),
			PipelineID: "news_blog",
			StageID:    "write",
			Timestamp:  1730000000 + int64(i),
			Type:       "stage_completed",
			Data: map[string]interface{}{
				"output_key": fmt.Sprintf("article_%d", i),
				"word_count": 600 + i*10,
			},
		}
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	stats, err := s.Compress(ctx, "news_blog", 20)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil stats for 10 events")
	}
	if stats.EventsCompressed != 10 {
		t.Errorf("EventsCompressed = %d, want 10", stats.EventsCompressed)
	}
	if stats.CompressedBytes <= 0 {
		t.Errorf("CompressedBytes = %d, want > 0", stats.CompressedBytes)
	}

	// Verify snapshot file was created.
	if _, err := os.Stat(stats.SnapshotPath); err != nil {
		t.Errorf("snapshot file not found: %v", err)
	}

	// Verify backup was created.
	if _, err := os.Stat(stats.BackupPath); err != nil {
		t.Errorf("backup file not found: %v", err)
	}
}

func TestCompress_SkipsSmallBatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Append only 1 event.
	if err := s.Append(ctx, "news_blog", mkEvent("e1", "news_blog", "x")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	stats, err := s.Compress(ctx, "news_blog", 20)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats != nil {
		t.Errorf("expected nil stats for 1 event (skip), got %+v", stats)
	}
}

func TestCompress_EmptyPipeline(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	stats, err := s.Compress(ctx, "news_blog", 20)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats != nil {
		t.Errorf("expected nil stats for empty pipeline, got %+v", stats)
	}
}

func TestCompress_PreservesOriginals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Append 5 events.
	for i := 0; i < 5; i++ {
		ev := mkEvent(fmt.Sprintf("evt-%d", i), "news_blog", "x")
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Read original file content.
	originalPath := s.eventsPath("news_blog")
	originalData, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	// Compress.
	_, err = s.Compress(ctx, "news_blog", 20)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	// Verify original file is unchanged (G3.1: never delete originals).
	afterData, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read original after: %v", err)
	}
	if !bytes.Equal(originalData, afterData) {
		t.Error("original .events.jsonl was modified by compression (G3.1 violation)")
	}
}

// --- Sprint 18b: spent_usd cache (G1.1 optimization) ---------------

func TestGetSpentToday_CacheHit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// First call: cache miss, scans files.
	spent1, err := s.GetSpentToday(ctx)
	if err != nil {
		t.Fatalf("GetSpentToday: %v", err)
	}

	// Second call within 60s: should hit cache.
	spent2, err := s.GetSpentToday(ctx)
	if err != nil {
		t.Fatalf("GetSpentToday: %v", err)
	}
	if spent1 != spent2 {
		t.Errorf("cache returned different values: %f != %f", spent1, spent2)
	}
}

func TestInvalidateSpentCache_ForcesRescan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Populate cache.
	_, err := s.GetSpentToday(ctx)
	if err != nil {
		t.Fatalf("GetSpentToday: %v", err)
	}

	// Invalidate.
	s.InvalidateSpentCache()

	// Verify cache time is zero (expired).
	s.spentCacheMu.Lock()
	isExpired := s.spentCacheTime.IsZero()
	s.spentCacheMu.Unlock()

	if !isExpired {
		t.Error("cache was not invalidated")
	}
}

// --- Sprint 18b P2: snapshot.txt must hold plain text, not the FFI envelope ---

func TestCompress_WritesPlainSnapshotNotJSONEnvelope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		ev := mkEvent(fmt.Sprintf("evt-%d", i), "news_blog", "stage_completed")
		ev.Timestamp = 1730000000 + int64(i)
		if err := s.Append(ctx, "news_blog", ev); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	stats, err := s.Compress(ctx, "news_blog", 20)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil stats for 5 events")
	}

	data, err := os.ReadFile(stats.SnapshotPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	content := strings.TrimSpace(string(data))

	// P2 assertion: the file must NOT be the CompressionResult JSON envelope.
	if strings.HasPrefix(content, "{") {
		t.Error("snapshot.txt holds the FFI JSON envelope; must hold result.Snapshot plain text (P2)")
	}
	if !strings.Contains(content, "[memory_snapshot v1]") {
		t.Errorf("snapshot missing version marker; got first 120 chars: %.120q", content)
	}

	// Stats must still come from the parsed FFI result.
	if stats.EventsCompressed != 5 {
		t.Errorf("EventsCompressed = %d, want 5", stats.EventsCompressed)
	}
	if stats.CompressedBytes <= 0 {
		t.Errorf("CompressedBytes = %d, want > 0", stats.CompressedBytes)
	}
}
