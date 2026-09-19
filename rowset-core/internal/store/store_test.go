package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestReopenPreservesUsersRolesAndConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rowset.sqlite3")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	org := domain.Organization{ID: "org-1", Name: "Persistent Org", CreatedAt: NowString()}
	role := domain.Role{ID: "role-1", OrgID: org.ID, Name: "admin"}
	user := domain.User{ID: "user-1", OrgID: org.ID, Email: "persist@example.com", PasswordHash: "$argon2id$fixture", Status: "active", CreatedAt: NowString()}
	if err := first.CreateOrganizationWithAdmin(ctx, org, role, user); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `INSERT INTO secrets(id,ciphertext,nonce) VALUES('secret-1',x'01',x'02')`); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `INSERT INTO connections(id,org_id,name,alias,engine,host,port,database,environment,tls_required,tech_username,secret_id,created_at,query_timeout_seconds)
		VALUES('connection-1','org-1','prod','prod','postgres','db',5432,'app','prod',1,'rowset','secret-1',datetime('now'),600)`); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	gotUser, err := second.UserByEmail(ctx, user.Email)
	if err != nil || gotUser.ID != user.ID {
		t.Fatalf("user was not preserved: %#v %v", gotUser, err)
	}
	var roleID string
	if err := second.db.QueryRowContext(ctx, "SELECT role_id FROM user_roles WHERE user_id=?", user.ID).Scan(&roleID); err != nil || roleID != role.ID {
		t.Fatalf("role was not preserved: %q %v", roleID, err)
	}
	// Rows written without tls_mode (older backups) keep the unverified
	// encryption PostgreSQL connections always had.
	if connection, err := second.Connection(ctx, "connection-1"); err != nil || connection.TLSMode != "require" || !connection.TLSRequired {
		t.Fatalf("legacy TLS row changed behavior: %#v %v", connection, err)
	}
	var connections int
	if err := second.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM connections WHERE id='connection-1'").Scan(&connections); err != nil || connections != 1 {
		t.Fatalf("connection was not preserved: %d %v", connections, err)
	}
}

func TestOrganizationConfigurationCanBeUpdated(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "configuration.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.CreateOrganization(ctx, domain.Organization{ID: "org", Name: "Before", CreatedAt: NowString()}); err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateOrganizationName(ctx, "org", "After"); err != nil {
		t.Fatal(err)
	}
	organization, err := data.Organization(ctx, "org")
	if err != nil || organization.Name != "After" {
		t.Fatalf("organization=%#v err=%v", organization, err)
	}
	if err := data.UpdateOrganizationName(ctx, "missing", "Nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing organization error=%v", err)
	}
}

func TestNewOrganizationStartsWithUnboundedSelectAllowed(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "default-policies.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.CreateOrganization(ctx, domain.Organization{ID: "policy-org", Name: "Policy", CreatedAt: NowString()}); err != nil {
		t.Fatal(err)
	}
	policies, err := data.ListPolicyOverrides(ctx, "policy-org")
	if err != nil {
		t.Fatal(err)
	}
	enabled := make(map[string]bool, len(policies))
	for _, item := range policies {
		enabled[item.Key] = item.Enabled
	}
	if enabled["deny_select_without_where"] {
		t.Fatal("SELECT without WHERE should be allowed in a new workspace")
	}
	for _, key := range []string{"deny_delete_without_where", "deny_update_without_where", "deny_drop", "deny_truncate", "limit_rows"} {
		if !enabled[key] {
			t.Fatalf("default safety policy %s is disabled", key)
		}
	}
}

func TestOpenMigratesMalformedDSNFilenameWithoutLosingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rowset.sqlite3")
	legacyPath := path + malformedDSNSuffix
	legacy, err := Open(ctx, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.CreateOrganization(ctx, domain.Organization{ID: "kept", Name: "Kept", CreatedAt: NowString()}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var count int
	if err := migrated.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM organizations WHERE id='kept'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy data not migrated: count=%d err=%v", count, err)
	}
}

func TestImportsRustSQLxMigrationHistoryWithoutReapplyingDDL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE _sqlx_migrations(version BIGINT PRIMARY KEY, description TEXT NOT NULL, installed_on TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, success BOOLEAN NOT NULL, checksum BLOB NOT NULL, execution_time BIGINT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "DELETE FROM rowset_go_migrations"); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		digest := sha256.Sum256(migration.sql)
		if _, err := db.db.ExecContext(ctx, "INSERT INTO _sqlx_migrations(version,description,success,checksum,execution_time) VALUES(?,?,1,?,0)", migration.version, migration.name, digest[:]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("legacy database upgrade failed: %v", err)
	}
	defer upgraded.Close()
	var count int
	if err := upgraded.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rowset_go_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("imported %d migrations, want %d", count, len(migrations))
	}
}

func TestMigrationChecksumsAreImmutable(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, migration := range migrations {
		if seen[migration.version] {
			t.Fatalf("duplicate migration %d", migration.version)
		}
		seen[migration.version] = true
		if len(migration.checksum) != 64 {
			t.Fatalf("bad checksum for %s: %s", migration.name, migration.checksum)
		}
	}
	if len(migrations) != 29 {
		t.Fatalf("migration inventory changed: got %d", len(migrations))
	}
}

func TestSchemaSnapshotsPersistUntilInvalidated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema-cache.sqlite3")
	data, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	org := domain.Organization{ID: "schema-org", Name: "Schema", CreatedAt: NowString()}
	if err := data.CreateOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if _, err := data.db.ExecContext(ctx, `INSERT INTO secrets(id,ciphertext,nonce) VALUES('schema-secret',x'01',x'02')`); err != nil {
		t.Fatal(err)
	}
	if _, err := data.db.ExecContext(ctx, `INSERT INTO connections(id,org_id,name,alias,engine,host,port,database,environment,tls_required,tech_username,secret_id,created_at,query_timeout_seconds)
		VALUES('schema-connection',?,'db','db','postgres','localhost',5432,'app','dev',0,'user','schema-secret',datetime('now'),600)`, org.ID); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"schemas":[{"name":"public"}]}`)
	if err := data.PutSchemaSnapshot(ctx, "schema-connection", "app", want); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if got, err := data.SchemaSnapshot(ctx, "schema-connection", "app"); err != nil || string(got) != string(want) {
		t.Fatalf("snapshot=%s err=%v", got, err)
	}
	if err := data.DeleteSchemaSnapshots(ctx, "schema-connection"); err != nil {
		t.Fatal(err)
	}
	if _, err := data.SchemaSnapshot(ctx, "schema-connection", "app"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot survived invalidation: %v", err)
	}
}

func TestRevisedMigrationTextKeepsExistingDatabases(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "revised.sqlite3")
	data, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.db.ExecContext(ctx, "UPDATE rowset_go_migrations SET checksum=? WHERE version=1", revisedMigrations[1]); err != nil {
		t.Fatal(err)
	}
	data.Close()
	if data, err = Open(ctx, path); err != nil {
		t.Fatalf("earlier migration text rejected: %v", err)
	}
	defer data.Close()
	migrations, _ := loadMigrations()
	var checksum string
	if err := data.db.QueryRowContext(ctx, "SELECT checksum FROM rowset_go_migrations WHERE version=1").Scan(&checksum); err != nil || checksum != migrations[0].checksum {
		t.Fatalf("checksum not moved to the current text: %q %v", checksum, err)
	}
}
