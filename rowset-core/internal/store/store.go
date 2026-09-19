package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("duplicate")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	db      *sql.DB
	auditMu sync.Mutex
	// migrationBackupDir receives a copy of the database before pending
	// migrations change it.
	migrationBackupDir string
}

// Option adjusts how a store is opened.
type Option func(*Store)

// WithMigrationBackup copies an existing database into directory before any
// pending migration is applied, and refuses to upgrade it when the copy
// cannot be written.
func WithMigrationBackup(directory string) Option {
	return func(s *Store) { s.migrationBackupDir = directory }
}

func Open(ctx context.Context, path string, options ...Option) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if path != ":memory:" {
		if err := ensureParent(path); err != nil {
			return nil, err
		}
		if err := migrateMalformedDSNDatabase(path); err != nil {
			return nil, err
		}
	}
	dsn := path
	if path == ":memory:" {
		dsn = "file:rowset-memory?mode=memory&cache=shared&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	} else {
		dsn = "file:" + filepath.ToSlash(path) + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(0)
	store := &Store{db: db}
	for _, option := range options {
		option(store)
	}
	if path == ":memory:" {
		store.migrationBackupDir = ""
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Snapshot writes a consistent copy of the database into directory and keeps
// only the newest keep files. SQLite reads the whole database in one
// transaction for VACUUM INTO, so the copy is safe to take while Rowset is
// serving, and it is the only copy that stays consistent with the WAL.
func (s *Store) Snapshot(ctx context.Context, directory string, keep int) (string, error) {
	if err := mkdirAll(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "rowset-"+time.Now().UTC().Format("20060102-150405")+".sqlite3")
	// VACUUM INTO refuses to overwrite, so a second snapshot within the same
	// second is simply left alone.
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return "", fmt.Errorf("snapshot database: %w", err)
	}
	if err := pruneSnapshots(directory, keep); err != nil {
		return path, err
	}
	return path, nil
}

// snapshotInterval is the least time between two daily snapshots. It is
// shorter than a day so someone who opens Rowset every morning, a little
// earlier or later each time, still gets one each day.
const snapshotInterval = 20 * time.Hour

// DailySnapshot takes a snapshot unless the newest one is recent. Restarting
// Rowset many times in a day therefore does not rotate out last week's copy.
func (s *Store) DailySnapshot(ctx context.Context, directory string, keep int) (string, error) {
	if newest, ok := newestSnapshot(directory); ok && time.Since(newest) < snapshotInterval {
		return "", nil
	}
	return s.Snapshot(ctx, directory, keep)
}

func newestSnapshot(directory string) (time.Time, bool) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "rowset-") || !strings.HasSuffix(name, ".sqlite3") {
			continue
		}
		taken, err := time.Parse("20060102-150405", strings.TrimSuffix(strings.TrimPrefix(name, "rowset-"), ".sqlite3"))
		if err == nil && taken.After(newest) {
			newest = taken
		}
	}
	return newest, !newest.IsZero()
}

// The names carry a sortable timestamp, so the oldest are simply the first.
func pruneSnapshots(directory string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "rowset-") && strings.HasSuffix(entry.Name(), ".sqlite3") {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return nil
}

const malformedDSNSuffix = "&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"

// Early Go builds accidentally used '&' instead of '?' before SQLite URI
// parameters, making the parameters part of the filename. Move that database
// (and any WAL sidecars) into the configured path before opening it. Rename on
// one filesystem is atomic, and an existing correct database always wins.
func migrateMalformedDSNDatabase(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect sqlite path: %w", err)
	}
	legacy := path + malformedDSNSuffix
	if _, err := os.Stat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect legacy sqlite path: %w", err)
	}
	// Publish the main database name last; if a sidecar rename fails, startup
	// retries migration instead of opening a half-migrated database.
	for _, suffix := range []string{"-shm", "-wal", ""} {
		if _, err := os.Stat(legacy + suffix); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect legacy sqlite sidecar: %w", err)
		}
		if err := os.Rename(legacy+suffix, path+suffix); err != nil {
			return fmt.Errorf("migrate legacy sqlite database: %w", err)
		}
	}
	return nil
}

func ensureParent(path string) error {
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return nil
	}
	return mkdirAll(parent)
}

var mkdirAll = func(path string) error { return fsMkdirAll(path) }

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) DB() *sql.DB                    { return s.db }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// purgeChunk bounds how long one delete holds the write lock, so statements
// keep being recorded while years of activity are removed.
const purgeChunk = 2000

