package api

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
)

func TestMiddlewareSecurityCORSCompressionAndRateLimit(t *testing.T) {
	server := &Server{config: config.Config{StudioOrigin: "https://studio.example", RequestBodyLimitBytes: 1024, RateLimitPerMinute: 2}, logger: slog.Default(), rateClients: make(map[string]*rateWindow)}
	handler := server.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, strings.Repeat("rowset", 100)) }))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Content-Security-Policy") != studioCSP {
		t.Fatalf("SPA CSP missing: %q", response.Header().Get("Content-Security-Policy"))
	}
	if response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("response was not compressed")
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := io.ReadAll(reader)
	if string(decoded) != strings.Repeat("rowset", 100) {
		t.Fatal("compressed response changed")
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/api/connections", nil)
	preflight.RemoteAddr = "192.0.2.2:1234"
	preflight.Header.Set("Origin", "https://studio.example")
	preflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent || preflightResponse.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("bad preflight: %d %#v", preflightResponse.Code, preflightResponse.Header())
	}

	for index := 0; index < 2; index++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.RemoteAddr = "192.0.2.3:1234"
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("request %d unexpectedly rejected: %d", index, res.Code)
		}
	}
	limited := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	limited.RemoteAddr = "192.0.2.3:1234"
	limitedResponse := httptest.NewRecorder()
	handler.ServeHTTP(limitedResponse, limited)
	if limitedResponse.Code != http.StatusTooManyRequests || limitedResponse.Header().Get("Content-Security-Policy") != apiCSP {
		t.Fatalf("rate limit/CSP mismatch: %d %#v", limitedResponse.Code, limitedResponse.Header())
	}
}

func TestRateLimiterClientMapHasHardMemoryBound(t *testing.T) {
	server := &Server{config: config.Config{RateLimitPerMinute: 1}, rateClients: make(map[string]*rateWindow)}
	now := time.Now()
	for index := 0; index < 10_000; index++ {
		server.rateClients["client-"+strconv.Itoa(index)] = &rateWindow{started: now, count: 1}
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.250:1234"
	if server.allowRequest(request) || len(server.rateClients) != 10_000 {
		t.Fatalf("client map exceeded its hard bound: %d", len(server.rateClients))
	}
}

func TestRateLimiterSeparatesAuthenticatedUsersBehindOneProxy(t *testing.T) {
	server := &Server{config: config.Config{Shared: true, RateLimitPerMinute: 2}, rateClients: make(map[string]*rateWindow)}
	for _, userID := range []string{"user-a", "user-b"} {
		first := server.allowIdentity(userID)
		second := server.allowIdentity(userID)
		if !first || !second {
			t.Fatalf("%s exhausted another user's allowance", userID)
		}
		if server.allowIdentity(userID) {
			t.Fatalf("%s exceeded its own allowance", userID)
		}
	}
	if len(server.rateClients) != 2 {
		t.Fatalf("got %d identity buckets, want 2", len(server.rateClients))
	}
}

func TestAnonymousLimiterIgnoresAssetsAndDefersBearerRequests(t *testing.T) {
	asset := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	if anonymousAPIRateLimitApplies(asset) {
		t.Fatal("static asset would consume the API rate limit")
	}
	authenticated := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	authenticated.Header.Set("Authorization", "Bearer token")
	if anonymousAPIRateLimitApplies(authenticated) {
		t.Fatal("authenticated API request would consume the shared IP bucket")
	}
	login := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	if !anonymousAPIRateLimitApplies(login) {
		t.Fatal("public login request bypassed the IP bucket")
	}
}

func TestQueryExecutionPathDetection(t *testing.T) {
	for _, path := range []string{"/api/connections/c/query", "/api/connections/c/txn/t/query", "/api/connections/c/schema", "/api/connections/c/export", "/api/connections/c/explain", "/api/connections/c/imports/i", "/api/connections/c/imports/i/run", "/api/row-backups/b/apply", "/api/multirun", "/api/connections/c/documents/find", "/api/ai/ask"} {
		if !isQueryExecutionPath(path) {
			t.Fatalf("not detected: %s", path)
		}
	}
	for _, path := range []string{"/api/query", "/api/connections/c/txn/t/commit", "/api/connections/c/databases", "/api/row-backups/b/restore", "/api/workspace", "/api/ai/settings", "/api/multirunx", "/api"} {
		if isQueryExecutionPath(path) {
			t.Fatalf("false positive: %s", path)
		}
	}
}
