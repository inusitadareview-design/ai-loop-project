// cache.go provides Go-side cache integration for AI responses.
// Sprint 12 (Cache Integration): implements decision tree D3 — cache hit
// (TTL valid) serves from disk at cost $0, expired hit triggers revalidation,
// miss triggers provider call and storage.
//
// Architecture: Rust cache-engine provides pure logic (FNV-1a hashing +
// TTL validation) via FFI. Go manages file I/O in data/cache/ so the
// Rust crate remains dependency-free and the build reproducible (G6.2).
//
// Guardrails enforced:
// - G5.3: all FFI calls use C-ABI, caller frees memory immediately
// - G5.1: all errors wrapped with fmt.Errorf
// - G2.2: cacheDir must be under data/ (caller responsibility)
// - D3: cache decision tree (hit → serve, expired → revalidate, miss → call)
package omniroute

/*
#cgo LDFLAGS: -L${SRCDIR}/../../pkg/ffi -l:libailoop_ffi.a -lm -ldl -lpthread
#cgo CFLAGS: -I${SRCDIR}/../../pkg/ffi
#include <stdlib.h>
#include "ailoop_ffi.h"
*/
import "C"
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"
)

// cacheEntry is the JSON structure persisted in data/cache/<hash>.json.
// Matches the Rust CacheEntry schema for cross-language compatibility.
type cacheEntry struct {
	Key           uint64 `json:"key"`
	Value         string `json:"value"`
	CreatedAtSecs int64  `json:"created_at_secs"`
	TTLSecs       int64  `json:"ttl_secs"`
}

// CacheHit is the result of a successful cache lookup (D3: valid hit).
type CacheHit struct {
	// Value is the cached response body (typically JSON from the provider).
	Value string
	// RemainingTTLSecs is how many seconds until the entry expires.
	// Used by the engine to decide whether to refresh proactively.
	RemainingTTLSecs int64
}

// CacheHash computes the FNV-1a 64-bit hash of a request string via the
// Rust FFI. Returns 0 on error (empty input, NULL). The caller should
// treat 0 as "cache miss" and not use it as a filename.
//
// Thread-safe: the Rust implementation is stateless.
func CacheHash(request string) (uint64, error) {
	if request == "" {
		return 0, fmt.Errorf("cache: empty request")
	}

	cRequest := C.CString(request)
	defer C.free(unsafe.Pointer(cRequest))

	hash := C.ffi_cache_hash(cRequest)
	if hash == 0 {
		return 0, fmt.Errorf("cache: FFI returned 0 (invalid input)")
	}

	return uint64(hash), nil
}

// CacheCheckTTL validates whether a cache entry is still fresh using the
// Rust FFI's pure TTL check. Returns true if the entry is valid
// (age <= ttl), false if expired. Uses saturating arithmetic to handle
// clock skew safely.
//
// Thread-safe: the Rust implementation is stateless.
func CacheCheckTTL(createdAtSecs, ttlSecs, nowSecs int64) bool {
	result := C.ffi_cache_check_ttl(
		C.int64_t(createdAtSecs),
		C.int64_t(ttlSecs),
		C.int64_t(nowSecs),
	)
	return result != 0
}

// cacheFilePath returns the absolute path for a cache entry given its hash.
// Uses hex encoding of the 64-bit hash as the filename.
func cacheFilePath(cacheDir string, hash uint64) string {
	return filepath.Join(cacheDir, fmt.Sprintf("%016x.json", hash))
}

// CacheLookup performs a cache lookup following decision tree D3:
// - File not found → returns (nil, nil) [cache miss]
// - File unreadable/corrupt → returns (nil, nil) [cache miss, log warn]
// - Entry expired → returns (nil, nil) [expired hit, caller should revalidate]
// - Entry valid → returns (*CacheHit, nil) [valid hit, serve from cache]
//
// The cacheDir must exist; if it does not, returns (nil, nil) [cache miss].
// Errors are logged but do not fail the pipeline (D3: cache write failure
// NEVER crashes pipeline; by extension, read failures are non-fatal).
func CacheLookup(ctx context.Context, cacheDir string, request string) (*CacheHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}

	hash, err := CacheHash(request)
	if err != nil {
		return nil, nil // treat hash failure as cache miss
	}

	path := cacheFilePath(cacheDir, hash)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // cache miss
		}
		// Unreadable file → log and treat as miss (D3: non-fatal)
		return nil, nil
	}

	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		// Corrupt entry → treat as miss (D3: non-fatal)
		return nil, nil
	}

	// Sanity: ensure the stored key matches the computed hash (detects
	// hash collisions or file corruption).
	if entry.Key != hash {
		return nil, nil
	}

	now := time.Now().Unix()
	if !CacheCheckTTL(entry.CreatedAtSecs, entry.TTLSecs, now) {
		return nil, nil // expired hit → caller should revalidate
	}

	remaining := entry.TTLSecs - (now - entry.CreatedAtSecs)
	if remaining < 0 {
		remaining = 0
	}

	return &CacheHit{
		Value:            entry.Value,
		RemainingTTLSecs: remaining,
	}, nil
}

// CacheStore persists a response to data/cache/<hash>.json with the given
// TTL. Follows atomic write pattern (write to .tmp, rename) to avoid
// partial reads during concurrent lookups.
//
// Errors:
//   - empty request or value → error
//   - cacheDir does not exist → error (caller must ensure it exists)
//   - I/O failure → error (D3: cache write failure is logged but does not
//     crash the pipeline; the engine is responsible for handling this)
//
// Thread-safe for concurrent writes: the rename is atomic on POSIX.
func CacheStore(ctx context.Context, cacheDir string, request string, value string, ttlSecs int64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if request == "" {
		return fmt.Errorf("cache: empty request")
	}
	if value == "" {
		return fmt.Errorf("cache: empty value")
	}
	if ttlSecs < 0 {
		return fmt.Errorf("cache: negative TTL")
	}

	hash, err := CacheHash(request)
	if err != nil {
		return fmt.Errorf("cache: hash: %w", err)
	}

	entry := cacheEntry{
		Key:           hash,
		Value:         value,
		CreatedAtSecs: time.Now().Unix(),
		TTLSecs:       ttlSecs,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("cache: marshal: %w", err)
	}

	path := cacheFilePath(cacheDir, hash)
	tmpPath := path + ".tmp"

	// Atomic write: write to temp file, then rename.
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("cache: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cache: rename: %w", err)
	}

	return nil
}
