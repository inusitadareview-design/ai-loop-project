//! cache-engine — response cache with TTL for `data/cache`. Uses FNV-1a
//! 64-bit hashing for deterministic cache keys so identical requests hit
//! the cache and cost $0 (decision tree D3: "valid hit (TTL ok): serve
//! from cache, cost = 0").
//!
//! Guardrails enforced by this crate:
//! - I1   — Token Pre-Validation: a cache hit means no HTTP call, no
//!          token spend. The Go engine checks the cache BEFORE calling
//!          token-budget, so cached responses bypass budget validation
//!          entirely (they cost nothing).
//! - D3   — Cache decision tree: this crate provides the primitives
//!          (`cache_key`, `is_expired`, `should_serve`) that the Go
//!          engine uses to implement "hit → serve", "expired → revalidate",
//!          "miss → call provider".
//! - G5.2 — No `unwrap()` / `expect()` in production code; every
//!          fallible public function returns `Result<T, E>`.
//!
//! Dependency-free (Rust standard library only) so it builds offline.

/// Errors from the cache engine. Invalid INPUTS are errors; a cache miss
/// or expired entry is NOT an error (the caller decides what to do).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CacheError {
    /// Input string was empty — an empty key is not valid for caching.
    EmptyKey,
}

impl core::fmt::Display for CacheError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            CacheError::EmptyKey => write!(f, "cache key input must not be empty"),
        }
    }
}

impl std::error::Error for CacheError {}

/// A cached response with metadata for TTL validation. `key` is the
/// FNV-1a hash of the original request; `value` is the cached response
/// body; `created_at_secs` is the Unix timestamp when the entry was
/// created; `ttl_secs` is the time-to-live in seconds.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CacheEntry {
    /// FNV-1a hash of the original request that produced this response.
    pub key: u64,
    /// The cached response body (typically JSON from the provider).
    pub value: String,
    /// Unix timestamp in seconds when this entry was created.
    pub created_at_secs: i64,
    /// Time-to-live in seconds. After `created_at_secs + ttl_secs`, the
    /// entry is considered expired and should be revalidated.
    pub ttl_secs: i64,
}

/// Computes the FNV-1a 64-bit hash of `input`. FNV-1a is a non-cryptographic
/// hash function chosen for its speed, simplicity, and good distribution
/// properties for cache keys. The algorithm:
///
/// 1. Start with offset basis `0xcbf29ce484222325`.
/// 2. For each byte `b` in the input:
///    - XOR the hash with `b`.
///    - Multiply the hash by the FNV prime `0x00000100000001b3`.
/// 3. Return the final hash.
///
/// Returns `Err(CacheError::EmptyKey)` if `input` is empty. Cannot fail
/// otherwise.
pub fn cache_key(input: &str) -> Result<u64, CacheError> {
    if input.is_empty() {
        return Err(CacheError::EmptyKey);
    }

    // FNV-1a 64-bit: offset basis and prime from the FNV specification.
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    let prime: u64 = 0x0000_0100_0000_01b3;

    for byte in input.as_bytes() {
        hash ^= *byte as u64;
        hash = hash.wrapping_mul(prime);
    }

    Ok(hash)
}

/// Returns `true` if the cache entry has expired relative to `now_secs`.
/// An entry is expired when `now_secs - created_at_secs > ttl_secs`.
/// This function cannot fail.
pub fn is_expired(entry: &CacheEntry, now_secs: i64) -> bool {
    let age = now_secs.saturating_sub(entry.created_at_secs);
    age > entry.ttl_secs
}

