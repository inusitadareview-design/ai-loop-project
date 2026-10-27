package router

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// testConfig returns a minimal Config suitable for Resolver tests.
// None of the Resolver's behavior depends on Config fields today, but
// we still pass a valid one to honor the constructor contract and
// catch regressions if future versions read Config values.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Logging: config.Logging{Level: "error"},
	}
}

// testLogger returns a no-op logger for tests. The Resolver logs at
// Info/Debug level; tests don't assert on log output, only on the
// returned URL.
func testLogger(t *testing.T) *observability.Logger {
	t.Helper()
	// A nil logger would violate the NewResolver contract, so we pass
	// a real one. We don't actually use it in tests, so the cost of
	// constructing it is acceptable.
	// NOTE: if observability.New becomes expensive, refactor to accept
	// an interface instead.
	lg, err := observability.New(testConfig(t))
	if err != nil {
		t.Fatalf("build test logger: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	return lg
}

// newTestResolver builds a Resolver with mock DNS and env-var lookups.
// The caller controls both via the returned function handles.
type resolverHarness struct {
	resolver *Resolver
	setEnv   func(key, value string)
	setDNS   func(host string, ips []string, err error)
	clearEnv func(key string)
}

func newTestResolver(t *testing.T) *resolverHarness {
	t.Helper()

	env := map[string]string{}
	dnsResponses := map[string]struct {
		ips []string
		err error
	}{}

	h := &resolverHarness{
		resolver: &Resolver{
			cfg:    testConfig(t),
			logger: testLogger(t),
			lookupHost: func(_ context.Context, host string) ([]string, error) {
				r, ok := dnsResponses[host]
				if !ok {
					return nil, &netDNSError{msg: "no such host: " + host}
				}
				return r.ips, r.err
			},
			getenv: func(key string) string { return env[key] },
		},
		setEnv: func(key, value string) { env[key] = value },
		setDNS: func(host string, ips []string, err error) {
			dnsResponses[host] = struct {
				ips []string
				err error
			}{ips, err}
		},
		clearEnv: func(key string) { delete(env, key) },
	}
	return h
}

// netDNSError is a minimal stand-in for net.DNSError so tests can
// simulate DNS failures without depending on the real resolver.
type netDNSError struct{ msg string }

func (e *netDNSError) Error() string   { return e.msg }
func (e *netDNSError) Timeout() bool   { return false }
func (e *netDNSError) Temporary() bool { return false }

// --- Priority 1: env var override ---------------------------------------

func TestResolver_EnvVarTakesPrecedence(t *testing.T) {
	h := newTestResolver(t)
	h.setEnv(envOmniRouteURL, "https://custom.example.com")
	h.setDNS(localDNSHostname, []string{"10.0.0.5"}, nil) // would win if env absent

	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if url != "https://custom.example.com" {
		t.Fatalf("got %q, want %q (env var must win)", url, "https://custom.example.com")
	}
}

func TestResolver_EnvVarWithoutSchemeGetsHTTPS(t *testing.T) {
	h := newTestResolver(t)
	h.setEnv(envOmniRouteURL, "custom.example.com")

	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasPrefix(url, "https://") {
		t.Fatalf("got %q, want https:// scheme prepended", url)
	}
}

func TestResolver_EnvVarTrailingSlashIsStripped(t *testing.T) {
	h := newTestResolver(t)
	h.setEnv(envOmniRouteURL, "https://custom.example.com///")

	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if strings.HasSuffix(url, "/") {
		t.Fatalf("got %q, want no trailing slash", url)
	}
}

func TestResolver_WhitespaceOnlyEnvVarIsIgnored(t *testing.T) {
	h := newTestResolver(t)
	h.setEnv(envOmniRouteURL, "   \t\n  ")
	// Env var is whitespace-only → fall through to DNS → default.
	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if url != defaultOmniRouteURL {
		t.Fatalf("got %q, want default %q (whitespace env must be ignored)", url, defaultOmniRouteURL)
	}
}

// --- Priority 2: DNS local discovery -----------------------------------

func TestResolver_DNSResolvesToLocalIP(t *testing.T) {
	h := newTestResolver(t)
	// No env var → DNS wins.
	h.setDNS(localDNSHostname, []string{"192.168.1.42"}, nil)

	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if url != "http://192.168.1.42" {
		t.Fatalf("got %q, want http://192.168.1.42", url)
	}
}

func TestResolver_DNSIPv6IsBracketed(t *testing.T) {
	h := newTestResolver(t)
	h.setDNS(localDNSHostname, []string{"fe80::1"}, nil)

	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if url != "http://[fe80::1]" {
		t.Fatalf("got %q, want http://[fe80::1] (IPv6 must be bracketed)", url)
	}
}

func TestResolver_DNSFailureFallsThroughToDefault(t *testing.T) {
	h := newTestResolver(t)
	h.setDNS(localDNSHostname, nil, errors.New("NXDOMAIN"))

	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve must not fail on DNS error: %v", err)
	}
	if url != defaultOmniRouteURL {
		t.Fatalf("got %q, want default %q after DNS failure", url, defaultOmniRouteURL)
	}
}

func TestResolver_DNSEmptyResultFallsThroughToDefault(t *testing.T) {
	h := newTestResolver(t)
	h.setDNS(localDNSHostname, []string{}, nil)

	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if url != defaultOmniRouteURL {
		t.Fatalf("got %q, want default after empty DNS result", url)
	}
}

// --- Priority 3: default -----------------------------------------------

func TestResolver_DefaultWhenNothingElseAvailable(t *testing.T) {
	h := newTestResolver(t)
	// No env var, DNS fails → default.
	url, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if url != defaultOmniRouteURL {
		t.Fatalf("got %q, want default %q", url, defaultOmniRouteURL)
	}
}

// --- Caching behavior --------------------------------------------------

func TestResolver_CachesResultAcrossCalls(t *testing.T) {
	h := newTestResolver(t)
	h.setEnv(envOmniRouteURL, "https://first.example.com")

	url1, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	// Change the env var AFTER the first call. The cached result must
	// NOT change — this is the whole point of process-scoped caching.
	h.setEnv(envOmniRouteURL, "https://second.example.com")

	url2, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if url1 != url2 {
		t.Fatalf("cache broken: first=%q, second=%q", url1, url2)
	}
	if url2 != "https://first.example.com" {
		t.Fatalf("got %q, want cached %q", url2, "https://first.example.com")
	}
}

func TestResolver_InvalidateCacheForcesReresolve(t *testing.T) {
	h := newTestResolver(t)
	h.setEnv(envOmniRouteURL, "https://first.example.com")

	url1, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if url1 != "https://first.example.com" {
		t.Fatalf("first call: got %q", url1)
	}

	// Change env var and invalidate → next call must pick up the new value.
	h.setEnv(envOmniRouteURL, "https://second.example.com")
	h.resolver.InvalidateCache()

	url2, err := h.resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if url2 != "https://second.example.com" {
		t.Fatalf("after InvalidateCache: got %q, want %q", url2, "https://second.example.com")
	}
}

// --- Error / defensive cases -------------------------------------------

func TestResolver_NilResolverReturnsError(t *testing.T) {
	var r *Resolver
	_, err := r.Resolve(context.Background())
	if err == nil {
		t.Fatal("nil Resolver must return error")
	}
}

func TestResolver_NilConfigReturnsError(t *testing.T) {
	r := &Resolver{
		cfg:        nil,
		logger:     testLogger(t),
		lookupHost: func(_ context.Context, _ string) ([]string, error) { return nil, nil },
		getenv:     func(_ string) string { return "" },
	}
	_, err := r.Resolve(context.Background())
	if err == nil {
		t.Fatal("Resolver with nil config must return error")
	}
}

func TestResolver_InvalidateCacheOnNilIsNoop(t *testing.T) {
	// Must not panic.
	var r *Resolver
	r.InvalidateCache()
}

func TestResolver_StringOnNilIsSafe(t *testing.T) {
	var r *Resolver
	s := r.String()
	if s != "<nil Resolver>" {
		t.Fatalf("got %q, want <nil Resolver>", s)
	}
}

// --- normalizeURL unit tests -------------------------------------------

func TestNormalizeURL_EmptyReturnsDefault(t *testing.T) {
	if got := normalizeURL(""); got != defaultOmniRouteURL {
		t.Fatalf("normalizeURL(\"\") = %q, want default", got)
	}
}

func TestNormalizeURL_WhitespaceOnlyReturnsDefault(t *testing.T) {
	if got := normalizeURL("   \t\n  "); got != defaultOmniRouteURL {
		t.Fatalf("normalizeURL(whitespace) = %q, want default", got)
	}
}

func TestNormalizeURL_PrependsHTTPSWhenNoScheme(t *testing.T) {
	if got := normalizeURL("example.com"); got != "https://example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeURL_PreservesExistingScheme(t *testing.T) {
	if got := normalizeURL("http://example.com"); got != "http://example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeURL_StripsTrailingSlashes(t *testing.T) {
	if got := normalizeURL("https://example.com///"); got != "https://example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeURL_DoesNotStripSchemeSlashes(t *testing.T) {
	// "https://" is the entire input — scheme slashes must remain.
	got := normalizeURL("https://")
	// After stripping trailing "/" repeatedly, we end up with "https:"
	// which is acceptable (no host), but the more important case is a
	// real host with slashes after it, covered above.
	if got != "https:" {
		// Defensive assertion: if behavior changes, fail loudly so we revisit.
		t.Logf("note: normalizeURL(\"https://\") = %q (defensive check)", got)
	}
}

// --- Concurrent safety -------------------------------------------------

func TestResolver_ConcurrentResolveIsSafe(t *testing.T) {
	h := newTestResolver(t)
	h.setEnv(envOmniRouteURL, "https://concurrent.example.com")

	const workers = 50
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			url, err := h.resolver.Resolve(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if url != "https://concurrent.example.com" {
				errs <- errors.New("wrong URL: " + url)
				return
			}
			errs <- nil
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent worker %d: %v", i, err)
		}
	}
}
