package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/activity"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/vault"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/web"
)

// RouteRegistrar mounts additional HTTP endpoints. Handlers must be wrapped
// with Kit.Authenticated or Kit.RequireAdmin. Registrars run after the
// built-in routes and before the API fallback and the web UI.
type RouteRegistrar func(mux *http.ServeMux, kit Kit)

// Kit exposes the server facilities that registered handlers need.
type Kit struct{ server *Server }

// AddRoutes registers extra endpoints; call it before Handler.
func (s *Server) AddRoutes(registrar RouteRegistrar) {
	s.routeRegistrars = append(s.routeRegistrars, registrar)
}

func (k Kit) Store() *store.Store { return k.server.store }

func (k Kit) Activity() activity.Store { return k.server.activity }

func (k Kit) Config() config.Config { return k.server.config }

// Vault encrypts and decrypts stored secrets with the installation key.
func (k Kit) Vault() *vault.Vault { return k.server.vault }

func (k Kit) Logger() *slog.Logger { return k.server.logger }

// ResetConnections closes pooled database connections so that changed
// connection definitions take effect on the next query.
func (k Kit) ResetConnections() { _ = k.server.engines.Close() }

// IssueSession signs a user in: it answers the request with an access token
// and sets the refresh cookie. passwordHash is the user's stored hash (or
// any stable value for users without a password); changing it ends the
// user's sessions.
func (k Kit) IssueSession(w http.ResponseWriter, r *http.Request, identity domain.Identity, passwordHash string) {
	k.server.issueSession(w, r, identity, passwordHash, nil)
}

func (k Kit) Authenticated(handler http.HandlerFunc) http.Handler {
	return k.server.authenticated(handler)
}

func (k Kit) RequireAdmin(handler http.HandlerFunc) http.Handler {
	return k.server.requireAdmin(handler)
}

// Identity returns the authenticated caller of a request passed through
// Kit.Authenticated or Kit.RequireAdmin.
func Identity(r *http.Request) domain.Identity { return identityFromContext(r.Context()) }

func WriteJSON(w http.ResponseWriter, status int, value any) { writeJSON(w, status, value) }

func WriteError(w http.ResponseWriter, status int, code, message string) {
	writeError(w, status, code, message)
}

func WriteStoreError(w http.ResponseWriter, err error) { writeStoreError(w, err) }

// DecodeJSON decodes a request body, rejecting unknown fields; on failure it
// has already written the error response.
func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeJSON(w, r, target)
}

// AddConnectionDetail adds fields to every connection returned by the API.
// Call before Handler.
func (s *Server) AddConnectionDetail(detail func(context.Context, domain.Connection, map[string]any)) {
	s.connectionDetails = append(s.connectionDetails, detail)
}

// ConnectionSaveHook runs after a connection is created or updated, with the
// JSON request body. Its error is reported to the caller and a connection
// that was just created is removed again.
type ConnectionSaveHook func(ctx context.Context, connection domain.Connection, created bool, body []byte) error

// AddConnectionSaveHook registers a hook together with the request fields it
// reads, which the connection endpoints then accept. Call before Handler.
func (s *Server) AddConnectionSaveHook(fields []string, hook ConnectionSaveHook) {
	if s.connectionFields == nil {
		s.connectionFields = map[string]bool{}
	}
	for _, field := range fields {
		s.connectionFields[field] = true
	}
	s.connectionSaveHooks = append(s.connectionSaveHooks, hook)
}

// SetWebUI serves a different Studio build (a single-page app: files as they
// are, index.html for every other path). Call before Handler.
func (s *Server) SetWebUI(build fs.FS) { s.webUI = web.HandlerFor(build) }
