package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/id"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

// sqlKindFor classifies a row-backup restore for the policy engine:
// restoring a delete makes a document reappear (an insert), restoring an
// update replaces one in place (an update).
func sqlKindFor(backupKind string) sqlguard.Kind {
	if backupKind == "delete" {
		return sqlguard.Insert
	}
	return sqlguard.Update
}

// mongoBackupPayload mirrors rowBackupPayload's role for MongoDB: the
// documents a filter matched right before an update/delete, restored by
// putting each one back (insert for a delete, full replace by _id for an
// update — a partial $set update cannot be inverted field by field, so the
// whole pre-update document is kept instead).
type mongoBackupPayload struct {
	BackupID   string            `json:"backupId"`
	UserID     string            `json:"userId"`
	Statement  string            `json:"statement"`
	Collection string            `json:"collection"`
	Documents  []json.RawMessage `json:"documents"`
}

func (s *Server) mongoFinishBackupCapture(identity domain.Identity, connection domain.Connection, database, collection, kind, statement string, docs []json.RawMessage, truncated bool, findErr error) Annotations {
	if findErr != nil {
		return Annotations{"backupSkipped": "the changed documents could not be read: " + findErr.Error()}
	}
	if truncated {
		return Annotations{"backupBlocked": fmt.Sprintf("This %s changes more than %d documents, too many to back up.", strings.ToUpper(kind), rowBackupLimit)}
	}
	if len(docs) == 0 {
		return nil
	}
	payload := mongoBackupPayload{BackupID: id.New(), UserID: identity.UserID, Statement: statement, Collection: collection, Documents: docs}
	plain, err := json.Marshal(payload)
	if err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	ciphertext, nonce, err := s.vault.Encrypt(plain)
	if err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	item := store.RowBackup{ID: payload.BackupID, OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: connection.ID, Database: database, Schema: "", Table: collection, Kind: kind, Rows: int64(len(docs)), Ciphertext: ciphertext, Nonce: nonce, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.store.CreateRowBackup(context.Background(), item); err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	return Annotations{"backup": map[string]any{"id": item.ID, "rows": item.Rows}}
}

// mongoCaptureBackup fetches the documents a filter currently matches and
// stores them as a row backup before a standalone (non-transaction) write.
// Personal workspaces only, same as SQL row backups: a restore would bypass
// a shared server's result hooks.
func (s *Server) mongoCaptureBackup(ctx context.Context, identity domain.Identity, connection domain.Connection, target engine.Connection, database, collection, kind, statement string, filter json.RawMessage) Annotations {
	if s.config.Shared || s.vault == nil {
		return nil
	}
	docs, truncated, err := s.engines.MongoFind(ctx, target, engine.MongoFindInput{Collection: collection, Filter: filter, Limit: rowBackupLimit})
	return s.mongoFinishBackupCapture(identity, connection, database, collection, kind, statement, docs, truncated, err)
}

// mongoCaptureTxnBackup is mongoCaptureBackup for a write inside a manual
// transaction: the read runs through the transaction's own session, so it
// sees exactly the state the write that follows it is about to change.
func (s *Server) mongoCaptureTxnBackup(identity domain.Identity, connection domain.Connection, transaction *engine.MongoTransaction, database, collection, kind, statement string, filter json.RawMessage) Annotations {
	if s.config.Shared || s.vault == nil {
		return nil
	}
	docs, truncated, err := transaction.FindMany(engine.MongoFindInput{Collection: collection, Filter: filter, Limit: rowBackupLimit})
	return s.mongoFinishBackupCapture(identity, connection, database, collection, kind, statement, docs, truncated, err)
}

func (s *Server) openMongoRowBackup(item store.RowBackup) (mongoBackupPayload, error) {
	var payload mongoBackupPayload
	if s.vault == nil {
		return payload, errors.New("secret vault unavailable")
	}
	plain, err := s.vault.Decrypt(item.Ciphertext, item.Nonce)
	if err != nil {
		return payload, err
	}
	if err := json.Unmarshal(plain, &payload); err != nil || payload.BackupID != item.ID || payload.UserID != item.UserID {
		return payload, errors.New("the backup does not belong to this record")
	}
	return payload, nil
}

// mongoDocumentID extracts a document's _id as Extended JSON, for the
// {"_id": ...} filter a restore uses to put it back in place.
func mongoDocumentID(doc json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, err
	}
	id, ok := fields["_id"]
	if !ok {
		return nil, errors.New("document has no _id")
	}
	raw, err := json.Marshal(map[string]json.RawMessage{"_id": id})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// mongoRestoreScript is restoreSQL's counterpart: shell-syntax text for
// review, opened in the editor rather than applied directly.
func mongoRestoreScript(item store.RowBackup, payload mongoBackupPayload) string {
	var out strings.Builder
	fmt.Fprintf(&out, "// Restores %d document(s) of %s backed up before this %s:\n", item.Rows, payload.Collection, strings.ToUpper(item.Kind))
	for _, line := range strings.Split(strings.TrimSpace(payload.Statement), "\n") {
		out.WriteString("//   " + line + "\n")
	}
	out.WriteString("// For review only: the Studio query bar understands find()/aggregate(),\n// not these calls. Use Activity → Row backups → Restore to actually apply them.\n\n")
	for _, doc := range payload.Documents {
		if item.Kind == "delete" {
			fmt.Fprintf(&out, "db.%s.insertOne(%s);\n", payload.Collection, doc)
		} else {
			id, err := mongoDocumentID(doc)
			if err != nil {
				fmt.Fprintf(&out, "// skipped a document with no _id\n")
				continue
			}
			fmt.Fprintf(&out, "db.%s.replaceOne(%s, %s);\n", payload.Collection, id, doc)
		}
	}
	return out.String()
}

// applyMongoRowBackup is applyRowBackup for a MongoDB backup: same auth and
// policy checks, but the restore runs through the Mongo write path instead
// of parsed SQL statements.
func (s *Server) applyMongoRowBackup(w http.ResponseWriter, r *http.Request, identity domain.Identity, connection domain.Connection, item store.RowBackup) {
	payload, err := s.openMongoRowBackup(item)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
		return
	}
	if len(payload.Documents) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"rows": 0})
		return
	}
	kind := sqlKindFor(item.Kind)
	info := nosqlStatement(kind, item.Database, payload.Collection, true)
	timeout, allowed := s.nosqlPolicyAllowed(w, r, connection, info, payload.Statement)
	if !allowed {
		return
	}
	target, err := s.engineConnection(r, connection, item.Database)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	defer cancel()
	started := time.Now()
	restored, err := s.mongoApplyRestore(ctx, target, item, payload)
	duration := elapsedMilliseconds(started)
	statement := fmt.Sprintf("-- Row backup restore: %d document(s) of %s\n%s", item.Rows, payload.Collection, payload.Statement)
	if err != nil {
		s.recordActivity(r, connection.ID, statement, "error", restored, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, statement, "success", restored, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, http.StatusOK, map[string]any{"rows": restored})
}

// mongoApplyRestore puts the backed-up documents back, one write at a time.
// Unlike the SQL restore this is not wrapped in a single transaction:
// MongoDB transactions require a replica set, and a restore should still
// work against a standalone server. A failure partway through is reported
// with how many documents it did manage before stopping.
func (s *Server) mongoApplyRestore(ctx context.Context, connection engine.Connection, item store.RowBackup, payload mongoBackupPayload) (int64, error) {
	var restored int64
	for _, doc := range payload.Documents {
		var err error
		if item.Kind == "delete" {
			_, err = s.engines.MongoInsertOne(ctx, connection, engine.MongoInsertInput{Collection: payload.Collection, Document: doc})
		} else {
			var docID json.RawMessage
			docID, err = mongoDocumentID(doc)
			if err == nil {
				err = s.engines.MongoReplaceOne(ctx, connection, engine.MongoReplaceInput{Collection: payload.Collection, Filter: docID, Document: doc})
			}
		}
		if err != nil {
			return restored, fmt.Errorf("after restoring %d of %d document(s): %w", restored, len(payload.Documents), err)
		}
		restored++
	}
	return restored, nil
}
