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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	// spentCache caches GetSpentToday results to avoid scanning all files
	// on every budget check. TTL: 60 seconds.
	spentCacheMu    sync.Mutex
	spentCacheValue float64
	spentCacheTime  time.Time
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
// has fewer than N events, all are returned. Uses LastNStreaming for
// large files (> 64KB) to avoid loading the entire log into memory.
func (s *Store) LastN(ctx context.Context, pipelineID string, n int) ([]Event, error) {
	if s == nil {
		return nil, errors.New("memory: nil Store")
	}
	if pipelineID == "" {
		return nil, errors.New("memory: pipelineID must not be empty")
	}
	if n < 0 {
		return nil, fmt.Errorf("memory: LastN n must be >= 0, got %d", n)
	}
	// Delegate to streaming variant — it handles both small and large files.
	return s.LastNStreaming(ctx, pipelineID, n)
}

// LastNStreaming returns the most recent N events for a pipeline by reading
// the JSONL file from the end backwards. This avoids loading the entire file
// into memory, making it suitable for large logs (I5/G3.4 bounded reads).
//
// Returns events in chronological order (oldest to newest).
// Falls back to ReadAll if the file is small enough (< 64KB).
func (s *Store) LastNStreaming(ctx context.Context, pipelineID string, n int) ([]Event, error) {
	if s == nil {
		return nil, errors.New("memory: nil Store")
	}
	if pipelineID == "" {
		return nil, errors.New("memory: pipelineID must not be empty")
	}
	if n < 0 {
		return nil, fmt.Errorf("memory: LastNStreaming n must be >= 0, got %d", n)
	}
	if n == 0 {
		return []Event{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}

	path := s.eventsPath(pipelineID)

	// Check file size — fall back to ReadAll for small files.
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, nil
		}
		return nil, fmt.Errorf("memory: stat %q: %w", path, err)
	}
	if info.Size() < 64*1024 { // < 64KB
		// CRITICAL: call ReadAll directly to avoid infinite recursion
		// (LastN → LastNStreaming → LastN → ...).
		all, readErr := s.ReadAll(ctx, pipelineID)
		if readErr != nil {
			return nil, readErr
		}
		if n >= len(all) {
			return all, nil
		}
		return all[len(all)-n:], nil
	}

	// Read from end backwards.
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("memory: open %q: %w", path, err)
	}
	defer f.Close()

	const chunkSize = 8 * 1024 // 8KB chunks
	buf := make([]byte, chunkSize)
	var events []Event
	var remainder []byte
	offset := info.Size()

	for offset > 0 && len(events) < n {
		readSize := int64(chunkSize)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("memory: seek: %w", err)
		}
		if _, err := io.ReadFull(f, buf[:readSize]); err != nil {
			return nil, fmt.Errorf("memory: read: %w", err)
		}

		// Prepend remainder from previous chunk.
		chunk := append(buf[:readSize], remainder...)
		remainder = nil

		// Split into lines (reverse order).
		lines := bytes.Split(chunk, []byte{'\n'})

		// Process lines from end to start.
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			if len(line) == 0 {
				continue
			}

			// If this is the first line of the chunk and offset > 0,
			// it might be incomplete — save as remainder for next chunk.
			if i == 0 && offset > 0 {
				remainder = line
				continue
			}

			// Try to parse the line.
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				// D4: truncated last record — skip and continue.
				log.Warn().
					Err(err).
					Str("path", path).
					Msg("memory: skipping malformed line in LastNStreaming")
				continue
			}

			events = append(events, ev)
			if len(events) >= n {
				break
			}
		}
	}

	// Reverse to chronological order.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nil
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

