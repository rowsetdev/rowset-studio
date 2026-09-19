package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
)

const (
	refreshCookie      = "rowset_refresh"
	refreshTTLDays     = 14
	loginMaxFailures   = 5
	loginLockoutPeriod = 15 * time.Minute
)

var dummyPassword struct {
	sync.Once
	hash string
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(request.Email))
	if len(request.Password) > 1024 || s.loginLocked(r, email) {
		if len(request.Password) > 1024 {
			writeError(w, http.StatusUnauthorized, "BAD_CREDENTIALS", "invalid email or password")
		} else {
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many failed attempts, try again later")
		}
		return
	}
	user, err := s.store.UserByEmail(r.Context(), email)
	valid := err == nil && user.Status == "active"
	hash := user.PasswordHash
	if !valid {
		dummyPassword.Do(func() { dummyPassword.hash, _ = auth.HashPassword("rowset-dummy-password") })
		hash = dummyPassword.hash
	}
	if auth.VerifyPassword(request.Password, hash) != nil || !valid {
		_ = s.store.RecordLoginFailure(r.Context(), email, time.Now().UTC().Format(time.RFC3339), int64(loginLockoutPeriod.Seconds()))
		writeError(w, http.StatusUnauthorized, "BAD_CREDENTIALS", "invalid email or password")
		return
	}
	_ = s.store.ClearLoginFailures(r.Context(), email)
	role, err := s.store.UserRole(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "user role is missing")
		return
	}
	s.issueSession(w, r, domain.Identity{UserID: user.ID, OrgID: user.OrgID, Email: user.Email, Role: role.Name}, user.PasswordHash, nil)
}

func (s *Server) loginLocked(r *http.Request, email string) bool {
	count, lastRaw, ok, err := s.store.LoginFailureState(r.Context(), email)
	if err != nil || !ok || count < loginMaxFailures {
		return false
	}
	last, err := time.Parse(time.RFC3339, lastRaw)
	return err == nil && time.Since(last) < loginLockoutPeriod
}

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
	role, err := s.store.UserRole(r.Context(), user.ID)
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
