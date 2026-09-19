package store

import (
	"context"
	"database/sql"
)

func (s *Store) CreateRefreshToken(ctx context.Context, id, userID, tokenHash, expiresAt string, sessionVersion int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO refresh_tokens(id,user_id,token_hash,expires_at,session_version) VALUES(?,?,?,?,?)", id, userID, tokenHash, expiresAt, sessionVersion)
	return mapError(err)
}

func (s *Store) ConsumeRefreshToken(ctx context.Context, tokenHash string) (string, int64, error) {
	var userID string
	var sessionVersion int64
	err := s.db.QueryRowContext(ctx, "UPDATE refresh_tokens SET revoked=1 WHERE token_hash=? AND revoked=0 AND expires_at>datetime('now') RETURNING user_id,session_version", tokenHash).Scan(&userID, &sessionVersion)
	if err != nil {
		return "", 0, mapError(err)
	}
	return userID, sessionVersion, nil
}

func (s *Store) RevokeRefreshTokensForUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE refresh_tokens SET revoked=1 WHERE user_id=? AND revoked=0", userID)
	return err
}

func (s *Store) UserSessionVersion(ctx context.Context, userID string) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx, "SELECT version FROM user_session_versions WHERE user_id=?", userID).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return version, err
}

func (s *Store) BumpUserSessionVersion(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO user_session_versions(user_id,version) VALUES(?,1) ON CONFLICT(user_id) DO UPDATE SET version=version+1", userID)
	return err
}
