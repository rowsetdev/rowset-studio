package store

import (
	"context"
	"database/sql"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

const userColumns = "id,org_id,email,password_hash,status,created_at"
const roleColumns = "id,org_id,name,is_readonly"

func scanUser(scanner interface{ Scan(...any) error }) (domain.User, error) {
	var user domain.User
	err := scanner.Scan(&user.ID, &user.OrgID, &user.Email, &user.PasswordHash, &user.Status, &user.CreatedAt)
	return user, mapError(err)
}

func scanRole(scanner interface{ Scan(...any) error }) (domain.Role, error) {
	var role domain.Role
	err := scanner.Scan(&role.ID, &role.OrgID, &role.Name, &role.IsReadOnly)
	return role, mapError(err)
}

func (s *Store) CreateUser(ctx context.Context, user domain.User) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO users("+userColumns+") VALUES(?,?,?,?,?,?)", user.ID, user.OrgID, user.Email, user.PasswordHash, user.Status, user.CreatedAt)
	return mapError(err)
}

func (s *Store) UserByEmail(ctx context.Context, email string) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE email=?", email))
}

func (s *Store) User(ctx context.Context, id string) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=?", id))
}

func (s *Store) ListUsers(ctx context.Context, orgID string) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users WHERE org_id=? ORDER BY created_at", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []domain.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) CreateRole(ctx context.Context, role domain.Role) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO roles("+roleColumns+") VALUES(?,?,?,?)", role.ID, role.OrgID, role.Name, role.IsReadOnly)
	return mapError(err)
}

func (s *Store) AssignRole(ctx context.Context, userID, roleID string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO user_roles(user_id,role_id) VALUES(?,?)", userID, roleID)
	return mapError(err)
}

func (s *Store) RoleByName(ctx context.Context, orgID, name string) (domain.Role, error) {
	return scanRole(s.db.QueryRowContext(ctx, "SELECT "+roleColumns+" FROM roles WHERE org_id=? AND name=?", orgID, name))
}

func (s *Store) UserRole(ctx context.Context, userID string) (domain.Role, error) {
	return scanRole(s.db.QueryRowContext(ctx, "SELECT r."+"id,r.org_id,r.name,r.is_readonly FROM roles r JOIN user_roles ur ON ur.role_id=r.id WHERE ur.user_id=? LIMIT 1", userID))
}

func (s *Store) Role(ctx context.Context, id string) (domain.Role, error) {
	return scanRole(s.db.QueryRowContext(ctx, "SELECT "+roleColumns+" FROM roles WHERE id=?", id))
}

func (s *Store) ListRoles(ctx context.Context, orgID string) ([]domain.Role, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+roleColumns+" FROM roles WHERE org_id=? ORDER BY name", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []domain.Role
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (s *Store) SetUserRole(ctx context.Context, userID, roleID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM user_roles WHERE user_id=?", userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO user_roles(user_id,role_id) VALUES(?,?)", userID, roleID); err != nil {
		return mapError(err)
	}
	return tx.Commit()
}

func (s *Store) SetUserPassword(ctx context.Context, userID, passwordHash string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE users SET password_hash=? WHERE id=?", passwordHash, userID)
	if err != nil {
		return mapError(err)
	}
	return requireChanged(result)
}

func (s *Store) SetRoleReadOnly(ctx context.Context, roleID string, readOnly bool) error {
	result, err := s.db.ExecContext(ctx, "UPDATE roles SET is_readonly=? WHERE id=?", readOnly, roleID)
	if err != nil {
		return mapError(err)
	}
	return requireChanged(result)
}

func requireChanged(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
