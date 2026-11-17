package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// Draft represents a draft file stored in data/outputs/
type Draft struct {
	ID              string            `json:"id"`
	Pipeline        string            `json:"pipeline"`
	CreatedAt       string            `json:"created_at"`
	StageID         string            `json:"stage_id"`
	EndpointEnv     string            `json:"endpoint_env"`
	Inputs          map[string]string `json:"inputs"`
	AllOutputs      map[string]string `json:"all_outputs"`
	Filename        string            `json:"filename"`
	ReviewCriteria  []string          `json:"review_criteria,omitempty"`  // Sprint 15 15d
	ReviewReasoning string            `json:"review_reasoning,omitempty"` // Sprint 15 15d
}

// listDrafts returns all .draft.json files from data/outputs/
func (h *handlers) listDrafts(w http.ResponseWriter, r *http.Request) {
	outputsDir := filepath.Join(filepath.Dir(h.cfg.Logging.File), "..", "outputs")

	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []Draft{})
			return
		}
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read outputs dir: %w", err))
		return
	}

	var drafts []Draft
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".draft.json") {
			continue
		}

		path := filepath.Join(outputsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn().Err(err).Str("path", path).Msg("api: failed to read draft file")
			continue
		}

		var draft Draft
		if err := json.Unmarshal(data, &draft); err != nil {
			log.Warn().Err(err).Str("path", path).Msg("api: failed to parse draft file")
			continue
		}

		draft.Filename = entry.Name()
		drafts = append(drafts, draft)
	}

	writeJSON(w, http.StatusOK, drafts)
}

// getDraft returns a specific draft by ID.
// Uses findDraftByID helper which matches by timestamp substring,
// resolving the ID-vs-filename format mismatch (hyphen vs dot).
func (h *handlers) getDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: draft ID is required"))
		return
	}

	draftPath, err := h.findDraftByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if draftPath == "" {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("api: draft %q not found", id))
		return
	}

	data, err := os.ReadFile(draftPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read draft: %w", err))
		return
	}

	var draft Draft
	if err := json.Unmarshal(data, &draft); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: parse draft: %w", err))
		return
	}

	draft.Filename = filepath.Base(draftPath)
	writeJSON(w, http.StatusOK, draft)
}

// approveDraft approves and publishes a draft.
//   - If the draft has an EndpointEnv, performs a real HTTP POST via engine.PublishDraft
//     (decision tree D6: retry 5xx, fail-fast 4xx).
//   - On success (2xx), renames the file to .published.json and emits draft_published event.
//   - On failure (4xx, missing env, network), returns 502 and keeps the draft on disk
//     so the user can retry manually.
//   - If EndpointEnv is empty (legacy draft), renames to .approved.json (stub).
func (h *handlers) approveDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: draft ID is required"))
		return
	}

	draftPath, err := h.findDraftByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if draftPath == "" {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("api: draft %q not found", id))
		return
	}

	data, err := os.ReadFile(draftPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read draft: %w", err))
		return
	}

	var draft Draft
	if err := json.Unmarshal(data, &draft); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: parse draft: %w", err))
		return
	}

	// Case 1: draft has an EndpointEnv → publish via real webhook (D6).
	if draft.EndpointEnv != "" {
		err := h.engine.PublishDraft(r.Context(), engine.DraftPublish{
			PipelineName: draft.Pipeline,
			StageID:      draft.StageID,
			EndpointEnv:  draft.EndpointEnv,
			Inputs:       draft.Inputs,
		})
		if err != nil {
			log.Error().Err(err).
				Str("draft_id", draft.ID).
				Str("pipeline", draft.Pipeline).
				Str("endpoint_env", draft.EndpointEnv).
				Msg("api: publish draft failed (webhook error)")

			writeError(w, http.StatusBadGateway,
				fmt.Errorf("webhook publish failed: %w (draft preserved for retry)", err))
			return
		}

		publishedPath := strings.Replace(draftPath, ".draft.json", ".published.json", 1)
		if err := os.Rename(draftPath, publishedPath); err != nil {
			writeError(w, http.StatusInternalServerError,
				fmt.Errorf("api: rename to published: %w", err))
			return
		}

		if h.hub != nil {
			h.hub.Broadcast("draft_published", map[string]any{
				"draft_id": draft.ID,
				"pipeline": draft.Pipeline,
				"filename": filepath.Base(draftPath),
			})
		}

		log.Info().
			Str("draft_id", draft.ID).
			Str("pipeline", draft.Pipeline).
			Str("endpoint_env", draft.EndpointEnv).
			Msg("api: draft published via webhook (D6)")

		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "published",
			"draft_id": draft.ID,
			"pipeline": draft.Pipeline,
		})
		return
	}

	// Case 2: legacy draft without EndpointEnv → stub approval.
	approvedPath := strings.Replace(draftPath, ".draft.json", ".approved.json", 1)
	if err := os.Rename(draftPath, approvedPath); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: approve draft: %w", err))
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("draft_approved", map[string]any{
			"draft_id": draft.ID,
			"pipeline": draft.Pipeline,
			"filename": filepath.Base(draftPath),
		})
	}

	log.Info().
		Str("draft_id", draft.ID).
		Str("pipeline", draft.Pipeline).
		Msg("api: draft approved (no endpoint_env, legacy path)")

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "approved",
		"draft_id": draft.ID,
		"pipeline": draft.Pipeline,
	})
}

