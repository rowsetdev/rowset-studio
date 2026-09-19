// Package apitest builds authenticated API servers for tests.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/api"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

// Env is a fresh control database with one administrator and a bearer token
// for that administrator.
type Env struct {
	Data   *store.Store
	Issuer *auth.Issuer
	Config config.Config
	Owner  domain.User
	Token  string
}

// New creates an Env for a shared installation.
func New(t testing.TB) Env {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err := data.ClaimMode(ctx, true); err != nil {
		t.Fatal(err)
	}
	owner := domain.User{ID: "owner", OrgID: "org", Email: "owner@example.com", PasswordHash: "fixture", Status: "active", CreatedAt: store.NowString()}
	if err := data.CreateOrganizationWithAdmin(ctx, domain.Organization{ID: "org", Name: "Org", CreatedAt: store.NowString()}, domain.Role{ID: "admin", OrgID: "org", Name: "admin"}, owner); err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewIssuer(strings.Repeat("s", 32), "")
	authVersion := auth.PasswordAuthVersion(owner.PasswordHash)
	sessionVersion, err := data.UserSessionVersion(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Issue(domain.Identity{UserID: owner.ID, OrgID: owner.OrgID, Email: owner.Email, Role: "admin"}, &authVersion, &sessionVersion)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Shared: true, RequestBodyLimitBytes: 1 << 20, EncryptionKey: bytes.Repeat([]byte{9}, 32)}
	return Env{Data: data, Issuer: issuer, Config: cfg, Owner: owner, Token: token}
}

// Server builds an API server over the Env; setup functions (for example
// registrars) run before the server handles requests.
func (e Env) Server(t testing.TB, setup ...func(*api.Server)) *api.Server {
	t.Helper()
	server := api.New(e.Config, e.Data, e.Issuer, nil)
	t.Cleanup(func() { server.Close() })
	for _, apply := range setup {
		apply(server)
	}
	return server
}

// Do sends an authenticated JSON request through server's handler.
func (e Env) Do(t testing.TB, server *api.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = encoded
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+e.Token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
