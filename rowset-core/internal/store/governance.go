package store

import (
	"context"
	"database/sql"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func (s *Store) ListPolicyOverrides(ctx context.Context, orgID string) ([]domain.PolicyOverride, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT rule_type,enabled,config FROM policies WHERE org_id=?", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.PolicyOverride
	for rows.Next() {
		var item domain.PolicyOverride
		var enabled int64
		var config sql.NullString
		if err := rows.Scan(&item.Key, &enabled, &config); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		if config.Valid {
			item.Config = &config.String
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Store) SetPolicyOverride(ctx context.Context, id, orgID, key string, enabled bool, config *string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO policies(id,org_id,name,rule_type,effect,enabled,config) VALUES(?,?,?,?,'deny',?,?) ON CONFLICT(org_id,rule_type) DO UPDATE SET enabled=excluded.enabled,config=COALESCE(excluded.config,policies.config)", id, orgID, key, key, enabled, config)
	return mapError(err)
}
func (s *Store) DeletePolicyOverride(ctx context.Context, orgID, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM policies WHERE org_id=? AND rule_type=?", orgID, key)
	return mapError(err)
}

func scanCustomPolicy(scanner interface{ Scan(...any) error }) (domain.CustomPolicy, error) {
	var item domain.CustomPolicy
	var connectionID, roleName sql.NullString
	var enabled int64
	err := scanner.Scan(&item.ID, &item.OrgID, &item.Name, &item.Kind, &item.Config, &connectionID, &roleName, &enabled, &item.CreatedAt)
	if err != nil {
		return item, mapError(err)
	}
	if connectionID.Valid {
		item.ConnectionID = &connectionID.String
	}
	if roleName.Valid {
		item.RoleName = &roleName.String
	}
	item.Enabled = enabled != 0
	return item, nil
}
func (s *Store) ListCustomPolicies(ctx context.Context, orgID string) ([]domain.CustomPolicy, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,org_id,name,kind,config,connection_id,role_name,enabled,created_at FROM custom_policies WHERE org_id=? ORDER BY created_at", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.CustomPolicy
	for rows.Next() {
		item, err := scanCustomPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Store) CreateCustomPolicy(ctx context.Context, item domain.CustomPolicy) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO custom_policies(id,org_id,name,kind,config,connection_id,role_name,enabled,created_at) VALUES(?,?,?,?,?,?,?,?,?)", item.ID, item.OrgID, item.Name, item.Kind, item.Config, item.ConnectionID, item.RoleName, item.Enabled, item.CreatedAt)
	return mapError(err)
}
func (s *Store) UpdateCustomPolicy(ctx context.Context, item domain.CustomPolicy) error {
	result, err := s.db.ExecContext(ctx, "UPDATE custom_policies SET name=?,kind=?,config=?,connection_id=?,role_name=?,enabled=? WHERE id=? AND org_id=?", item.Name, item.Kind, item.Config, item.ConnectionID, item.RoleName, item.Enabled, item.ID, item.OrgID)
	if err != nil {
		return mapError(err)
	}
	return requireChanged(result)
}
func (s *Store) SetCustomPolicyEnabled(ctx context.Context, id, orgID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, "UPDATE custom_policies SET enabled=? WHERE id=? AND org_id=?", enabled, id, orgID)
	if err != nil {
		return err
	}
	return requireChanged(result)
}
func (s *Store) DeleteCustomPolicy(ctx context.Context, id, orgID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM custom_policies WHERE id=? AND org_id=?", id, orgID)
	if err != nil {
		return err
	}
	return requireChanged(result)
}
