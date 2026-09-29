package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/vault"
)

func workspaceRequest(s *Server, identity domain.Identity, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/workspace", strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
	w := httptest.NewRecorder()
	if method == "GET" {
		s.getWorkspace(w, r)
	} else {
		s.putWorkspace(w, r)
	}
	return w
}

const testWorkspace = `{"revision":0,"document":{"version":1,"activeTabId":"q1","tabs":[{"id":"q1","title":"Draft","sql":"SELECT 'private-secret'","database":"CaseDb"}]}}`

func TestWorkspaceEncryptedIsolatedAndVersioned(t *testing.T) {
	s, owner := personalServer(t)
	w := workspaceRequest(s, owner, "GET", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":0`) {
		t.Fatal(w.Body.String())
	}
	w = workspaceRequest(s, owner, "PUT", testWorkspace)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	stored, err := s.store.Workspace(context.Background(), owner.UserID)
	if err != nil || bytes.Contains(stored.Ciphertext, []byte("private-secret")) || len(stored.Nonce) != 12 {
		t.Fatalf("unencrypted or missing workspace: %v", err)
	}
	w = workspaceRequest(s, owner, "GET", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "private-secret") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Body.String())
	}
	other := owner
	other.UserID = "other-user"
	w = workspaceRequest(s, other, "GET", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-secret") {
		t.Fatal(w.Body.String())
	}
	other = owner
	other.OrgID = "other-org"
	if w = workspaceRequest(s, other, "GET", ""); w.Code != 500 {
		t.Fatal("encrypted owner binding missing")
	}
	w = workspaceRequest(s, owner, "PUT", testWorkspace)
	if w.Code != 409 {
		t.Fatalf("stale creation: %d %s", w.Code, w.Body.String())
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- workspaceRequest(s, owner, "PUT", strings.Replace(testWorkspace, `"revision":0`, `"revision":1`, 1)).Code
		}()
	}
	wg.Wait()
	close(codes)
	success, conflicts := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		}
		if code == 409 {
			conflicts++
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS: success=%d conflicts=%d", success, conflicts)
	}
}

func TestWorkspaceValidationAndAuthentication(t *testing.T) {
	s, owner := personalServer(t)
	for _, body := range []string{
		strings.Replace(testWorkspace, `"revision":0`, `"revision":-1`, 1),
		strings.Replace(testWorkspace, `"revision":0,`, ``, 1),
		strings.Replace(testWorkspace, `"version":1`, `"version":2`, 1),
		strings.Replace(testWorkspace, `"activeTabId":"q1"`, `"activeTabId":"missing"`, 1),
		strings.Replace(testWorkspace, "private-secret", strings.Repeat("x", int(s.workspaceLimit())), 1),
	} {
		w := workspaceRequest(s, owner, "PUT", body)
		if w.Code < 400 {
			t.Fatal("invalid workspace accepted")
		}
	}
	for _, method := range []string{"GET", "PUT"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, "/api/workspace", strings.NewReader(testWorkspace)))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", method, w.Code)
		}
	}
	if w := workspaceRequest(s, owner, "PUT", testWorkspace); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := workspaceRequest(s, owner, "GET", "")
	var response struct{ Document workspaceDocument }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Document.Tabs[0].Database != "CaseDb" {
		t.Fatal("database context lost")
	}
}

// Drafts saved under a key this installation no longer has are not a
// transient failure. The answer has to name the cause and the way back,
// because the only other sign is "crypto operation failed" in a log.
func TestWorkspaceSaysWhenTheEncryptionKeyChanged(t *testing.T) {
	s, owner := personalServer(t)
	if w := workspaceRequest(s, owner, "PUT", testWorkspace); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	changed, err := vault.New(bytes.Repeat([]byte{7}, 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.vault = changed
	w := workspaceRequest(s, owner, "GET", "")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "WORKSPACE_KEY_MISMATCH") {
		t.Fatalf("a changed key was not reported as such: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ROWSET_ENC_KEY_PREVIOUS") {
		t.Fatalf("the answer does not say how to get the drafts back: %s", w.Body.String())
	}
	// The drafts themselves are left alone.
	stored, err := s.store.Workspace(context.Background(), owner.UserID)
	if err != nil || len(stored.Ciphertext) == 0 {
		t.Fatalf("stored workspace: %v", err)
	}
}

// A server whose key no longer opens the database says so once, at startup.
func TestStartupReportsAnEncryptionKeyThatDoesNotMatch(t *testing.T) {
	s, _ := personalServer(t)
	if err := s.store.CreateSecret(context.Background(), domain.Secret{ID: "secret-1", Ciphertext: []byte("not-openable"), Nonce: bytes.Repeat([]byte{1}, 12)}); err != nil {
		t.Fatal(err)
	}
	s.encryptionKeyMatches = true
	s.checkEncryptionKey(context.Background())
	if s.encryptionKeyMatches {
		t.Fatal("a key that cannot open the database was reported as matching")
	}
}
