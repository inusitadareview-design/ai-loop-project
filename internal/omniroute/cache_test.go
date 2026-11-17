package omniroute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheHash_Deterministic(t *testing.T) {
	request := "POST /api/v1/generate {\"prompt\":\"hello\"}"
	h1, err := CacheHash(request)
	if err != nil {
		t.Fatalf("CacheHash: %v", err)
	}
	h2, err := CacheHash(request)
	if err != nil {
		t.Fatalf("CacheHash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("hash not deterministic: %d != %d", h1, h2)
	}
	if h1 == 0 {
		t.Error("hash returned 0 for non-empty input")
	}
}

func TestCacheHash_DifferentInputsDiffer(t *testing.T) {
	h1, _ := CacheHash("request A")
	h2, _ := CacheHash("request B")
	if h1 == h2 {
		t.Errorf("different inputs produced same hash: %d", h1)
	}
}

func TestCacheHash_EmptyReturnsError(t *testing.T) {
	_, err := CacheHash("")
	if err == nil {
		t.Fatal("expected error for empty request")
	}
}

func TestCacheCheckTTL_Valid(t *testing.T) {
	now := time.Now().Unix()
	// Created 10s ago, TTL 60s → valid (age 10 < 60)
	if !CacheCheckTTL(now-10, 60, now) {
		t.Error("expected valid (age 10 < TTL 60)")
	}
	// Created exactly TTL ago → still valid (age == TTL is NOT expired per Rust)
	if !CacheCheckTTL(now-60, 60, now) {
		t.Error("expected valid at exact TTL boundary (age == TTL)")
	}
}

func TestCacheCheckTTL_Expired(t *testing.T) {
	now := time.Now().Unix()
	// Created 61s ago, TTL 60s → expired (age 61 > 60)
	if CacheCheckTTL(now-61, 60, now) {
		t.Error("expected expired (age 61 > TTL 60)")
	}
	// Zero TTL created 1s ago → expired
	if CacheCheckTTL(now-1, 0, now) {
		t.Error("expected expired for zero TTL after 1s")
	}
}

func TestCacheLookup_MissWhenFileMissing(t *testing.T) {
	tmp := t.TempDir()
	hit, err := CacheLookup(context.Background(), tmp, "nonexistent request")
	if err != nil {
		t.Fatalf("CacheLookup: %v", err)
	}
	if hit != nil {
		t.Errorf("expected nil hit for missing file, got %+v", hit)
	}
}

func TestCacheLookup_HitWhenValid(t *testing.T) {
	tmp := t.TempDir()
	request := "test request for lookup"
	value := `{"response":"cached data"}`
	ttl := int64(3600)

	if err := CacheStore(context.Background(), tmp, request, value, ttl); err != nil {
		t.Fatalf("CacheStore: %v", err)
	}

	hit, err := CacheLookup(context.Background(), tmp, request)
	if err != nil {
		t.Fatalf("CacheLookup: %v", err)
	}
	if hit == nil {
		t.Fatal("expected cache hit, got nil")
	}
	if hit.Value != value {
		t.Errorf("value = %q, want %q", hit.Value, value)
	}
	if hit.RemainingTTLSecs <= 0 || hit.RemainingTTLSecs > ttl {
		t.Errorf("remaining TTL = %d, want in (0, %d]", hit.RemainingTTLSecs, ttl)
	}
}

func TestCacheLookup_Expired(t *testing.T) {
	tmp := t.TempDir()
	request := "expired request"
	value := `{"old":"data"}`

	// Store with TTL 0 → expires immediately
	if err := CacheStore(context.Background(), tmp, request, value, 0); err != nil {
		t.Fatalf("CacheStore: %v", err)
	}

	// Wait 1 second to ensure expiration
	time.Sleep(1 * time.Second)

	hit, err := CacheLookup(context.Background(), tmp, request)
	if err != nil {
		t.Fatalf("CacheLookup: %v", err)
	}
	if hit != nil {
		t.Errorf("expected nil hit for expired entry, got %+v", hit)
	}
}

func TestCacheStore_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	request := "round-trip request"
	value := `{"test":"round trip"}`
	ttl := int64(300)

	if err := CacheStore(context.Background(), tmp, request, value, ttl); err != nil {
		t.Fatalf("CacheStore: %v", err)
	}

	// Verify file exists with expected name
	hash, _ := CacheHash(request)
	path := filepath.Join(tmp, fmt.Sprintf("%016x.json", hash))
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("cache file %q does not exist", path)
	}

	// Verify content via lookup
	hit, err := CacheLookup(context.Background(), tmp, request)
	if err != nil {
		t.Fatalf("CacheLookup: %v", err)
	}
	if hit == nil {
		t.Fatal("expected cache hit after store")
	}
	if hit.Value != value {
		t.Errorf("round-trip value = %q, want %q", hit.Value, value)
	}
}

func TestCacheStore_RejectsInvalidInputs(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()

	// Empty request
	if err := CacheStore(ctx, tmp, "", "value", 60); err == nil {
		t.Error("expected error for empty request")
	}
	// Empty value
	if err := CacheStore(ctx, tmp, "request", "", 60); err == nil {
		t.Error("expected error for empty value")
	}
	// Negative TTL
	if err := CacheStore(ctx, tmp, "request", "value", -1); err == nil {
		t.Error("expected error for negative TTL")
	}
}