func (s *Store) PurgeActivity(ctx context.Context, auditDays, historyDays *uint32) (uint64, uint64, error) {
	var deleted [2]uint64
	for index, item := range []struct {
		table string
		days  *uint32
	}{{"audit_logs", auditDays}, {"query_history", historyDays}} {
		if item.days == nil {
			continue
		}
		// created_at is stored as RFC 3339 ("2026-09-12T19:19:56.1Z"); the
		// cutoff is written the same way so the two compare as text.
		cutoff := time.Now().UTC().AddDate(0, 0, -int(*item.days)).Format(time.RFC3339Nano)
		for {
			result, err := s.db.ExecContext(ctx, "DELETE FROM "+item.table+" WHERE rowid IN (SELECT rowid FROM "+item.table+" WHERE created_at<? LIMIT ?)", cutoff, purgeChunk)
			if err != nil {
				return deleted[0], deleted[1], err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return deleted[0], deleted[1], err
			}
			deleted[index] += uint64(count)
			if count < purgeChunk {
				break
			}
		}
	}
	return deleted[0], deleted[1], nil
}

type migration struct {
	version  int64
	name     string
	sql      []byte
	checksum string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("invalid migration name %q", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid migration version %q: %w", entry.Name(), err)
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		result = append(result, migration{version: version, name: entry.Name(), sql: body, checksum: fmt.Sprintf("%x", sum[:])})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	return result, nil
}

// revisedMigrations holds the checksums of earlier texts of migrations that
// were later rewritten. Databases that applied an earlier text keep their
// schema; the stored checksum moves to the current text.
var revisedMigrations = map[int64]string{
	1: "ebda109d8d67eca3d934131d683620031cc8ec59a308eff8653686ba174b0bda",
	2: "b4fe56e6d84b7c7f8fb15468c633fa88bdac99f2911296f30633787496bfe49b",
	3: "277f27ddecc17edd707e90ff1195643e2129c2475ac7b9a80c795cb7f22cb2c3",
	5: "3f619108c75b8e8ee71f355b118e3fd993a671e2a51515d420acc16661d3c740",
	6: "b1c971667adde457524acb84e8e9bad788fe068ddbc32085cfc732a982706311",
	7: "4fb7b519e5b0e7f3ee425877039684f8506f387f766f762d0ad0721d1282e510",
}

// schemaExtensions run after the migrations on every open and must be
// idempotent.
var schemaExtensions []func(context.Context, *sql.Conn) error

func (s *Store) migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS rowset_go_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	var applied int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM rowset_go_migrations").Scan(&applied); err != nil {
		return err
	}
	if applied == 0 {
		if err := importSQLxMigrations(ctx, conn, migrations); err != nil {
			return err
		}
	}

	known := make(map[int64]string)
	rows, err := conn.QueryContext(ctx, "SELECT version, checksum FROM rowset_go_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var version int64
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return err
		}
		known[version] = checksum
	}
	if err := rows.Close(); err != nil {
		return err
	}

	// A database that already holds data is copied before a new version
	// changes its schema: that copy, not one taken afterwards, is what undoes
	// a migration that went wrong.
	if len(known) > 0 && s.migrationBackupDir != "" {
		for _, item := range migrations {
			if _, applied := known[item.version]; !applied {
				if err := s.backupBeforeMigrating(ctx, conn, item.name); err != nil {
					return err
				}
				break
			}
		}
	}

	for _, migration := range migrations {
		if checksum, ok := known[migration.version]; ok {
			if checksum != migration.checksum {
				if revisedMigrations[migration.version] != checksum {
					return fmt.Errorf("migration %d checksum mismatch", migration.version)
				}
				if _, err := conn.ExecContext(ctx, "UPDATE rowset_go_migrations SET name=?, checksum=? WHERE version=?", migration.name, migration.checksum, migration.version); err != nil {
					return err
				}
			}
			continue
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(migration.sql)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO rowset_go_migrations(version,name,checksum) VALUES(?,?,?)", migration.version, migration.name, migration.checksum); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	for _, extend := range schemaExtensions {
		if err := extend(ctx, conn); err != nil {
			return fmt.Errorf("extend schema: %w", err)
		}
	}
	return nil
}