// rejectDraft rejects a draft and moves it to .rejected.json
func (h *handlers) rejectDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: draft ID is required"))
		return
	}

	// Read optional rejection reason from request body
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
	}

	draftPath, err := h.findDraftByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if draftPath == "" {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("api: draft %q not found", id))
		return
	}

	data, err := os.ReadFile(draftPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read draft: %w", err))
		return
	}

	var draft Draft
	if err := json.Unmarshal(data, &draft); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: parse draft: %w", err))
		return
	}

	rejectedPath := strings.Replace(draftPath, ".draft.json", ".rejected.json", 1)
	if err := os.Rename(draftPath, rejectedPath); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: reject draft: %w", err))
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("draft_rejected", map[string]any{
			"draft_id": draft.ID,
			"pipeline": draft.Pipeline,
			"filename": filepath.Base(draftPath),
			"reason":   body.Reason,
		})
	}

	log.Info().
		Str("draft_id", draft.ID).
		Str("pipeline", draft.Pipeline).
		Str("reason", body.Reason).
		Msg("api: draft rejected")

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "rejected",
		"draft_id": draft.ID,
		"pipeline": draft.Pipeline,
		"reason":   body.Reason,
	})
}

// findDraftByID searches for a draft file whose filename contains the
// draft ID's timestamp. Fixes ID mismatch bug: engine saves ID as "name-timestamp"
// (hyphen) but filename uses "name.timestamp.draft.json" (dot).
//
// Backward compatibility: supports both ID formats:
//   - New (Sprint 15): "pipeline_name-timestamp" (hyphen)
//   - Legacy: "pipeline_name.timestamp" (dot, used by older tests/drafts)
//
// Returns the full path to the draft file, or empty string if not found.
// Sprint 15 hotfix-3: regression fix for G3.2 draft approval/rejection.
func (h *handlers) findDraftByID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("api: draft ID is required")
	}

	// Extract timestamp from ID.
	// Try hyphen first (new format), then dot (legacy format).
	var timestamp string
	if strings.Contains(id, "-") {
		parts := strings.Split(id, "-")
		timestamp = parts[len(parts)-1]
	} else if strings.Contains(id, ".") {
		parts := strings.Split(id, ".")
		timestamp = parts[len(parts)-1]
	} else {
		return "", fmt.Errorf("api: invalid draft ID format %q (no separator found)", id)
	}

	if timestamp == "" {
		return "", fmt.Errorf("api: empty timestamp in draft ID %q", id)
	}

	outputsDir := filepath.Join(filepath.Dir(h.cfg.Logging.File), "..", "outputs")
	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("api: read outputs dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Match by timestamp substring (present in both ID and filename)
		// AND the file is still a draft (not already approved/rejected/published).
		if strings.Contains(entry.Name(), timestamp) &&
			strings.HasSuffix(entry.Name(), ".draft.json") {
			return filepath.Join(outputsDir, entry.Name()), nil
		}
	}

	return "", nil
}
