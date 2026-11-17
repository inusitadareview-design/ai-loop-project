//! token-budget — pre-validates token counts and projected cost for a
//! single AI call BEFORE any HTTP request is allowed to hit the network.
//!
//! Guardrails enforced by this crate:
//! - I1   — Token Pre-Validation: no call proceeds unless it fits the
//!          remaining budget AND the per-request token limit (`fits`).
//! - G1.1 — Never exceed `budget.daily_limit_usd`: the projected cost of
//!          a call is compared against the remaining daily budget before
//!          the call is approved.
//! - G5.2 — No `unwrap()` / `expect()` in production code: every fallible
//!          public function returns `Result<T, E>`; invalid inputs surface
//!          as `BudgetError` variants instead of panics.
//!
//! This crate is intentionally dependency-free (Rust standard library
//! only) so that it builds offline.

/// Estimated token usage and budget fitness for a single AI call.
#[derive(Debug, Clone, PartialEq)]
pub struct BudgetCheck {
    /// Estimated token count of the prompt (~4 chars/token heuristic).
    pub estimated_tokens: usize,
    /// Projected cost of the call, in US dollars.
    pub projected_cost_usd: f64,
    /// Remaining daily budget after subtracting what was already spent,
    /// clamped at 0.0.
    pub remaining_budget_usd: f64,
    /// True when `projected_cost_usd <= remaining_budget_usd`.
    pub fits_budget: bool,
    /// True when `estimated_tokens <= max_tokens_per_request`, or when
    /// `max_tokens_per_request == 0` (which means "unlimited").
    pub fits_token_limit: bool,
}

impl BudgetCheck {
    /// True only if BOTH the cost budget and the per-request token limit
    /// are satisfied (guardrail I1).
    pub fn fits(&self) -> bool {
        self.fits_budget && self.fits_token_limit
    }
}

/// Errors from budget validation. Invalid INPUTS are errors; a call that
/// simply exceeds the remaining budget is NOT an error (`fits()` will
/// return `false` instead).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BudgetError {
    /// `daily_limit_usd` was negative.
    NegativeDailyLimit,
    /// `spent_usd` was negative.
    NegativeSpent,
    /// `cost_per_1k_input_tokens` was negative.
    NegativeCostRate,
}

impl core::fmt::Display for BudgetError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            BudgetError::NegativeDailyLimit => {
                write!(f, "daily_limit_usd must be >= 0.0")
            }
            BudgetError::NegativeSpent => {
                write!(f, "spent_usd must be >= 0.0")
            }
            BudgetError::NegativeCostRate => {
                write!(f, "cost_per_1k_input_tokens must be >= 0.0")
            }
        }
    }
}

impl std::error::Error for BudgetError {}

/// Estimates the token count of `text` using the ~4 chars/token
/// heuristic. Uses the Unicode char count (not the byte length). An
/// empty string yields 0. The result is rounded UP (ceiling). This
/// function cannot fail.
pub fn estimate_tokens(text: &str) -> usize {
    // Ceiling division by 4 over the scalar count: (n + 3) / 4.
    // Empty input gives 0 because 3 / 4 == 0 in integer division.
    (text.chars().count() + 3) / 4
}

/// Validates whether a single call described by `prompt` is allowed.
///
/// Semantics:
/// - `remaining_budget_usd` = max(0.0, daily_limit_usd - spent_usd)
/// - `projected_cost_usd`   = (estimated_tokens / 1000.0)
///                            * cost_per_1k_input_tokens
/// - `fits_budget`          = projected_cost_usd <= remaining_budget_usd
/// - `fits_token_limit`     = max_tokens_per_request == 0
///                            || estimated_tokens <= max_tokens_per_request
///
/// Returns `Err` only for invalid inputs:
/// - `daily_limit_usd < 0`               => `BudgetError::NegativeDailyLimit`
/// - `spent_usd < 0`                     => `BudgetError::NegativeSpent`
/// - `cost_per_1k_input_tokens < 0`      => `BudgetError::NegativeCostRate`
pub fn validate_call(
    prompt: &str,
    daily_limit_usd: f64,
    spent_usd: f64,
    cost_per_1k_input_tokens: f64,
    max_tokens_per_request: usize,
) -> Result<BudgetCheck, BudgetError> {
    // Guardrail G5.2: invalid inputs are reported as errors, never panic.
    if daily_limit_usd < 0.0 {
        return Err(BudgetError::NegativeDailyLimit);
    }
    if spent_usd < 0.0 {
        return Err(BudgetError::NegativeSpent);
    }
    if cost_per_1k_input_tokens < 0.0 {
        return Err(BudgetError::NegativeCostRate);
    }

    let estimated_tokens = estimate_tokens(prompt);
    let remaining_budget_usd = (daily_limit_usd - spent_usd).max(0.0);
    let projected_cost_usd =
        (estimated_tokens as f64 / 1000.0) * cost_per_1k_input_tokens;
    let fits_budget = projected_cost_usd <= remaining_budget_usd;
    let fits_token_limit = max_tokens_per_request == 0
        || estimated_tokens <= max_tokens_per_request;

    Ok(BudgetCheck {
        estimated_tokens,
        projected_cost_usd,
        remaining_budget_usd,
        fits_budget,
        fits_token_limit,
    })
}

