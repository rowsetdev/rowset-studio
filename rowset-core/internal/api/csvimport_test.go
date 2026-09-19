package api

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestImportLiteralsAndDelimiters(t *testing.T) {
	for _, test := range []struct{ engine, value, want string }{
		{"postgres", "O'Neil", "'O''Neil'"},
		{"mysql", `a\b'c`, `'a\\b''c'`},
		{"mariadb", `x\`, `'x\\'`},
		{"mssql", "Şule", "N'Şule'"},
	} {
		if got := importLiteral(test.engine, test.value); got != test.want {
			t.Errorf("%s %q: %s, want %s", test.engine, test.value, got, test.want)
		}
	}
	for value, want := range map[string]rune{"": ',', ";": ';', "\\t": '\t', "\t": '\t', "|": '|'} {
		if got, err := importDelimiter(value); err != nil || got != want {
			t.Errorf("delimiter %q: %q %v", value, got, err)
		}
	}
	if _, err := importDelimiter("ab"); err == nil {
		t.Fatal("two-character delimiter accepted")
	}
}

type importEngine struct {
	engine, passwordEnv, user, schema string
	port                              int
}

var importEngines = []importEngine{
	{"postgres", "ROWSET_MATRIX_POSTGRES_PASSWORD", "postgres", "public", 55432},
	{"mysql", "ROWSET_MATRIX_MYSQL_PASSWORD", "root", "", 53306},
	{"mariadb", "ROWSET_MATRIX_MARIADB_PASSWORD", "root", "", 53307},
	{"sqlserver", "ROWSET_MATRIX_MSSQL_PASSWORD", "sa", "dbo", 51433},
}

func importCall(t *testing.T, s *Server, identity domain.Identity, handler func(http.ResponseWriter, *http.Request), method, connectionID, importID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := personalRequest(identity, body)
	r.Method = method
	r.SetPathValue("id", connectionID)
	if importID != "" {
		r.SetPathValue("importId", importID)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestLiveCSVImportIsAllOrNothing(t *testing.T) {
	const csvText = "id,name,note\n1,\"Ayşe, Y.\",\"line1\nline2\"\n2,Bob,\n3,O'Neil,back\\slash\n"
	for _, engine := range importEngines {
		t.Run(engine.engine, func(t *testing.T) {
			password := os.Getenv(engine.passwordEnv)
			if password == "" {
				t.Skip(engine.passwordEnv + " is not configured")
			}
			s, identity := personalServer(t)
			body, _ := json.Marshal(map[string]any{"name": engine.engine, "engine": engine.engine, "host": "127.0.0.1", "port": engine.port, "database": "rowset_e2e", "connectionUsername": engine.user, "password": password, "tlsMode": "disable"})
			w := httptest.NewRecorder()
			s.createConnection(w, personalRequest(identity, string(body)))
			var connection struct{ ID string }
			if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &connection) != nil {
				t.Fatalf("connection: %d %s", w.Code, w.Body.String())
			}
			query := func(sql string) map[string]any {
				t.Helper()
				encoded, _ := json.Marshal(map[string]any{"sql": sql})
				w := importCall(t, s, identity, s.runQuery, "POST", connection.ID, "", string(encoded))
				var out map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &out)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", sql, w.Code, w.Body.String())
				}
				return out
			}
			table := fmt.Sprintf("import_people_%d", rand.Intn(1_000_000))
			text := "varchar(40)"
			if engine.engine == "sqlserver" {
				text = "nvarchar(40)"
			}
			query(fmt.Sprintf("CREATE TABLE %s(id int primary key, name %s, note %s)", table, text, text))
			importCSV := func(text string) *httptest.ResponseRecorder {
				t.Helper()
				w := importCall(t, s, identity, s.startImport, "POST", connection.ID, "", "")
				var started struct{ ImportID string }
				if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &started) != nil {
					t.Fatalf("start: %d %s", w.Code, w.Body.String())
				}
				// Two chunks, split inside a quoted field.
				for _, chunk := range []string{text[:20], text[20:]} {
					if w := importCall(t, s, identity, s.appendImport, "PUT", connection.ID, started.ImportID, chunk); w.Code != http.StatusOK {
						t.Fatalf("chunk: %d %s", w.Code, w.Body.String())
					}
				}
				run, _ := json.Marshal(map[string]any{"schema": engine.schema, "table": table, "header": true, "delimiter": ",", "nullEmpty": true, "columns": []map[string]any{{"source": 0, "target": "id"}, {"source": 1, "target": "name"}, {"source": 2, "target": "note"}}})
				return importCall(t, s, identity, s.runImport, "POST", connection.ID, started.ImportID, string(run))
			}
			if w := importCSV(csvText); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"rows":3`) {
				t.Fatalf("import: %d %s", w.Code, w.Body.String())
			}
			rows := query("SELECT id, name, note FROM " + table + " WHERE id > 0 ORDER BY id")["rows"].([]any)
			got := fmt.Sprint(rows)
			if len(rows) != 3 || !strings.Contains(got, "Ayşe, Y.") || !strings.Contains(got, "line1\nline2") || !strings.Contains(got, "<nil>") || !strings.Contains(got, "O'Neil") || !strings.Contains(got, `back\slash`) {
				t.Fatalf("imported rows: %v", got)
			}
			// A duplicate key in the last row rolls the whole file back.
			if w := importCSV("id,name,note\n10,a,b\n11,c,d\n1,dup,e\n"); w.Code == http.StatusOK {
				t.Fatalf("duplicate key accepted: %s", w.Body.String())
			}
			if count := query("SELECT id FROM " + table + " WHERE id >= 10")["rows"].([]any); len(count) != 0 {
				t.Fatalf("partial import left rows: %v", count)
			}
			// Policies apply to imports like any INSERT.
			policy, _ := json.Marshal(map[string]any{"name": "No imports", "kind": "deny_table", "config": table})
			if w := importCall(t, s, identity, s.createCustomPolicy, "POST", "", "", string(policy)); w.Code != http.StatusCreated {
				t.Fatalf("policy: %d %s", w.Code, w.Body.String())
			}
			if w := importCSV("id,name,note\n20,a,b\n"); w.Code != http.StatusForbidden {
				t.Fatalf("blocked table imported: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
