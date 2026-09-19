package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Statements from other clients go through the same hooks, policies and
// activity log as the HTTP API.
func TestRunAppliesHooksPoliciesAndRecordsActivity(t *testing.T) {
	s, identity := personalServer(t)
	path := filepath.Join(t.TempDir(), "client.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE people(id INTEGER PRIMARY KEY, email TEXT); INSERT INTO people VALUES (1,'ada@example.com'),(2,'bob@example.com')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	body, _ := json.Marshal(map[string]any{"name": "clients", "engine": "sqlite", "database": path, "tlsMode": "disable"})
	w := httptest.NewRecorder()
	s.createConnection(w, personalRequest(identity, string(body)))
	var created struct{ ID string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("connection: %d %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	connection, err := s.store.Connection(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.AddStatementHook(func(_ context.Context, request StatementRequest) (string, error) {
		if request.Statement.Kind == "select" {
			return strings.Replace(request.Statement.Raw, "id > 0", "id = 1", 1), nil
		}
		return request.Statement.Raw, nil
	}, "narrowed")
	s.AddResultHook(func(_ context.Context, request ResultRequest) (ResultTransforms, error) {
		for index, column := range request.Columns {
			if column == "email" {
				return ResultTransforms{index: func(any) any { return "hidden" }}, nil
			}
		}
		return nil, nil
	}, "hiddenColumns")

	outcome := s.Run(ctx, identity, connection, "SELECT id, email FROM people WHERE id > 0", Client{Type: "gateway", IP: "10.0.0.7"}, nil)
	if outcome.Stream == nil {
		t.Fatalf("no stream: %#v", outcome)
	}
	var rows [][]any
	for {
		row, ok, err := outcome.Stream.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		rows = append(rows, row)
	}
	if len(rows) != 1 || rows[0][1] != "hidden" || !outcome.Stream.Transformed(1) || outcome.Stream.Transformed(0) {
		t.Fatalf("rows=%v", rows)
	}
	if notes := outcome.Stream.Annotations(); notes["narrowed"] != true || len(notes["hiddenColumns"].([]string)) != 1 {
		t.Fatalf("annotations: %#v", notes)
	}
	refused := s.Run(ctx, identity, connection, "DELETE FROM people", Client{Type: "gateway"}, nil)
	if refused.Code != "POLICY_DENIED" || refused.Decision == nil {
		t.Fatalf("unfiltered delete: %#v", refused)
	}
	history, err := s.activity.ListQueryHistory(ctx, identity.UserID, connection.ID, nil, nil)
	if err != nil || len(history) < 2 {
		t.Fatalf("activity: %v %v", history, err)
	}
}