/// Returns `true` if the cache entry should be served (i.e., it is not
/// expired). This is the inverse of `is_expired` and is provided for
/// readability in the Go engine's decision tree (D3: "valid hit (TTL ok):
/// serve from cache"). This function cannot fail.
pub fn should_serve(entry: &CacheEntry, now_secs: i64) -> bool {
    !is_expired(entry, now_secs)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(key: u64, created: i64, ttl: i64) -> CacheEntry {
        CacheEntry {
            key,
            value: "cached_response".to_string(),
            created_at_secs: created,
            ttl_secs: ttl,
        }
    }

    // ---- cache_key: FNV-1a correctness ----------------------------------

    #[test]
    fn cache_key_empty_input_is_error() {
        let err = cache_key("");
        assert!(matches!(err, Err(CacheError::EmptyKey)));
    }

    #[test]
    fn cache_key_is_deterministic() -> Result<(), CacheError> {
        let k1 = cache_key("hello world")?;
        let k2 = cache_key("hello world")?;
        assert_eq!(k1, k2, "same input must produce same hash");
        Ok(())
    }

    #[test]
    fn cache_key_different_inputs_produce_different_hashes() -> Result<(), CacheError> {
        let k1 = cache_key("abc")?;
        let k2 = cache_key("abd")?;
        assert_ne!(k1, k2, "different inputs should produce different hashes");
        Ok(())
    }

    #[test]
    fn cache_key_known_vector_empty_string_equivalent() -> Result<(), CacheError> {
        // FNV-1a 64-bit of "a" is a known test vector.
        // Source: http://www.isthe.com/chongo/tech/comp/fnv/
        let k = cache_key("a")?;
        // Expected: 0xaf63dc4c8601ec8c
        assert_eq!(k, 0xaf63_dc4c_8601_ec8c);
        Ok(())
    }

    #[test]
    fn cache_key_known_vector_foobar() -> Result<(), CacheError> {
        // FNV-1a 64-bit of "foobar" is a known test vector.
        // Source: http://www.isthe.com/chongo/tech/comp/fnv/
        let k = cache_key("foobar")?;
        // Expected: 0x85944171f73967e8
        assert_eq!(k, 0x8594_4171_f739_67e8);
        Ok(())
    }

    #[test]
    fn cache_key_handles_unicode() -> Result<(), CacheError> {
        // Unicode input must not panic and must produce a valid hash.
        let k1 = cache_key("hello 世界")?;
        let k2 = cache_key("hello 世界")?;
        assert_eq!(k1, k2, "unicode input must be deterministic");
        // Different unicode strings should (almost certainly) differ.
        let k3 = cache_key("hello 世界!")?;
        assert_ne!(k1, k3);
        Ok(())
    }

    #[test]
    fn cache_key_handles_emoji() -> Result<(), CacheError> {
        let k1 = cache_key("🚀🎯✨")?;
        let k2 = cache_key("🚀🎯✨")?;
        assert_eq!(k1, k2);
        let k3 = cache_key("🚀🎯")?;
        assert_ne!(k1, k3);
        Ok(())
    }

    #[test]
    fn error_display_is_human_readable() {
        let e = CacheError::EmptyKey;
        assert_eq!(e.to_string(), "cache key input must not be empty");
        let dyn_ref: &dyn std::error::Error = &e;
        assert!(!dyn_ref.to_string().is_empty());
    }

    // ---- is_expired / should_serve: TTL logic ---------------------------

    #[test]
    fn entry_not_expired_when_age_less_than_ttl() {
        let e = entry(123, 1000, 60); // created at 1000, TTL 60s
        assert!(!is_expired(&e, 1050)); // age = 50s < 60s
        assert!(should_serve(&e, 1050));
    }

    #[test]
    fn entry_expired_when_age_exceeds_ttl() {
        let e = entry(123, 1000, 60);
        assert!(is_expired(&e, 1070)); // age = 70s > 60s
        assert!(!should_serve(&e, 1070));
    }

    #[test]
    fn entry_expired_at_exact_ttl_boundary() {
        let e = entry(123, 1000, 60);
        // At exactly TTL (age == ttl), the entry is NOT expired yet.
        // Expired means age > ttl, not age >= ttl.
        assert!(!is_expired(&e, 1060)); // age = 60s == 60s
        assert!(should_serve(&e, 1060));
    }

    #[test]
    fn entry_expired_one_second_after_ttl() {
        let e = entry(123, 1000, 60);
        assert!(is_expired(&e, 1061)); // age = 61s > 60s
        assert!(!should_serve(&e, 1061));
    }

    #[test]
    fn entry_with_zero_ttl_expires_immediately() {
        let e = entry(123, 1000, 0);
        // TTL = 0 means the entry is valid only at the exact moment of
        // creation. One second later, it is expired.
        assert!(!is_expired(&e, 1000)); // age = 0 == 0
        assert!(is_expired(&e, 1001)); // age = 1 > 0
    }

    #[test]
    fn entry_created_in_future_is_not_expired() {
        // Edge case: clock skew or entry created with a future timestamp.
        // saturating_sub prevents underflow, so age = 0, which is <= ttl.
        let e = entry(123, 2000, 60);
        assert!(!is_expired(&e, 1000)); // now < created_at
        assert!(should_serve(&e, 1000));
    }

    #[test]
    fn entry_with_negative_ttl_is_always_expired() {
        // Negative TTL is nonsensical but must not panic. Any age > negative
        // is true, so the entry is always expired.
        let e = entry(123, 1000, -10);
        assert!(is_expired(&e, 1000)); // age = 0 > -10
        assert!(!should_serve(&e, 1000));
    }

    #[test]
    fn entry_with_large_ttl_does_not_overflow() {
        // Large TTL values must not cause integer overflow. saturating_sub
        // and normal comparison handle this safely.
        let e = entry(123, 0, i64::MAX);
        assert!(!is_expired(&e, 1_000_000_000)); // age = 1B < i64::MAX
        assert!(should_serve(&e, 1_000_000_000));
    }

    // ---- integration: cache_key + TTL -----------------------------------

    #[test]
    fn cache_key_and_ttl_work_together() -> Result<(), CacheError> {
        let request = "GET /api/v1/models";
        let key = cache_key(request)?;
        let e = CacheEntry {
            key,
            value: r#"{"models": ["gpt-4", "claude-3"]}"#.to_string(),
            created_at_secs: 1700000000,
            ttl_secs: 3600, // 1 hour
        };
        // Within TTL: serve from cache.
        assert!(should_serve(&e, 1700001000)); // 1000s later
        // After TTL: revalidate.
        assert!(!should_serve(&e, 1700004000)); // 4000s later
        Ok(())
    }

    #[test]
    fn identical_requests_produce_identical_cache_keys() -> Result<(), CacheError> {
        // Two identical requests must hash to the same key so they hit the
        // same cache entry (decision tree D3: "valid hit → serve").
        let req1 = "POST /api/v1/generate {\"prompt\": \"hello\"}";
        let req2 = "POST /api/v1/generate {\"prompt\": \"hello\"}";
        assert_eq!(cache_key(req1)?, cache_key(req2)?);
        Ok(())
    }

    #[test]
    fn different_requests_produce_different_cache_keys() -> Result<(), CacheError> {
        // Two different requests must (almost certainly) hash to different
        // keys so they do not collide in the cache.
        let req1 = "POST /api/v1/generate {\"prompt\": \"hello\"}";
        let req2 = "POST /api/v1/generate {\"prompt\": \"world\"}";
        assert_ne!(cache_key(req1)?, cache_key(req2)?);
        Ok(())
    }
}