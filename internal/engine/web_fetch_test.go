package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// --- Sprint 17e-p4: deterministic grounding tests -------------------------

const groundingNewsFixture = `{"items":[{"title":"AMD TOP500 story","link":"https://example.com/amd-top500","source":"Test Source","description":"AMD reached 40% of TOP500 with 202 systems."},{"title":"Unrelated game promo","link":"https://example.com/game-promo","source":"Other","description":"A game promotion unrelated to the selected story."}],"fetched_at":"2026-11-17T00:00:00Z","sources_ok":2,"sources_failed":0,"total_filtered":0}`

func TestSelectNewsByPlan(t *testing.T) {
	cases := []struct {
		name      string
		news      string
		plan      string
		wantErr   string
		wantCount int
		wantIn    string
		wantNotIn string
	}{
		{
			name:      "exact match with fenced plan",
			news:      groundingNewsFixture,
			plan:      "```json\n{\"fontes\":[{\"url\":\"https://example.com/amd-top500\"}]}\n```",
			wantCount: 1,
			wantIn:    "amd-top500",
			wantNotIn: "game-promo",
		},
		{
			name:      "trailing slash normalized",
			news:      groundingNewsFixture,
			plan:      `{"fontes":[{"url":"https://example.com/amd-top500/"}]}`,
			wantCount: 1,
			wantIn:    "amd-top500",
			wantNotIn: "game-promo",
		},
		{
			name:      "prose-wrapped plan isolated",
			news:      groundingNewsFixture,
			plan:      "Here is the plan:\n{\"fontes\":[{\"url\":\"https://example.com/amd-top500\"}]}\nDone.",
			wantCount: 1,
			wantIn:    "amd-top500",
			wantNotIn: "game-promo",
		},
		{
			name:      "duplicate fonte urls deduplicated",
			news:      groundingNewsFixture,
			plan:      `{"fontes":[{"url":"https://example.com/amd-top500"},{"url":"https://example.com/amd-top500/"}]}`,
			wantCount: 1,
			wantIn:    "amd-top500",
			wantNotIn: "game-promo",
		},
		{
			name:    "no match fails explicitly",
			news:    groundingNewsFixture,
			plan:    `{"fontes":[{"url":"https://example.com/unknown"}]}`,
			wantErr: "no plan source matches",
		},
		{
			name:    "malformed plan fails explicitly",
			news:    groundingNewsFixture,
			plan:    "not json at all",
			wantErr: "parse plan JSON",
		},
		{
			name:    "malformed news fails explicitly",
			news:    "{bad",
			plan:    `{"fontes":[{"url":"https://example.com/amd-top500"}]}`,
			wantErr: "parse news JSON",
		},
		{
			name:    "empty fontes fails explicitly",
			news:    groundingNewsFixture,
			plan:    `{"fontes":[]}`,
			wantErr: "no fontes",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n, err := selectNewsByPlan(c.news, c.plan)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n != c.wantCount {
				t.Errorf("selected count = %d, want %d", n, c.wantCount)
			}
			if !strings.Contains(got, c.wantIn) {
				t.Errorf("output missing selected item %q", c.wantIn)
			}
			if c.wantNotIn != "" && strings.Contains(got, c.wantNotIn) {
				t.Errorf("discarded item %q leaked into grounded news", c.wantNotIn)
			}
		})
	}
}

// Integration: the writer prompt must contain ONLY the plan-selected item.
// RED before the loop.go wiring exists (discarded item leaks into prompt).
func TestRealGenerate_GroundsNewsToPlan(t *testing.T) {
	promptCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if p, ok := req["prompt"].(string); ok {
			promptCh <- p
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]string{{
				"text": "Draft article grounded in https://example.com/amd-top500 facts.",
			}},
		})
	}))
	defer srv.Close()

	eng, _ := newTestEngineWithRealRouter(t, srv.URL)
	eng.SetConfig(&config.Config{
		Budget:    config.Budget{DailyLimitUSD: 10.0, CostPer1kInputTokens: 0.002},
		Providers: config.Providers{Default: "test-provider"},
		Routing:   config.Routing{},
	})

	p := &Pipeline{
		Name:  "grounding_e2e",
		Model: Model{Preferred: "test-model", MaxTokens: 100},
	}
	stage := Stage{ID: "write_article", Action: "generate", OutputKey: "post"}
	inputs := map[string]string{
		"news": groundingNewsFixture,
		"plan": `{"fontes":[{"url":"https://example.com/amd-top500"}]}`,
	}

	out, err := eng.realGenerate(context.Background(), p, stage, inputs)
	if err != nil {
		t.Fatalf("realGenerate: %v", err)
	}
	if !strings.Contains(out, "amd-top500") {
		t.Errorf("output = %q, want grounded draft", out)
	}

	prompt := <-promptCh
	if !strings.Contains(prompt, "amd-top500") {
		t.Error("writer prompt missing the selected source material")
	}
	if strings.Contains(prompt, "game-promo") {
		t.Error("discarded news item leaked into the writer prompt — grounding not applied")
	}
}
