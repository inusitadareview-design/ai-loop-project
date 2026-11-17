// git_publish.go implements the git_publish action (Sprint 17 increment 17a).
// It publishes pipeline output to a static site repository by:
//  1. Creating a new branch from the configured base branch
//  2. Committing the formatted content as a markdown file
//  3. Opening a pull request for human approval (G3.2)
//
// Human approval = merging the PR. Rejection = closing the PR.
// This maps naturally to G3.2 without requiring any new approval mechanism.
//
// Decision tree: D11 from ARCHITECTURE.md
// Guardrails enforced:
//   - G2.1: GitHub PAT comes from env var, never hardcoded
//   - G2.2: no file writes outside data/
//   - G3.2: always opens a PR (human approval = merge)
//   - G5.1: all errors wrapped with fmt.Errorf
package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

// githubClient wraps HTTP calls to the GitHub REST API v3.
// Unexported: internal to this package.
type githubClient struct {
	token   string
	baseURL string // default: https://api.github.com
	client  *http.Client
}

// gitPublishConfig is the typed version of stage.Config for git_publish.
// Sprint 17 17a: per-pipeline publishing configuration.
type gitPublishConfig struct {
	Repo         string   // e.g. "owner/repo"
	BaseBranch   string   // e.g. "main"
	BranchPrefix string   // e.g. "ailoop/"
	FilePath     string   // e.g. "src/content/blog/news-{{.Date}}.md"
	Labels       []string // e.g. ["ai-draft", "needs-review"]
	PATEnvVar    string   // e.g. "AILOOP_GITHUB_PAT" (G2.1)
}

// gitPublish is the entrypoint for action=git_publish.
// It reads config from the stage, validates it, formats the content,
// and creates a PR on GitHub. Returns the PR URL on success.
//
// D11: 4xx errors fail fast (no retry). 5xx/network errors retry up to 3x.
func (e *Engine) gitPublish(
	ctx context.Context,
	p *Pipeline,
	stage Stage,
	inputs map[string]string,
) (string, error) {
	// 1. Parse and validate config.
	cfg, err := parseGitPublishConfig(stage.Config)
	if err != nil {
		return "", fmt.Errorf("git_publish: invalid config: %w", err)
	}

	// 2. Read GitHub PAT from env var (G2.1 — never hardcoded).
	pat := os.Getenv(cfg.PATEnvVar)
	if pat == "" {
		return "", fmt.Errorf("git_publish: env var %q not set (required for GitHub API access, G2.1)", cfg.PATEnvVar)
	}

	// 3. Pick the input to publish.
	content, err := e.pickPublishInput(stage, inputs)
	if err != nil {
		return "", fmt.Errorf("git_publish: %w", err)
	}

	// 4. Extract title from frontmatter and generate slug (Option B).
	title, err := extractTitleFromFrontmatter(content)
	if err != nil {
		return "", fmt.Errorf("git_publish: %w", err)
	}

	// Sprint 17d: timestamp completo para evitar colisão (2 posts/dia)
	now := time.Now()
	date := now.Format("2006-01-02")
	timestamp := now.Format("2006-01-02-150405") // YYYY-MM-DD-HHMMSS
	slug := generateSlugWithDate(title, date)

	// 5. Render branch name and file path using the slug.
	metadata := map[string]string{
		"Pipeline":  p.Name,
		"Date":      date,
		"Timestamp": timestamp, // NOVO: timestamp completo
		"StageID":   stage.ID,
		"Slug":      slug,
	}
	// Sprint 17d: branch usa timestamp completo (não mais só data)
	branchName := renderTemplateString(cfg.BranchPrefix+"{{.Timestamp}}", metadata)
	filePath := renderTemplateString(cfg.FilePath, metadata)

	// 6. Call GitHub API (with retry for transient errors).
	gh := &githubClient{
		token:   pat,
		baseURL: "https://api.github.com",
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	prURL, err := e.publishToGitHub(ctx, gh, cfg, branchName, filePath, content)
	if err != nil {
		return "", fmt.Errorf("git_publish: %w", err)
	}

	log.Info().
		Str("pipeline", p.Name).
		Str("pr_url", prURL).
		Msg("engine: git_publish PR created — awaiting human approval via merge (G3.2)")

	return fmt.Sprintf("pr_created:%s", prURL), nil
}

// publishToGitHub executes the D11 flow: get base SHA → create branch →
// commit file → open PR. Retries up to 3 times on 5xx/network errors.
// 4xx errors fail immediately (D11: client errors are permanent).
func (e *Engine) publishToGitHub(
	ctx context.Context,
	gh *githubClient,
	cfg gitPublishConfig,
	branchName, filePath, content string,
) (string, error) {
	// D11: retry up to 3 attempts for transient errors.
	const maxAttempts = 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("context cancelled: %w", err)
		}

		// Step 1: Get current SHA of base branch.
		baseSHA, err := gh.getBranchSHA(ctx, cfg.Repo, cfg.BaseBranch)
		if err != nil {
			if isClientError(err) {
				return "", err // D11: 4xx fail-fast
			}
			lastErr = err
			log.Warn().
				Err(err).
				Int("attempt", attempt).
				Msg("engine: git_publish transient error getting base SHA, will retry")
			time.Sleep(backoffDuration(attempt))
			continue
		}

		// Step 2: Create branch from base SHA.
		if err := gh.createBranch(ctx, cfg.Repo, branchName, baseSHA); err != nil {
			if isClientError(err) {
				// 422 Unprocessable Entity = branch already exists.
				// Per D11: update existing PR instead of duplicating.
				if strings.Contains(err.Error(), "422") {
					log.Info().
						Str("branch", branchName).
						Msg("engine: branch already exists, will update existing PR")
				} else {
					return "", err
				}
			} else {
				lastErr = err
				log.Warn().
					Err(err).
					Int("attempt", attempt).
					Msg("engine: git_publish transient error creating branch, will retry")
				time.Sleep(backoffDuration(attempt))
				continue
			}
		}

		// Step 3: Commit file to branch.
		if err := gh.createOrUpdateFile(ctx, cfg.Repo, branchName, filePath, content); err != nil {
			if isClientError(err) {
				return "", err // D11: 4xx fail-fast
			}
			lastErr = err
			log.Warn().
				Err(err).
				Int("attempt", attempt).
				Msg("engine: git_publish transient error committing file, will retry")
			time.Sleep(backoffDuration(attempt))
			continue
		}

		// Step 4: Open (or find existing) PR.
		prURL, err := gh.createOrFindPR(ctx, cfg.Repo, cfg.BaseBranch, branchName, cfg.Labels)
		if err != nil {
			if isClientError(err) {
				return "", err // D11: 4xx fail-fast
			}
			lastErr = err
			log.Warn().
				Err(err).
				Int("attempt", attempt).
				Msg("engine: git_publish transient error opening PR, will retry")
			time.Sleep(backoffDuration(attempt))
			continue
		}

		return prURL, nil
	}

	return "", fmt.Errorf("all %d attempts failed: %w", maxAttempts, lastErr)
}

