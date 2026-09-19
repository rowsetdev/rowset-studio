package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Run applies the row limit while reading, as Studio does: the statement runs
// as written, without LIMIT or TOP added, and the stream stops at the limit.
// Each query here would produce billions of rows, so reading on past the
// limit would not finish in time.
func TestLiveRunStopsAtTheRowLimitWithoutReadingTheRest(t *testing.T) {
	huge := map[string]string{
		"postgres":  "SELECT a.n FROM generate_series(1, 100000) a(n) CROSS JOIN generate_series(1, 100000) b(n) WHERE a.n > 0",
		"mysql":     "SELECT 1 AS n FROM information_schema.collations a CROSS JOIN information_schema.collations b CROSS JOIN information_schema.collations c WHERE a.id > 0",
		"mariadb":   "SELECT 1 AS n FROM information_schema.collations a CROSS JOIN information_schema.collations b CROSS JOIN information_schema.collations c WHERE a.id > 0",
		"sqlserver": "SELECT 1 AS n FROM sys.all_objects a CROSS JOIN sys.all_objects b CROSS JOIN sys.all_objects c WHERE a.object_id > -2147483647",
	}
	for _, engine := range importEngines {
		t.Run(engine.engine, func(t *testing.T) {
			password := os.Getenv(engine.passwordEnv)
			if password == "" {
				t.Skip(engine.passwordEnv + " is not configured")
			}
			s, identity := personalServer(t)
			body, _ := json.Marshal(map[string]any{"name": "client-" + engine.engine, "engine": engine.engine, "host": "127.0.0.1", "port": engine.port, "database": "rowset_e2e", "connectionUsername": engine.user, "password": password, "tlsMode": "disable"})
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

			started := time.Now()
			outcome := s.Run(context.Background(), identity, connection, huge[engine.engine], Client{Type: "test", IP: "127.0.0.1"}, nil)
			if outcome.Stream == nil {
				t.Fatalf("no stream: %s %s", outcome.Code, outcome.Message)
			}
			for {
				_, ok, err := outcome.Stream.Next()
				if err != nil {
					t.Fatalf("stream: %v", err)
				}
				if !ok {
					break
				}
			}
			_ = outcome.Stream.Close()
			elapsed := time.Since(started)
			if outcome.Stream.RowCount() != 10000 || !outcome.Stream.Truncated() {
				t.Fatalf("rows=%d truncated=%v", outcome.Stream.RowCount(), outcome.Stream.Truncated())
			}
			if elapsed > 10*time.Second {
				t.Fatalf("stopping at the limit took %s; the rest of the result was read", elapsed)
			}
			t.Logf("10,000 rows then stopped in %s", elapsed.Round(time.Millisecond))
		})
	}
}
