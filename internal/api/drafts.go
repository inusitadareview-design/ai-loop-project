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
	ReviewCriteria  []string          `json:"review_criteria,omitempty"`  // NEW: Sprint 15 15d
	ReviewReasoning string            `json:"review_reasoning,omitempty"` // NEW: Sprint 15 15d
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

// getDraft returns a specific draft by ID
func (h *handlers) getDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("api: draft ID is required"))
		return
	}

	outputsDir := filepath.Join(filepath.Dir(h.cfg.Logging.File), "..", "outputs")
	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound,
				fmt.Errorf("api: draft %q not found", id))
			return
		}
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read outputs dir: %w", err))
		return
	}

	for _, entry := range entries {
		if !strings.Contains(entry.Name(), id) {
			continue
		}

		path := filepath.Join(outputsDir, entry.Name())
		data, err := os.ReadFile(path)
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

		draft.Filename = entry.Name()
		writeJSON(w, http.StatusOK, draft)
		return
	}

	writeError(w, http.StatusNotFound,
		fmt.Errorf("api: draft %q not found", id))
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

	outputsDir := filepath.Join(filepath.Dir(h.cfg.Logging.File), "..", "outputs")
	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read outputs dir: %w", err))
		return
	}

	for _, entry := range entries {
		if !strings.Contains(entry.Name(), id) {
			continue
		}

		draftPath := filepath.Join(outputsDir, entry.Name())
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
				// G5.1: propagate error, keep draft on disk for manual retry.
				log.Error().Err(err).
					Str("draft_id", draft.ID).
					Str("pipeline", draft.Pipeline).
					Str("endpoint_env", draft.EndpointEnv).
					Msg("api: publish draft failed (webhook error)")

				writeError(w, http.StatusBadGateway,
					fmt.Errorf("webhook publish failed: %w (draft preserved for retry)", err))
				return
			}

			// Success: rename to .published.json
			publishedPath := strings.Replace(draftPath, ".draft.json", ".published.json", 1)
			if err := os.Rename(draftPath, publishedPath); err != nil {
				writeError(w, http.StatusInternalServerError,
					fmt.Errorf("api: rename to published: %w", err))
				return
			}

			// Emit draft_published event for real-time dashboard refresh (G3.2).
			if h.hub != nil {
				h.hub.Broadcast("draft_published", map[string]any{
					"draft_id": draft.ID,
					"pipeline": draft.Pipeline,
					"filename": entry.Name(),
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
				"filename": entry.Name(),
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
		return
	}

	writeError(w, http.StatusNotFound,
		fmt.Errorf("api: draft %q not found", id))
}

// rejectDraft rejects a draft
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

	outputsDir := filepath.Join(filepath.Dir(h.cfg.Logging.File), "..", "outputs")
	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("api: read outputs dir: %w", err))
		return
	}

	for _, entry := range entries {
		if !strings.Contains(entry.Name(), id) {
			continue
		}

		draftPath := filepath.Join(outputsDir, entry.Name())
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

		// Move to .rejected.json
		rejectedPath := strings.Replace(draftPath, ".draft.json", ".rejected.json", 1)
		if err := os.Rename(draftPath, rejectedPath); err != nil {
			writeError(w, http.StatusInternalServerError,
				fmt.Errorf("api: reject draft: %w", err))
			return
		}

		// Emit draft_rejected event for real-time dashboard refresh (G3.2).
		if h.hub != nil {
			h.hub.Broadcast("draft_rejected", map[string]any{
				"draft_id": draft.ID,
				"pipeline": draft.Pipeline,
				"filename": entry.Name(),
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
		return
	}

	writeError(w, http.StatusNotFound,
		fmt.Errorf("api: draft %q not found", id))
}