// pickPublishInput selects the content to publish using the same logic
// as pickReviewInput: explicit InputKeys first, then implicit.
func (e *Engine) pickPublishInput(stage Stage, inputs map[string]string) (string, error) {
	if len(stage.InputKeys) > 0 {
		for _, key := range stage.InputKeys {
			if val, ok := inputs[key]; ok {
				return val, nil
			}
		}
		return "", fmt.Errorf("no input found for declared keys %v", stage.InputKeys)
	}
	for _, val := range inputs {
		if val != "" {
			return val, nil
		}
	}
	return "", fmt.Errorf("no input found for publishing")
}

// parseGitPublishConfig validates and converts the untyped stage.Config
// into a typed gitPublishConfig. Returns an error if any required field
// is missing or malformed.
func parseGitPublishConfig(cfg map[string]interface{}) (gitPublishConfig, error) {
	if cfg == nil {
		return gitPublishConfig{}, fmt.Errorf("config block is required for git_publish action")
	}

	getString := func(key string) (string, error) {
		v, ok := cfg[key]
		if !ok {
			return "", fmt.Errorf("missing required field %q", key)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("field %q must be a string", key)
		}
		return s, nil
	}

	repo, err := getString("repo")
	if err != nil {
		return gitPublishConfig{}, err
	}
	// Validate repo format: owner/name (G2.2: no path traversal).
	if !regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`).MatchString(repo) {
		return gitPublishConfig{}, fmt.Errorf("invalid repo format %q (expected owner/name)", repo)
	}

	baseBranch, err := getString("base_branch")
	if err != nil {
		return gitPublishConfig{}, err
	}

	branchPrefix, _ := getString("branch_prefix")
	if branchPrefix == "" {
		branchPrefix = "ailoop/"
	}

	filePath, err := getString("file_path")
	if err != nil {
		return gitPublishConfig{}, err
	}

	patEnv, err := getString("pat_env")
	if err != nil {
		return gitPublishConfig{}, err
	}

	// Labels (optional).
	var labels []string
	if v, ok := cfg["labels"]; ok {
		if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok {
					labels = append(labels, s)
				}
			}
		}
	}

	return gitPublishConfig{
		Repo:         repo,
		BaseBranch:   baseBranch,
		BranchPrefix: branchPrefix,
		FilePath:     filePath,
		Labels:       labels,
		PATEnvVar:    patEnv,
	}, nil
}

// renderTemplateString is a minimal {{.Key}} substitution for branch
// names and file paths. Does NOT use text/template to keep the surface
// small and avoid code-fence injection risks (G2.2).
func renderTemplateString(tmpl string, data map[string]string) string {
	result := tmpl
	for k, v := range data {
		result = strings.ReplaceAll(result, "{{."+k+"}}", v)
	}
	return result
}

// isClientError returns true if the error represents a 4xx response
// (D11: fail fast, no retry).
func isClientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "400") ||
		strings.Contains(msg, "401") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "404") ||
		strings.Contains(msg, "422")
}

// ---------------------------------------------------------------------------
// GitHub API methods
// ---------------------------------------------------------------------------

// githubError represents an error response from the GitHub API.
type githubError struct {
	StatusCode int
	Message    string
}

func (e *githubError) Error() string {
	return fmt.Sprintf("github API error %d: %s", e.StatusCode, e.Message)
}

// doRequest performs an authenticated HTTP request to GitHub.
// The PAT is passed via the Authorization header (masked in logs via tracer, G2.1).
func (gh *githubClient) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	url := gh.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build github request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+gh.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "ai-loop/git-publish")

	resp, err := gh.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read github response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &githubError{
			StatusCode: resp.StatusCode,
			Message:    truncate(string(respBody), 300),
		}
	}

	return respBody, nil
}

// getBranchSHA returns the SHA of the tip of the given branch.
// Used as the starting point for the new branch.
func (gh *githubClient) getBranchSHA(ctx context.Context, repo, branch string) (string, error) {
	path := fmt.Sprintf("/repos/%s/git/ref/heads/%s", repo, branch)
	body, err := gh.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return "", err
	}

	var resp struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse branch ref: %w", err)
	}
	return resp.Object.SHA, nil
}

// createBranch creates a new branch pointing at the given SHA.
func (gh *githubClient) createBranch(ctx context.Context, repo, branch, sha string) error {
	path := fmt.Sprintf("/repos/%s/git/refs", repo)
	body := map[string]interface{}{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}
	_, err := gh.doRequest(ctx, "POST", path, body)
	return err
}

// createOrUpdateFile creates or updates a file on the given branch.
// Uses base64-encoded content as required by the Contents API.
func (gh *githubClient) createOrUpdateFile(ctx context.Context, repo, branch, path, content string) error {
	apiPath := fmt.Sprintf("/repos/%s/contents/%s", repo, path)

	// Try GET first to find existing file SHA (needed for updates).
	existingBody, _ := gh.doRequest(ctx, "GET", apiPath+"?ref="+branch, nil)

	reqBody := map[string]interface{}{
		"message": fmt.Sprintf("feat(post): %s", path),
		"content": encodeBase64(content),
		"branch":  branch,
	}

	if existingBody != nil {
		var existing struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(existingBody, &existing); err == nil && existing.SHA != "" {
			reqBody["sha"] = existing.SHA
		}
	}

	_, err := gh.doRequest(ctx, "PUT", apiPath, reqBody)
	return err
}

// createOrFindPR opens a PR from branch to baseBranch. If a PR already
// exists for this branch, returns its URL instead of creating a duplicate.
func (gh *githubClient) createOrFindPR(ctx context.Context, repo, baseBranch, branch string, labels []string) (string, error) {
	// First, check if a PR already exists for this branch.
	searchPath := fmt.Sprintf("/repos/%s/pulls?head=%s&state=open",
		repo, repo[:strings.Index(repo, "/")]+":"+branch)
	searchBody, err := gh.doRequest(ctx, "GET", searchPath, nil)
	if err == nil {
		var existing []struct {
			HTMLURL string `json:"html_url"`
		}
		if err := json.Unmarshal(searchBody, &existing); err == nil && len(existing) > 0 {
			// PR already exists — reuse it (D11: never duplicate).
			return existing[0].HTMLURL, nil
		}
	}

	// No existing PR — create a new one.
	path := fmt.Sprintf("/repos/%s/pulls", repo)
	body := map[string]interface{}{
		"title": fmt.Sprintf("feat(post): AI-generated content from %s", branch),
		"head":  branch,
		"base":  baseBranch,
		"body":  "This post was generated automatically by the AI Loop pipeline.\n\nReview the content below and merge to publish, or close this PR to reject.",
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}

	respBody, err := gh.doRequest(ctx, "POST", path, body)
	if err != nil {
		return "", err
	}

	var resp struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse PR response: %w", err)
	}
	return resp.HTMLURL, nil
}

// encodeBase64 encodes s as base64 (required by GitHub Contents API).
// Imported here to avoid adding a top-level dependency in the imports list
// just for this one usage.
func encodeBase64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// extractTitleFromFrontmatter parses the YAML frontmatter block from
// a markdown file and returns the value of the "title" field.
//
// Expects content in the format:
//
//	---
//	title: "Some Title"
//	...
//	---
//
//	Body content here...
//
// Returns an error if the frontmatter block is missing or the title field
// is absent/empty.
func extractTitleFromFrontmatter(content string) (string, error) {
	// Frontmatter is bounded by "---" at start and end.
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid frontmatter: expected YAML between --- markers")
	}

	yamlBlock := parts[1]
	var fm struct {
		Title string `yaml:"title"`
	}
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return "", fmt.Errorf("parse frontmatter YAML: %w", err)
	}

	if fm.Title == "" {
		return "", fmt.Errorf("frontmatter missing required field: title")
	}

	return fm.Title, nil
}
