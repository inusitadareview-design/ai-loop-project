package omniroute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
