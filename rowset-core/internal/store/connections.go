package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

const connectionColumns = "id,org_id,name,alias,engine,host,port,database,environment,tls_required,tech_username,secret_id,created_at,query_timeout_seconds,tls_mode,tls_server_name,tls_ca_pem,tls_client_cert_pem,tls_client_key_secret_id,ssh_host,ssh_port,ssh_user,ssh_auth_method,ssh_known_host,ssh_secret_id,ssh_passphrase_secret_id,read_only,cassandra_consistency,cassandra_page_size"
const connectionNodeColumns = "id,connection_id,name,host,port,detected_role,health,read_only,last_checked_at,last_error,created_at"

func scanConnection(scanner interface{ Scan(...any) error }) (domain.Connection, error) {
	var c domain.Connection
	var alias sql.NullString
	var tls, readOnly int64
	err := scanner.Scan(&c.ID, &c.OrgID, &c.Name, &alias, &c.Engine, &c.Host, &c.Port, &c.Database, &c.Environment, &tls, &c.ConnectionUsername, &c.SecretID, &c.CreatedAt, &c.QueryTimeoutSeconds, &c.TLSMode, &c.TLSServerName, &c.TLSCAPEM, &c.TLSClientCertPEM, &c.TLSClientKeySecret, &c.SSHHost, &c.SSHPort, &c.SSHUser, &c.SSHAuthMethod, &c.SSHKnownHost, &c.SSHSecretID, &c.SSHPassphraseSecretID, &readOnly, &c.CassandraConsistency, &c.CassandraPageSize)
	if err != nil {
		return c, mapError(err)
	}
	c.TLSRequired = tls != 0
	c.ReadOnly = readOnly != 0
	if c.TLSMode == "" {
		c.TLSMode = domain.LegacyTLSMode(c.Engine, c.TLSRequired)
	}
	if alias.Valid {
		c.Alias = &alias.String
	}
	return c, nil
}

func scanConnectionNode(scanner interface{ Scan(...any) error }) (domain.ConnectionNode, error) {
	var node domain.ConnectionNode
	var readOnly int64
	var checked, lastError sql.NullString
	err := scanner.Scan(&node.ID, &node.ConnectionID, &node.Name, &node.Host, &node.Port, &node.DetectedRole, &node.Health, &readOnly, &checked, &lastError, &node.CreatedAt)
	if err != nil {
		return node, mapError(err)
	}
	node.ReadOnly = readOnly != 0
	if checked.Valid {
		node.LastCheckedAt = &checked.String
	}
	if lastError.Valid {
		node.LastError = &lastError.String
	}
	return node, nil
}

func (s *Store) ListConnections(ctx context.Context, orgID string) ([]domain.Connection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+connectionColumns+" FROM connections WHERE org_id=? ORDER BY created_at DESC", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Connection
	for rows.Next() {
		item, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Connection(ctx context.Context, id string) (domain.Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx, "SELECT "+connectionColumns+" FROM connections WHERE id=?", id))
}

