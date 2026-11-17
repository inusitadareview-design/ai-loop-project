package memory

import (
	"context"
	"encoding/json"
	"errors"
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
