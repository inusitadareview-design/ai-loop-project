//! prompt-builder — deterministic assembly of prompts from system
//! instructions, memory context and user message. Produces the exact
//! same output for the exact same inputs (no timestamps, no randomness,
//! no non-deterministic field ordering), so that identical prompts hit
//! the cache-engine and cost $0 on repeated runs.
//!
//! Guardrails enforced by this crate:
//! - I1   — Token Pre-Validation: the assembled prompt is measured with
//!          the same ~4 chars/token heuristic used by `token-budget` and
//!          `payload-optimizer`. If it does not fit the provided
//!          `token_limit`, the function returns `BuilderError::ExceedsBudget`
//!          instead of silently truncating — truncation is the caller's
//!          responsibility (typically `payload-optimizer` after this
//!          crate returns).
//! - G2.1 — No prompt leak: the builder returns a single opaque `String`
//!          and never writes to any log, file or stdout. Leaks at the
//!          transport layer are prevented by the observability tracer.
//! - G5.2 — No `unwrap()` / `expect()` in production code; every fallible
//!          public function returns `Result<T, E>`.
//!
//! Dependency-free (Rust standard library only) so it builds offline.

/// Errors from the builder. Empty inputs and budget overflow are errors;
/// the builder never silently truncates — that is `payload-optimizer`'s
/// job (separation of concerns, R1).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum BuilderError {
    /// `system` prompt is empty — a prompt without system instructions
    /// is not valid for this project's pipelines.
    EmptySystem,
    /// `user` message is empty — a prompt without a user message is not
    /// valid for this project's pipelines.
    EmptyUser,
    /// `token_limit` was zero, which is not a valid budget.
    ZeroTokenLimit,
    /// The assembled prompt would exceed the requested token limit.
    /// Carries the measured token count and the requested limit so the
    /// caller (typically the Go engine) can decide whether to invoke
    /// `memory-compressor` to shrink context and retry.
    ExceedsBudget {
        /// Estimated tokens of the assembled prompt.
        required: usize,
        /// Requested token limit that was exceeded.
        limit: usize,
    },
}

impl core::fmt::Display for BuilderError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            BuilderError::EmptySystem => write!(f, "system prompt must not be empty"),
            BuilderError::EmptyUser => write!(f, "user message must not be empty"),
            BuilderError::ZeroTokenLimit => write!(f, "token_limit must be > 0"),
            BuilderError::ExceedsBudget { required, limit } => {
                write!(
                    f,
                    "assembled prompt requires {} tokens but limit is {}",
                    required, limit
                )
            }
        }
    }
}

impl std::error::Error for BuilderError {}

/// The assembled prompt and its metadata. `full` is the string to be
/// sent to the provider; `tokens` is the estimated token count,
/// computed with the same heuristic used by `token-budget` and
/// `payload-optimizer` so the three crates agree across the FFI boundary.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BuiltPrompt {
    /// The assembled prompt, ready to be sent to the provider.
    pub full: String,
    /// Estimated token count of `full`, using the ~4 chars/token
    /// ceiling heuristic.
    pub tokens: usize,
}

/// Estimates the token count of `text` using the ~4 chars/token heuristic,
/// identical to `token-budget::estimate_tokens` and
/// `payload-optimizer::estimate_tokens` so all three crates agree across
/// the FFI boundary. Uses Unicode char count (not byte length). Empty
/// string yields 0. Result is rounded UP (ceiling). Cannot fail.
pub fn estimate_tokens(text: &str) -> usize {
    // Same formula as token-budget and payload-optimizer:
    // ceiling of chars/4.
    (text.chars().count() + 3) / 4
}