func TestCacheStore_ContextCancellation(t *testing.T) {
	tmp := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := CacheStore(ctx, tmp, "request", "value", 60)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestCacheLookup_ContextCancellation(t *testing.T) {
	tmp := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CacheLookup(ctx, tmp, "request")
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

// --- Sprint 18a: Cache Eviction (G3.4 — bounded resource growth) ---

func TestCacheEvict_ByMaxEntries(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()

	// Store 5 entries with 1-second gaps so timestamps differ (LRU by age).
	for i := 0; i < 5; i++ {
		request := fmt.Sprintf("request-%d", i)
		if err := CacheStore(ctx, tmp, request, "value", 3600); err != nil {
			t.Fatalf("CacheStore[%d]: %v", i, err)
		}
		time.Sleep(1100 * time.Millisecond)
	}

	// Evict to max 3 entries — should remove the 2 oldest.
	evicted, err := CacheEvict(ctx, tmp, 3, 0) // maxEntries=3, maxBytes=0 (disabled)
	if err != nil {
		t.Fatalf("CacheEvict: %v", err)
	}
	if evicted != 2 {
		t.Errorf("evicted = %d, want 2", evicted)
	}

	// Verify only 3 files remain.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("remaining entries = %d, want 3", len(entries))
	}

	// The NEWEST entry (request-4) must still be present.
	hit, err := CacheLookup(ctx, tmp, "request-4")
	if err != nil {
		t.Fatalf("CacheLookup request-4: %v", err)
	}
	if hit == nil {
		t.Error("newest entry (request-4) was wrongly evicted")
	}

	// The OLDEST entry (request-0) must be gone.
	hit, err = CacheLookup(ctx, tmp, "request-0")
	if err != nil {
		t.Fatalf("CacheLookup request-0: %v", err)
	}
	if hit != nil {
		t.Error("oldest entry (request-0) was NOT evicted (LRU violated)")
	}
}

func TestCacheEvict_ByMaxBytes(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()

	// Store 3 entries of ~120 bytes each (30 "x"s instead of 80).
	// JSON structure overhead is ~80 chars, so value of 30 chars = ~110-120 bytes per file.
	for i := 0; i < 3; i++ {
		request := fmt.Sprintf("request-%d", i)
		value := fmt.Sprintf("value-%d-%s", i, strings.Repeat("x", 30)) // reduced from 80
		if err := CacheStore(ctx, tmp, request, value, 3600); err != nil {
			t.Fatalf("CacheStore[%d]: %v", i, err)
		}
		time.Sleep(1100 * time.Millisecond)
	}

	// Evict to max 150 bytes — should remove 2 oldest entries.
	// 3 entries × ~120 bytes = ~360 bytes total.
	// After removing 2 oldest: ~120 bytes remaining ≤ 150 (limit).
	evicted, err := CacheEvict(ctx, tmp, 0, 150) // maxEntries=0 (disabled), maxBytes=150
	if err != nil {
		t.Fatalf("CacheEvict: %v", err)
	}
	if evicted < 1 {
		t.Errorf("evicted = %d, want >= 1", evicted)
	}

	// The NEWEST entry must still be present.
	hit, err := CacheLookup(ctx, tmp, "request-2")
	if err != nil {
		t.Fatalf("CacheLookup request-2: %v", err)
	}
	if hit == nil {
		t.Error("newest entry (request-2) was wrongly evicted")
	}
}

func TestCacheEvict_ZeroLimitsMeansNoEviction(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()

	// Store 5 entries.
	for i := 0; i < 5; i++ {
		if err := CacheStore(ctx, tmp, fmt.Sprintf("request-%d", i), "value", 3600); err != nil {
			t.Fatalf("CacheStore: %v", err)
		}
	}

	// Both limits zero = disabled. Nothing should be evicted.
	evicted, err := CacheEvict(ctx, tmp, 0, 0)
	if err != nil {
		t.Fatalf("CacheEvict: %v", err)
	}
	if evicted != 0 {
		t.Errorf("evicted = %d, want 0 (both limits disabled)", evicted)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("entries = %d, want 5 (no eviction)", len(entries))
	}
}

func TestCacheEvict_EmptyDirectory(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()

	// Evict on empty dir should not error.
	evicted, err := CacheEvict(ctx, tmp, 10, 1024)
	if err != nil {
		t.Fatalf("CacheEvict on empty dir: %v", err)
	}
	if evicted != 0 {
		t.Errorf("evicted = %d, want 0 (empty dir)", evicted)
	}
}

func TestCacheEvict_NonExistentDirectory(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "nonexistent", "cache")
	ctx := context.Background()

	// Evict on non-existent dir should not error (graceful).
	evicted, err := CacheEvict(ctx, nonExistent, 10, 1024)
	if err != nil {
		t.Fatalf("CacheEvict on non-existent dir: %v (should be non-fatal per D3)", err)
	}
	if evicted != 0 {
		t.Errorf("evicted = %d, want 0 (dir does not exist)", evicted)
	}
}
