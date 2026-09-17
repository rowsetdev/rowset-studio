package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/store"
)

func saveMongoConnection(t *testing.T, s *Server, identity domain.Identity, id string) {
	t.Helper()
	ctx := context.Background()
	if err := s.store.CreateConnection(ctx, domain.Connection{ID: id, OrgID: identity.OrgID, Name: id, Engine: "mongodb", Host: "127.0.0.1", Port: 27017, Database: "app", Environment: "development", CreatedAt: store.NowString()}); err != nil {
		t.Fatal(err)
	}
}

// An UPDATE or DELETE with an empty MongoDB filter must be blocked by the
// same built-in "without WHERE" guardrail SQL writes go through: the
// synthetic statement's WHERE clause must actually reflect whether the
// filter has a predicate, not be a hardcoded placeholder that always fakes
// one (a real bug caught in review: the placeholder used to be unconditional).
func TestMongoUpdateWithoutFilterIsBlockedByWithoutWhereGuardrail(t *testing.T) {
	s, identity := personalServer(t)
	saveMongoConnection(t, s, identity, "mongo1")
	w := httptest.NewRecorder()
	r := personalRequest(identity, `{"collection":"docs","filter":{},"update":{"$set":{"a":1}}}`)
	r.SetPathValue("id", "mongo1")
	s.mongoUpdate(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "deny_update_without_where") {
		t.Fatalf("empty-filter update was not blocked: %d %s", w.Code, w.Body.String())
	}
}

func TestMongoDeleteWithoutFilterIsBlockedByWithoutWhereGuardrail(t *testing.T) {
	s, identity := personalServer(t)
	saveMongoConnection(t, s, identity, "mongo1")
	w := httptest.NewRecorder()
	r := personalRequest(identity, `{"collection":"docs","filter":{}}`)
	r.SetPathValue("id", "mongo1")
	s.mongoDelete(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "deny_delete_without_where") {
		t.Fatalf("empty-filter delete was not blocked: %d %s", w.Code, w.Body.String())
	}
}

// A filter with a real predicate must pass the guardrail; it then fails at
// the network call (no live MongoDB in this test), which is the boundary
// this test is checking up to.
func TestMongoUpdateWithFilterPassesWithoutWhereGuardrail(t *testing.T) {
	s, identity := personalServer(t)
	saveMongoConnection(t, s, identity, "mongo1")
	w := httptest.NewRecorder()
	r := personalRequest(identity, `{"collection":"docs","filter":{"_id":"x"},"update":{"$set":{"a":1}}}`)
	r.SetPathValue("id", "mongo1")
	s.mongoUpdate(w, r)
	if strings.Contains(w.Body.String(), "deny_update_without_where") {
		t.Fatalf("a real filter tripped the without-WHERE guardrail: %d %s", w.Code, w.Body.String())
	}
}