/// Assembles a deterministic prompt from the three input parts:
///
/// - `system`: mandatory system instructions for the model.
/// - `memory`: optional compressed context from `memory-compressor`.
///   If empty, the `[memory]` section is omitted entirely to save tokens.
/// - `user`: mandatory user message for this call.
///
/// The output format is fixed and deterministic:
///
/// ```text
/// [system]
/// <system>
///
/// [memory]
/// <memory>
///
/// [user]
/// <user>
/// ```
///
/// (the `[memory]` block is omitted when `memory` is empty).
///
/// Returns `Err` for:
/// - empty `system`  → `BuilderError::EmptySystem`
/// - empty `user`    → `BuilderError::EmptyUser`
/// - `token_limit == 0` → `BuilderError::ZeroTokenLimit`
/// - assembled prompt exceeds `token_limit` →
///   `BuilderError::ExceedsBudget { required, limit }`
///
/// The builder never truncates silently. If the caller needs a smaller
/// prompt, it must either pass a smaller `memory` (typically produced
/// by `memory-compressor`) or pass the result through
/// `payload-optimizer::optimize`.
pub fn build(
    system: &str,
    memory: &str,
    user: &str,
    token_limit: usize,
) -> Result<BuiltPrompt, BuilderError> {
    if system.trim().is_empty() {
        return Err(BuilderError::EmptySystem);
    }
    if user.trim().is_empty() {
        return Err(BuilderError::EmptyUser);
    }
    if token_limit == 0 {
        return Err(BuilderError::ZeroTokenLimit);
    }

    // Build the deterministic template. Using a `String` with
    // pre-allocated capacity avoids intermediate allocations and keeps
    // the output byte-for-byte identical across calls with the same
    // inputs (cache-friendliness for cache-engine).
    let has_memory = !memory.trim().is_empty();
    let estimated_cap = system.len() + memory.len() + user.len() + 64;
    let mut full = String::with_capacity(estimated_cap);

    full.push_str("[system]\n");
    full.push_str(system);
    full.push('\n');

    if has_memory {
        full.push_str("\n[memory]\n");
        full.push_str(memory);
        full.push('\n');
    }

    full.push_str("\n[user]\n");
    full.push_str(user);
    full.push('\n');

    let tokens = estimate_tokens(&full);
    if tokens > token_limit {
        return Err(BuilderError::ExceedsBudget {
            required: tokens,
            limit: token_limit,
        });
    }

    Ok(BuiltPrompt { full, tokens })
}

#[cfg(test)]
mod tests {
    use super::*;

    // ---- estimate_tokens ------------------------------------------------

    #[test]
    fn estimate_tokens_matches_sibling_crates() {
        // Same formula as token-budget and payload-optimizer.
        assert_eq!(estimate_tokens(""), 0);
        assert_eq!(estimate_tokens("a"), 1);
        assert_eq!(estimate_tokens("abcd"), 1);
        assert_eq!(estimate_tokens("abcde"), 2);
        let text = "a".repeat(40);
        assert_eq!(estimate_tokens(&text), 10);
        // Unicode: chars count, not bytes.
        assert_eq!(estimate_tokens("🚀🚀🚀🚀"), 1);
    }

    // ---- error cases ----------------------------------------------------

    #[test]
    fn empty_system_is_error() {
        let err = build("", "memory", "hello", 100);
        assert!(matches!(err, Err(BuilderError::EmptySystem)));
    }

    #[test]
    fn whitespace_only_system_is_error() {
        let err = build("   \n\t  ", "memory", "hello", 100);
        assert!(matches!(err, Err(BuilderError::EmptySystem)));
    }

    #[test]
    fn empty_user_is_error() {
        let err = build("system", "memory", "", 100);
        assert!(matches!(err, Err(BuilderError::EmptyUser)));
    }

    #[test]
    fn whitespace_only_user_is_error() {
        let err = build("system", "memory", "   \n", 100);
        assert!(matches!(err, Err(BuilderError::EmptyUser)));
    }

    #[test]
    fn zero_token_limit_is_error() {
        let err = build("system", "memory", "user", 0);
        assert!(matches!(err, Err(BuilderError::ZeroTokenLimit)));
    }

    #[test]
    fn exceeds_budget_returns_required_and_limit() {
        // 200 chars = 50 tokens. Limit = 10 → ExceedsBudget.
        let system = "a".repeat(200);
        let err = build(&system, "", "user", 10);
        match err {
            Err(BuilderError::ExceedsBudget { required, limit }) => {
                assert!(required > 10, "required should exceed limit, got {}", required);
                assert_eq!(limit, 10);
            }
            other => panic!("expected ExceedsBudget, got {:?}", other),
        }
    }

    #[test]
    fn error_display_is_human_readable() {
        let cases: Vec<(BuilderError, &str)> = vec![
            (BuilderError::EmptySystem, "system prompt must not be empty"),
            (BuilderError::EmptyUser, "user message must not be empty"),
            (BuilderError::ZeroTokenLimit, "token_limit must be > 0"),
            (
                BuilderError::ExceedsBudget { required: 100, limit: 50 },
                "assembled prompt requires 100 tokens but limit is 50",
            ),
        ];
        for (err, expected) in cases {
            assert_eq!(err.to_string(), expected);
            let dyn_ref: &dyn std::error::Error = &err;
            assert!(!dyn_ref.to_string().is_empty());
        }
    }

    // ---- successful builds: shape of output -----------------------------

    #[test]
    fn build_without_memory_omits_memory_section() -> Result<(), BuilderError> {
        let r = build("You are a helpful assistant.", "", "Hello!", 100)?;
        assert!(r.full.contains("[system]\nYou are a helpful assistant."));
        assert!(r.full.contains("[user]\nHello!"));
        // The [memory] section must be completely absent — not even an
        // empty block. This is what saves tokens.
        assert!(
            !r.full.contains("[memory]"),
            "empty memory should not produce a [memory] section, got: {:?}",
            r.full
        );
        assert!(r.tokens > 0);
        Ok(())
    }

