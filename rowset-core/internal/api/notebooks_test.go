package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func notebookCall(t *testing.T, handler http.HandlerFunc, identity domain.Identity, notebookID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(context.WithValue(context.Background(), contextKey{}, identity))
	r.SetPathValue("id", notebookID)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestNotebooksAreEncryptedOwnedAndVersioned(t *testing.T) {
	s, identity := personalServer(t)
	doc := map[string]any{"title": "Weekly checks", "cells": []map[string]any{
		{"id": "note", "kind": "markdown", "content": "## Orders"},
		{"id": "sql", "kind": "sql", "content": "SELECT count(*) FROM orders WHERE id > 0", "connectionId": "c1", "database": "sales"},
	}}
	created := notebookCall(t, s.createNotebook, identity, "", map[string]any{"document": doc})
	var notebook struct {
		ID       string
		Revision int64
	}
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &notebook) != nil || notebook.Revision != 1 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	stored, err := s.store.Notebook(context.Background(), notebook.ID, identity.UserID)
	if err != nil || bytes.Contains(stored.Ciphertext, []byte("orders")) {
		t.Fatalf("notebook must be stored encrypted: %v", err)
	}
	if w := notebookCall(t, s.getNotebook, identity, notebook.ID, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"database":"sales"`) {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	intruder := domain.Identity{OrgID: identity.OrgID, UserID: "someone-else", Role: "admin"}
	if w := notebookCall(t, s.getNotebook, intruder, notebook.ID, nil); w.Code != 404 {
		t.Fatalf("another user read the notebook: %d %s", w.Code, w.Body.String())
	}
	doc["title"] = "Weekly checks v2"
	if w := notebookCall(t, s.putNotebook, identity, notebook.ID, map[string]any{"revision": 1, "document": doc}); w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":2`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := notebookCall(t, s.putNotebook, identity, notebook.ID, map[string]any{"revision": 1, "document": doc}); w.Code != 409 {
		t.Fatalf("stale revision overwrote the notebook: %d %s", w.Code, w.Body.String())
	}
	bad := map[string]any{"title": "Bad", "cells": []map[string]any{{"id": "x", "kind": "script", "content": "rm -rf"}}}
	if w := notebookCall(t, s.putNotebook, identity, notebook.ID, map[string]any{"revision": 2, "document": bad}); w.Code != 400 {
		t.Fatalf("invalid cell kind accepted: %d %s", w.Code, w.Body.String())
	}
	if w := notebookCall(t, s.deleteNotebook, intruder, notebook.ID, nil); w.Code != 404 {
		t.Fatalf("another user deleted the notebook: %d %s", w.Code, w.Body.String())
	}
	if w := notebookCall(t, s.deleteNotebook, identity, notebook.ID, nil); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := notebookCall(t, s.getNotebook, identity, notebook.ID, nil); w.Code != 404 {
		t.Fatalf("deleted notebook still readable: %d %s", w.Code, w.Body.String())
	}
}

func TestSavedQueriesBecomeOneNotebookOnce(t *testing.T) {
	s, identity := personalServer(t)
	ctx := context.Background()
	for _, name := range []string{"first", "second"} {
		if err := s.store.CreateSavedQuery(ctx, domain.SavedQuery{ID: name, OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: "c1", Name: name, SQL: "SELECT '" + name + "'", CreatedAt: store.NowString()}); err != nil {
			t.Fatal(err)
		}
	}
	var listed struct {
		Notebooks []struct {
			ID        string
			Title     string
			CellCount int
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		w := notebookCall(t, s.listNotebooks, identity, "", nil)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		if len(listed.Notebooks) != 1 || listed.Notebooks[0].Title != "Saved queries" || listed.Notebooks[0].CellCount != 4 {
			t.Fatalf("attempt %d: %+v", attempt, listed.Notebooks)
		}
	}
	if w := notebookCall(t, s.getNotebook, identity, listed.Notebooks[0].ID, nil); !strings.Contains(w.Body.String(), `"connectionId":"c1"`) || !strings.Contains(w.Body.String(), "### first") {
		t.Fatalf("imported cells: %s", w.Body.String())
	}
	if queries, _ := s.store.ListSavedQueries(ctx, identity.UserID); len(queries) != 2 {
		t.Fatalf("import must keep the saved queries, got %d", len(queries))
	}
}
