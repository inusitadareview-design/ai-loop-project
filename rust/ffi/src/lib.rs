//! ffi — camada C-ABI consumida pelo Go via CGO (GUARDRAILS G5.3).
//!
//! Sprint 2: expõe funções dos crates Rust para Go:
//! - `ffi_hello` — handshake validation (Sprint 1)
//! - `ffi_memory_compress` — semantic memory compression (Sprint 5)
//!
//! Regras:
//! - Funções `extern "C"` usam apenas tipos C-ABI simples
//! - Nunca panic através da fronteira (G5.2: Result interno, retorno defensivo no FFI)
//! - Go caller copia/libera memória imediatamente após chamada (G5.3)

use std::os::raw::c_char;
use std::ffi::{CStr, CString};

// ============================================================================
// Sprint 1: Hello handshake
// ============================================================================

/// Mensagem canônica de handshake do bridge FFI.
static HELLO: &[u8] = b"Hello from Rust FFI\0";

/// Retorna um ponteiro para string C "Hello from Rust FFI".
///
/// # Safety
/// O chamador deve tratar o retorno como `const char*` NUL-terminated e
/// copiá-lo imediatamente; a memória pertence à estática do processo Rust.
#[no_mangle]
pub extern "C" fn ffi_hello() -> *const c_char {
    HELLO.as_ptr() as *const c_char
}

/// Versão interna com Result<T, E> (padrão do workspace — G5.2).
pub fn hello_message() -> Result<&'static str, std::str::Utf8Error> {
    std::str::from_utf8(&HELLO[..HELLO.len() - 1])
}

// ============================================================================
// Sprint 5: Memory compression
// ============================================================================

/// Comprime eventos de memória em um snapshot determinístico.
///
/// # Input
/// `json_events` é uma string JSON no formato:
/// ```json
/// [
///   {"id": "e1", "content": "hello", "timestamp_secs": 1000},
///   {"id": "e2", "content": "world", "timestamp_secs": 2000}
/// ]
/// ```
///
/// # Output
/// Retorna uma string JSON no formato:
/// ```json
/// {
///   "snapshot": "[memory_snapshot v1]\n...",
///   "event_count": 2,
///   "original_size": 100,
///   "compressed_size": 80,
///   "reduction_pct": 20.0
/// }
/// ```
///
/// # Safety
/// - Retorna NULL se parsing falhar ou compress retornar erro
/// - Go caller DEVE liberar o ponteiro retornado via `ffi_free_string()`
/// - Go caller DEVE copiar o conteúdo imediatamente (não armazenar o ponteiro)
#[no_mangle]
pub extern "C" fn ffi_memory_compress(json_events: *const c_char) -> *mut c_char {
    // Defensive: check for NULL input
    if json_events.is_null() {
        return std::ptr::null_mut();
    }

    // Convert C string to Rust &str
    let c_str = unsafe { CStr::from_ptr(json_events) };
    let json_str = match c_str.to_str() {
        Ok(s) => s,
        Err(_) => return std::ptr::null_mut(), // invalid UTF-8
    };

    // Parse JSON array into MemoryEvent vec
    // Expected format: [{"id": "...", "content": "...", "timestamp_secs": ...}]
    let events: Vec<memory_compressor::MemoryEvent> = match serde_json::from_str(json_str) {
        Ok(evts) => evts,
        Err(_) => return std::ptr::null_mut(), // parse error
    };

    // Call the compressor
    let result = match memory_compressor::compress(&events) {
        Ok(res) => res,
        Err(_) => return std::ptr::null_mut(), // compression error (empty/duplicate)
    };

    // Build response JSON
    let response = match serde_json::to_string(&CompressionResponse {
        snapshot: result.snapshot,
        event_count: result.event_count,
        original_size: result.original_size,
        compressed_size: result.compressed_size,
        reduction_pct: result.reduction_pct,
    }) {
        Ok(json) => json,
        Err(_) => return std::ptr::null_mut(), // serialization error
    };

    // Convert to CString and return raw pointer
    // IMPORTANT: caller must free this with ffi_free_string()
    match CString::new(response) {
        Ok(c_string) => c_string.into_raw(),
        Err(_) => std::ptr::null_mut(), // NUL byte in response (should never happen)
    }
}

/// Libera memória alocada por `ffi_memory_compress()`.
///
/// # Safety
/// - `ptr` deve ser um ponteiro retornado por `ffi_memory_compress()`
/// - Chame esta função EXATAMENTE UMA VEZ para cada ponteiro
/// - NÃO chame esta função em ponteiros de `ffi_hello()` (memória estática)
#[no_mangle]
pub extern "C" fn ffi_free_string(ptr: *mut c_char) {
    if !ptr.is_null() {
        unsafe {
            // Reconstruct the CString so it can be dropped
            let _ = CString::from_raw(ptr);
        }
    }
}

