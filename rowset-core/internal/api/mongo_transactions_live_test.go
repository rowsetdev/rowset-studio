package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestLiveMongoTransactionAPIRollbackAndCommit exercises manual-commit
// MongoDB writes end to end through the HTTP handlers: begin, insert inside
// the transaction, rollback (must revert), then begin, insert, commit (must
// persist). MongoDB only accepts a transaction on a replica set member or
// mongos, so this needs a replica-set server, not the single-node
// ROWSET_TEST_MONGODB_HOST fixture other MongoDB tests use.
func TestLiveMongoTransactionAPIRollbackAndCommit(t *testing.T) {
	host := os.Getenv("ROWSET_TEST_MONGODB_REPLICASET_HOST")
	if host == "" {
		t.Skip("mongodb replica-set test server not configured")
	}
	port := 27017
	if v := os.Getenv("ROWSET_TEST_MONGODB_REPLICASET_PORT"); v != "" {
		var err error
		if port, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
	}

	s, identity := personalServer(t)
	body, _ := json.Marshal(map[string]any{"name": "mongo-rs", "engine": "mongodb", "host": host, "port": port, "database": "rowset_api_tx_check", "tlsMode": "disable"})
	w := httptest.NewRecorder()
	s.createConnection(w, personalRequest(identity, string(body)))
	var connection struct{ ID string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &connection) != nil {
		t.Fatalf("connection: %d %s", w.Code, w.Body.String())
	}
	connID := connection.ID
	// A run-specific collection name avoids any cross-run pollution instead
	// of relying on cleanup.
	collection := "probe_" + strconv.FormatInt(time.Now().UnixNano(), 36)

	begin := func() string {
		w := httptest.NewRecorder()
		r := personalRequest(identity, `{}`)
		r.SetPathValue("id", connID)
		s.mongoBeginTransaction(w, r)
		if w.Code != 200 {
			t.Fatalf("begin: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			TxnID string `json:"txnId"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.TxnID
	}
	insert := func(txnID, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := personalRequest(identity, body)
		r.SetPathValue("id", connID)
		r.SetPathValue("txn_id", txnID)
		s.mongoTxnInsert(w, r)
		if w.Code != 200 {
			t.Fatalf("insert: %d %s", w.Code, w.Body.String())
		}
		// A prior bug swallowed a bson.MarshalExtJSON error on the
		// InsertedID, giving a 200 with an empty response body.
		var parsed struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil || len(parsed.ID) == 0 {
			t.Fatalf("insert response has no usable id: %s (err=%v)", w.Body.String(), err)
		}
	}
	findCount := func() int {
		t.Helper()
		w := httptest.NewRecorder()
		r := personalRequest(identity, fmt.Sprintf(`{"collection":%q,"limit":10}`, collection))
		r.SetPathValue("id", connID)
		s.mongoFind(w, r)
		if w.Code != 200 {
			t.Fatalf("find: %d %s", w.Code, w.Body.String())
		}
		var found struct {
			Documents []json.RawMessage `json:"documents"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil {
			t.Fatal(err)
		}
		return len(found.Documents)
	}

	// Rollback: the insert must not be visible afterward.
	txnID := begin()
	insert(txnID, fmt.Sprintf(`{"collection":%q,"document":{"x":1}}`, collection))
	w = httptest.NewRecorder()
	r := personalRequest(identity, `{}`)
	r.SetPathValue("id", connID)
	r.SetPathValue("txn_id", txnID)
	s.mongoRollbackTransaction(w, r)
	if w.Code != 204 {
		t.Fatalf("rollback: %d %s", w.Code, w.Body.String())
	}
	if count := findCount(); count != 0 {
		t.Fatalf("rollback did not revert the insert via the API: %d docs", count)
	}

	// Commit: the insert must persist.
	txnID = begin()
	insert(txnID, fmt.Sprintf(`{"collection":%q,"document":{"x":2}}`, collection))
	w = httptest.NewRecorder()
	r = personalRequest(identity, `{}`)
	r.SetPathValue("id", connID)
	r.SetPathValue("txn_id", txnID)
	s.mongoCommitTransaction(w, r)
	if w.Code != 204 {
		t.Fatalf("commit: %d %s", w.Code, w.Body.String())
	}
	if count := findCount(); count != 1 {
		t.Fatalf("commit did not persist via the API: %d docs", count)
	}

	// A transaction begun on another database writes there, and its row
	// backup names that database, not the connection's default one.
	w = httptest.NewRecorder()
	r = personalRequest(identity, `{"database":"rowset_api_tx_other"}`)
	r.SetPathValue("id", connID)
	s.mongoBeginTransaction(w, r)
	var other struct {
		TxnID string `json:"txnId"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &other) != nil {
		t.Fatalf("begin on another database: %d %s", w.Code, w.Body.String())
	}
	insert(other.TxnID, fmt.Sprintf(`{"collection":%q,"document":{"x":3}}`, collection))
	w = httptest.NewRecorder()
	r = personalRequest(identity, fmt.Sprintf(`{"collection":%q,"filter":{"x":3},"update":{"$set":{"x":4}},"backup":true}`, collection))
	r.SetPathValue("id", connID)
	r.SetPathValue("txn_id", other.TxnID)
	s.mongoTxnUpdate(w, r)
	if w.Code != 200 {
		t.Fatalf("update on another database: %d %s", w.Code, w.Body.String())
	}
	list := httptest.NewRecorder()
	s.listRowBackups(list, personalRequest(identity, ""))
	var listed struct {
		Backups []struct{ Database string }
	}
	if json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Backups) == 0 || listed.Backups[0].Database != "rowset_api_tx_other" {
		t.Fatalf("backup database: %s", list.Body.String())
	}
	w = httptest.NewRecorder()
	r = personalRequest(identity, `{}`)
	r.SetPathValue("id", connID)
	r.SetPathValue("txn_id", other.TxnID)
	s.mongoRollbackTransaction(w, r)
	if w.Code != 204 {
		t.Fatalf("rollback on another database: %d %s", w.Code, w.Body.String())
	}
}