/// Supports decision tree D2 ("reduce max_tokens, revalidate"): returns
/// the maximum number of whole tokens affordable with `remaining_usd` at
/// the given cost rate (floored). If `cost_per_1k_input_tokens <= 0.0`,
/// tokens are effectively free and `usize::MAX` is returned. Never
/// panics (no division by zero).
pub fn max_affordable_tokens(
    remaining_usd: f64,
    cost_per_1k_input_tokens: f64,
) -> usize {
    // Free (or degenerate) pricing: no upper bound on token count.
    if cost_per_1k_input_tokens <= 0.0 || !cost_per_1k_input_tokens.is_finite()
    {
        return usize::MAX;
    }
    // A non-positive or NaN remaining budget affords nothing.
    if !(remaining_usd > 0.0) {
        return 0;
    }
    let tokens = remaining_usd * 1000.0 / cost_per_1k_input_tokens;
    if !tokens.is_finite() || tokens >= usize::MAX as f64 {
        return usize::MAX;
    }
    // `as` truncation toward zero == floor for non-negative values.
    tokens as usize
}

#[cfg(test)]
mod tests {
    use super::*;

    // ---- estimate_tokens ------------------------------------------------

    #[test]
    fn estimate_tokens_empty_is_zero() {
        assert_eq!(estimate_tokens(""), 0);
    }

    #[test]
    fn estimate_tokens_one_char_is_one() {
        assert_eq!(estimate_tokens("a"), 1);
    }

    #[test]
    fn estimate_tokens_four_chars_is_one() {
        assert_eq!(estimate_tokens("abcd"), 1);
    }

    #[test]
    fn estimate_tokens_five_chars_rounds_up_to_two() {
        assert_eq!(estimate_tokens("abcde"), 2);
    }

    #[test]
    fn estimate_tokens_known_longer_string() {
        // 40 ASCII chars => exactly 10 tokens (ceiling of 40 / 4).
        let text = "a".repeat(40);
        assert_eq!(estimate_tokens(&text), 10);
        // 41 chars => ceiling rounds up to 11.
        let text = "a".repeat(41);
        assert_eq!(estimate_tokens(&text), 11);
    }

    #[test]
    fn estimate_tokens_counts_unicode_chars_not_bytes() {
        // "héllo→" is 6 Unicode scalars (8 bytes in UTF-8) => ceil(6/4) = 2.
        assert_eq!(estimate_tokens("héllo→"), 2);
        // 4 multi-byte emoji => 1 token even though each is 4 bytes.
        assert_eq!(estimate_tokens("🚀🚀🚀🚀"), 1);
    }

    // ---- validate_call ---------------------------------------------------

    #[test]
    fn validate_call_fits_when_budget_ample() -> Result<(), BudgetError> {
        let check = validate_call("hello world prompt text", 10.0, 1.0, 0.5, 0)?;
        assert!(check.fits_budget);
        assert!(check.fits_token_limit);
        assert!(check.fits());
        assert_eq!(check.remaining_budget_usd, 9.0);
        assert!(check.projected_cost_usd < check.remaining_budget_usd);
        Ok(())
    }

