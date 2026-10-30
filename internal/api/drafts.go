package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// Draft represents a draft file stored in data/outputs/
type Draft struct {
	ID          string            `json:"id"`
	Pipeline    string            `json:"pipeline"`
	CreatedAt   string            `json:"created_at"`
	StageID     string            `json:"stage_id"`
	EndpointEnv string            `json:"endpoint_env"`
	Inputs      map[string]string `json:"inputs"`
	AllOutputs  map[string]string `json:"all_outputs"`
	Filename    string            `json:"filename"`
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

// approveDraft approves and publishes a draft
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

		// In a real implementation, this would:
		// 1. Send HTTP POST to draft.EndpointEnv with draft.Inputs
		// 2. Move draft to .published.json
		// For now, just rename to .approved.json as a stub.
		approvedPath := strings.Replace(draftPath, ".draft.json", ".approved.json", 1)
		if err := os.Rename(draftPath, approvedPath); err != nil {
			writeError(w, http.StatusInternalServerError,
				fmt.Errorf("api: approve draft: %w", err))
			return
		}

		// Emit draft_approved event for real-time dashboard refresh (G3.2).
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
			Msg("api: draft approved (stub — real HTTP publish not implemented)")

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
