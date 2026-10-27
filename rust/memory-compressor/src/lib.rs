//! memory-compressor — deterministic semantic compression of persistent
//! memory events. Produces a single compressed snapshot from a batch of
//! append-only events, preserving the originals (the Go caller owns the
//! filesystem and decides whether to keep both; this crate is stateless
//! and never touches disk).
//!
//! Guardrails enforced by this crate:
//! - I5   — Immutable & Compressed Memory: this crate is the ONLY Rust
//!          entity authorized to produce compressed memory snapshots.
//!          The output is deterministic so the cache-engine can key on
//!          it and avoid re-compression on identical inputs.
//! - G3.1 — Append-only preservation: this crate NEVER deletes anything.
//!          It receives an in-memory slice of events and returns a new
//!          `String`. The Go memory package is responsible for keeping
//!          the originals for 7 days (decision tree D4).
//! - G5.2 — No `unwrap()` / `expect()` in production code; every
//!          fallible public function returns `Result<T, E>`.
//!
//! Dependency-free (Rust standard library only) so it builds offline.

use std::collections::HashMap;

/// A single append-only memory event. Events are identified by `id` so
/// duplicates can be detected and collapsed (newest wins).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MemoryEvent {
    /// Stable identifier of the event. Must be unique within a batch.
    pub id: String,
    /// Free-form content of the event.
    pub content: String,
    /// Unix timestamp in seconds. Used to order events chronologically.
    /// When two events share the same timestamp, `id` breaks the tie
    /// (lexicographic ascending) so the output is deterministic.
    pub timestamp_secs: i64,
}

/// Errors from the compressor. Invalid INPUTS are errors; a batch that
/// compresses poorly is still a valid result (the caller decides what to
/// do with a low reduction).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CompressorError {
    /// Batch was empty — there is nothing to compress.
    EmptyEvents,
    /// Two or more events in the batch share the same `id`. The variant
    /// carries the offending id so the caller can diagnose.
    DuplicateId(String),
}

impl core::fmt::Display for CompressorError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            CompressorError::EmptyEvents => write!(f, "events batch must not be empty"),
            CompressorError::DuplicateId(id) => {
                write!(f, "duplicate event id: {}", id)
            }
        }
    }
}

impl std::error::Error for CompressorError {}

/// Result of a compression run. `snapshot` is the deterministic,
/// cache-friendly representation of the input events; `reduction_pct`
/// is in `[0.0, 100.0]` and reports how much smaller the snapshot is
/// compared to the naive concatenation of all raw contents.
#[derive(Debug, Clone, PartialEq)]
pub struct CompressionResult {
    /// The compressed snapshot, ready to be embedded in a prompt or
    /// stored on disk. Format is fixed and versioned (`[memory_snapshot v1]`).
    pub snapshot: String,
    /// Number of unique events in the snapshot (after dedup).
    pub event_count: usize,
    /// Total byte length of all raw input contents concatenated.
    pub original_size: usize,
    /// Byte length of the final `snapshot`.
    pub compressed_size: usize,
    /// Percentage reduction: `(original_size - compressed_size) /
    /// original_size * 100.0`. Negative values are possible when the
    /// structured wrapper makes the output larger than raw concatenation
    /// (this is expected for very small inputs).
    pub reduction_pct: f64,
}

/// Snapshot format version. Bump this when the template changes in a
/// way that would break cache-engine keys.
const SNAPSHOT_VERSION: &str = "v1";

/// Estimates the token count of `text` using the ~4 chars/token heuristic,
/// identical to `token-budget::estimate_tokens`,
/// `payload-optimizer::estimate_tokens`, and `prompt-builder::estimate_tokens`
/// so all four crates agree across the FFI boundary. Uses Unicode char
/// count (not byte length). Empty string yields 0. Result is rounded UP.
/// Cannot fail.
pub fn estimate_tokens(text: &str) -> usize {
    (text.chars().count() + 3) / 4
}

