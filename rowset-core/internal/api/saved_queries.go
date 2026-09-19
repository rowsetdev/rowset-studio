package api

import (
	"net/http"
	"strings"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func (s *Server) listSavedQueries(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSavedQueries(r.Context(), identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to list saved queries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queries": items})
}

type savedQueryInput struct {
	ConnectionID string `json:"connectionId"`
	Name         string `json:"name"`
	SQL          string `json:"sql"`
}

func (s *Server) createSavedQuery(w http.ResponseWriter, r *http.Request) {
	var input savedQueryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ConnectionID, input.Name, input.SQL = strings.TrimSpace(input.ConnectionID), strings.TrimSpace(input.Name), strings.TrimSpace(input.SQL)
	if input.ConnectionID == "" || input.Name == "" || input.SQL == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "connectionId, name and sql are required")
		return
	}
	connection, err := s.store.Connection(r.Context(), input.ConnectionID)
	identity := identityFromContext(r.Context())
	if err != nil || connection.OrgID != identity.OrgID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "connection not found")
		return
	}
	// Reuse the same RBAC check without depending on route path values.
	if !identity.IsAdmin() {
		role, e := s.store.UserRole(r.Context(), identity.UserID)
		allowed := false
		if e == nil {
			access, _ := s.store.ListRoleConnectionAccess(r.Context(), role.ID)
			for _, item := range access {
				allowed = allowed || item.ConnectionID == connection.ID
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "POLICY_DENIED", "role has no access to this connection")
			return
		}
	}
	item := domain.SavedQuery{ID: id.New(), OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: connection.ID, Name: input.Name, SQL: input.SQL, CreatedAt: store.NowString()}
	if err := s.store.CreateSavedQuery(r.Context(), item); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) deleteSavedQuery(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteSavedQuery(r.Context(), r.PathValue("id"), identityFromContext(r.Context()).UserID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
