package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper: creates a test environment with a mock webhook server
func newTestEnvWithWebhook(t *testing.T, webhookHandler http.HandlerFunc) (*testEnv, *httptest.Server) {
	t.Helper()
	e := newTestEnv(t)
	srv := httptest.NewServer(webhookHandler)
	t.Cleanup(srv.Close)
	return e, srv
}

// helper: writes a draft file to outputs dir and returns its path
func writeDraftFile(t *testing.T, outputsDir string, draft Draft) string {
	t.Helper()
	if err := os.MkdirAll(outputsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(draft)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	filename := draft.Pipeline + ".123.draft.json"
	path := filepath.Join(outputsDir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestApproveDraft_WithEndpointEnv_PublishesToWebhook(t *testing.T) {
	var receivedBody map[string]interface{}
	e, webhookSrv := newTestEnvWithWebhook(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	t.Setenv("TEST_BLOG_WEBHOOK", webhookSrv.URL)

	outputsDir := filepath.Join(filepath.Dir(e.cfg.Logging.File), "..", "outputs")
	writeDraftFile(t, outputsDir, Draft{
		ID:          "news_blog.123",
		Pipeline:    "news_blog",
		StageID:     "publish",
		EndpointEnv: "TEST_BLOG_WEBHOOK",
		Inputs:      map[string]string{"content": "Published content"},
	})

	req := httptest.NewRequest("POST", "/api/v1/drafts/news_blog.123/approve", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Verify webhook was called with correct payload
	payload, ok := receivedBody["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("webhook payload missing or wrong type: %v", receivedBody)
	}
	if payload["content"] != "Published content" {
		t.Errorf("webhook content = %v, want 'Published content'", payload["content"])
	}

	// Verify draft file was renamed to .published.json
	publishedPath := filepath.Join(outputsDir, "news_blog.123.published.json")
	if _, err := os.Stat(publishedPath); os.IsNotExist(err) {
		t.Error("expected .published.json file to exist")
	}
}

func TestApproveDraft_WithEndpointEnv_Webhook4xx_KeepsDraft(t *testing.T) {
	e, webhookSrv := newTestEnvWithWebhook(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad payload"}`))
	})

	t.Setenv("TEST_BLOG_WEBHOOK", webhookSrv.URL)

	outputsDir := filepath.Join(filepath.Dir(e.cfg.Logging.File), "..", "outputs")
	draftPath := writeDraftFile(t, outputsDir, Draft{
		ID:          "news_blog.123",
		Pipeline:    "news_blog",
		StageID:     "publish",
		EndpointEnv: "TEST_BLOG_WEBHOOK",
		Inputs:      map[string]string{"x": "y"},
	})

	req := httptest.NewRequest("POST", "/api/v1/drafts/news_blog.123/approve", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	// G5.1: must return 502 Bad Gateway, NOT 200
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (Bad Gateway); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "webhook publish failed") {
		t.Errorf("body = %q, want mention of 'webhook publish failed'", rec.Body.String())
	}

	// G3.2 + D6: draft must be preserved for manual retry
	if _, err := os.Stat(draftPath); os.IsNotExist(err) {
		t.Error("draft file was deleted on 4xx — must be preserved for retry")
	}
}

func TestApproveDraft_NoEndpointEnv_LegacyApproval(t *testing.T) {
	e := newTestEnv(t)

	outputsDir := filepath.Join(filepath.Dir(e.cfg.Logging.File), "..", "outputs")
	writeDraftFile(t, outputsDir, Draft{
		ID:          "legacy_pipeline.123", // ← ponto em vez de hífen
		Pipeline:    "legacy_pipeline",
		StageID:     "publish",
		EndpointEnv: "", // empty → legacy path
		Inputs:      map[string]string{"x": "y"},
	})

	req := httptest.NewRequest("POST", "/api/v1/drafts/legacy_pipeline.123/approve", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["status"] != "approved" {
		t.Errorf("status = %v, want 'approved'", body["status"])
	}

	// Verify renamed to .approved.json
	approvedPath := filepath.Join(outputsDir, "legacy_pipeline.123.approved.json")
	if _, err := os.Stat(approvedPath); os.IsNotExist(err) {
		t.Error("expected .approved.json file for legacy draft")
	}
}

func TestApproveDraft_MissingEnvVar_Returns502(t *testing.T) {
	e := newTestEnv(t)
	// Do NOT set the env var — simulate misconfiguration

	outputsDir := filepath.Join(filepath.Dir(e.cfg.Logging.File), "..", "outputs")
	writeDraftFile(t, outputsDir, Draft{
		ID:          "news_blog.123",
		Pipeline:    "news_blog",
		StageID:     "publish",
		EndpointEnv: "NONEXISTENT_WEBHOOK", // env var not set
		Inputs:      map[string]string{"x": "y"},
	})

	req := httptest.NewRequest("POST", "/api/v1/drafts/news_blog.123/approve", nil)
	rec := httptest.NewRecorder()
	e.server.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "NONEXISTENT_WEBHOOK") {
		t.Errorf("body = %q, want mention of env var name", rec.Body.String())
	}
}
