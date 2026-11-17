package engine

import (
	"strings"
	"testing"
)

func TestParseOutputContract_DisabledWhenAbsent(t *testing.T) {
	_, enabled, err := parseOutputContract(map[string]interface{}{"urls": []interface{}{"x"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Fatal("enabled must be false when output_contract is absent")
	}
}

func TestParseOutputContract_RejectsInvertedBounds(t *testing.T) {
	_, _, err := parseOutputContract(map[string]interface{}{
		"output_contract": map[string]interface{}{"min_words": 900, "max_words": 600},
	})
	if err == nil {
		t.Fatal("expected error for min_words > max_words")
	}
}

func TestOutputContract_Validate(t *testing.T) {
	long := strings.Repeat("palavra ", 700)
	article := "---\ntitle: \"x\"\n---\n\n## TL;DR\n\n- a\n- b\n- c\n\n" +
		"## O que aconteceu\n\n" + long + "\n\n" + long + "\n\n" + long + "\n\n## Fontes\n\n- [S](https://s.com)\n"

	c := outputContract{
		MinWords:                600,
		MaxWords:                3000,
		TLDRBullets:             3,
		MinParagraphsPerSection: 3,
		RequiredSections:        []string{"## TL;DR", "## O que aconteceu", "## Fontes"},
	}
	if v := c.validate(article); len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}

	short := "---\ntitle: \"x\"\n---\n\n## TL;DR\n\n- a\n- b\n\n## O que aconteceu\n\nUm parágrafo só.\n"
	v := c.validate(short)
	if len(v) == 0 {
		t.Fatal("expected violations for short article")
	}
	joined := strings.Join(v, " | ")
	for _, want := range []string{"minimum is 600", "bullets", "paragraph", "missing required section"} {
		if !strings.Contains(joined, want) {
			t.Errorf("violations missing %q: %s", want, joined)
		}
	}
}

func TestOCStripFrontmatter(t *testing.T) {
	if got := ocStripFrontmatter("---\ntitle: x\n---\n\ncorpo"); got != "corpo" {
		t.Fatalf("got %q, want corpo", got)
	}
	if got := ocStripFrontmatter("sem frontmatter"); got != "sem frontmatter" {
		t.Fatalf("got %q, want unchanged", got)
	}
}
