#ifndef AILOOP_FFI_H
#define AILOOP_FFI_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Sprint 1: Hello handshake
const char* ffi_hello(void);

// Sprint 5: Memory compression
char* ffi_memory_compress(const char* json_events);

// Sprint 9: Token budget validation (I1)
char* ffi_token_budget_validate(const char* json_input);

// Sprint 12: Cache engine FFI (D3 — Cache decision tree)

/* Compute FNV-1a 64-bit hash of request. Returns 0 on error. */
uint64_t ffi_cache_hash(const char *request);

/* Pure TTL check: returns 1 if valid (age <= ttl), 0 if expired. */
int ffi_cache_check_ttl(int64_t created_at_secs, int64_t ttl_secs, int64_t now_secs);

// Memory management (caller frees pointers returned by functions above)
void ffi_free_string(char* ptr);

#ifdef __cplusplus
}
#endif

#endif // AILOOP_FFI_H
