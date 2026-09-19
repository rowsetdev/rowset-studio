package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func personalServer(t *testing.T) (*Server, domain.Identity) {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "personal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err := data.ClaimMode(ctx, false); err != nil {
		t.Fatal(err)
	}
	identity := domain.Identity{OrgID: "org", UserID: "owner", Role: "admin", Email: "owner@example.com"}
	if err := data.CreateOrganizationWithAdmin(ctx,
		domain.Organization{ID: "org", Name: "Personal", CreatedAt: store.NowString()},
		domain.Role{ID: "admin", OrgID: "org", Name: "admin"},
		domain.User{ID: "owner", OrgID: "org", Email: identity.Email, PasswordHash: "unused", Status: "active", CreatedAt: store.NowString()},
	); err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{RequestBodyLimitBytes: 1 << 20, EncryptionKey: bytes.Repeat([]byte{9}, 32)}, data, auth.NewIssuer(strings.Repeat("s", 32), ""), nil)
	t.Cleanup(func() { s.Close() })
	return s, identity
}

func personalRequest(identity domain.Identity, body string) *http.Request {
	r := httptest.NewRequest("POST", "/api/policies/custom", strings.NewReader(body))
	return r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
}

func TestInstanceReportsPersonalMode(t *testing.T) {
	s, _ := personalServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/meta/instance", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"personal"`) {
		t.Fatal(w.Body.String())
	}
}

func TestInstancePublishesEngineCapabilities(t *testing.T) {
	s, _ := personalServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/meta/instance", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("metadata: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Capabilities map[string]engine.Capabilities `json:"engineCapabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Capabilities["cockroachdb"].Explain || !response.Capabilities["cassandra"].CSVImport {
		t.Fatalf("incorrect advertised capabilities: %#v", response.Capabilities)
	}
	if !response.Capabilities["mongodb"].DocumentWrite || !response.Capabilities["elasticsearch"].DocumentWrite || !response.Capabilities["redis"].KeyWrite || !response.Capabilities["valkey"].KeyWrite {
		t.Fatalf("missing advertised write capabilities: %#v", response.Capabilities)
	}
	if !response.Capabilities["sqlite"].CSVImport || !response.Capabilities["duckdb"].CSVImport {
		t.Fatalf("file engines should advertise CSV import: %#v", response.Capabilities)
	}
}

func TestPersonalPoliciesCanBeCreatedAndEnforceAgainstOwner(t *testing.T) {
	s, identity := personalServer(t)
	w := httptest.NewRecorder()
	s.createCustomPolicy(w, personalRequest(identity, `{"name":"scoped","kind":"deny_table","config":"customers","role":"admin"}`))
	if w.Code != 403 {
		t.Fatalf("role-scoped policy accepted: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.createCustomPolicy(w, personalRequest(identity, `{"name":"Protect customers","kind":"deny_table","config":"customers"}`))
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Blocking happens before a database connection is opened, including the
	// same executor used by transactionQuery.
	for _, txn := range []*engine.Transaction{nil, {}} {
		w = httptest.NewRecorder()
		s.executeQuery(w, personalRequest(identity, `{}`), domain.Connection{ID: "c", OrgID: "org"}, queryInput{SQL: "UPDATE customers SET name='x' WHERE id=1"}, txn)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "Protect customers") {
			t.Fatalf("owner policy bypass: %d %s", w.Code, w.Body.String())
		}
	}
	items, err := s.activity.ListQueryHistory(context.Background(), identity.UserID, "c", nil, nil)
	if err != nil || len(items) != 2 {
		t.Fatalf("local history: %d %v", len(items), err)
	}
	w = httptest.NewRecorder()
	s.listPolicies(w, personalRequest(identity, `{}`))
	if !strings.Contains(w.Body.String(), "deny_delete_without_where") {
		t.Fatal(w.Body.String())
	}
}
