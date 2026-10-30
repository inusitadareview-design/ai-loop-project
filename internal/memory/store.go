// Package memory implements the persistent memory layer (invariant I5,
// ARCHITECTURE.md 4.12). Go only reads and appends; compression is
// delegated to Rust's memory-compressor via FFI (in a later increment).
//
// File layout (inside data/memory/):
//
//	<pipeline_id>.events.jsonl     — append-only log, one JSON per line
//	<pipeline_id>.events.jsonl.bak — backup of previous version (D4)
//
// Corruption handling follows decision tree D4 from docs/DECISIONS.md:
//   - last record truncated → discard only last, keep the rest
//   - file unreadable        → use .bak if available
//   - no backup              → return empty slice, log ERROR
//
// Guardrails enforced:
//   - I5   — Append-only; no deletes, no in-place edits.
//   - G2.2 — All writes stay inside the configured data/memory/ path.
//   - G3.1 — Compression (future) will create new files, never delete
//     originals without a backup.
//   - G5.1 — Errors are wrapped with fmt.Errorf, never swallowed.
package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Event is a single append-only record in a pipeline's memory log.
// Fields are exported so they round-trip through JSON cleanly.
type Event struct {
	// ID is a stable identifier for the event. Caller-supplied; the
	// Store does not generate IDs so that the same event can be
	// re-appended idempotently by the engine if needed.
	ID string `json:"id"`
	// PipelineID identifies the pipeline this event belongs to.
	PipelineID string `json:"pipeline_id"`
	// StageID is the stage that produced this event (may be empty for
	// pipeline-level events like "pipeline_complete").
	StageID string `json:"stage_id,omitempty"`
	// Timestamp is the Unix timestamp in seconds when the event was
	// produced. The Store records the value as-is; the caller decides
	// the source of truth.
	Timestamp int64 `json:"timestamp"`
	// Type is a free-form category: "stage_output", "pipeline_complete",
	// "pipeline_failed", etc. Kept as a string so the Store stays
	// schema-flexible.
	Type string `json:"type"`
	// Data carries the payload (stage output, metadata, etc.). The
	// Store does not interpret it.
	Data map[string]interface{} `json:"data,omitempty"`
}

// Store manages append-only event logs for pipelines. It is safe for
// concurrent use (writes are serialized per pipeline_id via a mutex map).
type Store struct {
	// basePath is the root directory for all memory files. Must be
	// inside data/ per guardrail G2.2.
	basePath string

	// mu gates concurrent writes to the same pipeline_id. A single
	// mutex would suffice but serializes unrelated pipelines; a map
	// keyed by pipeline_id gives per-pipeline concurrency.
	muMap sync.Map // map[string]*sync.Mutex
}

// NewStore builds a Store rooted at basePath (typically "data/memory").
// It creates the directory if it does not exist (G2.2: runtime may
// create data/ subdirs). Returns an error only if the directory cannot
// be prepared.
func NewStore(basePath string) (*Store, error) {
	if basePath == "" {
		return nil, errors.New("memory: basePath must not be empty")
	}
	if err := os.MkdirAll(basePath, 0o750); err != nil {
		return nil, fmt.Errorf("memory: create base path %q: %w", basePath, err)
	}
	return &Store{basePath: basePath}, nil
}

// Append adds an event to the pipeline's event log. The event is
// written as a single JSON line followed by a newline. The file is
// opened in append mode and created if it does not exist.
//
// Append is idempotent only at the I/O level: duplicate IDs are NOT
// deduplicated. The engine is responsible for not re-appending the
// same event twice within a single run.
func (s *Store) Append(ctx context.Context, pipelineID string, ev Event) error {
	if s == nil {
		return errors.New("memory: nil Store")
	}
	if pipelineID == "" {
		return errors.New("memory: pipelineID must not be empty")
	}
	if ev.ID == "" {
		return errors.New("memory: event.ID must not be empty")
	}
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().Unix()
	}

	// Check context before any I/O.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memory: %w", err)
	}

	// Serialize writes to this pipeline_id.
	mu := s.lockFor(pipelineID)
	mu.Lock()
	defer mu.Unlock()

	path := s.eventsPath(pipelineID)

	// Open for append, create if missing, mode 0640 (owner rw, group r).
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("memory: open %q: %w", path, err)
	}
	defer f.Close()

	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("memory: marshal event %q: %w", ev.ID, err)
	}
	line = append(line, '\n')

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("memory: write to %q: %w", path, err)
	}

	log.Debug().
		Str("pipeline_id", pipelineID).
		Str("event_id", ev.ID).
		Str("event_type", ev.Type).
		Msg("memory: event appended")

	return nil
}