    #[test]
    fn validate_call_does_not_fit_when_cost_exceeds_remaining()
    -> Result<(), BudgetError> {
        // 100 tokens => 0.1 USD projected vs 0.001 USD remaining.
        let prompt = "x".repeat(400);
        let check = validate_call(&prompt, 1.0, 0.999, 1.0, 0)?;
        assert_eq!(check.estimated_tokens, 100);
        assert!(!check.fits_budget);
        assert!(!check.fits());
        Ok(())
    }

    #[test]
    fn validate_call_zero_cost_rate_always_fits_budget()
    -> Result<(), BudgetError> {
        let prompt = "y".repeat(4000);
        let check = validate_call(&prompt, 0.0, 0.0, 0.0, 0)?;
        assert!(check.projected_cost_usd.abs() < f64::EPSILON);
        assert!(check.fits_budget);
        assert!(check.fits());
        Ok(())
    }

    #[test]
    fn validate_call_enforces_per_request_token_limit()
    -> Result<(), BudgetError> {
        let prompt = "z".repeat(41); // 11 tokens
        // Limit of 10 tokens: budget ample, but token limit violated.
        let tight = validate_call(&prompt, 100.0, 0.0, 0.01, 10)?;
        assert!(tight.fits_budget);
        assert!(!tight.fits_token_limit);
        assert!(!tight.fits());
        // Limit of 11 tokens: exactly at the cap, allowed.
        let exact = validate_call(&prompt, 100.0, 0.0, 0.01, 11)?;
        assert!(exact.fits_token_limit);
        assert!(exact.fits());
        Ok(())
    }

    #[test]
    fn validate_call_zero_max_tokens_means_unlimited()
    -> Result<(), BudgetError> {
        let prompt = "w".repeat(4000); // 1000 tokens
        let check = validate_call(&prompt, 100.0, 0.0, 0.01, 0)?;
        assert!(check.fits_token_limit);
        assert!(check.fits());
        Ok(())
    }

    #[test]
    fn validate_call_spent_over_limit_clamps_remaining_to_zero()
    -> Result<(), BudgetError> {
        let check = validate_call("some prompt", 5.0, 7.5, 0.5, 0)?;
        assert_eq!(check.remaining_budget_usd, 0.0);
        assert!(!check.fits_budget);
        assert!(!check.fits());
        Ok(())
    }

    #[test]
    fn validate_call_negative_daily_limit_is_error() {
        let err = validate_call("p", -1.0, 0.0, 0.5, 0);
        assert!(matches!(err, Err(BudgetError::NegativeDailyLimit)));
    }

    #[test]
    fn validate_call_negative_spent_is_error() {
        let err = validate_call("p", 1.0, -0.5, 0.5, 0);
        assert!(matches!(err, Err(BudgetError::NegativeSpent)));
    }

    #[test]
    fn validate_call_negative_cost_rate_is_error() {
        let err = validate_call("p", 1.0, 0.0, -0.001, 0);
        assert!(matches!(err, Err(BudgetError::NegativeCostRate)));
    }

    // ---- max_affordable_tokens -------------------------------------------

    #[test]
    fn max_affordable_tokens_floors_correctly() {
        // 1.0 USD at 3.0 USD/1k => 333.33... tokens => floor => 333.
        assert_eq!(max_affordable_tokens(1.0, 3.0), 333);
        // Exact division: 2.0 USD at 4.0 USD/1k => 500 tokens.
        assert_eq!(max_affordable_tokens(2.0, 4.0), 500);
    }

    #[test]
    fn max_affordable_tokens_non_positive_rate_is_unlimited() {
        assert_eq!(max_affordable_tokens(5.0, 0.0), usize::MAX);
        assert_eq!(max_affordable_tokens(5.0, -1.0), usize::MAX);
    }

    #[test]
    fn max_affordable_tokens_zero_remaining_is_zero() {
        assert_eq!(max_affordable_tokens(0.0, 1.0), 0);
        // Negative remaining budgets also afford nothing (never panics).
        assert_eq!(max_affordable_tokens(-3.0, 1.0), 0);
    }

    // ---- misc ------------------------------------------------------------

    #[test]
    fn budget_error_display_and_error_trait() {
        let e = BudgetError::NegativeSpent;
        assert_eq!(e.to_string(), "spent_usd must be >= 0.0");
        // Usable as a std::error::Error reference.
        let dyn_ref: &dyn std::error::Error = &e;
        assert!(!dyn_ref.to_string().is_empty());
    }
}
