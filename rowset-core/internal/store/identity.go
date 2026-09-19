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

func (s *Store) RoleByName(ctx context.Context, orgID, name string) (domain.Role, error) {
	return scanRole(s.db.QueryRowContext(ctx, "SELECT "+roleColumns+" FROM roles WHERE org_id=? AND name=?", orgID, name))
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