    #[test]
    fn build_with_memory_includes_memory_section() -> Result<(), BuilderError> {
        let r = build(
            "You are a news editor.",
            "Recent topic: AI governance in Brazil.",
            "Write a summary.",
            100,
        )?;
        assert!(r.full.contains("[system]\nYou are a news editor."));
        assert!(r.full.contains("[memory]\nRecent topic: AI governance in Brazil."));
        assert!(r.full.contains("[user]\nWrite a summary."));
        // Order is deterministic: system → memory → user.
        let sys_pos = r.full.find("[system]").unwrap();
        let mem_pos = r.full.find("[memory]").unwrap();
        let usr_pos = r.full.find("[user]").unwrap();
        assert!(sys_pos < mem_pos, "system must come before memory");
        assert!(mem_pos < usr_pos, "memory must come before user");
        Ok(())
    }

    #[test]
    fn whitespace_only_memory_is_treated_as_empty() -> Result<(), BuilderError> {
        let r = build("sys", "   \n\n   ", "user", 100)?;
        assert!(
            !r.full.contains("[memory]"),
            "whitespace-only memory should not produce a [memory] section"
        );
        Ok(())
    }

    // ---- determinism ----------------------------------------------------

    #[test]
    fn build_is_deterministic_across_calls() -> Result<(), BuilderError> {
        let sys = "system instructions with unicode: café 🎯";
        let mem = "memory context with emoji: 你好";
        let usr = "user message with mixed: αβγ 123";
        let r1 = build(sys, mem, usr, 1000)?;
        let r2 = build(sys, mem, usr, 1000)?;
        // Identical inputs must produce byte-for-byte identical outputs.
        assert_eq!(r1.full, r2.full, "build must be deterministic");
        assert_eq!(r1.tokens, r2.tokens, "token count must be deterministic");
        Ok(())
    }

    #[test]
    fn token_count_matches_measured_estimate() -> Result<(), BuilderError> {
        let r = build(
            "You are a helpful assistant.",
            "Some context about the user.",
            "Please help me write an email.",
            1000,
        )?;
        assert_eq!(r.tokens, estimate_tokens(&r.full));
        Ok(())
    }

    // ---- boundary: exact fit ------------------------------------------

    #[test]
    fn build_succeeds_when_tokens_equal_limit() -> Result<(), BuilderError> {
        // Build with a generous limit, then re-call with the exact measured
        // token count as the new limit — must still succeed (not "exceeds").
        let r1 = build("sys", "mem", "user message", 1000)?;
        let exact_limit = r1.tokens;
        let r2 = build("sys", "mem", "user message", exact_limit)?;
        assert_eq!(r1.full, r2.full);
        assert_eq!(r2.tokens, exact_limit);
        Ok(())
    }

    #[test]
    fn build_fails_when_limit_is_one_below_required() {
        let r1 = build("sys", "mem", "user message", 1000).unwrap();
        let too_tight = r1.tokens.saturating_sub(1).max(1);
        let err = build("sys", "mem", "user message", too_tight);
        assert!(
            matches!(err, Err(BuilderError::ExceedsBudget { .. })),
            "expected ExceedsBudget when limit is one below required, got {:?}",
            err
        );
    }

    // ---- unicode --------------------------------------------------------

    #[test]
    fn build_handles_unicode_correctly() -> Result<(), BuilderError> {
        let r = build(
            "You are a translator.",
            "Contexto em português com acentos: saúde, coração.",
            "Translate '你好世界' to English.",
            1000,
        )?;
        // Every input substring must be preserved verbatim.
        assert!(r.full.contains("saúde"));
        assert!(r.full.contains("coração"));
        assert!(r.full.contains("你好世界"));
        assert!(r.full.contains("Translate"));
        // Token count must use char count, not byte length.
        assert_eq!(r.tokens, estimate_tokens(&r.full));
        Ok(())
    }

    #[test]
    fn build_handles_emoji_in_all_sections() -> Result<(), BuilderError> {
        let r = build("🚀 system", "📝 memory", "👤 user", 100)?;
        assert!(r.full.contains("🚀 system"));
        assert!(r.full.contains("📝 memory"));
        assert!(r.full.contains("👤 user"));
        Ok(())
    }

    // ---- integration: separation of concerns ----------------------------

    #[test]
    fn build_does_not_truncate_on_overflow() {
        // The builder must NEVER silently truncate. If the prompt does not
        // fit, it must return an error so the engine can invoke
        // memory-compressor or payload-optimizer and retry.
        let huge_system = "x".repeat(10_000); // ~2500 tokens
        let err = build(&huge_system, "", "user", 10);
        assert!(
            matches!(err, Err(BuilderError::ExceedsBudget { .. })),
            "builder must refuse, never truncate"
        );
    }
}