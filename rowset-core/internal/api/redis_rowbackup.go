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

// redisRowBackupPayload mirrors rowBackupPayload's role for Redis: the exact
// state of one key right before a write or delete, kept as an opaque DUMP
// payload (see engine.RedisSnapshot) rather than a parsed value, so it
// restores byte-for-byte regardless of type.
type redisRowBackupPayload struct {
	BackupID  string `json:"backupId"`
	UserID    string `json:"userId"`
	Statement string `json:"statement"`
	Database  string `json:"database"`
	Key       string `json:"key"`
	Existed   bool   `json:"existed"`
	Dump      string `json:"dump,omitempty"`
	TTLMs     int64  `json:"ttlMs,omitempty"`
}

// redisCaptureBackup snapshots a key before a write or delete touches it.
// Unlike SQL/Mongo/Cassandra there is no "matched rows" count to check
// against a limit — Redis writes and deletes only ever target one key — so
// this only skips when the key can't be read, or (for a delete) when there
// was nothing there to delete in the first place.
func (s *Server) redisCaptureBackup(ctx context.Context, identity domain.Identity, connection domain.Connection, target engine.Connection, database, key, kind, statement string) Annotations {
	if s.config.Shared || s.vault == nil {
		return nil
	}
	snapshot, err := s.engines.RedisSnapshotKey(ctx, target, key)
	if err != nil {
		return Annotations{"backupSkipped": "the previous value could not be read: " + err.Error()}
	}
	if kind == "delete" && !snapshot.Existed {
		return nil
	}
	payload := redisRowBackupPayload{BackupID: id.New(), UserID: identity.UserID, Statement: statement, Database: database, Key: key, Existed: snapshot.Existed, Dump: snapshot.Dump, TTLMs: snapshot.TTLMs}
	plain, err := json.Marshal(payload)
	if err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	ciphertext, nonce, err := s.vault.Encrypt(plain)
	if err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	item := store.RowBackup{ID: payload.BackupID, OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: connection.ID, Database: database, Schema: "", Table: key, Kind: kind, Rows: 1, Ciphertext: ciphertext, Nonce: nonce, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.store.CreateRowBackup(context.Background(), item); err != nil {
		return Annotations{"backupSkipped": "the backup could not be stored"}
	}
	return Annotations{"backup": map[string]any{"id": item.ID, "rows": int64(1)}}
}

func (s *Server) openRedisRowBackup(item store.RowBackup) (redisRowBackupPayload, error) {
	var payload redisRowBackupPayload
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

// redisRestoreScript is restoreSQL's counterpart, for review only: DUMP's
// payload is Redis's own binary serialization, not something meaningful to
// print as a literal, and there is no free-text Redis command console to
// run it in anyway. It describes the restore instead of spelling it out.
func redisRestoreScript(item store.RowBackup, payload redisRowBackupPayload) string {
	if !payload.Existed {
		return fmt.Sprintf("# Key %q did not exist before this %s.\n# Restore removes it.\n# Use Activity → Row backups → Restore to actually apply it.\n", payload.Key, strings.ToUpper(item.Kind))
	}
	ttl := "no expiry"
	if payload.TTLMs > 0 {
		ttl = fmt.Sprintf("a %dms TTL", payload.TTLMs)
	}
	return fmt.Sprintf(
		"# Restores key %q (%s) backed up before this %s:\n#   %s\n# The previous value is stored as an opaque Redis DUMP payload — Redis's\n# own serialization, not a readable value — so there's nothing to show here.\n# Use Activity → Row backups → Restore to put it back exactly as it was.\n",
		payload.Key, ttl, strings.ToUpper(item.Kind), payload.Statement,
	)
}

// applyRedisRowBackup is applyRowBackup for a Redis backup: same auth and
// policy checks, but the restore runs through RESTORE (or DEL, if the key
// didn't exist before) instead of parsed SQL statements.
func (s *Server) applyRedisRowBackup(w http.ResponseWriter, r *http.Request, identity domain.Identity, connection domain.Connection, item store.RowBackup) {
	payload, err := s.openRedisRowBackup(item)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
		return
	}
	info := nosqlStatement(sqlguard.Update, payload.Database, payload.Key, true)
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
	err = s.engines.RedisRestoreSnapshot(ctx, target, payload.Key, engine.RedisSnapshot{Existed: payload.Existed, Dump: payload.Dump, TTLMs: payload.TTLMs})
	duration := elapsedMilliseconds(started)
	statement := fmt.Sprintf("-- Row backup restore: key %s\n%s", payload.Key, payload.Statement)
	if err != nil {
		s.recordActivity(r, connection.ID, statement, "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, statement, "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, http.StatusOK, map[string]any{"rows": 1})
}
