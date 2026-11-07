// resolver.go builds the fallback chain of provider URLs used by the
// router.Client.Call method. It walks cfg.Routing.FallbackChain in
// order, resolves each provider name via Resolver.ResolveProvider, and
// returns the ordered list of base URLs.
//
// This file is the integration point between the static configuration
// (settings.yaml) and the dynamic discovery (env var → config → default).
//
// Guardrails enforced:
//   - G2.1: URLs returned contain NO auth tokens — auth is added per-request
//     by client.go.
//   - G1.2: Free-tier providers are NOT filtered here — the caller decides
//     based on cfg.Budget settings.
package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// ProviderEndpoint represents a resolved provider with its name and base URL.
// Used by BuildFallbackChain to return both pieces of information so callers
// can look up per-provider configuration (e.g., auth keys from env vars).
type ProviderEndpoint struct {
	Name string
	URL  string
}

// BuildFallbackChain resolves each provider name in cfg.Routing.FallbackChain
// to a base URL and returns the ordered list. Providers that fail to resolve
// are skipped (with a warning log) rather than failing the entire chain —
// this ensures partial availability when one provider is misconfigured.
//
// Returns an error only if:
//   - resolver is nil
//   - cfg is nil
//   - no providers in the chain could be resolved (empty result)
//
// The caller is responsible for appending the path suffix (e.g. "/v1/completions")
// to each URL before passing to client.Call.
func BuildFallbackChain(ctx context.Context, resolver *Resolver, cfg *config.Config) ([]ProviderEndpoint, error) {
	if resolver == nil {
		return nil, errors.New("router: nil Resolver")
	}
	if cfg == nil {
		return nil, errors.New("router: nil Config")
	}

	chain := cfg.Routing.FallbackChain()
	if len(chain) == 0 {
		// Fallback to the configured default provider if chain is empty.
		chain = []string{cfg.Providers.Default}
	}
	if len(chain) == 0 || chain[0] == "" {
		return nil, errors.New("router: empty fallback chain and no default provider configured")
	}

	endpoints := make([]ProviderEndpoint, 0, len(chain))
	for _, name := range chain {
		if name == "" {
			continue
		}
		url, err := resolver.ResolveProvider(ctx, name)
		if err != nil {
			// Log and skip — don't fail the whole chain for one bad provider.
			log.Warn().
				Err(err).
				Str("provider", name).
				Msg("router: skipping provider in fallback chain (failed to resolve)")
			continue
		}
		endpoints = append(endpoints, ProviderEndpoint{Name: name, URL: url})
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("router: no providers in fallback chain could be resolved (chain=%v)", cfg.Routing.FallbackChain())
	}

	log.Info().
		Strs("providers", cfg.Routing.FallbackChain()).
		Strs("urls", func() []string {
			urls := make([]string, len(endpoints))
			for i, ep := range endpoints {
				urls[i] = ep.URL
			}
			return urls
		}()).
		Msg("router: fallback chain built")
	return endpoints, nil
}

// ResolveSingle is a convenience wrapper that returns only the primary
// provider's URL (first in the fallback chain). Used by callers that
// don't need fallback (e.g., discovery-only calls, metadata lookups).
func ResolveSingle(ctx context.Context, resolver *Resolver, cfg *config.Config) (string, error) {
	urls, err := BuildFallbackChain(ctx, resolver, cfg)
	if err != nil {
		return "", err
	}
	return urls[0].URL, nil
}
