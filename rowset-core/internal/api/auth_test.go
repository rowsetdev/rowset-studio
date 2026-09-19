package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func newAuthTestServer(t *testing.T) http.Handler {
	t.Helper()
	data, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rowset.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	hash, err := auth.HashPassword("secure-password")
	if err != nil {
		t.Fatal(err)
	}
	org := domain.Organization{ID: "org-1", Name: "Test", CreatedAt: store.NowString()}
	role := domain.Role{ID: "role-1", OrgID: org.ID, Name: "admin"}
	user := domain.User{ID: "user-1", OrgID: org.ID, Email: "admin@example.com", PasswordHash: hash, Status: "active", CreatedAt: store.NowString()}
	if err := data.CreateOrganizationWithAdmin(context.Background(), org, role, user); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{RequestBodyLimitBytes: 1 << 20, SecureCookies: false}
	return New(cfg, data, auth.NewIssuer("test-secret-that-is-at-least-thirty-two-bytes", ""), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))).Handler()
}

func TestRefreshCookieRestoresReloadedSessionAndRotates(t *testing.T) {
	handler := newAuthTestServer(t)
	login := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"email":"admin@example.com","password":"secure-password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != refreshCookie || !cookies[0].HttpOnly || cookies[0].Path != "/api/auth" {
		t.Fatalf("bad refresh cookie: %#v", cookies)
	}
	var loginBody struct {
		AccessToken string          `json:"accessToken"`
		User        domain.Identity `json:"user"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	if loginBody.AccessToken == "" || loginBody.User.UserID != "user-1" {
		t.Fatalf("bad login response: %#v", loginBody)
	}

	refresh := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	refresh.AddCookie(cookies[0])
	refreshResponse := httptest.NewRecorder()
	handler.ServeHTTP(refreshResponse, refresh)
	if refreshResponse.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", refreshResponse.Code, refreshResponse.Body.String())
	}
	rotated := refreshResponse.Result().Cookies()
	if len(rotated) != 1 || rotated[0].Value == cookies[0].Value {
		t.Fatal("refresh token was not rotated")
	}

	replay := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	replay.AddCookie(cookies[0])
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusNoContent {
		t.Fatalf("replayed refresh token response: %d", replayResponse.Code)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	me.Header.Set("Authorization", "Bearer "+loginBody.AccessToken)
	meResponse := httptest.NewRecorder()
	handler.ServeHTTP(meResponse, me)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("access token failed: %d %s", meResponse.Code, meResponse.Body.String())
	}
}

func TestRefreshWithoutSessionIsQuietNoContent(t *testing.T) {
	handler := newAuthTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("refresh without session: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestInvalidLoginDoesNotRevealUnknownEmail(t *testing.T) {
	handler := newAuthTestServer(t)
	for _, body := range []string{
		`{"email":"unknown@example.com","password":"wrong"}`,
		`{"email":"admin@example.com","password":"wrong"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Body.String() != "{\"error\":{\"code\":\"BAD_CREDENTIALS\",\"message\":\"invalid email or password\"}}\n" {
			t.Fatalf("unexpected login error: %d %s", response.Code, response.Body.String())
		}
	}
}
