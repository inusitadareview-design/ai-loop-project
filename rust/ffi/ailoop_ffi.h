#ifndef AILOOP_FFI_H
#define AILOOP_FFI_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// Sprint 1: Hello handshake
const char* ffi_hello(void);

// Sprint 5: Memory compression
char* ffi_memory_compress(const char* json_events);

// Sprint 9: Token budget validation (I1)
char* ffi_token_budget_validate(const char* json_input);

// Memory management (caller frees pointers returned by functions above)
void ffi_free_string(char* ptr);

#ifdef __cplusplus
}
#endif

#endif // AILOOP_FFI_H