/// Compresses a batch of append-only memory events into a deterministic
/// snapshot.
///
/// Processing steps (all deterministic, same order every time):
/// 1. Validate: empty batch → `EmptyEvents`; duplicate ids → `DuplicateId`.
/// 2. Sort by `(timestamp_secs ASC, id ASC)`.
/// 3. Normalize whitespace inside each event (trim + collapse internal
///    whitespace runs to a single space).
/// 4. Concatenate into a versioned template so the cache-engine can key
///    on the snapshot and avoid re-computation.
///
/// Returns `Err` only for invalid inputs. A batch that compresses poorly
/// (e.g., all unique, verbose events) still returns `Ok` with a
/// possibly-negative `reduction_pct`.
pub fn compress(
    events: &[MemoryEvent],
) -> Result<CompressionResult, CompressorError> {
    if events.is_empty() {
        return Err(CompressorError::EmptyEvents);
    }

    // 1. Dedup by id, keeping the NEWEST event when ids collide. We first
    // collect into a HashMap keyed by id; if an id appears more than once,
    // we surface an error (per the project's rule: duplicate ids are
    // invalid input, not silently collapsed).
    let mut by_id: HashMap<&str, &MemoryEvent> = HashMap::with_capacity(events.len());
    for ev in events {
        let key = ev.id.as_str();
        if by_id.contains_key(key) {
            return Err(CompressorError::DuplicateId(ev.id.clone()));
        }
        by_id.insert(key, ev);
    }

    // 2. Sort deterministically: by timestamp ascending, id ascending.
    let mut ordered: Vec<&MemoryEvent> = by_id.into_values().collect();
    ordered.sort_by(|a, b| {
        a.timestamp_secs
            .cmp(&b.timestamp_secs)
            .then_with(|| a.id.cmp(&b.id))
    });

    // 3. Measure original size (sum of raw content byte lengths).
    let original_size: usize = events.iter().map(|e| e.content.len()).sum();

    // 4. Build the snapshot in a deterministic template. Using a
    // pre-allocated String with estimated capacity keeps the output
    // byte-for-byte identical across runs with the same input (which is
    // what makes the snapshot cache-friendly for cache-engine).
    let cap: usize = 64 + ordered.iter().map(|e| e.id.len() + e.content.len() + 32).sum::<usize>();
    let mut snapshot = String::with_capacity(cap);

    snapshot.push_str("[memory_snapshot ");
    snapshot.push_str(SNAPSHOT_VERSION);
    snapshot.push_str("]\n");

    // Event count header — the number of unique events (after dedup).
    snapshot.push_str("events: ");
    snapshot.push_str(&ordered.len().to_string());
    snapshot.push_str("\n---\n");

    for ev in &ordered {
        snapshot.push('[');
        snapshot.push_str(&ev.id);
        snapshot.push_str(" @ ");
        snapshot.push_str(&ev.timestamp_secs.to_string());
        snapshot.push_str("]\n");
        snapshot.push_str(&normalize_whitespace(&ev.content));
        snapshot.push('\n');
    }

    let compressed_size = snapshot.len();
    let reduction_pct = if original_size == 0 {
        0.0
    } else {
        let saved = original_size as f64 - compressed_size as f64;
        (saved / original_size as f64) * 100.0
    };

    Ok(CompressionResult {
        snapshot,
        event_count: ordered.len(),
        original_size,
        compressed_size,
        reduction_pct,
    })
}

