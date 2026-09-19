package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// elasticsearchRowBackupPayload mirrors mongoBackupPayload's role for
// Elasticsearch: the document a WHERE-equivalent id matched right before an
// update or delete, kept whole so a restore is a full re-index rather than
// a field-by-field replay of the update.
type elasticsearchRowBackupPayload struct {
	BackupID  string          `json:"backupId"`
	UserID    string          `json:"userId"`
	Statement string          `json:"statement"`
	Database  string          `json:"database"`
	Index     string          `json:"index"`
	DocID     string          `json:"docId"`
	Source    json.RawMessage `json:"source,omitempty"`
}

// elasticsearchCaptureBackup fetches a document's current _source before an
// update or delete changes it. It returns nil (nothing to back up, not an
// error) when the document doesn't exist yet — an update/delete on a
// missing id fails on its own before this would ever be applied.
func (s *Server) elasticsearchCaptureBackup(ctx context.Context, identity domain.Identity, connection domain.Connection, target engine.Connection, database, index, docID, kind, statement string) Annotations {
	if s.vault == nil {
		return nil
	}
	source, exists, err := s.engines.ElasticsearchGet(ctx, target, index, docID)
	if err != nil {
		return Annotations{"backupSkipped": "the previous document could not be read: " + err.Error()}
	}
	if !exists {
		return nil
	}
	payload := elasticsearchRowBackupPayload{BackupID: id.New(), UserID: identity.UserID, Statement: statement, Database: database, Index: index, DocID: docID, Source: source}
	plain, err := json.Marshal(payload)
	if err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	ciphertext, nonce, err := s.vault.Encrypt(plain)
	if err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	item := store.RowBackup{ID: payload.BackupID, OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: connection.ID, Database: database, Schema: "", Table: index, Kind: kind, Rows: 1, Ciphertext: ciphertext, Nonce: nonce, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.store.CreateRowBackup(context.Background(), item); err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	return Annotations{"backup": map[string]any{"id": item.ID, "rows": int64(1)}}
}

func (s *Server) openElasticsearchRowBackup(item store.RowBackup) (elasticsearchRowBackupPayload, error) {
	var payload elasticsearchRowBackupPayload
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

// elasticsearchRestoreScript is restoreSQL's counterpart: for review only,
// the same way mongoRestoreScript is (there's no free-text Elasticsearch
// query/write console to run this in either).
func elasticsearchRestoreScript(item store.RowBackup, payload elasticsearchRowBackupPayload) string {
	var out strings.Builder
	fmt.Fprintf(&out, "// Restores document %q of %q backed up before this %s:\n", payload.DocID, payload.Index, strings.ToUpper(item.Kind))
	for _, line := range strings.Split(strings.TrimSpace(payload.Statement), "\n") {
		out.WriteString("//   " + line + "\n")
	}
	out.WriteString("// For review only: re-indexes the document exactly as it was.\n// Use Activity → Row backups → Restore to actually apply it.\n\n")
	fmt.Fprintf(&out, "{\"index\":%q,\"id\":%q,\"document\":%s}\n", payload.Index, payload.DocID, payload.Source)
	return out.String()
}

// applyElasticsearchRowBackup is applyRowBackup for an Elasticsearch backup:
// same auth and policy checks, but the restore is a full re-index of the
// captured document instead of parsed SQL statements.
func (s *Server) applyElasticsearchRowBackup(w http.ResponseWriter, r *http.Request, identity domain.Identity, connection domain.Connection, item store.RowBackup) {
	payload, err := s.openElasticsearchRowBackup(item)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
		return
	}
	info := nosqlStatement(sqlguard.Update, payload.Database, payload.Index, true)
	timeout, allowed := s.nosqlPolicyAllowed(w, r, connection, info, payload.Statement)
	if !allowed {
		return
	}
	target, err := s.engineConnection(r, connection, payload.Database)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	defer cancel()
	started := time.Now()
	_, err = s.engines.ElasticsearchIndex(ctx, target, engine.ElasticsearchIndexInput{Index: payload.Index, ID: payload.DocID, Document: payload.Source})
	duration := elapsedMilliseconds(started)
	statement := fmt.Sprintf("-- Row backup restore: document %s of %s\n%s", payload.DocID, payload.Index, payload.Statement)
	if err != nil {
		s.recordActivity(r, connection.ID, statement, "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, statement, "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, http.StatusOK, map[string]any{"rows": 1})
}
