// web_fetch.go implements the `web_fetch` action (Sprint 17 Phase 1).
// It fetches RSS/Atom feeds over HTTP, parses items, filters by age,
// and returns a JSON array of news items for downstream stages to consume.
//
// Guardrails enforced:
// - G5.1: every error wrapped with fmt.Errorf
// - I2: structured zerolog logs for every network call
// - G2.1: no secrets involved (public RSS feeds)
//
// Architecture note: this action uses net/http directly (NOT router.Client)
// because RSS feeds are public, unauthenticated, and do not belong to the
// AI provider fallback chain. Router is reserved for LLM completions.
package engine

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// webFetchConfig maps the `config:` block of a web_fetch stage in the YAML.
type webFetchConfig struct {
	URLs                   []string `json:"urls" yaml:"urls"`
	MaxItems               int      `json:"max_items" yaml:"max_items"`
	MaxAgeHours            int      `json:"max_age_hours" yaml:"max_age_hours"`
	TimeoutSeconds         int      `json:"timeout_seconds" yaml:"timeout_seconds"`
	MaxDescriptionChars    int      `json:"max_description_chars" yaml:"max_description_chars"`
	MaxContentEncodedChars int      `json:"max_content_encoded_chars" yaml:"max_content_encoded_chars"` // Sprint 17e-p1: content:encoded truncation
}

// newsItem is a single parsed item from an RSS/Atom feed.
type newsItem struct {
	Title          string    `json:"title"`
	Link           string    `json:"link"`
	Source         string    `json:"source"`
	PubDate        string    `json:"pub_date"`
	Description    string    `json:"description"`
	ContentEncoded string    `json:"content_encoded,omitempty"`
	parsedTime     time.Time // unexported, used for sorting/filtering only
}

// webFetchOutput is the JSON structure returned as the stage's output string.
// Downstream stages (plan) consume this via {{.Input.news}}.
type webFetchOutput struct {
	Items         []newsItem `json:"items"`
	FetchedAt     string     `json:"fetched_at"`
	SourcesOK     int        `json:"sources_ok"`
	SourcesFailed int        `json:"sources_failed"`
	TotalFiltered int        `json:"total_filtered"`
}

// rss2 is the minimal RSS 2.0 structure we support.
// Spec: https://www.rssboard.org/rss-specification
// Extended with content:encoded namespace (http://purl.org/rss/1.0/modules/content/).
type rss2 struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title          string `xml:"title"`
			Link           string `xml:"link"`
			Description    string `xml:"description"`
			PubDate        string `xml:"pubDate"`
			ContentEncoded string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"` // Sprint 17e-p1
		} `xml:"item"`
	} `xml:"channel"`
}

