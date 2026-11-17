//! payload-optimizer — compresses and normalizes text payloads so they
//! fit the token budget enforced by `token-budget`.
//!
//! Guardrails enforced:
//! - I1   — Token Pre-Validation: every returned payload is sized to the
//!          requested target, measured with the same ~4 chars/token
//!          heuristic used by `token-budget` so both crates stay
//!          consistent across the FFI boundary.
//! - I3   — Intelligent Retry: when a provider returns HTTP 400
//!          (payload too large), the engine calls this crate to produce a
//!          smaller payload and retries (max 2 cycles).
//! - G5.2 — No `unwrap()` / `expect()` in production code; every fallible
//!          public function returns `Result<T, E>`.
//!
//! Dependency-free (Rust standard library only) so it builds offline.

/// Errors from the optimizer. Invalid INPUTS are errors; a payload that
/// simply does not fit after all strategies is returned truncated (not
/// an error) — the caller decides whether to retry with a smaller target.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum OptimizerError {
    /// `target_tokens` was zero, which is not a valid optimization goal.
    ZeroTarget,
}

impl core::fmt::Display for OptimizerError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            OptimizerError::ZeroTarget => write!(f, "target_tokens must be > 0"),
        }
    }
}

impl std::error::Error for OptimizerError {}

/// Result of an optimization run. `reduction_pct` is in `[0.0, 100.0]`.
#[derive(Debug, Clone, PartialEq)]
pub struct OptimizationResult {
    /// Estimated tokens of the original payload.
    pub original_tokens: usize,
    /// Estimated tokens of the optimized payload.
    pub optimized_tokens: usize,
    /// Percentage reduction: `(original - optimized) / original * 100.0`.
    /// 0.0 when original_tokens == 0.
    pub reduction_pct: f64,
    /// The optimized payload itself.
    pub optimized: String,
}

/// Estimates the token count of `text` using the ~4 chars/token heuristic,
/// identical to `token-budget::estimate_tokens` so both crates agree across
/// the FFI boundary. Uses Unicode char count (not byte length). Empty
/// string yields 0. Result is rounded UP (ceiling). Cannot fail.
pub fn estimate_tokens(text: &str) -> usize {
    // Same formula as token-budget: ceiling of chars/4.
    (text.chars().count() + 3) / 4
}

/// Optimizes `payload` to fit within `target_tokens` applying strategies in
/// order until it fits:
///
/// 1. Whitespace normalization (trim + collapse internal whitespace runs).
/// 2. Intelligent truncation (preserves the start of the payload, never
///    cuts in the middle of a Unicode scalar).
///
/// Returns `Err(OptimizerError::ZeroTarget)` only when `target_tokens` is
/// 0. A payload that does not fit after strategy 1 is truncated (not an
/// error) — the caller inspects `optimized_tokens` to decide whether to retry.
pub fn optimize(
    payload: &str,
    target_tokens: usize,
) -> Result<OptimizationResult, OptimizerError> {
    if target_tokens == 0 {
        return Err(OptimizerError::ZeroTarget);
    }

    let original_tokens = estimate_tokens(payload);

    // Strategy 1: whitespace normalization.
    let mut current = normalize_whitespace(payload);

    // Strategy 2: truncate to fit, preserving the start.
    if estimate_tokens(&current) > target_tokens {
        current = truncate_to_tokens(&current, target_tokens);
    }

    let optimized_tokens = estimate_tokens(&current);
    let reduction_pct = if original_tokens == 0 {
        0.0
    } else {
        let saved = original_tokens.saturating_sub(optimized_tokens) as f64;
        (saved / original_tokens as f64) * 100.0
    };

    Ok(OptimizationResult {
        original_tokens,
        optimized_tokens,
        reduction_pct,
        optimized: current,
    })
}