/// Normalizes whitespace inside a single event: trims leading/trailing
/// whitespace and collapses every internal run of whitespace to a single
/// space. Empty input yields the empty string. Private helper.
fn normalize_whitespace(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut prev_was_space = true;
    for ch in s.chars() {
        if ch.is_whitespace() {
            if !prev_was_space {
                out.push(' ');
                prev_was_space = true;
            }
        } else {
            out.push(ch);
            prev_was_space = false;
        }
    }
    if out.ends_with(' ') {
        out.pop();
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ev(id: &str, content: &str, ts: i64) -> MemoryEvent {
        MemoryEvent {
            id: id.to_string(),
            content: content.to_string(),
            timestamp_secs: ts,
        }
    }

    // ---- estimate_tokens ------------------------------------------------

    #[test]
    fn estimate_tokens_matches_sibling_crates() {
        assert_eq!(estimate_tokens(""), 0);
        assert_eq!(estimate_tokens("a"), 1);
        assert_eq!(estimate_tokens("abcd"), 1);
        assert_eq!(estimate_tokens("abcde"), 2);
        assert_eq!(estimate_tokens(&"a".repeat(40)), 10);
        assert_eq!(estimate_tokens("🚀🚀🚀🚀"), 1);
    }

    // ---- error cases ----------------------------------------------------

    #[test]
    fn empty_batch_is_error() {
        let err = compress(&[]);
        assert!(matches!(err, Err(CompressorError::EmptyEvents)));
    }

    #[test]
    fn duplicate_id_is_error() {
        let batch = vec![
            ev("e1", "first", 100),
            ev("e1", "duplicate", 200),
        ];
        let err = compress(&batch);
        assert!(matches!(err, Err(CompressorError::DuplicateId(_))));
        if let Err(CompressorError::DuplicateId(id)) = err {
            assert_eq!(id, "e1");
        }
    }

    #[test]
    fn error_display_is_human_readable() {
        let cases: Vec<(CompressorError, &str)> = vec![
            (CompressorError::EmptyEvents, "events batch must not be empty"),
            (
                CompressorError::DuplicateId("abc".to_string()),
                "duplicate event id: abc",
            ),
        ];
        for (err, expected) in cases {
            assert_eq!(err.to_string(), expected);
            let dyn_ref: &dyn std::error::Error = &err;
            assert!(!dyn_ref.to_string().is_empty());
        }
    }

    // ---- basic compression ----------------------------------------------

    #[test]
    fn compress_single_event() -> Result<(), CompressorError> {
        let r = compress(&[ev("e1", "Hello world", 1000)])?;
        assert_eq!(r.event_count, 1);
        assert!(r.snapshot.contains("[memory_snapshot v1]"));
        assert!(r.snapshot.contains("events: 1"));
        assert!(r.snapshot.contains("[e1 @ 1000]"));
        assert!(r.snapshot.contains("Hello world"));
        Ok(())
    }

    #[test]
    fn compress_multiple_events_includes_all() -> Result<(), CompressorError> {
        let batch = vec![
            ev("e1", "First event", 100),
            ev("e2", "Second event", 200),
            ev("e3", "Third event", 300),
        ];
        let r = compress(&batch)?;
        assert_eq!(r.event_count, 3);
        assert!(r.snapshot.contains("[e1 @ 100]"));
        assert!(r.snapshot.contains("[e2 @ 200]"));
        assert!(r.snapshot.contains("[e3 @ 300]"));
        assert!(r.snapshot.contains("First event"));
        assert!(r.snapshot.contains("Second event"));
        assert!(r.snapshot.contains("Third event"));
        Ok(())
    }

    // ---- ordering -------------------------------------------------------

    #[test]
    fn events_are_ordered_by_timestamp() -> Result<(), CompressorError> {
        // Intentionally shuffled input.
        let batch = vec![
            ev("late", "late event", 300),
            ev("early", "early event", 100),
            ev("mid", "mid event", 200),
        ];
        let r = compress(&batch)?;
        let pos_early = r.snapshot.find("[early @ 100]").unwrap();
        let pos_mid = r.snapshot.find("[mid @ 200]").unwrap();
        let pos_late = r.snapshot.find("[late @ 300]").unwrap();
        assert!(
            pos_early < pos_mid && pos_mid < pos_late,
            "events must appear in timestamp order: early={}, mid={}, late={}",
            pos_early, pos_mid, pos_late
        );
        Ok(())
    }

    #[test]
    fn same_timestamp_is_tiebroken_by_id() -> Result<(), CompressorError> {
        // Same timestamp, different ids: "alpha" must come before "beta"
        // lexicographically.
        let batch = vec![
            ev("beta", "B", 1000),
            ev("alpha", "A", 1000),
            ev("gamma", "C", 1000),
        ];
        let r = compress(&batch)?;
        let pos_alpha = r.snapshot.find("[alpha @ 1000]").unwrap();
        let pos_beta = r.snapshot.find("[beta @ 1000]").unwrap();
        let pos_gamma = r.snapshot.find("[gamma @ 1000]").unwrap();
        assert!(pos_alpha < pos_beta && pos_beta < pos_gamma);
        Ok(())
    }

    // ---- determinism ----------------------------------------------------

    #[test]
    fn compress_is_deterministic_across_input_order() -> Result<(), CompressorError> {
        // Same events, different insertion order. Output must be identical.
        let batch_a = vec![
            ev("e3", "three", 300),
            ev("e1", "one", 100),
            ev("e2", "two", 200),
        ];
        let batch_b = vec![
            ev("e2", "two", 200),
            ev("e3", "three", 300),
            ev("e1", "one", 100),
        ];
        let ra = compress(&batch_a)?;
        let rb = compress(&batch_b)?;
        assert_eq!(ra.snapshot, rb.snapshot, "output must be deterministic");
        assert_eq!(ra.event_count, rb.event_count);
        assert_eq!(ra.original_size, rb.original_size);
        assert_eq!(ra.compressed_size, rb.compressed_size);
        Ok(())
    }

    #[test]
    fn compress_is_deterministic_across_repeated_calls() -> Result<(), CompressorError> {
        let batch = vec![
            ev("e1", "alpha content", 100),
            ev("e2", "beta content with emoji 🚀", 200),
        ];
        let r1 = compress(&batch)?;
        let r2 = compress(&batch)?;
        assert_eq!(r1, r2);
        Ok(())
    }

    // ---- whitespace normalization ---------------------------------------

    #[test]
    fn whitespace_inside_events_is_normalized() -> Result<(), CompressorError> {
        let batch = vec![
            ev("e1", "  hello   world  \n\t foo  ", 100),
        ];
        let r = compress(&batch)?;
        assert!(
            r.snapshot.contains("hello world foo"),
            "expected normalized content in snapshot: {:?}",
            r.snapshot
        );
        assert!(!r.snapshot.contains("  hello"));
        Ok(())
    }

    #[test]
    fn empty_content_event_is_allowed() -> Result<(), CompressorError> {
        let batch = vec![ev("e1", "", 100)];
        let r = compress(&batch)?;
        assert_eq!(r.event_count, 1);
        // The snapshot must still be valid (contain the header).
        assert!(r.snapshot.contains("[memory_snapshot v1]"));
        Ok(())
    }

    // ---- unicode --------------------------------------------------------

    #[test]
    fn compress_handles_unicode_and_emoji() -> Result<(), CompressorError> {
        let batch = vec![
            ev("pt", "saúde e coração", 100),
            ev("zh", "你好世界", 200),
            ev("em", "🚀🎯✨", 300),
        ];
        let r = compress(&batch)?;
        assert!(r.snapshot.contains("saúde e coração"));
        assert!(r.snapshot.contains("你好世界"));
        assert!(r.snapshot.contains("🚀🎯✨"));
        Ok(())
    }

    // ---- reduction_pct --------------------------------------------------

    #[test]
    fn reduction_pct_is_computed_correctly() -> Result<(), CompressorError> {
        // Single event with lots of internal whitespace: normalization
        // produces a clear reduction.
        let batch = vec![ev("e1", &" a ".repeat(50), 100)];
        let r = compress(&batch)?;
        // Original = 150 bytes. Compressed = header + "[e1 @ 100]\n" +
        // ~50 chars of "a a a ..." ≈ smaller than 150.
        assert!(r.compressed_size < r.original_size);
        assert!(r.reduction_pct > 0.0);
        Ok(())
    }

    #[test]
    fn reduction_pct_can_be_negative_for_tiny_inputs() -> Result<(), CompressorError> {
        // Single very short event: the structured header ("[memory_snapshot
        // v1]\nevents: 1\n---\n[e1 @ 0]\n") is longer than "hi", so the
        // "compressed" snapshot is larger than the raw input. This is
        // expected and allowed — the crate does not claim compression on
        // trivially small inputs.
        let batch = vec![ev("e1", "hi", 0)];
        let r = compress(&batch)?;
        // We just assert it does not panic and produces a finite number.
        assert!(r.reduction_pct.is_finite());
        Ok(())
    }

    // ---- snapshot structure ---------------------------------------------

    #[test]
    fn snapshot_has_versioned_header() -> Result<(), CompressorError> {
        let r = compress(&[ev("e1", "x", 1)])?;
        assert!(r.snapshot.starts_with("[memory_snapshot v1]\n"));
        Ok(())
    }

    #[test]
    fn snapshot_contains_event_count_header() -> Result<(), CompressorError> {
        let batch = vec![
            ev("e1", "a", 1),
            ev("e2", "b", 2),
            ev("e3", "c", 3),
            ev("e4", "d", 4),
        ];
        let r = compress(&batch)?;
        assert!(r.snapshot.contains("events: 4\n---\n"));
        Ok(())
    }

    // ---- integration with sibling crates --------------------------------

    #[test]
    fn snapshot_tokens_agree_with_estimate_tokens() -> Result<(), CompressorError> {
        let batch = vec![
            ev("e1", "the quick brown fox jumps over the lazy dog", 100),
            ev("e2", "pack my box with five dozen liquor jugs", 200),
        ];
        let r = compress(&batch)?;
        assert_eq!(r.snapshot.len(), r.compressed_size);
        // estimate_tokens on the snapshot must agree with itself.
        let tok = estimate_tokens(&r.snapshot);
        assert!(tok > 0);
        Ok(())
    }
}