/// Response struct for JSON serialization.
#[derive(serde::Serialize)]
struct CompressionResponse {
    snapshot: String,
    event_count: usize,
    original_size: usize,
    compressed_size: usize,
    reduction_pct: f64,
}

// ============================================================================
// Tests
// ============================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use std::ffi::CStr;

    #[test]
    fn ffi_hello_returns_expected_cstring() {
        let ptr = ffi_hello();
        assert!(!ptr.is_null());
        let cstr = unsafe { CStr::from_ptr(ptr) };
        assert_eq!(cstr.to_str().unwrap(), "Hello from Rust FFI");
    }

    #[test]
    fn hello_message_matches_ffi_hello() -> Result<(), std::str::Utf8Error> {
        assert_eq!(hello_message()?, "Hello from Rust FFI");
        Ok(())
    }

    #[test]
    fn ffi_memory_compress_valid_input() {
        let json = r#"[{"id":"e1","content":"hello","timestamp_secs":1000}]"#;
        let c_json = CString::new(json).unwrap();
        let result_ptr = ffi_memory_compress(c_json.as_ptr());
        
        assert!(!result_ptr.is_null());
        
        let c_result = unsafe { CStr::from_ptr(result_ptr) };
        let result_str = c_result.to_str().unwrap();
        
        // Verify response contains expected fields
        assert!(result_str.contains("snapshot"));
        assert!(result_str.contains("event_count"));
        assert!(result_str.contains("original_size"));
        
        // Free the allocated memory
        ffi_free_string(result_ptr);
    }

    #[test]
    fn ffi_memory_compress_null_input() {
        let result_ptr = ffi_memory_compress(std::ptr::null());
        assert!(result_ptr.is_null());
    }

    #[test]
    fn ffi_memory_compress_invalid_json() {
        let json = "not valid json";
        let c_json = CString::new(json).unwrap();
        let result_ptr = ffi_memory_compress(c_json.as_ptr());
        assert!(result_ptr.is_null());
    }

    #[test]
    fn ffi_memory_compress_empty_array() {
        let json = "[]";
        let c_json = CString::new(json).unwrap();
        let result_ptr = ffi_memory_compress(c_json.as_ptr());
        assert!(result_ptr.is_null()); // empty batch is an error
    }

    #[test]
    fn ffi_free_string_null_is_safe() {
        ffi_free_string(std::ptr::null_mut()); // should not panic
    }

    #[test]
    fn ffi_cache_hash_deterministic() {
        let input = CString::new("POST /api/v1/generate {\"prompt\":\"hello\"}").unwrap();
        let h1 = ffi_cache_hash(input.as_ptr());
        let h2 = ffi_cache_hash(input.as_ptr());
        assert_ne!(h1, 0, "non-empty input must produce non-zero hash");
        assert_eq!(h1, h2, "same input must produce same hash");
    }

    #[test]
    fn ffi_cache_hash_different_inputs_differ() {
        let i1 = CString::new("request A").unwrap();
        let i2 = CString::new("request B").unwrap();
        assert_ne!(ffi_cache_hash(i1.as_ptr()), ffi_cache_hash(i2.as_ptr()));
    }

    #[test]
    fn ffi_cache_hash_null_returns_zero() {
        assert_eq!(ffi_cache_hash(std::ptr::null()), 0);
    }

    #[test]
    fn ffi_cache_hash_empty_returns_zero() {
        let empty = CString::new("").unwrap();
        assert_eq!(ffi_cache_hash(empty.as_ptr()), 0);
    }

    #[test]
    fn ffi_cache_check_ttl_valid() {
        assert!(ffi_cache_check_ttl(1000, 60, 1050));
        assert!(ffi_cache_check_ttl(1000, 60, 1060)); // boundary
    }

    #[test]
    fn ffi_cache_check_ttl_expired() {
        assert!(!ffi_cache_check_ttl(1000, 60, 1061));
    }

    #[test]
    fn ffi_cache_check_ttl_clock_skew() {
        assert!(ffi_cache_check_ttl(2000, 60, 1000)); // future creation
    }
}

// ============================================================================
// Sprint 9: Token budget validation (I1 — Token Pre-Validation)
// ============================================================================

