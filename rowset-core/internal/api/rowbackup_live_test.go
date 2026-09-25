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
)

func TestLiveRowBackupRestoresUpdateAndDelete(t *testing.T) {
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
				encoded, _ := json.Marshal(map[string]any{"sql": sql, "backup": true})
				w := importCall(t, s, identity, s.runQuery, "POST", connection.ID, "", string(encoded))
				var out map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &out)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", sql, w.Code, w.Body.String())
				}
				return out
			}
			restore := func(result map[string]any) {
				t.Helper()
				backup, ok := result["backup"].(map[string]any)
				if !ok {
					t.Fatalf("no backup: %v", result)
				}
				r := personalRequest(identity, "")
				r.SetPathValue("id", backup["id"].(string))
				w := httptest.NewRecorder()
				s.rowBackupRestore(w, r)
				var script struct{ SQL string }
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &script) != nil {
					t.Fatalf("restore: %d %s", w.Code, w.Body.String())
				}
				var statements []string
				for _, line := range strings.Split(script.SQL, "\n") {
					if !strings.HasPrefix(line, "--") {
						statements = append(statements, line)
					}
				}
				for _, statement := range strings.Split(strings.Join(statements, "\n"), ";\n") {
					if statement = strings.TrimSpace(statement); statement != "" {
						query(statement)
					}
				}
			}
			types := map[string][4]string{
				"postgres":  {"varchar(40)", "numeric(10,2)", "boolean", "timestamp"},
				"mysql":     {"varchar(40)", "decimal(10,2)", "boolean", "datetime(3)"},
				"mariadb":   {"varchar(40)", "decimal(10,2)", "boolean", "datetime(3)"},
				"sqlserver": {"nvarchar(40)", "decimal(10,2)", "bit", "datetime2"},
			}[engine.engine]
			binary := map[string]string{"postgres": "bytea", "sqlserver": "varbinary(8)"}[engine.engine]
			if binary == "" {
				binary = "varbinary(8)"
			}
			blob := map[string]string{"postgres": `'\x616263'`, "sqlserver": "0x616263"}[engine.engine]
			if blob == "" {
				blob = "X'616263'"
			}
			yes, no := "true", "false"
			if engine.engine == "sqlserver" {
				yes, no = "1", "0"
			}
			table := fmt.Sprintf("backup_orders_%d", rand.Intn(1_000_000))
			query(fmt.Sprintf("CREATE TABLE %s(id int primary key, name %s, amount %s, paid %s, placed %s, data %s)", table, types[0], types[1], types[2], types[3], binary))
			query(fmt.Sprintf("INSERT INTO %s VALUES (1, 'Ayşe O''Neil', 12.50, %s, '2024-03-01 10:15:30.250', %s), (2, NULL, 7.00, %s, '2024-03-02 08:00:00', NULL), (3, 'keep', 1.00, %s, '2024-03-03 00:00:00', NULL)", table, yes, blob, no, no))
			snapshot := func() string {
				rows := query("SELECT id, name, amount, paid, placed, data FROM " + table + " WHERE id > 0 ORDER BY id")["rows"]
				return fmt.Sprint(rows)
			}
			before := snapshot()

			updated := query("UPDATE " + table + " SET name = 'changed', amount = 0, paid = " + no + ", placed = '2000-01-01 00:00:00', data = NULL WHERE id <= 2")
			if snapshot() == before {
				t.Fatal("update changed nothing")
			}
			if rows := updated["backup"].(map[string]any)["rows"]; rows != float64(2) {
				t.Fatalf("update backup rows: %v", rows)
			}
			restore(updated)
			if got := snapshot(); got != before {
				t.Fatalf("update restore:\nwant %s\ngot  %s", before, got)
			}

			deleted := query("DELETE FROM " + table + " WHERE id IN (1, 2)")
			restore(deleted)
			if got := snapshot(); got != before {
				t.Fatalf("delete restore:\nwant %s\ngot  %s", before, got)
			}

			// An UPDATE is backed up column by column: the script puts back
			// the key and the columns the statement wrote, and says nothing
			// about the rest of the row, so a restore cannot undo someone
			// else's later edit to a column this statement never touched.
			script := func(result map[string]any) string {
				t.Helper()
				r := personalRequest(identity, "")
				r.SetPathValue("id", result["backup"].(map[string]any)["id"].(string))
				w := httptest.NewRecorder()
				s.rowBackupRestore(w, r)
				var body struct{ SQL string }
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
					t.Fatalf("restore script: %d %s", w.Code, w.Body.String())
				}
				return body.SQL
			}
			partial := query("UPDATE " + table + " SET name = 'one column' WHERE id = 3")
			text := script(partial)
			if !strings.Contains(strings.ToLower(text), "name") || !strings.Contains(strings.ToLower(text), "id") {
				t.Fatalf("the script lost the changed column or the key: %s", text)
			}
			for _, untouched := range []string{"amount", "paid", "placed", "data"} {
				if strings.Contains(strings.ToLower(text), untouched) {
					t.Fatalf("the backup kept %s, which the statement never wrote: %s", untouched, text)
				}
			}
			restore(partial)
			if got := snapshot(); got != before {
				t.Fatalf("one-column restore:\nwant %s\ngot  %s", before, got)
			}

			// MySQL rewrites an ON UPDATE CURRENT_TIMESTAMP column by itself,
			// so the backup keeps it even though no statement names it.
			if engine.engine == "mysql" || engine.engine == "mariadb" {
				stamped := table + "_stamped"
				query("CREATE TABLE " + stamped + "(id int primary key, name varchar(40), touched datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3))")
				query("INSERT INTO " + stamped + "(id, name, touched) VALUES (1, 'a', '2024-01-01 00:00:00.000')")
				changed := query("UPDATE " + stamped + " SET name = 'b' WHERE id = 1")
				if text := script(changed); !strings.Contains(strings.ToLower(text), "touched") {
					t.Fatalf("the backup lost the column MySQL rewrites: %s", text)
				}
				restore(changed)
				if got := fmt.Sprint(query("SELECT name, touched FROM " + stamped + " WHERE id = 1")["rows"]); !strings.Contains(got, "2024-01-01 00:00:00") {
					t.Fatalf("the restore did not put the rewritten column back: %s", got)
				}
			}

			apply := func(result map[string]any) *httptest.ResponseRecorder {
				t.Helper()
				r := personalRequest(identity, "")
				r.SetPathValue("id", result["backup"].(map[string]any)["id"].(string))
				w := httptest.NewRecorder()
				s.applyRowBackup(w, r)
				return w
			}
			// Restoring values the row already holds is not a missing row,
			// even where the engine counts only changed rows.
			unchanged := query("UPDATE " + table + " SET amount = amount WHERE id = 3")
			if w := apply(unchanged); w.Code != http.StatusOK {
				t.Fatalf("restore of unchanged row: %d %s", w.Code, w.Body.String())
			}
			// An update that moved a row's key leaves nothing under the
			// backed-up key; the restore says so and changes nothing.
			moved := query("UPDATE " + table + " SET id = 30, name = 'moved' WHERE id = 3")
			if w := apply(moved); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "RESTORE_ROW_MISSING") {
				t.Fatalf("restore of moved row: %d %s", w.Code, w.Body.String())
			}
			if names := fmt.Sprint(query("SELECT name FROM " + table + " WHERE id = 30")["rows"]); names != "[[moved]]" {
				t.Fatalf("refused restore changed the row: %s", names)
			}
			query("UPDATE " + table + " SET id = 3, name = 'keep' WHERE id = 30")

			// Without the option nothing is backed up.
			if w := importCall(t, s, identity, s.runQuery, "POST", connection.ID, "", `{"sql":"UPDATE `+table+` SET amount = amount WHERE id = 3"}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "backup") {
				t.Fatalf("backup without the option: %d %s", w.Code, w.Body.String())
			}
			// Restore scripts are backed up too, so they can be undone.
			// A failed statement keeps no backup.
			if w := importCall(t, s, identity, s.runQuery, "POST", connection.ID, "", `{"sql":"UPDATE `+table+` SET id = 3 WHERE id = 1"}`); w.Code == http.StatusOK {
				t.Fatalf("duplicate key accepted: %s", w.Body.String())
			}
			// An UPDATE that cannot be backed up waits for the user to run it
			// without a backup.
			query("CREATE TABLE " + table + "_nokey(id int, name " + types[0] + ")")
			query("INSERT INTO " + table + "_nokey VALUES (1, 'a')")
			keyless := `{"sql":"UPDATE ` + table + `_nokey SET name = 'b' WHERE id = 1","backup":true}`
			if w := importCall(t, s, identity, s.runQuery, "POST", connection.ID, "", keyless); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "BACKUP_UNAVAILABLE") {
				t.Fatalf("keyless update: %d %s", w.Code, w.Body.String())
			}
			if names := fmt.Sprint(query("SELECT name FROM " + table + "_nokey WHERE id = 1")["rows"]); names != "[[a]]" {
				t.Fatalf("blocked update ran: %s", names)
			}
			if w := importCall(t, s, identity, s.runQuery, "POST", connection.ID, "", strings.Replace(keyless, `"backup":true`, `"backup":false`, 1)); w.Code != http.StatusOK {
				t.Fatalf("keyless update without backup: %d %s", w.Code, w.Body.String())
			}
			list := httptest.NewRecorder()
			s.listRowBackups(list, personalRequest(identity, ""))
			var listed struct{ Backups []map[string]any }
			// Two more than the statements above: restoring is itself a
			// write, so each restore is backed up too. MySQL runs two more
			// for the column it rewrites on its own.
			want := 9
			if engine.engine == "mysql" || engine.engine == "mariadb" {
				want = 11
			}
			if json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Backups) != want {
				t.Fatalf("backups: %s", list.Body.String())
			}
		})
	}
}