// ReadAll returns every event in the pipeline's log in append order.
// Implements D4:
//
//   - If the file does not exist, returns an empty slice (not an error).
//   - If the last line is truncated (e.g., process crashed mid-write),
//     it is silently discarded and the rest are returned.
//   - If the file is entirely unreadable (permissions, corruption),
//     falls back to the .bak file.
//   - If neither file is usable, returns an empty slice and logs ERROR.
func (s *Store) ReadAll(ctx context.Context, pipelineID string) ([]Event, error) {
	if s == nil {
		return nil, errors.New("memory: nil Store")
	}
	if pipelineID == "" {
		return nil, errors.New("memory: pipelineID must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}

	path := s.eventsPath(pipelineID)
	events, err := s.readEventsFile(path)

	if err != nil {
		// File entirely unreadable — try .bak (D4: "use last backup
		// available").
		bakPath := path + ".bak"
		log.Warn().
			Err(err).
			Str("path", path).
			Str("backup", bakPath).
			Msg("memory: primary file unreadable, trying backup")

		events, err = s.readEventsFile(bakPath)
		if err != nil {
			// D4: "no backup? → recreate empty, log ERROR with hash
			// of lost file". We skip the hash for now (increment 2
			// will add it) but still log the error clearly.
			log.Error().
				Err(err).
				Str("pipeline_id", pipelineID).
				Str("primary", path).
				Str("backup", bakPath).
				Msg("memory: both primary and backup unreadable; returning empty")
			return []Event{}, nil
		}
	}

	return events, nil
}

// LastN returns the most recent N events for a pipeline. If the log
// has fewer than N events, all are returned. This is a convenience
// wrapper around ReadAll; the full log is read into memory. For very
// large logs, a future increment may add a streaming variant.
func (s *Store) LastN(ctx context.Context, pipelineID string, n int) ([]Event, error) {
	if n < 0 {
		return nil, fmt.Errorf("memory: LastN n must be >= 0, got %d", n)
	}
	all, err := s.ReadAll(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	if n >= len(all) {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// Backup creates a copy of the current events file at `<path>.bak`.
// This is called by the compressor (future increment) BEFORE producing
// a new compressed file, so the original is preserved (G3.1).
//
// If the source file does not exist, Backup returns nil without
// creating a .bak (nothing to back up).
func (s *Store) Backup(ctx context.Context, pipelineID string) error {
	if s == nil {
		return errors.New("memory: nil Store")
	}
	if pipelineID == "" {
		return errors.New("memory: pipelineID must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memory: %w", err)
	}

	src := s.eventsPath(pipelineID)
	dst := src + ".bak"

	// Check source existence.
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing to back up — not an error
		}
		return fmt.Errorf("memory: stat %q: %w", src, err)
	}
	if info.IsDir() {
		return fmt.Errorf("memory: %q is a directory, not a file", src)
	}

	// Open source for read.
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("memory: open source %q: %w", src, err)
	}
	defer in.Close()

	// Create/truncate destination.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("memory: open backup %q: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("memory: copy to backup %q: %w", dst, err)
	}

	log.Debug().
		Str("pipeline_id", pipelineID).
		Str("backup", dst).
		Msg("memory: backup created")
	return nil
}

// eventsPath returns the canonical path for a pipeline's event log.
// pipelineID is sanitized to prevent path traversal.
func (s *Store) eventsPath(pipelineID string) string {
	// Strip any path components so a malicious pipelineID like
	// "../../etc/passwd" cannot escape basePath.
	safe := filepath.Base(pipelineID)
	if safe == "." || safe == "/" {
		safe = "default"
	}
	return filepath.Join(s.basePath, safe+".events.jsonl")
}

// SnapshotPath returns the canonical path for a pipeline's snapshot file.
// Used by the compressor to write compressed snapshots.
func (s *Store) SnapshotPath(pipelineID string) string {
	safe := filepath.Base(pipelineID)
	if safe == "." || safe == "/" {
		safe = "default"
	}
	return filepath.Join(s.basePath, safe+".snapshot.txt")
}

// lockFor returns the per-pipeline mutex, creating it lazily.
func (s *Store) lockFor(pipelineID string) *sync.Mutex {
	v, _ := s.muMap.LoadOrStore(pipelineID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// readEventsFile reads a .jsonl file, one JSON object per line. It
// applies the D4 "truncated last record" rule: if the last line is
// not valid JSON, it is dropped and the rest is returned. An entirely
// empty file yields an empty slice.
func (s *Store) readEventsFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	// 1 MB per line should be more than enough for any event payload.
	const maxLineBytes = 1 << 20
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxLineBytes)

	var lines [][]byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Copy the line out of the scanner's buffer — it is reused.
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, cp)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}

	// Try parsing every line. If the LAST line fails to parse, treat
	// it as a truncated write from a crashed process (D4) and drop it.
	// Any OTHER line failing to parse is a real corruption error.
	for i, raw := range lines {
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			if i == len(lines)-1 {
				// D4: "last record truncated? → discard only last".
				log.Warn().
					Err(err).
					Str("path", path).
					Int("line", i+1).
					Msg("memory: dropping truncated last record")
				break
			}
			return nil, fmt.Errorf("parse line %d of %q: %w", i+1, path, err)
		}
		events = append(events, ev)
	}

	return events, nil
}
