package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func TestMyHistorySpansConnectionsButOnlyTheCaller(t *testing.T) {
	s, identity := personalServer(t)
	ctx := context.Background()
	for _, item := range []domain.QueryHistory{
		{ID: "mine-a", UserID: identity.UserID, ConnectionID: "connection-a", SQL: "SELECT 1", Status: "success", CreatedAt: store.NowString()},
		{ID: "mine-b", UserID: identity.UserID, ConnectionID: "connection-b", SQL: "SELECT 2", Status: "error", CreatedAt: store.NowString()},
		{ID: "theirs", UserID: "someone-else", ConnectionID: "connection-a", SQL: "SELECT secret", Status: "success", CreatedAt: store.NowString()},
	} {
		if err := s.activity.CreateQueryHistory(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("GET", "/api/history", nil).WithContext(context.WithValue(ctx, contextKey{}, identity))
	w := httptest.NewRecorder()
	s.myHistory(w, r)
	var body struct {
		History []struct {
			ID, ConnectionID, SQL string
		} `json:"history"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	seen := map[string]string{}
	for _, item := range body.History {
		seen[item.ID] = item.ConnectionID
	}
	if len(seen) != 2 || seen["mine-a"] != "connection-a" || seen["mine-b"] != "connection-b" {
		t.Fatalf("want only the caller's two statements with connection ids, got %s", w.Body.String())
	}
}
