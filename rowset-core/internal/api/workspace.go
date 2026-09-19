package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

// workspaceLimit caps the saved editor tabs. A personal workspace keeps
// long scripts, so it allows more than a shared server.
func (s *Server) workspaceLimit() int64 {
	if s.config.Shared {
		return 768 * 1024
	}
	return 16 << 20
}

type workspaceTab struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	SQL          string  `json:"sql"`
	ConnectionID *string `json:"connectionId,omitempty"`
	Database     string  `json:"database,omitempty"`
	NodeRole     string  `json:"nodeRole,omitempty"`
	RestoreOf    string  `json:"restoreOf,omitempty"`
}
type workspaceDocument struct {
	Version     int            `json:"version"`
	Tabs        []workspaceTab `json:"tabs"`
	ActiveTabID string         `json:"activeTabId"`
}
type workspaceEnvelope struct {
	UserID   string            `json:"userId"`
	OrgID    string            `json:"orgId"`
	Document workspaceDocument `json:"document"`
}

func validWorkspace(doc workspaceDocument) bool {
	if doc.Version != 1 || len(doc.Tabs) == 0 || len(doc.Tabs) > 100 {
		return false
	}
	ids := make(map[string]bool)
	for _, tab := range doc.Tabs {
		if tab.ID == "" || len(tab.ID) > 128 || ids[tab.ID] || len(tab.Title) > 512 || len(tab.Database) > 512 || len(tab.RestoreOf) > 128 || (tab.ConnectionID != nil && len(*tab.ConnectionID) > 128) {
			return false
		}
		if tab.NodeRole != "" && tab.NodeRole != "primary" && tab.NodeRole != "secondary" {
			return false
		}
		ids[tab.ID] = true
	}
	return ids[doc.ActiveTabID]
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	identity := identityFromContext(r.Context())
	item, err := s.store.Workspace(r.Context(), identity.UserID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"revision": 0, "document": nil})
		return
	}
	if err != nil || s.vault == nil || len(item.Nonce) != 12 {
		writeError(w, 500, "WORKSPACE_UNAVAILABLE", "Workspace unavailable; existing drafts have not been changed.")
		return
	}
	plain, err := s.vault.Decrypt(item.Ciphertext, item.Nonce)
	var envelope workspaceEnvelope
	if err != nil || json.Unmarshal(plain, &envelope) != nil || envelope.UserID != identity.UserID || envelope.OrgID != identity.OrgID || !validWorkspace(envelope.Document) {
		writeError(w, 500, "WORKSPACE_UNAVAILABLE", "Workspace could not be decrypted or validated; existing drafts have not been changed.")
		return
	}
	writeJSON(w, 200, map[string]any{"revision": item.Revision, "document": envelope.Document})
}

func (s *Server) putWorkspace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, s.workspaceLimit())
	var input struct {
		Revision *int64            `json:"revision"`
		Document workspaceDocument `json:"document"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Revision == nil || *input.Revision < 0 || *input.Revision >= 9007199254740991 || !validWorkspace(input.Document) {
		writeError(w, 400, "BAD_WORKSPACE", "Workspace must have version 1, 1–100 uniquely identified tabs, a valid active tab and an expected revision.")
		return
	}
	identity := identityFromContext(r.Context())
	plain, err := json.Marshal(workspaceEnvelope{UserID: identity.UserID, OrgID: identity.OrgID, Document: input.Document})
	if err != nil || s.vault == nil {
		writeError(w, 500, "WORKSPACE_UNAVAILABLE", "Workspace encryption unavailable")
		return
	}
	ciphertext, nonce, err := s.vault.Encrypt(plain)
	if err != nil {
		writeError(w, 500, "WORKSPACE_UNAVAILABLE", "Workspace encryption failed")
		return
	}
	err = s.store.SaveWorkspace(r.Context(), identity.UserID, *input.Revision, ciphertext, nonce)
	if errors.Is(err, store.ErrWorkspaceConflict) {
		writeError(w, 409, "WORKSPACE_CONFLICT", "Another window saved a newer workspace. Export your unsaved drafts before reopening; no changes were overwritten.")
		return
	}
	if err != nil {
		writeError(w, 500, "WORKSPACE_UNAVAILABLE", "Workspace save failed")
		return
	}
	writeJSON(w, 200, map[string]any{"revision": *input.Revision + 1})
}