// CompressionStats holds metadata about a memory compression run.
// Returned by Compress() so the caller can log observability events.
type CompressionStats struct {
	// EventsCompressed is the number of events included in the snapshot.
	EventsCompressed int
	// OriginalBytes is the total size of raw event contents.
	OriginalBytes int
	// CompressedBytes is the size of the resulting snapshot.
	CompressedBytes int
	// ReductionPct is the percentage reduction achieved.
	ReductionPct float64
	// SnapshotPath is the absolute path where the snapshot was written.
	SnapshotPath string
	// BackupPath is the absolute path of the .bak file created before compression.
	BackupPath string
}

// Compress produces a deterministic compressed snapshot of the most recent
// N events for a pipeline using the Rust memory-compressor via FFI (I5).
//
// Decision tree:
//   - Fewer than 2 events → skip compression (not worth it)
//   - Backup fails → abort (G3.1: never lose originals)
//   - FFI fails → abort, log ERROR, keep originals intact
//   - Write snapshot fails → abort, keep originals intact
//
// The snapshot is written to `<pipeline_id>.snapshot.txt`. The original
// `.events.jsonl` file is preserved (plus a .bak backup) per G3.1.
//
// This method is safe to call from a goroutine — it acquires the per-pipeline
// mutex during the backup+write sequence to prevent concurrent corruption.
func (s *Store) Compress(ctx context.Context, pipelineID string, maxEvents int) (*CompressionStats, error) {
	if s == nil {
		return nil, errors.New("memory: nil Store")
	}
	if pipelineID == "" {
		return nil, errors.New("memory: pipelineID must not be empty")
	}
	if maxEvents <= 0 {
		maxEvents = 50 // sensible default
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}

	// 1. Read the most recent events (bounded, I5/G3.4).
	events, err := s.LastNStreaming(ctx, pipelineID, maxEvents)
	if err != nil {
		return nil, fmt.Errorf("memory: read events for compression: %w", err)
	}

	// Skip compression for tiny batches — not worth the FFI overhead.
	if len(events) < 2 {
		log.Debug().
			Str("pipeline_id", pipelineID).
			Int("event_count", len(events)).
			Msg("memory: skipping compression (batch too small)")
		return nil, nil
	}

	// 2. Serialize writes to prevent concurrent corruption.
	mu := s.lockFor(pipelineID)
	mu.Lock()
	defer mu.Unlock()

	// 3. Backup BEFORE compression (G3.1: never lose originals).
	if err := s.Backup(ctx, pipelineID); err != nil {
		return nil, fmt.Errorf("memory: backup before compression: %w", err)
	}

	// 4. Compress via the package FFI wrapper (I5: Rust-only compression).
	// CompressEvents owns the CGO call, copies the result and frees the Rust
	// pointer immediately (G5.3). Reusing it keeps a SINGLE FFI call site for
	// compression instead of two divergent wrappers.
	log.Debug().
		Str("pipeline_id", pipelineID).
		Int("events", len(events)).
		Msg("memory: calling Rust memory-compressor FFI")

	result, compressErr := CompressEvents(ctx, events)
	if compressErr != nil {
		// D7: FFI failure — abort compression, originals stay intact (G3.1).
		log.Error().
			Err(compressErr).
			Str("pipeline_id", pipelineID).
			Msg("memory: Rust memory-compressor FFI failed (originals preserved)")
		return nil, fmt.Errorf("memory: FFI compress: %w", compressErr)
	}

	// 5. Write the PLAIN snapshot text (result.Snapshot), NOT the FFI JSON
	// envelope. Context assembly (I5) reads this file as prompt-ready text.
	// Atomic write: temp file + rename, mode 0640.
	snapshotPath := s.SnapshotPath(pipelineID)
	tmpPath := snapshotPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(result.Snapshot), 0o640); err != nil {
		return nil, fmt.Errorf("memory: write snapshot tmp: %w", err)
	}
	if err := os.Rename(tmpPath, snapshotPath); err != nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Warn().Err(rmErr).Str("path", tmpPath).
				Msg("memory: failed to remove snapshot tmp (non-fatal)")
		}
		return nil, fmt.Errorf("memory: rename snapshot: %w", err)
	}

	stats := &CompressionStats{
		EventsCompressed: result.EventCount,
		OriginalBytes:    result.OriginalSize,
		CompressedBytes:  result.CompressedSize,
		ReductionPct:     result.ReductionPct,
		SnapshotPath:     snapshotPath,
		BackupPath:       s.eventsPath(pipelineID) + ".bak",
	}

	log.Info().
		Str("pipeline_id", pipelineID).
		Int("events", stats.EventsCompressed).
		Int("original_bytes", stats.OriginalBytes).
		Int("compressed_bytes", stats.CompressedBytes).
		Float64("reduction_pct", stats.ReductionPct).
		Str("snapshot", stats.SnapshotPath).
		Msg("memory: compression completed (I5, G3.1)")

	return stats, nil
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

