package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func localOriginAllowed(r *http.Request) bool {
	return r.Header.Get("Origin") == "" || r.Header.Get("Origin") == "http://"+r.Host
}

func (s *Server) localOpen(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !localOriginAllowed(r) || subtle.ConstantTimeCompare([]byte(key), []byte(s.config.LocalLauncherKey)) != 1 {
		writeError(w, 403, "FORBIDDEN", "invalid local launcher")
		return
	}
	token, _, err := auth.NewRefreshToken()
	if err != nil {
		writeError(w, 500, "INTERNAL", "cannot create local ticket")
		return
	}
	s.localMu.Lock()
	if s.localTickets == nil {
		s.localTickets = make(map[string]time.Time)
	}
	for k, expiry := range s.localTickets {
		if time.Now().After(expiry) {
			delete(s.localTickets, k)
		}
	}
	s.localTickets[auth.RefreshHash(token)] = time.Now().Add(time.Minute)
	s.localMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"ticket": token})
}

func (s *Server) localStop(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !localOriginAllowed(r) || subtle.ConstantTimeCompare([]byte(key), []byte(s.config.LocalLauncherKey)) != 1 {
		writeError(w, 403, "FORBIDDEN", "invalid local launcher")
		return
	}
	s.localQuit(w, r)
}

func (s *Server) localLogin(w http.ResponseWriter, r *http.Request) {
	if !localOriginAllowed(r) {
		writeError(w, 403, "FORBIDDEN", "invalid origin")
		return
	}
	var input struct {
		Ticket string `json:"ticket"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	s.localMu.Lock()
	key := auth.RefreshHash(input.Ticket)
	expiry, ok := s.localTickets[key]
	delete(s.localTickets, key)
	s.localMu.Unlock()
	if !ok || time.Now().After(expiry) {
		writeError(w, 401, "UNAUTHORIZED", "local ticket expired; reopen Rowset")
		return
	}
	user, err := s.store.UserByEmail(r.Context(), s.config.LocalOwnerEmail)
	if err != nil || user.Status != "active" {
		writeError(w, 401, "UNAUTHORIZED", "local owner unavailable")
		return
	}
	role, err := s.role(r.Context(), user.ID)
	if err != nil {
		writeError(w, 500, "INTERNAL", "local role unavailable")
		return
	}
	s.issueSession(w, r, domain.Identity{UserID: user.ID, OrgID: user.OrgID, Email: user.Email, Role: role.Name}, user.PasswordHash, nil)
}

func (s *Server) localQuit(w http.ResponseWriter, r *http.Request) {
	if !localOriginAllowed(r) {
		writeError(w, 403, "FORBIDDEN", "invalid origin")
		return
	}
	s.txnMu.Lock()
	count := len(s.txns)
	s.txnMu.Unlock()
	if count > 0 && r.URL.Query().Get("rollback") != "true" {
		writeError(w, 409, "OPEN_TRANSACTIONS", "Open transactions must be rolled back before quitting")
		return
	}
	w.WriteHeader(http.StatusNoContent)
	if s.config.LocalShutdown != nil {
		go s.config.LocalShutdown()
	}
}
