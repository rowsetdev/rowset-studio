package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
)

const currentAuditHashVersion int64 = 2

func auditHash(version int64, previous string, item domain.AuditLog) string {
	values := []string{previous, item.ID, item.UserID, item.OrgID, item.ConnectionID, item.QueryHash, item.StartedAt, item.PolicyDecision, item.Reference}
	if version == 2 {
		values = append(values, item.SQL, item.ErrorMessage, item.ClientIP, optionalNumber(item.RowsReturned, item.RowsReturnedSet), optionalNumber(item.RowsAffected, item.RowsAffectedSet))
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "|")))
	return hex.EncodeToString(sum[:])
}

func PrepareAudit(previous string, item domain.AuditLog) domain.AuditLog {
	item.PreviousHash = previous
	item.HashVersion = currentAuditHashVersion
	item.EntryHash = auditHash(item.HashVersion, item.PreviousHash, item)
	return item
}

func VerifyPreparedAudit(version int64, previous, expected string, item domain.AuditLog) bool {
	return (version == 1 || version == currentAuditHashVersion) && auditHash(version, previous, item) == expected
}

// ChainBreak identifies the first audit entry whose hash no longer matches
// what it was written with — either the entry itself or the entry it chains
// from was altered after the fact.
type ChainBreak struct {
	OrgID   string
	AuditID string
}

const auditChainScanQuery = "SELECT id, org_id, user_id, connection_id, query_hash, started_at, policy_decision, sql, error_message, client_ip, rows_returned, rows_affected, prev_hash, entry_hash, hash_version FROM audit_logs WHERE org_id IS NOT NULL AND entry_hash IS NOT NULL ORDER BY org_id, rowid"

// VerifyAuditChain walks every organization's audit hash chain in insertion
// order and confirms each entry's hash still matches what PrepareAudit wrote
// for it, chained from the previous entry's hash. It reports the first break
// per organization (there is no point reporting every entry after a tampered
// one, since a single edit invalidates the rest of that org's chain too) and
// how many entries were checked in total.
func (s *Store) VerifyAuditChain(ctx context.Context) ([]ChainBreak, int, error) {
	rows, err := s.db.QueryContext(ctx, auditChainScanQuery)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var breaks []ChainBreak
	broken := map[string]bool{}
	previous := map[string]string{}
	checked := 0
	for rows.Next() {
		var item domain.AuditLog
		var orgID, userID, connectionID, queryHash, startedAt, policyDecision, sqlText, errorMessage, clientIP, prevHash sql.NullString
		var rowsReturned, rowsAffected sql.NullInt64
		if err := rows.Scan(&item.ID, &orgID, &userID, &connectionID, &queryHash, &startedAt, &policyDecision, &sqlText, &errorMessage, &clientIP, &rowsReturned, &rowsAffected, &prevHash, &item.EntryHash, &item.HashVersion); err != nil {
			return nil, 0, err
		}
		item.OrgID, item.UserID, item.ConnectionID, item.QueryHash = orgID.String, userID.String, connectionID.String, queryHash.String
		item.StartedAt, item.PolicyDecision, item.SQL, item.ErrorMessage, item.ClientIP = startedAt.String, policyDecision.String, sqlText.String, errorMessage.String, clientIP.String
		item.RowsReturned, item.RowsReturnedSet = rowsReturned.Int64, rowsReturned.Valid
		item.RowsAffected, item.RowsAffectedSet = rowsAffected.Int64, rowsAffected.Valid
		checked++
		if broken[item.OrgID] {
			continue
		}
		expectedPrevious := previous[item.OrgID]
		if prevHash.String != expectedPrevious || !VerifyPreparedAudit(item.HashVersion, prevHash.String, item.EntryHash, item) {
			broken[item.OrgID] = true
			breaks = append(breaks, ChainBreak{OrgID: item.OrgID, AuditID: item.ID})
			continue
		}
		previous[item.OrgID] = item.EntryHash
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return breaks, checked, nil
}
func optionalNumber(value int64, set bool) string {
	if !set {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

// auditReferenceColumn names the column that stores AuditLog.Reference; the
// reference is not stored when it is empty.
var auditReferenceColumn string

func (s *Store) WriteAudit(ctx context.Context, item domain.AuditLog) error {
	return s.WriteActivity(ctx, []domain.AuditLog{item}, nil)
}

const auditChainQuery = "SELECT entry_hash FROM audit_logs WHERE org_id=? AND entry_hash IS NOT NULL ORDER BY rowid DESC LIMIT 1"

// WriteActivity stores audit entries and history rows in one transaction.
// The chain head of each organization is read once and carried forward in
// memory, so a batch costs one lookup and one commit however many rows it
// holds. Entries are chained in the order given.
func (s *Store) WriteActivity(ctx context.Context, audits []domain.AuditLog, histories []domain.QueryHistory) error {
	if len(audits) == 0 && len(histories) == 0 {
		return nil
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// IMMEDIATE takes the write lock before the chain head is read, so no
	// other writer can commit between the read and the inserts; waiting for
	// the lock goes through busy_timeout like any other write.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return mapError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	heads := map[string]string{}
	for _, item := range audits {
		previous := ""
		if item.OrgID != "" {
			head, known := heads[item.OrgID]
			if !known {
				var value sql.NullString
				if err := conn.QueryRowContext(ctx, auditChainQuery, item.OrgID).Scan(&value); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return mapError(err)
				}
				head = value.String
			}
			previous = head
		}
		item = PrepareAudit(previous, item)
		if item.OrgID != "" {
			heads[item.OrgID] = item.EntryHash
		}
		columns := "id,user_id,org_id,role,connection_id,sql,normalized_sql,query_hash,client_type,client_ip,user_agent,started_at,duration_ms,rows_returned,rows_affected,policy_decision,policy_reason,policy_id,error_message,created_at,prev_hash,entry_hash,hash_version"
		args := []any{item.ID, nullText(item.UserID), nullText(item.OrgID), nullText(item.Role), nullText(item.ConnectionID), nullText(item.SQL), nullText(item.NormalizedSQL), nullText(item.QueryHash), nullText(item.ClientType), nullText(item.ClientIP), nullText(item.UserAgent), nullText(item.StartedAt), item.DurationMS, nullableNumber(item.RowsReturned, item.RowsReturnedSet), nullableNumber(item.RowsAffected, item.RowsAffectedSet), nullText(item.PolicyDecision), nullText(item.PolicyReason), nullText(item.PolicyID), nullText(item.ErrorMessage), item.CreatedAt, nullText(item.PreviousHash), item.EntryHash, item.HashVersion}
		if auditReferenceColumn != "" {
			columns += "," + auditReferenceColumn
			args = append(args, nullText(item.Reference))
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO audit_logs("+columns+") VALUES("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...); err != nil {
			return mapError(err)
		}
	}
	for _, item := range histories {
		if _, err := conn.ExecContext(ctx, insertQueryHistory, item.ID, item.UserID, item.ConnectionID, item.SQL, item.NormalizedSQL, item.QueryHash, item.Status, item.RowsReturned, item.DurationMS, item.CreatedAt); err != nil {
			return mapError(err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return mapError(err)
	}
	committed = true
	return nil
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableNumber(value int64, set bool) any {
	if !set {
		return nil
	}
	return value
}
