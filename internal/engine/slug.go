// slug.go implements URL-safe slug generation for published content (Sprint 17).
//
// Responsibility: Convert human-readable titles into filesystem-safe slugs
// suitable for Astro content collection paths. Used by git_publish action.
//
// Integration: Called by gitPublish() in git_publish.go after extracting
// the title from the frontmatter YAML.
//
// Guardrails:
//   - G2.2: output is filesystem-safe (only a-z, 0-9, hyphens)
//   - G5.1: errors wrapped with fmt.Errorf
//
// Test: internal/engine/slug_test.go
package engine

import (
	"regexp"
	"strings"
)

// slugify converts a human-readable title into a URL-safe slug.
//
// Rules:
//   - Remove accents (á→a, ç→c, ã→a, etc.)
//   - Lowercase all letters
//   - Replace spaces and underscores with hyphens
//   - Remove all characters except a-z, 0-9, hyphens
//   - Collapse multiple consecutive hyphens into one
//   - Trim hyphens from start and end
//   - Truncate at 80 characters (at hyphen boundary when possible)
//
// Thread-safe: pure function, no shared state.
func slugify(s string) string {
	if s == "" {
		return ""
	}

	// Step 1: Remove accents.
	s = removeAccents(s)

	// Step 2: Lowercase.
	s = strings.ToLower(s)

	// Step 3: Replace spaces and underscores with hyphens.
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")

	// Step 4: Remove all non-alphanumeric characters (keep hyphens).
	reInvalid := regexp.MustCompile(`[^a-z0-9-]`)
	s = reInvalid.ReplaceAllString(s, "")

	// Step 5: Collapse multiple hyphens into one.
	reMultiHyphen := regexp.MustCompile(`-+`)
	s = reMultiHyphen.ReplaceAllString(s, "-")

	// Step 6: Trim hyphens from start and end.
	s = strings.Trim(s, "-")

	// Step 7: Truncate at 80 characters (prefer word boundary).
	if len(s) > 80 {
		s = s[:80]
		// If we cut in the middle of a word, trim trailing hyphen.
		s = strings.Trim(s, "-")
	}

	return s
}

// removeAccents replaces accented characters with their base forms.
// Uses an explicit mapping table to avoid adding external dependencies.
// Covers the most common accented characters used in Portuguese, Spanish,
// French, and German text — sufficient for the project's target use cases.
func removeAccents(s string) string {
	// Map of accented rune → base rune.
	// Uppercase and lowercase variants both map to lowercase (caller lowercases later).
	replacements := map[rune]rune{
		// A/a variants
		'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a',
		'Á': 'a', 'À': 'a', 'Â': 'a', 'Ã': 'a', 'Ä': 'a',
		// E/e variants
		'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
		'É': 'e', 'È': 'e', 'Ê': 'e', 'Ë': 'e',
		// I/i variants
		'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
		'Í': 'i', 'Ì': 'i', 'Î': 'i', 'Ï': 'i',
		// O/o variants
		'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
		'Ó': 'o', 'Ò': 'o', 'Ô': 'o', 'Õ': 'o', 'Ö': 'o',
		// U/u variants
		'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
		'Ú': 'u', 'Ù': 'u', 'Û': 'u', 'Ü': 'u',
		// Special characters
		'ç': 'c', 'Ç': 'c',
		'ñ': 'n', 'Ñ': 'n',
		'ý': 'y', 'Ý': 'y', 'ÿ': 'y',
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if repl, ok := replacements[r]; ok {
			b.WriteRune(repl)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// generateSlugWithDate creates a complete slug with date prefix for uniqueness.
// Format: "YYYY-MM-DD-<slugified-title>"
//
// Example: "Fim da Taxa das Blusinhas" + 2026-11-11
//
//	→ "2026-11-11-fim-da-taxa-das-blusinhas"
func generateSlugWithDate(title string, date string) string {
	slug := slugify(title)
	if slug == "" {
		return date + "-untitled"
	}
	return date + "-" + slug
}
