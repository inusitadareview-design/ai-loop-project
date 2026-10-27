// Package router implements provider routing, endpoint discovery, and
// HTTP client orchestration for the AI Loop Project. It is the
// integration point between Go's orchestration layer and the Rust hot
// paths (via the omniroute FFI bridge).
//
// discovery.go resolves the OmniRoute endpoint dynamically so the system
// works in any deployment environment (local dev, Docker, Kubernetes,
// bare metal) without config changes — enforcing invariant I4.
package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// Default endpoint when neither env var nor DNS resolves. This is the
// public OmniRoute endpoint documented in docs/SPEC.md.
const defaultOmniRouteURL = "https://api.omniroute.ai"

// DNS hostname used for local service discovery (Docker/K8s). When
// resolved successfully, the system treats this as the OmniRoute
// endpoint. This enables "nomadic" deployments without env var config.
const localDNSHostname = "omniroute.local"

// Environment variable name for explicit endpoint override. Takes
// precedence over DNS and default.
const envOmniRouteURL = "OMNI_ROUTE_URL"

// dnsLookupTimeout caps how long we wait for the DNS probe. Keeps
// Resolve() responsive even when the local DNS server is unresponsive.
const dnsLookupTimeout = 500 * time.Millisecond

// Resolver returns the OmniRoute base URL with a priority chain:
// env var → DNS → default. The result is cached for the lifetime of
// the Resolver (process-scoped) so subsequent calls are O(1).
//
// Callers MUST treat the returned URL as read-only and MUST NOT append
// authentication tokens to it — auth is added per-request by client.go
// (guardrail G2.1: secrets never live in the URL itself).
type Resolver struct {
	cfg    *config.Config
	logger *observability.Logger

	// lookupHost is the DNS probe function. Exposed as a field (not a
	// package-level call) so tests can inject a mock without hitting
	// the network. Defaults to net.DefaultResolver.LookupHost.
	lookupHost func(ctx context.Context, host string) ([]string, error)

	// getenv is the env-var reader. Exposed so tests can inject a
	// deterministic environment without mutating os.Environ.
	getenv func(key string) string

	once     sync.Once
	cached   string
	cacheErr error
}

// NewResolver builds a Resolver wired to the project's config and
// logger. The resolver is safe for concurrent use.
func NewResolver(cfg *config.Config, logger *observability.Logger) *Resolver {
	return &Resolver{
		cfg:    cfg,
		logger: logger,
		lookupHost: func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		},
		getenv: os.Getenv,
	}
}

// Resolve returns the OmniRoute base URL. The priority chain is:
//
//  1. `OMNI_ROUTE_URL` env var (if non-empty after trimming).
//  2. DNS resolution of `omniroute.local` (if it resolves within
//     dnsLookupTimeout, returns `http://<first-ip>`).
//  3. Default `https://api.omniroute.ai`.
//
// The result is cached after the first successful call. Subsequent
// calls return the cached value without re-probing. This is
// intentional: the endpoint is process-scoped; a restart refreshes it.
//
// Resolve never returns an error under normal operation — even a DNS
// failure simply falls through to the default. Errors are only
// returned for truly broken states (e.g., nil config).
func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	if r == nil {
		return "", errors.New("router: nil Resolver")
	}
	if r.cfg == nil {
		return "", errors.New("router: Resolver has nil config")
	}

	r.once.Do(func() {
		r.cached, r.cacheErr = r.resolveOnce(ctx)
	})

	return r.cached, r.cacheErr
}

// resolveOnce performs the priority-chain resolution. Called exactly
// once per Resolver lifetime (guarded by sync.Once).
func (r *Resolver) resolveOnce(ctx context.Context) (string, error) {
	// 1. Env var override.
	if url := strings.TrimSpace(r.getenv(envOmniRouteURL)); url != "" {
		log.Info().
			Str("source", "env").
			Str("url", url).
			Msg("router: OmniRoute endpoint resolved from environment")
		return normalizeURL(url), nil
	}

	// 2. DNS probe for local service discovery (Docker/K8s).
	if url, ok := r.probeLocalDNS(ctx); ok {
		log.Info().
			Str("source", "dns").
			Str("url", url).
			Msg("router: OmniRoute endpoint resolved from local DNS")
		return url, nil
	}

	// 3. Default public endpoint.
	log.Info().
		Str("source", "default").
		Str("url", defaultOmniRouteURL).
		Msg("router: OmniRoute endpoint using default public URL")
	return defaultOmniRouteURL, nil
}

// probeLocalDNS attempts to resolve `omniroute.local` within the
// configured timeout. Returns the first resolved IP wrapped in an
// http:// URL, and true on success. Returns ("", false) on any
// failure (timeout, NXDOMAIN, no records, context canceled).
//
// Failures here are expected in most deployments (public cloud, local
// dev without a local DNS entry) and are logged at debug level — they
// are NOT errors, they are simply the "DNS did not resolve" branch of
// the priority chain.
func (r *Resolver) probeLocalDNS(ctx context.Context) (string, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()

	ips, err := r.lookupHost(probeCtx, localDNSHostname)
	if err != nil {
		// Expected in most environments; debug-only to avoid log noise.
		log.Debug().
			Err(err).
			Str("hostname", localDNSHostname).
			Msg("router: local DNS probe did not resolve")
		return "", false
	}
	if len(ips) == 0 {
		return "", false
	}

	// Pick the first IP. In a real deployment the operator controls
	// which records are published, so the first one is canonical.
	ip := ips[0]
	// Wrap IPv6 addresses in brackets so http://[::1]/path parses.
	if strings.Contains(ip, ":") {
		ip = "[" + ip + "]"
	}
	return "http://" + ip, true
}

// normalizeURL ensures the returned URL has a scheme and no trailing
// slash. Idempotent — safe to call on an already-normalized URL.
//
// - Empty input → defaultOmniRouteURL (defensive).
// - Missing scheme → prepends `https://`.
// - Trailing slashes are stripped.
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultOmniRouteURL
	}
	// Prepend scheme if missing.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	// Strip trailing slashes (but preserve scheme's "//").
	for strings.HasSuffix(raw, "/") {
		raw = strings.TrimSuffix(raw, "/")
	}
	return raw
}

// InvalidateCache clears the cached resolution result so the next
// Resolve() call re-runs the priority chain. Primarily useful in
// tests; production code should create a new Resolver instead.
//
// Concurrency-safe: can be called while other goroutines are calling
// Resolve() — the next call after InvalidateCache will re-resolve.
func (r *Resolver) InvalidateCache() {
	if r == nil {
		return
	}
	// Reset sync.Once by replacing it with a fresh one. This is safe
	// because sync.Once's zero value is a valid "not yet done" state.
	r.once = sync.Once{}
	r.cached = ""
	r.cacheErr = nil
}

// String returns a human-readable representation of the Resolver's
// current state. Useful for structured logging and debugging.
func (r *Resolver) String() string {
	if r == nil {
		return "<nil Resolver>"
	}
	return fmt.Sprintf("Resolver{cached=%q, err=%v}", r.cached, r.cacheErr)
}
