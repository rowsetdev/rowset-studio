package api

import (
	"net/http"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
)

// setUserPassword changes an account password and ends that account's
// sessions. The single local owner uses it from the Account page.
func (s *Server) setUserPassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if len(request.Password) < 8 || len(request.Password) > 1024 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "password must be 8 to 1024 characters")
		return
	}
	identity := identityFromContext(r.Context())
	user, err := s.store.User(r.Context(), r.PathValue("id"))
	if err != nil || user.OrgID != identity.OrgID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}
	hash, err := auth.HashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to hash password")
		return
	}
	if err := s.store.SetUserPassword(r.Context(), user.ID, hash); err != nil {
		writeStoreError(w, err)
		return
	}
	_ = s.store.RevokeRefreshTokensForUser(r.Context(), user.ID)
	_ = s.store.BumpUserSessionVersion(r.Context(), user.ID)
	w.WriteHeader(http.StatusNoContent)
}