// GetSpentToday returns the total spent_usd across all pipelines for today (UTC).
// Results are cached in memory for 60 seconds to avoid scanning all files
// on every budget check (G1.1 optimization).
func (s *Store) GetSpentToday(ctx context.Context) (float64, error) {
	if s == nil {
		return 0.0, errors.New("memory: nil Store")
	}
	if err := ctx.Err(); err != nil {
		return 0.0, fmt.Errorf("memory: %w", err)
	}

	// Check cache (60s TTL).
	s.spentCacheMu.Lock()
	if time.Since(s.spentCacheTime) < 60*time.Second {
		cached := s.spentCacheValue
		s.spentCacheMu.Unlock()
		return cached, nil
	}
	s.spentCacheMu.Unlock()

	// Get today's UTC date boundaries.
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	todayEnd := todayStart.Add(24 * time.Hour)

	// Read all .events.jsonl files in basePath.
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0.0, nil // no memory dir yet
		}
		return 0.0, fmt.Errorf("memory: read dir %q: %w", s.basePath, err)
	}

	var totalSpent float64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".events.jsonl") {
			continue
		}
		path := filepath.Join(s.basePath, entry.Name())
		spent, err := s.sumCostsInFile(path, todayStart, todayEnd)
		if err != nil {
			log.Warn().Err(err).Str("file", path).Msg("memory: failed to sum costs in file")
			continue // skip file, don't fail the whole operation
		}
		totalSpent += spent
	}

	// Update cache.
	s.spentCacheMu.Lock()
	s.spentCacheValue = totalSpent
	s.spentCacheTime = time.Now()
	s.spentCacheMu.Unlock()

	return totalSpent, nil
}

// InvalidateSpentCache clears the spent_usd cache, forcing the next
// GetSpentToday call to re-scan all files. Called after cost events
// are appended to keep budget enforcement accurate.
func (s *Store) InvalidateSpentCache() {
	if s == nil {
		return
	}
	s.spentCacheMu.Lock()
	s.spentCacheTime = time.Time{} // zero time = expired
	s.spentCacheMu.Unlock()
}

// sumCostsInFile reads a single .events.jsonl file and sums the "cost_usd"
// field from events within the given time range.
func (s *Store) sumCostsInFile(path string, start, end time.Time) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0.0, err
	}
	defer f.Close()

	var total float64
	scanner := bufio.NewScanner(f)
	const maxLineBytes = 1 << 20 // 1 MB
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse event to check timestamp and extract cost_usd.
		var ev struct {
			Timestamp int64                  `json:"timestamp"`
			Data      map[string]interface{} `json:"data,omitempty"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}

		// Check if event is within today's range.
		eventTime := time.Unix(ev.Timestamp, 0).UTC()
		if eventTime.Before(start) || !eventTime.Before(end) {
			continue
		}

		// Extract cost_usd from data.
		if cost, ok := ev.Data["cost_usd"].(float64); ok {
			total += cost
		}
	}

	if err := scanner.Err(); err != nil {
		return 0.0, fmt.Errorf("read %q: %w", path, err)
	}

	return total, nil
}
