package store

import (
	"context"
	"database/sql"
	"errors"
)

// RowBackup holds the rows a single-table UPDATE or DELETE was about to
// change. Ciphertext carries the statement and the rows.
type RowBackup struct {
	ID, OrgID, UserID, ConnectionID, Database, Schema, Table, Kind string
	Rows                                                           int64
	Ciphertext, Nonce                                              []byte
	CreatedAt                                                      string
}

const rowBackupColumns = "id, org_id, user_id, connection_id, database_name, schema_name, table_name, kind, row_count, ciphertext, nonce, created_at"

// rowBackupsKept is how many backups each user keeps; older ones are removed.
const rowBackupsKept = 100

func scanRowBackup(scanner interface{ Scan(...any) error }) (RowBackup, error) {
	var item RowBackup
	err := scanner.Scan(&item.ID, &item.OrgID, &item.UserID, &item.ConnectionID, &item.Database, &item.Schema, &item.Table, &item.Kind, &item.Rows, &item.Ciphertext, &item.Nonce, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

// CreateRowBackup stores a backup and keeps the user's newest ones.
func (s *Store) CreateRowBackup(ctx context.Context, item RowBackup) error {
	if _, err := s.db.ExecContext(ctx, "INSERT INTO row_backups("+rowBackupColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", item.ID, item.OrgID, item.UserID, item.ConnectionID, item.Database, item.Schema, item.Table, item.Kind, item.Rows, item.Ciphertext, item.Nonce, item.CreatedAt); err != nil {
		return mapError(err)
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM row_backups WHERE user_id = ?1 AND id NOT IN (SELECT id FROM row_backups WHERE user_id = ?1 ORDER BY created_at DESC LIMIT ?2)", item.UserID, rowBackupsKept)
	return err
}

func (s *Store) ListRowBackups(ctx context.Context, userID string) ([]RowBackup, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+rowBackupColumns+" FROM row_backups WHERE user_id = ? ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RowBackup{}
	for rows.Next() {
		item, err := scanRowBackup(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RowBackup(ctx context.Context, id, userID string) (RowBackup, error) {
	return scanRowBackup(s.db.QueryRowContext(ctx, "SELECT "+rowBackupColumns+" FROM row_backups WHERE id = ? AND user_id = ?", id, userID))
}

// OrgRowBackup reads a backup belonging to anyone in the organisation. It is
// for administrators: on a shared installation the person who can approve a
// write is the one who has to be able to put it back.
func (s *Store) OrgRowBackup(ctx context.Context, id, orgID string) (RowBackup, error) {
	return scanRowBackup(s.db.QueryRowContext(ctx, "SELECT "+rowBackupColumns+" FROM row_backups WHERE id = ? AND org_id = ?", id, orgID))
}

// ListOrgRowBackups lists every backup in the organisation, newest first.
func (s *Store) ListOrgRowBackups(ctx context.Context, orgID string) ([]RowBackup, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+rowBackupColumns+" FROM row_backups WHERE org_id = ? ORDER BY created_at DESC", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RowBackup{}
	for rows.Next() {
		item, err := scanRowBackup(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteRowBackup(ctx context.Context, id, userID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM row_backups WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return err
	}
	return requireChanged(result)
}
