package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

// A ';' hidden behind '#' on PostgreSQL must be seen and refused as multiple
// statements, and the '#>' JSON operator must still run as one query.
func TestLiveHashHandlingOnPostgres(t *testing.T) {
	password := os.Getenv("ROWSET_MATRIX_POSTGRES_PASSWORD")
	if password == "" {
		t.Skip("ROWSET_MATRIX_POSTGRES_PASSWORD is not configured")
	}
	s, identity := personalServer(t)
	if err := s.store.CreateSecret(context.Background(), domain.Secret{ID: "sec", Ciphertext: []byte("x"), Nonce: []byte("n")}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"name": "pg", "engine": "postgres", "host": "127.0.0.1", "port": 55432, "database": "rowset_e2e", "connectionUsername": "postgres", "password": password, "tlsMode": "disable"})
	w := httptest.NewRecorder()
	s.createConnection(w, personalRequest(identity, string(body)))
	var created struct{ ID string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("connection: %d %s", w.Code, w.Body.String())
	}
	connection, err := s.store.Connection(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := func(sql string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.executeQuery(w, personalRequest(identity, `{}`), connection, queryInput{SQL: sql}, nil)
		return w
	}

	hidden := run("SELECT 1 AS n #> '{a}'; DROP TABLE IF EXISTS rowset_should_not_exist")
	if hidden.Code != http.StatusForbidden || !strings.Contains(hidden.Body.String(), "multiple SQL statements") {
		t.Fatalf("hidden second statement not refused: %d %s", hidden.Code, hidden.Body.String())
	}
	operator := run("SELECT '{\"a\": 5}'::jsonb #> '{a}' AS v")
	if operator.Code != http.StatusOK || !strings.Contains(operator.Body.String(), `"5"`) && !strings.Contains(operator.Body.String(), "5") {
		t.Fatalf("'#>' operator query failed: %d %s", operator.Code, operator.Body.String())
	}
}