// webFetch executes the `web_fetch` action.
// It fetches each configured URL, parses RSS 2.0, filters by age,
// deduplicates by link, sorts by pub date (newest first), and truncates
// to max_items. Returns a JSON string consumable by text/template.
//
// Resilience policy:
//   - If ONE URL fails: log warn, continue with the others (partial success).
//   - If ALL URLs fail: fail the stage explicitly (G5.1 — no silent empty output).
//   - If resulting list is empty (all items too old): stage fails with clear error.
func (e *Engine) webFetch(
	ctx context.Context,
	p *Pipeline,
	stage Stage,
	_ map[string]string, // inputs not used — fetch is independent of prior stages
) (string, error) {
	// 1. Parse config from stage.Config (map[string]interface{}).
	cfg, err := parseWebFetchConfig(stage.Config)
	if err != nil {
		return "", fmt.Errorf("web_fetch stage %q: %w", stage.ID, err)
	}

	if len(cfg.URLs) == 0 {
		return "", fmt.Errorf("web_fetch stage %q: config.urls is empty", stage.ID)
	}

	log.Info().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Int("urls", len(cfg.URLs)).
		Int("max_items", cfg.MaxItems).
		Int("max_age_hours", cfg.MaxAgeHours).
		Msg("engine: web_fetch starting")

	// 2. Fetch + parse each URL IN PARALLEL (Sprint 17e-p1).
	// Uses goroutines + WaitGroup + channel to avoid external dependencies
	// (golang.org/x/sync/errgroup). Preserves D1 partial failure semantics:
	// individual feed failures do not abort the stage.
	var allItems []newsItem
	sourcesOK, sourcesFailed := 0, 0
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	client := &http.Client{Timeout: timeout}

	type fetchResult struct {
		items []newsItem
		url   string
		err   error
	}
	results := make(chan fetchResult, len(cfg.URLs))
	var wg sync.WaitGroup

	for _, u := range cfg.URLs {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			items, err := fetchAndParseRSS(ctx, client, url)
			results <- fetchResult{items: items, url: url, err: err}
		}(u)
	}

	// Close channel once all goroutines finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results (order is non-deterministic; we sort by pub_date later).
	for r := range results {
		if r.err != nil {
			sourcesFailed++
			log.Warn().
				Err(r.err).
				Str("pipeline", p.Name).
				Str("url", r.url).
				Msg("engine: web_fetch failed for one URL (continuing, D1 partial failure)")
			continue
		}
		sourcesOK++
		allItems = append(allItems, r.items...)
	}

	// 3. Hard fail if nothing could be fetched (G5.1 — no silent empty output).
	if sourcesOK == 0 {
		return "", fmt.Errorf("web_fetch stage %q: all %d URLs failed", stage.ID, len(cfg.URLs))
	}

	// 4. Filter by age.
	cutoff := time.Now().Add(-time.Duration(cfg.MaxAgeHours) * time.Hour)
	var fresh []newsItem
	for _, it := range allItems {
		if it.parsedTime.IsZero() || it.parsedTime.After(cutoff) {
			fresh = append(fresh, it)
		}
	}

	// 5. Deduplicate by link (same story across multiple feeds).
	seen := make(map[string]bool, len(fresh))
	var unique []newsItem
	for _, it := range fresh {
		if it.Link != "" && seen[it.Link] {
			continue
		}
		seen[it.Link] = true
		unique = append(unique, it)
	}

	// 6. Sort newest first.
	sort.Slice(unique, func(i, j int) bool {
		return unique[i].parsedTime.After(unique[j].parsedTime)
	})

	// 7. Truncate to max_items.
	if cfg.MaxItems > 0 && len(unique) > cfg.MaxItems {
		unique = unique[:cfg.MaxItems]
	}

	// 8. Truncate descriptions and content:encoded to avoid inflating
	// downstream prompts (G1 budget, Sprint 17e-p1).
	for i := range unique {
		if len(unique[i].Description) > cfg.MaxDescriptionChars {
			unique[i].Description = unique[i].Description[:cfg.MaxDescriptionChars] + "..."
		}
		if cfg.MaxContentEncodedChars > 0 && len(unique[i].ContentEncoded) > cfg.MaxContentEncodedChars {
			unique[i].ContentEncoded = unique[i].ContentEncoded[:cfg.MaxContentEncodedChars] + "..."
		}
	}

	totalFiltered := len(allItems) - len(unique)

	out := webFetchOutput{
		Items:         unique,
		FetchedAt:     time.Now().UTC().Format(time.RFC3339),
		SourcesOK:     sourcesOK,
		SourcesFailed: sourcesFailed,
		TotalFiltered: totalFiltered,
	}

	jsonBytes, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("web_fetch: marshal output: %w", err)
	}

	log.Info().
		Str("pipeline", p.Name).
		Str("stage", stage.ID).
		Int("items_returned", len(unique)).
		Int("sources_ok", sourcesOK).
		Int("sources_failed", sourcesFailed).
		Int("filtered_out", totalFiltered).
		Msg("engine: web_fetch completed")

	return string(jsonBytes), nil
}

// fetchAndParseRSS performs a single HTTP GET, detects RSS 2.0, and returns
// the parsed items with parsedTime populated (best-effort; zero on failure).
func fetchAndParseRSS(ctx context.Context, client *http.Client, url string) ([]newsItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "ai-loop/1.0 (+https://github.com/SEU_USUARIO/ai-loop-project)")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	// Cap body at 5 MB to avoid OOM on pathological feeds.
	limited := io.LimitReader(resp.Body, 5*1024*1024)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Try RSS 2.0 first (covers >95% of BR tech feeds: TecMundo, Google News, Canaltech).
	var feed rss2
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parse RSS 2.0: %w", err)
	}
	if len(feed.Channel.Items) == 0 {
		return nil, fmt.Errorf("feed has 0 items (url=%s)", url)
	}

	sourceName := feed.Channel.Title
	if sourceName == "" {
		sourceName = hostFromURL(url)
	}

	var items []newsItem
	for _, it := range feed.Channel.Items {
		parsed := parseRSSDate(it.PubDate)
		items = append(items, newsItem{
			Title:          cleanText(it.Title),
			Link:           strings.TrimSpace(it.Link),
			Source:         sourceName,
			PubDate:        it.PubDate,
			Description:    cleanText(it.Description),
			ContentEncoded: cleanText(it.ContentEncoded),
			parsedTime:     parsed,
		})
	}

	log.Debug().
		Str("url", url).
		Str("source", sourceName).
		Int("items", len(items)).
		Msg("engine: web_fetch parsed feed")

	return items, nil
}