func (s *Store) backupBeforeMigrating(ctx context.Context, conn *sql.Conn, next string) error {
	if err := mkdirAll(s.migrationBackupDir); err != nil {
		return fmt.Errorf("prepare the backup before upgrading the database: %w", err)
	}
	name := "before-" + strings.TrimSuffix(next, ".sql") + "-" + time.Now().UTC().Format("20060102-150405") + ".sqlite3"
	path := filepath.Join(s.migrationBackupDir, name)
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("the database was not upgraded because a copy of it could not be written to %s first (%w); free some disk space and start Rowset again", s.migrationBackupDir, err)
	}
	return nil
}

// Existing Rust installations use sqlx's _sqlx_migrations table. Importing
// those versions before running anything is what makes the Go binary an
// in-place upgrade rather than a fresh database initializer.
func importSQLxMigrations(ctx context.Context, conn *sql.Conn, migrations []migration) error {
	var exists int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='_sqlx_migrations'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT version FROM _sqlx_migrations WHERE success=1 ORDER BY version")
	if err != nil {
		return fmt.Errorf("read legacy migrations: %w", err)
	}
	defer rows.Close()
	byVersion := make(map[int64]migration, len(migrations))
	latest := int64(0)
	for _, item := range migrations {
		byVersion[item.version] = item
		latest = max(latest, item.version)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return err
		}
		item, ok := byVersion[version]
		if !ok && version > latest {
			return fmt.Errorf("database has unknown Rust migration version %d", version)
		}
		if !ok {
			continue
		}
		if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO rowset_go_migrations(version,name,checksum) VALUES(?,?,?)", version, item.name, item.checksum); err != nil {
			return err
		}
	}
	return rows.Err()
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint failed") || strings.Contains(message, "constraint failed") {
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}
	return err
}

func NowString() string { return time.Now().UTC().Format(time.RFC3339) }

// defaultPolicies seed a new workspace. Unbounded SELECTs remain available,
// while limit_rows still prevents an accidental read from filling the editor.
// Every policy can be changed later in My policies.
var defaultPolicies = []struct {
	key, config string
	enabled     bool
}{
	{key: "deny_select_without_where", enabled: false},
	{key: "deny_delete_without_where", enabled: true},
	{key: "deny_update_without_where", enabled: true},
	{key: "deny_drop", enabled: true},
	{key: "deny_truncate", enabled: true},
	{key: "deny_unclassified", enabled: true},
	{key: "limit_rows", config: "10000", enabled: true},
}

func seedDefaultPolicies(ctx context.Context, tx *sql.Tx, orgID string) error {
	for _, policy := range defaultPolicies {
		var config any
		if policy.config != "" {
			config = policy.config
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO policies(id,org_id,name,rule_type,effect,enabled,config) VALUES(lower(hex(randomblob(16))),?,?,?,'deny',?,?) ON CONFLICT(org_id,rule_type) DO NOTHING", orgID, policy.key, policy.key, policy.enabled, config); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) CreateOrganization(ctx context.Context, org domain.Organization) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO organizations(id,name,created_at) VALUES(?,?,?)", org.ID, org.Name, org.CreatedAt); err != nil {
		return mapError(err)
	}
	if err := seedDefaultPolicies(ctx, tx, org.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Organization(ctx context.Context, orgID string) (domain.Organization, error) {
	var item domain.Organization
	err := s.db.QueryRowContext(ctx, "SELECT id,name,created_at FROM organizations WHERE id=?", orgID).Scan(&item.ID, &item.Name, &item.CreatedAt)
	return item, mapError(err)
}

func (s *Store) UpdateOrganizationName(ctx context.Context, orgID, name string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE organizations SET name=? WHERE id=?", name, orgID)
	if err != nil {
		return mapError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateOrganizationWithAdmin(ctx context.Context, org domain.Organization, role domain.Role, user domain.User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO organizations(id,name,created_at) VALUES(?,?,?)", org.ID, org.Name, org.CreatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO roles(id,org_id,name,is_readonly) VALUES(?,?,?,?)", role.ID, role.OrgID, role.Name, role.IsReadOnly); err != nil {
		return mapError(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO users(id,org_id,email,password_hash,status,created_at) VALUES(?,?,?,?,?,?)", user.ID, user.OrgID, user.Email, user.PasswordHash, user.Status, user.CreatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO user_roles(user_id,role_id) VALUES(?,?)", user.ID, role.ID); err != nil {
		return mapError(err)
	}
	if err = seedDefaultPolicies(ctx, tx, org.ID); err != nil {
		return err
	}
	return tx.Commit()
}
