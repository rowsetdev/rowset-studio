package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
)

// End-to-end through the HTTP handlers Studio uses: a manual transaction that
// the database has discarded must never be offered for Commit again.
func TestLiveManualTransactionRecoveryResponses(t *testing.T) {
	tests := []struct {
		name, passwordEnv, user, table, sleep, backendID, kill, errorState string
		port                                                               int
		setup                                                              []string
	}{
		{"postgres", "ROWSET_MATRIX_POSTGRES_PASSWORD", "postgres", "public.rowset_api_tx", "SELECT pg_sleep(5)", "SELECT pg_backend_pid()", "SELECT pg_terminate_backend(%v)", "aborted", 55432, []string{`DROP TABLE IF EXISTS public.rowset_api_tx`, `CREATE TABLE public.rowset_api_tx(id int primary key)`}},
		{"mysql", "ROWSET_MATRIX_MYSQL_PASSWORD", "root", "rowset_api_tx", "SELECT SLEEP(5)", "SELECT CONNECTION_ID()", "KILL %v", "active", 53306, []string{`DROP TABLE IF EXISTS rowset_api_tx`, `CREATE TABLE rowset_api_tx(id int primary key)`}},
		{"mssql", "ROWSET_MATRIX_MSSQL_PASSWORD", "sa", "dbo.rowset_api_tx", "SELECT MAX(CHECKSUM(a.name, b.name, c.name)) FROM sys.all_objects a CROSS JOIN sys.all_objects b CROSS JOIN sys.all_objects c WHERE a.object_id > 0", "SELECT @@SPID", "KILL %v", "active", 51433, []string{`IF OBJECT_ID('dbo.rowset_api_tx','U') IS NOT NULL DROP TABLE dbo.rowset_api_tx`, `CREATE TABLE dbo.rowset_api_tx(id int primary key)`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			password := os.Getenv(test.passwordEnv)
			if password == "" {
				t.Skip(test.passwordEnv + " is not configured")
			}
			s, identity := personalServer(t)
			direct := engine.Connection{ID: "direct-" + test.name, Engine: test.name, Host: "127.0.0.1", Port: test.port, Database: "rowset_e2e", Username: test.user, Password: password, PoolSize: 2}
			manager := engine.NewManager()
			defer manager.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			for _, statement := range test.setup {
				if _, err := manager.Execute(ctx, direct, statement, 0); err != nil {
					t.Fatal(err)
				}
			}
			call := func(requestCtx context.Context, handler http.HandlerFunc, connectionID, txnID string, body any) *httptest.ResponseRecorder {
				t.Helper()
				encoded, _ := json.Marshal(body)
				r := httptest.NewRequest("POST", "/", bytes.NewReader(encoded)).WithContext(context.WithValue(requestCtx, contextKey{}, identity))
				r.SetPathValue("id", connectionID)
				r.SetPathValue("txn_id", txnID)
				w := httptest.NewRecorder()
				handler(w, r)
				return w
			}
			created := call(ctx, s.createConnection, "", "", map[string]any{"name": test.name, "engine": test.name, "host": "127.0.0.1", "port": test.port, "database": "rowset_e2e", "environment": "dev", "connectionUsername": test.user, "password": password, "tlsRequired": false})
			var connection struct{ ID string }
			if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &connection) != nil {
				t.Fatalf("create connection: %d %s", created.Code, created.Body.String())
			}
			s.refreshConnectionTopology(ctx, connection.ID)
			begin := func(id int) string {
				t.Helper()
				w := call(ctx, s.beginTransaction, connection.ID, "", map[string]any{})
				var started struct{ TxnID string }
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &started) != nil {
					t.Fatalf("begin: %d %s", w.Code, w.Body.String())
				}
				if w := call(ctx, s.transactionQuery, connection.ID, started.TxnID, map[string]any{"sql": fmt.Sprintf("INSERT INTO %s VALUES(%d)", test.table, id)}); w.Code != 200 {
					t.Fatalf("insert: %d %s", w.Code, w.Body.String())
				}
				return started.TxnID
			}
			expectError := func(w *httptest.ResponseRecorder, status int, code, transactionState string) {
				t.Helper()
				var body struct {
					Error struct{ Code, TransactionState string }
				}
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				if w.Code != status || (code != "" && body.Error.Code != code) || body.Error.TransactionState != transactionState {
					t.Fatalf("want %d %s state=%q, got %d %s", status, code, transactionState, w.Code, w.Body.String())
				}
			}

			// Statement error: PostgreSQL aborts the transaction and Commit is refused.
			txn := begin(1)
			expectError(call(ctx, s.transactionQuery, connection.ID, txn, map[string]any{"sql": "SELECT * FROM rowset_no_such_table WHERE id > 0"}), 502, "", test.errorState)
			if test.errorState == "aborted" {
				expectError(call(ctx, s.commitTransaction, connection.ID, txn, nil), 409, "TXN_ROLLED_BACK", "")
			} else if w := call(ctx, s.rollbackTransaction, connection.ID, txn, nil); w.Code != 204 {
				t.Fatalf("rollback: %d %s", w.Code, w.Body.String())
			}

			// Server-side kill: the failed statement reports a lost transaction and
			// the server forgets it, so Commit cannot be attempted.
			txn = begin(2)
			w := call(ctx, s.transactionQuery, connection.ID, txn, map[string]any{"sql": test.backendID})
			var backend struct{ Rows [][]any }
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &backend) != nil || len(backend.Rows) != 1 {
				t.Fatalf("backend id: %d %s", w.Code, w.Body.String())
			}
			if _, err := manager.Execute(ctx, direct, fmt.Sprintf(test.kill, backend.Rows[0][0]), 0); err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond)
			expectError(call(ctx, s.transactionQuery, connection.ID, txn, map[string]any{"sql": "SELECT COUNT(*) FROM " + test.table + " WHERE id > 0"}), 502, "", "lost")
			expectError(call(ctx, s.commitTransaction, connection.ID, txn, nil), 404, "TXN_NOT_FOUND", "")

			// Browser Stop cancels the request context; the transaction is removed.
			txn = begin(3)
			stopped, stop := context.WithTimeout(ctx, time.Second)
			started := time.Now()
			w = call(stopped, s.transactionQuery, connection.ID, txn, map[string]any{"sql": test.sleep})
			stop()
			if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > 4*time.Second {
				t.Fatalf("stopped statement did not run until cancellation (%s): %d %s", elapsed, w.Code, w.Body.String())
			}
			expectError(call(ctx, s.commitTransaction, connection.ID, txn, nil), 404, "TXN_NOT_FOUND", "")

			result, err := manager.Execute(ctx, direct, "SELECT COUNT(*) FROM "+test.table, 0)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(result.Rows[0][0]) != "0" {
				t.Fatalf("discarded transactions left rows: %v", result.Rows)
			}
		})
	}
}