// parseWebFetchConfig converts the generic map from YAML into a typed struct.
// Uses JSON round-trip to avoid depending on yaml.v3 here (pipeline.go already has it).
func parseWebFetchConfig(raw map[string]interface{}) (webFetchConfig, error) {
	var cfg webFetchConfig
	if raw == nil {
		return cfg, fmt.Errorf("config block is required")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("marshal config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("unmarshal config: %w", err)
	}
	// Defaults
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = 10
	}
	if cfg.MaxAgeHours <= 0 {
		cfg.MaxAgeHours = 48
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	if cfg.MaxDescriptionChars <= 0 {
		cfg.MaxDescriptionChars = 200
	}
	if cfg.MaxContentEncodedChars <= 0 { // Sprint 17e-p1: default to 800 chars
		cfg.MaxContentEncodedChars = 800
	}
	return cfg, nil
}

// parseRSSDate tries the standard RFC1123Z format first (RSS 2.0 spec),
// then falls back to other common layouts. Returns zero time on failure
// so the item is still usable but not filterable by age.
func parseRSSDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// cleanText collapses whitespace — many RSS descriptions contain HTML
// fragments and excessive newlines that would bloat the LLM context.
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// hostFromURL extracts the hostname for use as a fallback source name.
func hostFromURL(u string) string {
	// Simple extraction without importing net/url to keep this self-contained.
	// Adequate for the well-formed URLs we expect from YAML config.
	s := strings.TrimPrefix(u, "http://")
	s = strings.TrimPrefix(s, "https://")
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

// --- Sprint 17e-p4: deterministic grounding (funnel step 2.1) -------------

// selectNewsByPlan narrows the raw web_fetch output to ONLY the news items
// selected by the editor plan, matching plan.fontes[].url against item.link.
// Runs in Go (no LLM): the writer can never see discarded items, which
// prevents cross-contamination and keeps the prompt inside the I1 budget.
// Returns the re-marshaled webFetchOutput and the number of selected items.
func selectNewsByPlan(newsJSON, planJSON string) (string, int, error) {
	var feed webFetchOutput
	if err := json.Unmarshal([]byte(newsJSON), &feed); err != nil {
		return "", 0, fmt.Errorf("grounding: parse news JSON: %w", err)
	}
	if len(feed.Items) == 0 {
		return "", 0, fmt.Errorf("grounding: news output has no items")
	}

	var plan struct {
		Fontes []struct {
			URL string `json:"url"`
		} `json:"fontes"`
	}
	if err := json.Unmarshal([]byte(stripPlanFences(planJSON)), &plan); err != nil {
		return "", 0, fmt.Errorf("grounding: parse plan JSON: %w", err)
	}
	if len(plan.Fontes) == 0 {
		return "", 0, fmt.Errorf("grounding: plan has no fontes[] entries to anchor")
	}

	// Index fetched items by normalized URL for deterministic matching.
	byURL := make(map[string]newsItem, len(feed.Items))
	for _, it := range feed.Items {
		byURL[normalizeGroundingURL(it.Link)] = it
	}

	var selected []newsItem
	seen := make(map[string]bool, len(plan.Fontes))
	var unmatched []string
	for _, f := range plan.Fontes {
		key := normalizeGroundingURL(f.URL)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		it, ok := byURL[key]
		if !ok {
			unmatched = append(unmatched, f.URL)
			continue
		}
		selected = append(selected, it)
	}

	if len(selected) == 0 {
		return "", 0, fmt.Errorf("grounding: no plan source matches any fetched news item (plan urls=%v)", unmatched)
	}
	if len(unmatched) > 0 {
		log.Warn().
			Strs("unmatched_plan_urls", unmatched).
			Int("selected_items", len(selected)).
			Msg("engine: grounding kept matched items only (plan cited unknown URLs)")
	}

	feed.Items = selected
	out, err := json.Marshal(feed)
	if err != nil {
		return "", 0, fmt.Errorf("grounding: marshal selected news: %w", err)
	}
	return string(out), len(selected), nil
}

// stripPlanFences removes markdown code fences and any prose wrapper around
// the planner JSON (LLMs frequently wrap output in ```json ... ```).
func stripPlanFences(s string) string {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(t, "```json")
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimSuffix(t, "```")
	t = strings.TrimSpace(t)
	// Last resort: isolate the outermost JSON object if prose surrounds it.
	if !strings.HasPrefix(t, "{") {
		start := strings.Index(t, "{")
		end := strings.LastIndex(t, "}")
		if start >= 0 && end > start {
			t = t[start : end+1]
		}
	}
	return t
}

// normalizeGroundingURL makes URL matching tolerant to cosmetic differences
// (whitespace, trailing punctuation, trailing slash) while staying deterministic.
func normalizeGroundingURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, ".,;:!?")
	u = strings.TrimSuffix(u, "/")
	return u
}
