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
}