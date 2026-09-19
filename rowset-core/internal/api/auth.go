package api

import (
	"net/http"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
)

const (
	refreshCookie  = "rowset_refresh"
	refreshTTLDays = 14
)

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookie)
	if err != nil || cookie.Value == "" {
		s.noRefreshSession(w)
		return
	}
	userID, expectedVersion, err := s.store.ConsumeRefreshToken(r.Context(), auth.RefreshHash(cookie.Value))
	if err != nil {
		s.noRefreshSession(w)
		return
	}
	user, err := s.store.User(r.Context(), userID)
	if err != nil || user.Status != "active" {
		s.noRefreshSession(w)
		return
	}
	role, err := s.role(r.Context(), user.ID)
	if err != nil {
		s.noRefreshSession(w)
		return
	}
	s.issueSession(w, r, domain.Identity{UserID: user.ID, OrgID: user.OrgID, Email: user.Email, Role: role.Name}, user.PasswordHash, &expectedVersion)
}

// Refresh is probed during application bootstrap. An absent/expired session is
// normal browser state rather than a failed API operation, so return an empty
// success response while still clearing any stale cookie.
func (s *Server) noRefreshSession(w http.ResponseWriter) {
	s.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, identity domain.Identity, passwordHash string, expectedVersion *int64) {
	version, err := s.store.UserSessionVersion(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "session version lookup failed")
		return
	}
	if expectedVersion != nil && *expectedVersion != version {
		s.clearRefreshCookie(w)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "session expired")
		return
	}
	authVersion := auth.PasswordAuthVersion(passwordHash)
	accessToken, err := s.issuer.Issue(identity, &authVersion, &version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "token issue failed")
		return
	}
	refreshToken, tokenHash, err := auth.NewRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "session creation failed")
		return
	}
	expires := time.Now().UTC().AddDate(0, 0, refreshTTLDays)
	if err := s.store.CreateRefreshToken(r.Context(), id.New(), identity.UserID, tokenHash, expires.Format("2006-01-02 15:04:05"), version); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "session creation failed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: refreshCookie, Value: refreshToken, Path: "/api/auth", HttpOnly: true, Secure: s.config.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: refreshTTLDays * 24 * 3600, Expires: expires})
	writeJSON(w, http.StatusOK, map[string]any{"accessToken": accessToken, "user": identity})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(refreshCookie); err == nil {
		_, _, _ = s.store.ConsumeRefreshToken(r.Context(), auth.RefreshHash(cookie.Value))
	}
	if identity, err := s.authenticate(r); err == nil {
		_ = s.store.RevokeRefreshTokensForUser(r.Context(), identity.UserID)
		_ = s.store.BumpUserSessionVersion(r.Context(), identity.UserID)
	}
	s.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: refreshCookie, Path: "/api/auth", HttpOnly: true, Secure: s.config.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": identityFromContext(r.Context())})
}
