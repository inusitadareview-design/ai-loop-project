// output_contract.go — deterministic output quality gate for generate stages.
//
// Sprint 17e-p3: prompts are probabilistic; contracts are deterministic.
// This mirrors invariant I1 (token pre-validation) on the OUTPUT side:
// instead of hoping the model respects a length/structure instruction,
// the engine measures the result and reports every violation.
//
// NOT YET WIRED into realGenerate — parsing returns enabled=false when the
// stage has no output_contract config, so behaviour is unchanged until the
// refinement loop lands in a separate increment (R1).
//
// Guardrails: G5.1 (explicit errors, never panic), I2 (measurable violations).
package engine

import (
	"encoding/json"
	"fmt"
	"strings"
)

// outputContract is the declarative quality gate for a generate stage.
// All fields are optional; zero values disable the corresponding check.
type outputContract struct {
	MinWords                int      `json:"min_words" yaml:"min_words"`
	MaxWords                int      `json:"max_words" yaml:"max_words"`
	TLDRBullets             int      `json:"tldr_bullets" yaml:"tldr_bullets"`
	MinParagraphsPerSection int      `json:"min_paragraphs_per_section" yaml:"min_paragraphs_per_section"`
	RequiredSections        []string `json:"required_sections" yaml:"required_sections"`
}

// parseOutputContract extracts the output_contract block from stage.Config.
// Returns (contract, enabled, error). enabled=false means "no contract
// configured" — the caller must skip validation entirely.
func parseOutputContract(raw map[string]interface{}) (outputContract, bool, error) {
	var c outputContract
	if raw == nil {
		return c, false, nil
	}
	block, ok := raw["output_contract"]
	if !ok || block == nil {
		return c, false, nil
	}
	b, err := json.Marshal(block)
	if err != nil {
		return c, false, fmt.Errorf("output_contract: marshal config: %w", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, false, fmt.Errorf("output_contract: unmarshal config: %w", err)
	}
	if c.MinWords < 0 || c.MaxWords < 0 {
		return c, false, fmt.Errorf("output_contract: word bounds must be >= 0")
	}
	if c.MaxWords > 0 && c.MinWords > c.MaxWords {
		return c, false, fmt.Errorf("output_contract: min_words (%d) > max_words (%d)", c.MinWords, c.MaxWords)
	}
	return c, true, nil
}

// validate measures the article against the contract and returns EVERY
// violation found, so a refinement prompt can ask the model to fix all of
// them in a single retry (I3 refinement cycle). Empty slice = contract met.
func (c outputContract) validate(article string) []string {
	var violations []string
	body := ocStripFrontmatter(article)

	if c.MinWords > 0 || c.MaxWords > 0 {
		n := len(strings.Fields(body))
		if c.MinWords > 0 && n < c.MinWords {
			violations = append(violations,
				fmt.Sprintf("body has %d words, minimum is %d", n, c.MinWords))
		}
		if c.MaxWords > 0 && n > c.MaxWords {
			violations = append(violations,
				fmt.Sprintf("body has %d words, maximum is %d", n, c.MaxWords))
		}
	}

	for _, heading := range c.RequiredSections {
		if !strings.Contains(body, heading) {
			violations = append(violations,
				fmt.Sprintf("missing required section %q", heading))
			continue
		}
		if c.MinParagraphsPerSection > 0 && heading != "## TL;DR" && heading != "## Fontes" {
			n := ocCountParagraphs(ocSectionBody(body, heading))
			if n < c.MinParagraphsPerSection {
				violations = append(violations,
					fmt.Sprintf("section %q has %d paragraph(s), minimum is %d",
						heading, n, c.MinParagraphsPerSection))
			}
		}
	}

	if c.TLDRBullets > 0 {
		n := ocCountBullets(ocSectionBody(body, "## TL;DR"))
		if n != c.TLDRBullets {
			violations = append(violations,
				fmt.Sprintf("TL;DR has %d bullets, required is exactly %d", n, c.TLDRBullets))
		}
	}

	return violations
}

// ocStripFrontmatter removes the leading YAML frontmatter block so word
// counts measure the article body only. Returns the input unchanged when
// no frontmatter is present.
func ocStripFrontmatter(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "---") {
		return t
	}
	rest := t[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return t
	}
	return strings.TrimSpace(rest[idx+4:])
}

// ocSectionBody returns the text between the given heading and the next
// "## " heading (or end of document). Empty when the heading is absent.
func ocSectionBody(body, heading string) string {
	start := strings.Index(body, heading)
	if start < 0 {
		return ""
	}
	rest := body[start+len(heading):]
	if idx := strings.Index(rest, "\n## "); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.TrimSpace(rest)
}

// ocCountBullets counts markdown list items ("- " or "* ") in a section.
func ocCountBullets(section string) int {
	n := 0
	for _, line := range strings.Split(section, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			n++
		}
	}
	return n
}

// ocCountParagraphs counts non-empty blocks separated by blank lines,
// ignoring headings and list items (which are not prose paragraphs).
func ocCountParagraphs(section string) int {
	n := 0
	for _, block := range strings.Split(section, "\n\n") {
		t := strings.TrimSpace(block)
		if t == "" || strings.HasPrefix(t, "#") ||
			strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			continue
		}
		n++
	}
	return n
}
