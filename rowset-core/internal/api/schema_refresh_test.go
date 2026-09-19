package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func TestSchemaRefreshIsLimitedToTheCallersConnections(t *testing.T) {
	s, identity := personalServer(t)
	ctx := context.Background()
	if err := s.store.CreateSecret(ctx, domain.Secret{ID: "secret", Ciphertext: []byte("x"), Nonce: []byte("n")}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateConnection(ctx, domain.Connection{ID: "conn", OrgID: identity.OrgID, Name: "Local", Engine: "postgres", Host: "127.0.0.1", Port: 5432, Database: "app", Environment: "development", SecretID: "secret", CreatedAt: store.NowString()}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id   string
		want int
	}{{"conn", 204}, {"someone-elses", 404}} {
		r := httptest.NewRequest("POST", "/api/connections/"+item.id+"/schema/refresh", nil)
		r.SetPathValue("id", item.id)
		r = r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
		w := httptest.NewRecorder()
		s.refreshConnectionSchema(w, r)
		if w.Code != item.want {
			t.Fatalf("%s: %d %s", item.id, w.Code, w.Body.String())
		}
	}
}