/// Normalizes whitespace: trims leading/trailing whitespace and collapses
/// every internal run of whitespace (spaces, tabs, newlines) to a single
/// space. Empty input yields the empty string.
fn normalize_whitespace(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut prev_was_space = true; // treat start as "after a space" to trim leading
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

/// Truncates `s` so its estimated token count is at most `target`.
/// Preserves the START of the string (where the most semantically relevant
/// content typically lives in prompts). Never cuts in the middle of a
/// Unicode scalar — walks char boundaries.
fn truncate_to_tokens(s: &str, target: usize) -> String {
    if target == 0 {
        return String::new();
    }
    let mut chars_taken: usize = 0;
    let mut end_byte: usize = 0;
    for ch in s.chars() {
        let next_tokens = (chars_taken + 1 + 3) / 4;
        if next_tokens > target {
            break;
        }
        chars_taken += 1;
        end_byte += ch.len_utf8();
    }
    s[..end_byte].to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    // ---- estimate_tokens ----

    #[test]
    fn estimate_tokens_matches_token_budget_heuristic() {
        assert_eq!(estimate_tokens(""), 0);
        assert_eq!(estimate_tokens("a"), 1);
        assert_eq!(estimate_tokens("abcd"), 1);
        assert_eq!(estimate_tokens("abcde"), 2);
        let text = "a".repeat(40);
        assert_eq!(estimate_tokens(&text), 10);
        assert_eq!(estimate_tokens("🚀🚀🚀🚀"), 1);
    }

    // ---- optimize: error cases ----

    #[test]
    fn optimize_zero_target_is_error() {
        let err = optimize("anything", 0);
        assert!(matches!(err, Err(OptimizerError::ZeroTarget)));
    }

    #[test]
    fn error_display_is_human_readable() {
        let e = OptimizerError::ZeroTarget;
        assert_eq!(e.to_string(), "target_tokens must be > 0");
        let dyn_ref: &dyn std::error::Error = &e;
        assert!(!dyn_ref.to_string().is_empty());
    }

    // ---- optimize: no-op cases ----

    #[test]
    fn empty_payload_returns_empty_result() -> Result<(), OptimizerError> {
        let r = optimize("", 10)?;
        assert_eq!(r.original_tokens, 0);
        assert_eq!(r.optimized_tokens, 0);
        assert_eq!(r.reduction_pct, 0.0);
        assert_eq!(r.optimized, "");
        Ok(())
    }

    #[test]
    fn payload_already_fits_returns_normalized() -> Result<(), OptimizerError> {
        let r = optimize("hello world", 100)?;
        assert_eq!(r.optimized, "hello world");
        assert!(r.optimized_tokens <= 100);
        assert!(r.reduction_pct >= 0.0);
        Ok(())
    }

    // ---- strategy 1: whitespace normalization ----

    #[test]
    fn whitespace_is_normalized() -> Result<(), OptimizerError> {
        let r = optimize("  hello   world  \n\t foo  ", 100)?;
        assert_eq!(r.optimized, "hello world foo");
        Ok(())
    }

    #[test]
    fn leading_and_trailing_whitespace_trimmed() -> Result<(), OptimizerError> {
        let r = optimize("\n\n  hi  \n\n", 100)?;
        assert_eq!(r.optimized, "hi");
        Ok(())
    }

    // ---- strategy 2: intelligent truncation ----

    #[test]
    fn truncation_preserves_start_of_payload() -> Result<(), OptimizerError> {
        let input = "a".repeat(40);
        let r = optimize(&input, 3)?;
        assert!(r.optimized.starts_with("aaaaaaaaaaa"));
        assert!(r.optimized_tokens <= 3);
        assert!(r.reduction_pct > 0.0);
        Ok(())
    }

    #[test]
    fn truncation_respects_exact_token_limit() -> Result<(), OptimizerError> {
        let input = "x".repeat(100);
        let r = optimize(&input, 5)?;
        assert!(r.optimized_tokens <= 5);
        assert!(r.optimized.chars().count() <= 20);
        Ok(())
    }

    #[test]
    fn truncation_target_one_token_allows_up_to_four_chars() -> Result<(), OptimizerError> {
        let input = "abcdefghij";
        let r = optimize(input, 1)?;
        assert!(r.optimized_tokens <= 1);
        assert!(r.optimized.chars().count() <= 4);
        Ok(())
    }

    #[test]
    fn truncation_does_not_split_unicode_scalar() -> Result<(), OptimizerError> {
        // 8 multi-byte emoji (8 chars / 32 bytes) = 2 tokens. Target = 1
        // token forces truncation. The cut must land on a char boundary:
        // every resulting char is a whole rocket emoji. A mid-scalar cut
        // would panic inside truncate_to_tokens (byte-slice not on a char
        // boundary), so reaching this point proves boundary safety.
        let input = "🚀🚀🚀🚀🚀🚀🚀🚀";
        let r = optimize(input, 1)?;
        assert!(r.optimized_tokens <= 1);
        assert!(
            r.optimized_tokens < estimate_tokens(input),
            "truncation must reduce the token count"
        );
        assert!(!r.optimized.is_empty(), "must keep at least one emoji");
        for ch in r.optimized.chars() {
            assert_eq!(ch, '🚀', "every char must be a whole emoji");
        }
        Ok(())
    }

    // ---- integration: reduction_pct ----

    #[test]
    fn reduction_pct_is_correct() -> Result<(), OptimizerError> {
        let input = "a".repeat(40);
        let r = optimize(&input, 5)?;
        assert!(
            (r.reduction_pct - 50.0).abs() < 1.0,
            "expected ~50% reduction, got {}",
            r.reduction_pct
        );
        Ok(())
    }

    #[test]
    fn no_reduction_means_zero_percent() -> Result<(), OptimizerError> {
        let r = optimize("short", 100)?;
        assert_eq!(r.reduction_pct, 0.0);
        Ok(())
    }
}