/// Validates whether an AI call fits within budget constraints.
///
/// # Input
/// `json_input` is a JSON string in the format:
/// ```json
/// {
///   "prompt": "your prompt text",
///   "daily_limit_usd": 10.0,
///   "spent_usd": 1.5,
///   "cost_per_1k_input_tokens": 0.002,
///   "max_tokens_per_request": 4000
/// }
/// ```
///
/// # Output
/// Returns JSON in the format:
/// ```json
/// {
///   "fits": true,
///   "fits_budget": true,
///   "fits_token_limit": true,
///   "estimated_tokens": 100,
///   "projected_cost_usd": 0.0002,
///   "remaining_budget_usd": 8.5,
///   "error": null
/// }
/// ```
///
/// On invalid inputs, returns JSON with `fits=false` and non-null `error`:
/// ```json
/// {
///   "fits": false,
///   "error": "daily_limit_usd must be >= 0.0"
/// }
/// ```
///
/// # Safety
/// - Returns NULL only on FFI failure (NULL input, invalid UTF-8, serialization error)
/// - Caller MUST free returned pointer via `ffi_free_string()`
#[no_mangle]
pub extern "C" fn ffi_token_budget_validate(json_input: *const c_char) -> *mut c_char {
    // Defensive NULL check (G5.3)
    if json_input.is_null() {
        return std::ptr::null_mut();
    }

    // Convert C string to Rust &str
    let c_str = unsafe { CStr::from_ptr(json_input) };
    let json_str = match c_str.to_str() {
        Ok(s) => s,
        Err(_) => return std::ptr::null_mut(), // invalid UTF-8
    };

    // Parse input JSON
    let input: TokenBudgetInput = match serde_json::from_str(json_str) {
        Ok(i) => i,
        Err(_) => return std::ptr::null_mut(), // parse error
    };

    // Call token-budget crate (uses ~4 chars/token heuristic).
    let response = match token_budget::validate_call(
        &input.prompt,
        input.daily_limit_usd,
        input.spent_usd,
        input.cost_per_1k_input_tokens,
        input.max_tokens_per_request,
    ) {
        Ok(check) => TokenBudgetResponse {
            fits: check.fits(),
            fits_budget: check.fits_budget,
            fits_token_limit: check.fits_token_limit,
            estimated_tokens: check.estimated_tokens,
            projected_cost_usd: check.projected_cost_usd,
            remaining_budget_usd: check.remaining_budget_usd,
            error: None,
        },
        Err(budget_err) => TokenBudgetResponse {
            fits: false,
            fits_budget: false,
            fits_token_limit: false,
            estimated_tokens: 0,
            projected_cost_usd: 0.0,
            remaining_budget_usd: 0.0,
            error: Some(budget_err.to_string()),
        },
    };

    // Serialize response to JSON
    let response_json = match serde_json::to_string(&response) {
        Ok(json) => json,
        Err(_) => return std::ptr::null_mut(), // serialization error
    };

    // Return CString pointer (caller frees via ffi_free_string)
    match CString::new(response_json) {
        Ok(c_string) => c_string.into_raw(),
        Err(_) => std::ptr::null_mut(), // NUL byte in response (should never happen)
    }
}

/// Input struct for token budget validation FFI.
#[derive(serde::Deserialize)]
struct TokenBudgetInput {
    prompt: String,
    daily_limit_usd: f64,
    spent_usd: f64,
    cost_per_1k_input_tokens: f64,
    max_tokens_per_request: usize,
}

/// Response struct for token budget validation FFI.
#[derive(serde::Serialize)]
struct TokenBudgetResponse {
    fits: bool,
    fits_budget: bool,
    fits_token_limit: bool,
    estimated_tokens: usize,
    projected_cost_usd: f64,
    remaining_budget_usd: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

// ============================================================================
// Sprint 12: Cache engine FFI (D3 — Cache decision tree)
// ============================================================================

/// Computes the FNV-1a 64-bit hash of a request string. The hash is used
/// as the filename (in hex) for cache entries in data/cache/.
///
/// # Returns
/// The 64-bit hash as u64. Returns 0 on error (NULL input, invalid UTF-8,
/// or empty input). Caller must treat 0 as "cache miss" (do not use as key).
///
/// # Safety
/// - `request` must be a valid NUL-terminated C string or NULL
/// - Never panics across FFI boundary (G5.2)
#[no_mangle]
pub extern "C" fn ffi_cache_hash(request: *const c_char) -> u64 {
    if request.is_null() {
        return 0;
    }
    let c_str = unsafe { CStr::from_ptr(request) };
    let input = match c_str.to_str() {
        Ok(s) => s,
        Err(_) => return 0, // invalid UTF-8
    };
    match cache_engine::cache_key(input) {
        Ok(hash) => hash,
        Err(_) => 0, // empty input
    }
}

/// Pure TTL validation for cache entries stored externally. The Go engine
/// reads the cache file, extracts created_at_secs and ttl_secs, and calls
/// this function to decide whether to serve (D3: "valid hit") or revalidate
/// (D3: "expired hit").
///
/// # Returns
/// `true` if the entry is still valid (age <= ttl), `false` if expired.
///
/// # Safety
/// Never panics. Uses saturating arithmetic to handle clock skew.
#[no_mangle]
pub extern "C" fn ffi_cache_check_ttl(
    created_at_secs: i64,
    ttl_secs: i64,
    now_secs: i64,
) -> bool {
    cache_engine::check_ttl(created_at_secs, ttl_secs, now_secs)
}