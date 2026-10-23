//! ffi — camada C-ABI consumida pelo Go via CGO (GUARDRAILS G5.3).
//!
//! Sprint 1: expõe `ffi_hello` para validar o pipeline de build Rust → .a → CGO.
//! Regras: funções extern "C" usam apenas tipos C-ABI simples; nunca panic
//! através da fronteira (G5.2: Result interno, retorno defensivo no FFI).

use std::os::raw::c_char;

/// Mensagem canônica de handshake do bridge FFI.
/// Estática: o ponteiro permanece válido após o retorno (o Go copia na hora).
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

/// Versão interna com Result<T, E> (padrão do workspace — G5.2):
/// valida a mensagem estática sem nunca usar unwrap() em produção.
pub fn hello_message() -> Result<&'static str, std::str::Utf8Error> {
    std::str::from_utf8(&HELLO[..HELLO.len() - 1])
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::ffi::CStr;

    #[derive(Debug, PartialEq)]
    struct FfiHelloError(&'static str);

    /// Caminho de teste com Result<T, E> explícito (sem unwrap).
    fn check_ffi_hello() -> Result<String, FfiHelloError> {
        let ptr = ffi_hello();
        if ptr.is_null() {
            return Err(FfiHelloError("ffi_hello retornou NULL"));
        }
        // SAFETY: HELLO é uma estática NUL-terminated válida neste processo.
        let cstr = unsafe { CStr::from_ptr(ptr) };
        cstr.to_str()
            .map(|s| s.to_string())
            .map_err(|_| FfiHelloError("UTF-8 invalido na estatica HELLO"))
    }

    #[test]
    fn ffi_hello_returns_expected_cstring() {
        assert_eq!(check_ffi_hello(), Ok("Hello from Rust FFI".to_string()));
    }

    #[test]
    fn hello_message_matches_ffi_hello() -> Result<(), std::str::Utf8Error> {
        assert_eq!(hello_message()?, "Hello from Rust FFI");
        Ok(())
    }
}
