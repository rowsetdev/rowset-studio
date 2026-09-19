package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// fileRowBackupRoundTrip backs up an UPDATE and a DELETE on a local database
// file and restores both through the API.
func fileRowBackupRoundTrip(t *testing.T, engine, driver, setup string) {
	t.Helper()
	s, identity := personalServer(t)
	path := filepath.Join(t.TempDir(), "backup."+engine)
	db, err := sql.Open(driver, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(setup); err != nil {
		t.Fatal(err)
	}
	db.Close()
	body, _ := json.Marshal(map[string]any{"name": engine, "engine": engine, "database": path, "tlsMode": "disable"})
	w := httptest.NewRecorder()
	s.createConnection(w, personalRequest(identity, string(body)))
	var connection struct{ ID string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &connection) != nil {
		t.Fatalf("connection: %d %s", w.Code, w.Body.String())
	}
	query := func(sql string, backup bool) map[string]any {
		t.Helper()
		encoded, _ := json.Marshal(map[string]any{"sql": sql, "backup": backup})
		w := importCall(t, s, identity, s.runQuery, "POST", connection.ID, "", string(encoded))
		var out map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s: %d %s", sql, w.Code, w.Body.String())
		}
		return out
	}
	apply := func(result map[string]any) {
		t.Helper()
		backup, ok := result["backup"].(map[string]any)
		if !ok {
			t.Fatalf("no backup: %v", result)
		}
		r := personalRequest(identity, "")
		r.SetPathValue("id", backup["id"].(string))
		w := httptest.NewRecorder()
		s.applyRowBackup(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("restore: %d %s", w.Code, w.Body.String())
		}
	}
	snapshot := func() string {
		return fmt.Sprint(query("SELECT id, name, paid, data FROM items WHERE id > 0 ORDER BY id", false)["rows"])
	}
	before := snapshot()
	apply(query("UPDATE items SET name = 'changed', paid = false, data = NULL WHERE id <= 2", true))
	if got := snapshot(); got != before {
		t.Fatalf("update restore:\nwant %s\ngot  %s", before, got)
	}
	apply(query("DELETE FROM items WHERE id IN (1, 2)", true))
	if got := snapshot(); got != before {
		t.Fatalf("delete restore:\nwant %s\ngot  %s", before, got)
	}
}

func TestRowBackupRestoresSQLite(t *testing.T) {
	fileRowBackupRoundTrip(t, "sqlite", "sqlite", "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT, paid BOOLEAN, data BLOB); INSERT INTO items VALUES (1, 'Ayşe O''Neil', 1, X'616263'), (2, NULL, 0, NULL), (3, 'keep', 1, NULL)")
}
