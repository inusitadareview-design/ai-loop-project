package engine

import (
	"fmt"
	"testing"
)

func TestParseGitPublishConfig_Valid(t *testing.T) {
	cfg := map[string]interface{}{
		"repo":        "owner/repo",
		"base_branch": "main",
		"file_path":   "src/posts/{{.Date}}.md",
		"pat_env":     "AILOOP_GITHUB_PAT",
	}
	parsed, err := parseGitPublishConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want owner/repo", parsed.Repo)
	}
	if parsed.BranchPrefix != "ailoop/" {
		t.Errorf("BranchPrefix default = %q, want ailoop/", parsed.BranchPrefix)
	}
}

func TestParseGitPublishConfig_InvalidRepo(t *testing.T) {
	cfg := map[string]interface{}{
		"repo":        "../../etc/passwd", // G2.2: path traversal attempt
		"base_branch": "main",
		"file_path":   "x.md",
		"pat_env":     "FOO",
	}
	_, err := parseGitPublishConfig(cfg)
	if err == nil {
		t.Fatal("expected error for path-traversal repo")
	}
}

func TestRenderTemplateString(t *testing.T) {
	got := renderTemplateString("ailoop/news-{{.Date}}", map[string]string{
		"Date": "2026-11-11",
	})
	want := "ailoop/news-2026-11-11"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIsClientError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "401 unauthorized",
			err:  fmt.Errorf("github API error 401: bad token"),
			want: true,
		},
		{
			name: "500 server error (not client)",
			err:  fmt.Errorf("github API error 500: server down"),
			want: false,
		},
		{
			name: "network timeout (not client)",
			err:  fmt.Errorf("network timeout"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "422 unprocessable",
			err:  fmt.Errorf("github API error 422: validation failed"),
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isClientError(c.err)
			if got != c.want {
				t.Errorf("isClientError() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestExtractTitleFromFrontmatter(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{
			name: "valid frontmatter",
			content: `---
title: "Fim da Taxa das Blusinhas"
description: "teste"
pubDate: 2026-11-11
---

Corpo do post aqui.`,
			want:    "Fim da Taxa das Blusinhas",
			wantErr: false,
		},
		{
			name: "title with accents",
			content: `---
title: "Análise Completa: Ações & Reações"
---

Corpo.`,
			want:    "Análise Completa: Ações & Reações",
			wantErr: false,
		},
		{
			name:    "missing frontmatter markers",
			content: "Sem frontmatter aqui",
			wantErr: true,
		},
		{
			name: "empty title",
			content: `---
description: "sem título"
---

Corpo.`,
			wantErr: true,
		},
		{
			name: "invalid YAML",
			content: `---
title: [invalid yaml
---

Corpo.`,
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extractTitleFromFrontmatter(c.content)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("extractTitleFromFrontmatter() = %q, want %q", got, c.want)
			}
		})
	}
}
