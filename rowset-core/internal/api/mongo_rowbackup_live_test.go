package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveMongoRowBackupUpdateAndDeleteRestore exercises the MongoDB
// row-backup path end to end through the HTTP handlers: an update with
// backup:true captures the pre-update document, restoring it puts the
// original field values back (not just re-running the $set); a delete with
// backup:true captures the document and restoring it re-inserts it. Uses
// the standalone ROWSET_TEST_MONGODB_HOST fixture: this path needs no
// transaction/replica-set support, unlike the manual-commit feature.
func TestLiveMongoRowBackupUpdateAndDeleteRestore(t *testing.T) {
	host := os.Getenv("ROWSET_TEST_MONGODB_HOST")
	if host == "" {
		t.Skip("mongodb test server not configured")
	}
	port := 57017
	if v := os.Getenv("ROWSET_MATRIX_MONGODB_PORT"); v != "" {
		var err error
		if port, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
	}

	s, identity := personalServer(t)
	body, _ := json.Marshal(map[string]any{"name": "mongo-backup", "engine": "mongodb", "host": host, "port": port, "database": "rowset_test", "tlsMode": "disable"})
	w := httptest.NewRecorder()
	s.createConnection(w, personalRequest(identity, string(body)))
	var connection struct{ ID string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &connection) != nil {
		t.Fatalf("connection: %d %s", w.Code, w.Body.String())
	}
	connID := connection.ID
	// A run-specific collection name avoids collisions with a previous run's
	// leftover data instead of relying on cleanup.
	collection := "rowset_backup_probe_" + strconv.FormatInt(time.Now().UnixNano(), 36)

	insert := func(doc string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := personalRequest(identity, `{"collection":"`+collection+`","document":`+doc+`}`)
		r.SetPathValue("id", connID)
		s.mongoInsert(w, r)
		if w.Code != 200 {
			t.Fatalf("insert: %d %s", w.Code, w.Body.String())
		}
	}
	findOne := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r := personalRequest(identity, `{"collection":"`+collection+`","filter":{"_id":1},"limit":10}`)
		r.SetPathValue("id", connID)
		s.mongoFind(w, r)
		var found struct {
			Documents []json.RawMessage `json:"documents"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil || len(found.Documents) != 1 {
			t.Fatalf("find: %d %s (err=%v)", w.Code, w.Body.String(), err)
		}
		var doc map[string]any
		if err := json.Unmarshal(found.Documents[0], &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}

	// --- Update + restore ---
	insert(`{"_id":1,"x":"original"}`)
	w = httptest.NewRecorder()
	r := personalRequest(identity, `{"collection":"`+collection+`","filter":{"_id":1},"update":{"$set":{"x":"changed"}},"backup":true}`)
	r.SetPathValue("id", connID)
	s.mongoUpdate(w, r)
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var updateResp struct {
		Backup struct {
			ID   string `json:"id"`
			Rows int64  `json:"rows"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updateResp); err != nil || updateResp.Backup.ID == "" {
		t.Fatalf("update response carries no backup: %s (err=%v)", w.Body.String(), err)
	}
	if updateResp.Backup.Rows != 1 {
		t.Fatalf("backup rows: %d", updateResp.Backup.Rows)
	}
	if doc := findOne(); doc["x"] != "changed" {
		t.Fatalf("update did not apply: %#v", doc)
	}
	// The generated script must be readable Mongo shell syntax, not SQL.
	w = httptest.NewRecorder()
	r = personalRequest(identity, "")
	r.SetPathValue("id", updateResp.Backup.ID)
	s.rowBackupRestore(w, r)
	if w.Code != 200 {
		t.Fatalf("restore script: %d %s", w.Code, w.Body.String())
	}
	var scriptResp struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &scriptResp); err != nil || !strings.Contains(scriptResp.SQL, "replaceOne") {
		t.Fatalf("restore script missing replaceOne: %s", scriptResp.SQL)
	}
	w = httptest.NewRecorder()
	r = personalRequest(identity, "")
	r.SetPathValue("id", updateResp.Backup.ID)
	s.applyRowBackup(w, r)
	if w.Code != 200 {
		t.Fatalf("apply restore: %d %s", w.Code, w.Body.String())
	}
	if doc := findOne(); doc["x"] != "original" {
		t.Fatalf("restore did not revert the update: %#v", doc)
	}

	// --- Delete + restore ---
	w = httptest.NewRecorder()
	r = personalRequest(identity, `{"collection":"`+collection+`","filter":{"_id":1},"backup":true}`)
	r.SetPathValue("id", connID)
	s.mongoDelete(w, r)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	var deleteResp struct {
		Backup struct{ ID string } `json:"backup"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &deleteResp); err != nil || deleteResp.Backup.ID == "" {
		t.Fatalf("delete response carries no backup: %s (err=%v)", w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	r = personalRequest(identity, "")
	r.SetPathValue("id", deleteResp.Backup.ID)
	s.applyRowBackup(w, r)
	if w.Code != 200 {
		t.Fatalf("apply restore: %d %s", w.Code, w.Body.String())
	}
	if doc := findOne(); doc["x"] != "original" {
		t.Fatalf("restore did not re-insert the deleted document: %#v", doc)
	}
}
