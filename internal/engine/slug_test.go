package engine

import (
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple lowercase",
			input: "hello world",
			want:  "hello-world",
		},
		{
			name:  "with uppercase",
			input: "Hello World",
			want:  "hello-world",
		},
		{
			name:  "Portuguese accents",
			input: "Fim da Taxa das Blusinhas",
			want:  "fim-da-taxa-das-blusinhas",
		},
		{
			name:  "heavy accents (pt-BR)",
			input: "Análise: Ações & Reações em São Paulo",
			want:  "analise-acoes-reacoes-em-sao-paulo",
		},
		{
			name:  "special characters removed",
			input: "GPU RTX 5090 — Análise & Review!",
			want:  "gpu-rtx-5090-analise-review",
		},
		{
			name:  "underscores become hyphens",
			input: "hello_world_test",
			want:  "hello-world-test",
		},
		{
			name:  "multiple spaces collapse",
			input: "hello    world",
			want:  "hello-world",
		},
		{
			name:  "trailing hyphens trimmed",
			input: "hello world -",
			want:  "hello-world",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "only special chars",
			input: "!@#$%^&*()",
			want:  "",
		},
		{
			name:  "long title truncated at 80 chars",
			input: "Uma análise completa e detalhada sobre o fim da taxa das blusinhas no AliExpress e seu impacto no mercado brasileiro de importados em 2026 com todas as implicações econômicas",
			want:  "uma-analise-completa-e-detalhada-sobre-o-fim-da-taxa-das-blusinhas-no-aliexpress",
		},
		{
			name:  "cedilla and tilde",
			input: "Aço e Coração",
			want:  "aco-e-coracao",
		},
		{
			name:  "numbers preserved",
			input: "GPU RTX 5090 Ti Review 2026",
			want:  "gpu-rtx-5090-ti-review-2026",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := slugify(c.input)
			if got != c.want {
				t.Errorf("slugify(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestGenerateSlugWithDate(t *testing.T) {
	cases := []struct {
		name  string
		title string
		date  string
		want  string
	}{
		{
			name:  "normal case",
			title: "Fim da Taxa",
			date:  "2026-11-11",
			want:  "2026-11-11-fim-da-taxa",
		},
		{
			name:  "empty title fallback",
			title: "",
			date:  "2026-11-11",
			want:  "2026-11-11-untitled",
		},
		{
			name:  "accented title",
			title: "Análise Completa",
			date:  "2026-01-15",
			want:  "2026-01-15-analise-completa",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := generateSlugWithDate(c.title, c.date)
			if got != c.want {
				t.Errorf("generateSlugWithDate(%q, %q) = %q, want %q",
					c.title, c.date, got, c.want)
			}
		})
	}
}