func (s *Store) CreateConnection(ctx context.Context, c domain.Connection) error {
	mode := c.EffectiveTLSMode()
	_, err := s.db.ExecContext(ctx, "INSERT INTO connections("+connectionColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", c.ID, c.OrgID, c.Name, c.Alias, c.Engine, c.Host, c.Port, c.Database, c.Environment, mode != "disable", c.ConnectionUsername, c.SecretID, c.CreatedAt, c.QueryTimeoutSeconds, mode, c.TLSServerName, c.TLSCAPEM, c.TLSClientCertPEM, c.TLSClientKeySecret, c.SSHHost, c.SSHPort, c.SSHUser, c.SSHAuthMethod, c.SSHKnownHost, c.SSHSecretID, c.SSHPassphraseSecretID, c.ReadOnly, c.CassandraConsistency, c.CassandraPageSize)
	return mapError(err)
}

func (s *Store) UpdateConnection(ctx context.Context, c domain.Connection) error {
	mode := c.EffectiveTLSMode()
	result, err := s.db.ExecContext(ctx, `UPDATE connections SET name=?,alias=?,engine=?,host=?,port=?,database=?,environment=?,tls_required=?,tech_username=?,secret_id=?,query_timeout_seconds=?,tls_mode=?,tls_server_name=?,tls_ca_pem=?,tls_client_cert_pem=?,tls_client_key_secret_id=?,ssh_host=?,ssh_port=?,ssh_user=?,ssh_auth_method=?,ssh_known_host=?,ssh_secret_id=?,ssh_passphrase_secret_id=?,read_only=?,cassandra_consistency=?,cassandra_page_size=? WHERE id=? AND org_id=?`, c.Name, c.Alias, c.Engine, c.Host, c.Port, c.Database, c.Environment, mode != "disable", c.ConnectionUsername, c.SecretID, c.QueryTimeoutSeconds, mode, c.TLSServerName, c.TLSCAPEM, c.TLSClientCertPEM, c.TLSClientKeySecret, c.SSHHost, c.SSHPort, c.SSHUser, c.SSHAuthMethod, c.SSHKnownHost, c.SSHSecretID, c.SSHPassphraseSecretID, c.ReadOnly, c.CassandraConsistency, c.CassandraPageSize, c.ID, c.OrgID)
	if err != nil {
		return mapError(err)
	}
	return requireChanged(result)
}

// connectionTables hold rows that belong to a single connection.
var connectionTables = []string{"role_connection_access", "saved_queries", "query_history", "custom_policies", "policies", "scheduled_queries", "row_backups"}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range connectionTables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE connection_id=?", id); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM connections WHERE id=?", id)
	if err != nil {
		return err
	}
	if err := requireChanged(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListConnectionNodes(ctx context.Context, connectionID string) ([]domain.ConnectionNode, error) {
	return s.listNodes(ctx, " WHERE connection_id=? ORDER BY name", connectionID)
}

func (s *Store) ListAllConnectionNodes(ctx context.Context) ([]domain.ConnectionNode, error) {
	return s.listNodes(ctx, " ORDER BY connection_id,name")
}

func (s *Store) listNodes(ctx context.Context, suffix string, args ...any) ([]domain.ConnectionNode, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+connectionNodeColumns+" FROM connection_nodes"+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ConnectionNode
	for rows.Next() {
		item, err := scanConnectionNode(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ReplaceConnectionNodes(ctx context.Context, connectionID string, nodes []domain.ConnectionNode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM connection_nodes WHERE connection_id=?", connectionID); err != nil {
		return err
	}
	for _, node := range nodes {
		if _, err := tx.ExecContext(ctx, "INSERT INTO connection_nodes("+connectionNodeColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?)", node.ID, connectionID, node.Name, node.Host, node.Port, node.DetectedRole, node.Health, node.ReadOnly, node.LastCheckedAt, node.LastError, node.CreatedAt); err != nil {
			return mapError(err)
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateConnectionNodeStatus(ctx context.Context, id, role, health string, readOnly bool, lastError *string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE connection_nodes SET detected_role=?,health=?,read_only=?,last_checked_at=?,last_error=? WHERE id=?", role, health, readOnly, NowString(), lastError, id)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func (s *Store) CreateSecret(ctx context.Context, secret domain.Secret) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO secrets(id,ciphertext,nonce) VALUES(?,?,?)", secret.ID, secret.Ciphertext, secret.Nonce)
	return mapError(err)
}

func (s *Store) SecretForConnection(ctx context.Context, orgID, connectionID string) (domain.Secret, error) {
	var secret domain.Secret
	err := s.db.QueryRowContext(ctx, "SELECT s.id,s.ciphertext,s.nonce FROM secrets s JOIN connections c ON c.secret_id=s.id WHERE c.org_id=? AND c.id=?", orgID, connectionID).Scan(&secret.ID, &secret.Ciphertext, &secret.Nonce)
	return secret, mapError(err)
}

func (s *Store) TLSClientKeySecretForConnection(ctx context.Context, orgID, connectionID string) (domain.Secret, error) {
	var secret domain.Secret
	err := s.db.QueryRowContext(ctx, "SELECT s.id,s.ciphertext,s.nonce FROM secrets s JOIN connections c ON c.tls_client_key_secret_id=s.id WHERE c.org_id=? AND c.id=?", orgID, connectionID).Scan(&secret.ID, &secret.Ciphertext, &secret.Nonce)
	return secret, mapError(err)
}

func (s *Store) SSHSecretForConnection(ctx context.Context, orgID, connectionID string) (domain.Secret, error) {
	var secret domain.Secret
	err := s.db.QueryRowContext(ctx, "SELECT s.id,s.ciphertext,s.nonce FROM secrets s JOIN connections c ON c.ssh_secret_id=s.id WHERE c.org_id=? AND c.id=?", orgID, connectionID).Scan(&secret.ID, &secret.Ciphertext, &secret.Nonce)
	return secret, mapError(err)
}

func (s *Store) SSHPassphraseSecretForConnection(ctx context.Context, orgID, connectionID string) (domain.Secret, error) {
	var secret domain.Secret
	err := s.db.QueryRowContext(ctx, "SELECT s.id,s.ciphertext,s.nonce FROM secrets s JOIN connections c ON c.ssh_passphrase_secret_id=s.id WHERE c.org_id=? AND c.id=?", orgID, connectionID).Scan(&secret.ID, &secret.Ciphertext, &secret.Nonce)
	return secret, mapError(err)
}

func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM secrets WHERE id=?", id)
	return err
}

func placeholders(count int) string { return strings.TrimRight(strings.Repeat("?,", count), ",") }
