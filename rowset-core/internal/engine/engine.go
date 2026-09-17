package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
	mysqlclient "github.com/go-mysql-org/go-mysql/client"
	gosqlmysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
	"golang.org/x/crypto/ssh"
)

type Connection struct {
	ID                           string
	Engine, Host                 string
	Port                         int
	Database, Username, Password string
	TLS                          TLSSettings
	PoolSize                     int
	// SSH, when enabled, is an SSH server the database is reached through.
	SSH                  SSHConfig
	ContactPoints        []string
	CassandraConsistency string
	CassandraPageSize    int
}
type Result struct {
	Columns      []string `json:"columns,omitempty"`
	Rows         [][]any  `json:"rows,omitempty"`
	RowsAffected int64    `json:"rowsAffected,omitempty"`
	DurationMS   int64    `json:"durationMs"`
	Truncated    bool     `json:"truncated,omitempty"`
}

type RowStream struct {
	rows    *sql.Rows
	columns []string
	types   []string
	origins []domain.ColumnOrigin
	started time.Time
	release func() error
	closed  sync.Once
}

func (s *RowStream) Columns() []string       { return append([]string(nil), s.columns...) }
func (s *RowStream) DatabaseTypes() []string { return append([]string(nil), s.types...) }
func (s *RowStream) ColumnOrigins() []domain.ColumnOrigin {
	return append([]domain.ColumnOrigin(nil), s.origins...)
}
func (s *RowStream) DurationMS() int64 { return time.Since(s.started).Milliseconds() }
func (s *RowStream) Close() (result error) {
	s.closed.Do(func() {
		result = s.rows.Close()
		if s.release != nil {
			result = errors.Join(result, s.release())
		}
	})
	return result
}
func (s *RowStream) Next() ([]any, bool, error) {
	values, ok, err := s.NextRaw()
	if ok {
		normalizeValues(values, s.types)
	}
	return values, ok, err
}

// NextRaw returns the next row as the driver scanned it, with binary values
// still as bytes.
func (s *RowStream) NextRaw() ([]any, bool, error) {
	if !s.rows.Next() {
		return nil, false, s.rows.Err()
	}
	values := make([]any, len(s.columns))
	pointers := make([]any, len(s.columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	if err := s.rows.Scan(pointers...); err != nil {
		return nil, false, err
	}
	return values, true, nil
}

type Column struct {
	Schema, Table, Name, DataType string
	Nullable, PrimaryKey          bool
	References                    string
	Default, Generated, Comment   string
}
type Index struct {
	Name            string   `json:"name"`
	Columns         []string `json:"columns"`
	IncludedColumns []string `json:"includedColumns,omitempty"`
	Filter          string   `json:"filter,omitempty"`
	Unique          bool     `json:"unique"`
	Primary         bool     `json:"primary"`
}
type Routine struct{ Schema, Name, Kind string }
type Sequence struct{ Schema, Name string }
type Trigger struct{ Schema, Name, Table, Timing, Event string }
type Schema struct {
	Tables    map[string][]Column
	Indexes   map[string][]Index
	Views     map[string]bool
	Routines  []Routine
	Triggers  []Trigger
	Sequences []Sequence
	Warnings  []string
}

type Manager struct {
	mu       sync.Mutex
	pools    map[string]*sql.DB
	tunnels  map[string]*ssh.Client
	opening  map[string]*poolOpening
	closed   bool
	openPool func(Connection, *ssh.Client) (*sql.DB, error)
	schemas  schemaCache
}

type poolOpening struct {
	ready chan struct{}
	db    *sql.DB
	err   error
}

func NewManager() *Manager {
	return &Manager{pools: make(map[string]*sql.DB), tunnels: make(map[string]*ssh.Client), opening: make(map[string]*poolOpening)}
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	for key := range m.opening {
		delete(m.opening, key)
	}
	pools, tunnels := m.pools, m.tunnels
	m.pools, m.tunnels = make(map[string]*sql.DB), make(map[string]*ssh.Client)
	m.mu.Unlock()
	var joined error
	for _, db := range pools {
		joined = errors.Join(joined, db.Close())
	}
	for _, tunnel := range tunnels {
		joined = errors.Join(joined, tunnel.Close())
	}
	return joined
}

// Invalidate closes every pool belonging to a saved connection. This prevents
// stale credentials and edited endpoints from accumulating until shutdown.
func (m *Manager) Invalidate(connectionID string) error {
	if connectionID == "" {
		return nil
	}
	m.InvalidateSchema(connectionID)
	m.mu.Lock()
	prefix := connectionID + "|"
	var pools []*sql.DB
	var tunnels []*ssh.Client
	for key, db := range m.pools {
		if strings.HasPrefix(key, prefix) {
			pools = append(pools, db)
			delete(m.pools, key)
		}
	}
	for key, tunnel := range m.tunnels {
		if strings.HasPrefix(key, prefix) {
			tunnels = append(tunnels, tunnel)
			delete(m.tunnels, key)
		}
	}
	for key := range m.opening {
		if strings.HasPrefix(key, prefix) {
			delete(m.opening, key)
		}
	}
	m.mu.Unlock()
	var joined error
	for _, db := range pools {
		joined = errors.Join(joined, db.Close())
	}
	for _, tunnel := range tunnels {
		joined = errors.Join(joined, tunnel.Close())
	}
	return joined
}

func (m *Manager) Test(ctx context.Context, connection Connection) error {
	if connection.Engine == "mongodb" {
		return mongoTest(ctx, connection)
	}
	if connection.Engine == "redis" || connection.Engine == "valkey" {
		return redisTest(ctx, connection)
	}
	if connection.Engine == "cassandra" {
		return cassandraTest(ctx, connection)
	}
	if connection.Engine == "elasticsearch" {
		return elasticsearchTest(ctx, connection)
	}
	db, err := m.database(connection)
	if err != nil {
		return err
	}
	return db.PingContext(ctx)
}

func (m *Manager) Execute(ctx context.Context, connection Connection, query string, maxRows int) (Result, error) {
	started := time.Now()
	db, err := m.database(connection)
	if err != nil {
		return Result{}, err
	}
	if returnsRows(query) {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return Result{}, err
		}
		defer rows.Close()
		result, err := readRows(rows, maxRows)
		result.DurationMS = time.Since(started).Milliseconds()
		return result, err
	}
	execution, err := db.ExecContext(ctx, query)
	if err != nil {
		return Result{}, err
	}
	affected, _ := execution.RowsAffected()
	return Result{RowsAffected: affected, DurationMS: time.Since(started).Milliseconds()}, nil
}

func (m *Manager) Query(ctx context.Context, connection Connection, query string) (*RowStream, error) {
	started := time.Now()
	db, err := m.database(connection)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	origins := columnOrigins(ctx, connection, conn, query)
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		conn.Close()
		return nil, err
	}
	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, err
	}
	return newRowStream(rows, columns, origins, started, conn.Close), nil
}

type Transaction struct {
	tx      *sql.Tx
	conn    *sql.Conn
	config  Connection
	cancel  context.CancelFunc
	finish  sync.Once
	started time.Time
}

// Session pins one physical database connection for a native wire-protocol
// client. Unlike an HTTP request, database clients expect SET state, temporary
// objects, prepared/session state, and transactions to survive across commands.
type Session struct {
	conn     *sql.Conn
	config   Connection
	tx       *sql.Tx
	lifetime context.Context
	cancel   context.CancelFunc
	closed   bool
}

func (m *Manager) OpenSession(ctx context.Context, connection Connection) (*Session, error) {
	db, err := m.database(connection)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Session{conn: conn, config: connection, lifetime: lifetime, cancel: cancel}, nil
}

func (s *Session) Begin(options TransactionOptions) error {
	if s.config.Engine == "clickhouse" || s.config.Engine == "mongodb" {
		return errors.New("interactive transactions are not supported for this engine")
	}
	if s.closed {
		return errors.New("database session is closed")
	}
	if s.tx != nil {
		return errors.New("transaction is already active")
	}
	txOptions, err := sqlTransactionOptions(options)
	if err != nil {
		return err
	}
	s.tx, err = s.conn.BeginTx(s.lifetime, txOptions)
	return err
}

func (s *Session) InTransaction() bool { return s != nil && s.tx != nil }

func (s *Session) Execute(ctx context.Context, query string, maxRows int) (Result, error) {
	if s == nil || s.closed {
		return Result{}, errors.New("database session is closed")
	}
	started := time.Now()
	if returnsRows(query) {
		var rows *sql.Rows
		var err error
		if s.tx != nil {
			rows, err = s.tx.QueryContext(ctx, query)
		} else {
			rows, err = s.conn.QueryContext(ctx, query)
		}
		if err != nil {
			return Result{}, err
		}
		defer rows.Close()
		result, err := readRows(rows, maxRows)
		result.DurationMS = time.Since(started).Milliseconds()
		return result, err
	}
	var execution sql.Result
	var err error
	if s.tx != nil {
		execution, err = s.tx.ExecContext(ctx, query)
	} else {
		execution, err = s.conn.ExecContext(ctx, query)
	}
	if err != nil {
		return Result{}, err
	}
	affected, _ := execution.RowsAffected()
	return Result{RowsAffected: affected, DurationMS: time.Since(started).Milliseconds()}, nil
}

func (s *Session) Query(ctx context.Context, query string) (*RowStream, error) {
	if s == nil || s.closed {
		return nil, errors.New("database session is closed")
	}
	started := time.Now()
	var rows *sql.Rows
	var err error
	var origins []domain.ColumnOrigin
	if s.tx != nil {
		rows, err = s.tx.QueryContext(ctx, query)
	} else {
		origins = columnOrigins(ctx, s.config, s.conn, query)
		rows, err = s.conn.QueryContext(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, err
	}
	return newRowStream(rows, columns, origins, started, nil), nil
}

func (s *Session) Commit() error {
	if s == nil || s.tx == nil {
		return nil
	}
	err := s.tx.Commit()
	s.tx = nil
	return err
}

func (s *Session) Rollback() error {
	if s == nil || s.tx == nil {
		return nil
	}
	err := s.tx.Rollback()
	s.tx = nil
	return err
}

func (s *Session) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	var result error
	if s.tx != nil {
		result = s.tx.Rollback()
		s.tx = nil
	}
	s.cancel()
	return errors.Join(result, s.conn.Close())
}

func (m *Manager) Begin(ctx context.Context, connection Connection) (*Transaction, error) {
	return m.BeginWithOptions(ctx, connection, TransactionOptions{})
}

type TransactionOptions struct {
	Isolation string
	ReadOnly  bool
}

func (m *Manager) BeginWithOptions(ctx context.Context, connection Connection, options TransactionOptions) (*Transaction, error) {
	if connection.Engine == "clickhouse" || connection.Engine == "mongodb" {
		return nil, errors.New("interactive transactions are not supported for this engine")
	}
	db, err := m.database(connection)
	if err != nil {
		return nil, err
	}
	// Acquire the dedicated connection with the caller's deadline, then give
	// the transaction its own lifetime. Passing the HTTP request context to
	// BeginTx would make database/sql roll the transaction back as soon as the
	// begin handler returns and cancels that request context.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	txOptions, err := sqlTransactionOptions(options)
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	tx, err := conn.BeginTx(lifetime, txOptions)
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	return &Transaction{tx: tx, conn: conn, config: connection, cancel: cancel, started: time.Now()}, nil
}

func sqlTransactionOptions(options TransactionOptions) (*sql.TxOptions, error) {
	isolation := strings.ToLower(strings.Join(strings.Fields(options.Isolation), " "))
	level := sql.LevelDefault
	switch isolation {
	case "", "default":
	case "read uncommitted":
		level = sql.LevelReadUncommitted
	case "read committed":
		level = sql.LevelReadCommitted
	case "repeatable read":
		level = sql.LevelRepeatableRead
	case "snapshot":
		level = sql.LevelSnapshot
	case "serializable":
		level = sql.LevelSerializable
	default:
		return nil, fmt.Errorf("unsupported transaction isolation level %q", options.Isolation)
	}
	return &sql.TxOptions{Isolation: level, ReadOnly: options.ReadOnly}, nil
}
func (t *Transaction) Execute(ctx context.Context, query string, maxRows int) (Result, error) {
	started := time.Now()
	if returnsRows(query) {
		rows, err := t.tx.QueryContext(ctx, query)
		if err != nil {
			return Result{}, err
		}
		defer rows.Close()
		result, err := readRows(rows, maxRows)
		result.DurationMS = time.Since(started).Milliseconds()
		return result, err
	}
	execution, err := t.tx.ExecContext(ctx, query)
	if err != nil {
		return Result{}, err
	}
	affected, _ := execution.RowsAffected()
	return Result{RowsAffected: affected, DurationMS: time.Since(started).Milliseconds()}, nil
}
func (t *Transaction) Query(ctx context.Context, query string) (*RowStream, error) {
	started := time.Now()
	// Origins let Studio edit rows of the result inside the transaction too.
	origins := columnOrigins(ctx, t.config, t.conn, query)
	rows, err := t.tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, err
	}
	return newRowStream(rows, columns, origins, started, nil), nil
}

func newRowStream(rows *sql.Rows, columns []string, origins []domain.ColumnOrigin, started time.Time, release func() error) *RowStream {
	types := make([]string, len(columns))
	if columnTypes, err := rows.ColumnTypes(); err == nil {
		for index, columnType := range columnTypes {
			if index < len(types) {
				types[index] = strings.ToUpper(columnType.DatabaseTypeName())
			}
		}
	}
	if len(origins) != len(columns) {
		origins = make([]domain.ColumnOrigin, len(columns))
	}
	return &RowStream{rows: rows, columns: columns, types: types, origins: origins, started: started, release: release}
}

func columnOrigins(ctx context.Context, connection Connection, conn *sql.Conn, query string) []domain.ColumnOrigin {
	switch strings.ToLower(connection.Engine) {
	case "mssql", "sqlserver":
		return mssqlColumnOrigins(ctx, conn, query)
	case "mysql", "mariadb":
		return mysqlColumnOrigins(ctx, connection, query)
	case "postgres", "postgresql", "cockroachdb":
	default:
		return nil
	}
	var origins []domain.ColumnOrigin
	_ = conn.Raw(func(driverConn any) error {
		pgConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return nil
		}
		description, err := pgConn.Conn().Prepare(ctx, "", query)
		if err != nil {
			return nil
		}
		origins = make([]domain.ColumnOrigin, len(description.Fields))
		oids := make(map[uint32]struct{})
		for index, field := range description.Fields {
			if field.TableOID == 0 || field.TableAttributeNumber == 0 {
				origins[index].Expression = true
				continue
			}
			origins[index].Table = fmt.Sprint(field.TableOID)
			origins[index].Column = fmt.Sprint(field.TableAttributeNumber)
			oids[field.TableOID] = struct{}{}
		}
		if len(oids) == 0 {
			return nil
		}
		values := make([]string, 0, len(oids))
		for oid := range oids {
			values = append(values, strconv.FormatUint(uint64(oid), 10))
		}
		catalogSQL := "SELECT c.oid::text,a.attnum::text,n.nspname,c.relname,a.attname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid WHERE c.oid IN (" + strings.Join(values, ",") + ") AND a.attnum>0 AND NOT a.attisdropped"
		rows, err := pgConn.Conn().Query(ctx, catalogSQL)
		if err != nil {
			return nil
		}
		defer rows.Close()
		resolved := make(map[string]domain.ColumnOrigin)
		for rows.Next() {
			var oid, attribute, schema, table, column string
			if err := rows.Scan(&oid, &attribute, &schema, &table, &column); err != nil {
				return nil
			}
			resolved[oid+":"+attribute] = domain.ColumnOrigin{Schema: schema, Table: table, Column: column, Resolved: true}
		}
		for index, origin := range origins {
			if value, ok := resolved[origin.Table+":"+origin.Column]; ok {
				origins[index] = value
			}
		}
		return nil
	})
	return origins
}

func mysqlColumnOrigins(ctx context.Context, connection Connection, query string) []domain.ColumnOrigin {
	address := net.JoinHostPort(connection.Host, strconv.Itoa(connection.Port))
	options := make([]mysqlclient.Option, 0, 1)
	config, err := tlsConfig(connection.TLS, connection.Host)
	if err != nil {
		return nil
	}
	if config != nil {
		options = append(options, func(conn *mysqlclient.Conn) error {
			conn.SetTLSConfig(config)
			return nil
		})
	}
	metadataConn, err := mysqlclient.ConnectWithContext(ctx, address, connection.Username, connection.Password, connection.Database, 10*time.Second, options...)
	if err != nil {
		return nil
	}
	defer metadataConn.Close()
	statement, err := metadataConn.Prepare(query)
	if err != nil {
		return nil
	}
	defer statement.Close()
	fields, err := statement.GetColumnFields()
	if err != nil {
		return nil
	}
	origins := make([]domain.ColumnOrigin, len(fields))
	for index, field := range fields {
		if len(field.OrgTable) == 0 || len(field.OrgName) == 0 {
			origins[index].Expression = true
			continue
		}
		origins[index] = domain.ColumnOrigin{Schema: string(field.Schema), Table: string(field.OrgTable), Column: string(field.OrgName), Resolved: true}
	}
	return origins
}

func mssqlColumnOrigins(ctx context.Context, conn *sql.Conn, query string) []domain.ColumnOrigin {
	const describe = `SELECT column_ordinal,source_schema,source_table,source_column
FROM sys.dm_exec_describe_first_result_set(@tsql,NULL,1)
WHERE is_hidden=0 ORDER BY column_ordinal`
	rows, err := conn.QueryContext(ctx, describe, sql.Named("tsql", query))
	if err != nil {
		return nil
	}
	defer rows.Close()
	origins := make([]domain.ColumnOrigin, 0)
	for rows.Next() {
		var ordinal int
		var schema, table, column sql.NullString
		if err := rows.Scan(&ordinal, &schema, &table, &column); err != nil || ordinal < 1 {
			return nil
		}
		for len(origins) < ordinal {
			origins = append(origins, domain.ColumnOrigin{})
		}
		if schema.Valid && table.Valid && column.Valid {
			origins[ordinal-1] = domain.ColumnOrigin{Schema: schema.String, Table: table.String, Column: column.String, Resolved: true}
		} else {
			origins[ordinal-1].Expression = true
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return origins
}
func (t *Transaction) close(commit bool) error {
	var result error
	t.finish.Do(func() {
		if commit {
			result = t.tx.Commit()
		} else {
			result = t.tx.Rollback()
		}
		t.cancel()
		result = errors.Join(result, t.conn.Close())
	})
	return result
}
func (t *Transaction) Commit() error   { return t.close(true) }
func (t *Transaction) Rollback() error { return t.close(false) }

// TransactionState describes whether a manual transaction can continue.
type TransactionState string

const (
	TransactionActive TransactionState = "active"
	// TransactionAborted: PostgreSQL rejects further statements after an
	// error; only rollback is possible and commit applies nothing.
	TransactionAborted TransactionState = "aborted"
	// TransactionLost: the pinned connection is gone (statement cancel,
	// server kill, network failure). The database discards uncommitted work
	// when the session ends, so the transaction cannot be resumed.
	TransactionLost TransactionState = "lost"
)

// State inspects the pinned connection after an error. All three bundled
// drivers close the connection when a statement context is cancelled, so a
// stopped statement inside a transaction also reports TransactionLost.
func (t *Transaction) State() TransactionState {
	state := TransactionActive
	err := t.conn.Raw(func(driverConn any) error {
		if pg, ok := driverConn.(*stdlib.Conn); ok {
			if pg.Conn().IsClosed() {
				state = TransactionLost
			} else if pg.Conn().PgConn().TxStatus() == 'E' {
				state = TransactionAborted
			}
			return nil
		}
		if validator, ok := driverConn.(driver.Validator); ok && !validator.IsValid() {
			state = TransactionLost
		}
		return nil
	})
	if err != nil {
		return TransactionLost
	}
	return state
}

func readRows(rows *sql.Rows, maxRows int) (Result, error) {
	columns, err := rows.Columns()
	if err != nil {
		return Result{}, err
	}
	result := Result{Columns: columns, Rows: make([][]any, 0)}
	types := make([]string, len(columns))
	if columnTypes, err := rows.ColumnTypes(); err == nil {
		for index, columnType := range columnTypes {
			if index < len(types) {
				types[index] = strings.ToUpper(columnType.DatabaseTypeName())
			}
		}
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return Result{}, err
		}
		normalizeValues(values, types)
		if maxRows > 0 && len(result.Rows) >= maxRows {
			result.Truncated = true
			break
		}
		result.Rows = append(result.Rows, values)
	}
	return result, rows.Err()
}

// normalizeValues turns driver values into what results, exports and
// generated SQL show: text as strings, binary as \x-prefixed hex, SQL Server
// GUIDs in their usual form, and dates and times as literals every engine
// reads back into the same column type.
func normalizeValues(values []any, types []string) {
	for index, value := range values {
		kind := ""
		if index < len(types) {
			kind = types[index]
		}
		switch v := value.(type) {
		case []byte:
			switch {
			case kind == "UNIQUEIDENTIFIER" && len(v) == 16:
				values[index] = formatGUID(v)
			case !binaryDisplayType(kind) && utf8.Valid(v):
				values[index] = string(v)
			default:
				values[index] = "\\x" + hex.EncodeToString(v)
			}
		case time.Time:
			values[index] = FormatTime(v, kind)
		case float64:
			if special, ok := specialFloat(v); ok {
				values[index] = special
			}
		case fmt.Stringer:
			values[index] = v.String()
		case float32:
			if special, ok := specialFloat(float64(v)); ok {
				values[index] = special
			}
		}
	}
}

// specialFloat names NaN and the infinities, which JSON cannot carry as
// numbers; PostgreSQL reads these names back as the same values.
func specialFloat(value float64) (string, bool) {
	switch {
	case math.IsNaN(value):
		return "NaN", true
	case math.IsInf(value, 1):
		return "Infinity", true
	case math.IsInf(value, -1):
		return "-Infinity", true
	}
	return "", false
}

func binaryDisplayType(kind string) bool {
	if kind == "BIT" {
		return true
	}
	for _, name := range []string{"BINARY", "BYTEA", "BLOB", "IMAGE", "GEOMETRY", "GEOGRAPHY", "HIERARCHYID"} {
		if strings.Contains(kind, name) {
			return true
		}
	}
	return false
}

// formatGUID renders SQL Server's uniqueidentifier bytes, whose first three
// groups are stored little-endian.
func formatGUID(b []byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X-%02X%02X-%02X%02X-%X-%X", b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6], b[8:10], b[10:16])
}

// FormatTime renders a date or time the way its column type reads it back.
func FormatTime(value time.Time, databaseType string) string {
	switch kind := strings.ToUpper(databaseType); {
	case kind == "DATE":
		return value.Format("2006-01-02")
	case kind == "TIME":
		return value.Format("15:04:05.999999999")
	case strings.Contains(kind, "OFFSET"), kind == "TIMESTAMPTZ", kind == "TIMETZ":
		return value.Format("2006-01-02 15:04:05.999999999-07:00")
	case kind == "":
		return value.Format(time.RFC3339Nano)
	default:
		return value.Format("2006-01-02 15:04:05.999999999")
	}
}

func ReturnsRows(query string) bool {
	if info, err := sqlguard.Parse(query); err == nil {
		if info.Command == sqlguard.Select {
			return true
		}
		if info.Command == sqlguard.Insert || info.Command == sqlguard.Update || info.Command == sqlguard.Delete {
			for _, token := range info.Tokens {
				if token.Depth == 0 && (token.Lower == "returning" || token.Lower == "output") {
					return true
				}
			}
			return false
		}
	}
	query = strings.TrimSpace(strings.TrimLeft(query, "(\ufeff"))
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "SELECT", "WITH", "SHOW", "EXPLAIN", "DESCRIBE", "DESC", "PRAGMA", "EXEC", "EXECUTE", "VALUES":
		return true
	default:
		return strings.Contains(strings.ToUpper(query), " RETURNING ") || strings.HasSuffix(strings.ToUpper(query), " RETURNING")
	}
}

func returnsRows(query string) bool { return ReturnsRows(query) }

// DiscoverHostKey dials the SSH server and returns its host key line, even if
// the credentials are wrong, so it can be trusted before the connection is
// saved. The server presents its host key during the handshake, before
// authentication.
func (m *Manager) DiscoverHostKey(ctx context.Context, config SSHConfig) (string, error) {
	var key string
	client, err := dialSSH(ctx, config, func(line string) { key = line })
	if client != nil {
		_ = client.Close()
	}
	if key != "" {
		return key, nil
	}
	return "", err
}

// TestTunnel opens the SSH tunnel only, to check the server is reachable and
// to learn its host key. It returns the key line to trust; the caller stores
// it so later connections can verify against it.
func (m *Manager) TestTunnel(ctx context.Context, connection Connection) (string, error) {
	if !connection.SSH.enabled() {
		return "", nil
	}
	var hostKey string
	client, err := dialSSH(ctx, connection.SSH, func(line string) { hostKey = line })
	if err != nil {
		return "", err
	}
	_ = client.Close()
	return hostKey, nil
}

func (m *Manager) database(connection Connection) (*sql.DB, error) {
	key := poolKey(connection)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("database manager is closed")
	}
	if db := m.pools[key]; db != nil {
		m.mu.Unlock()
		return db, nil
	}
	if pending := m.opening[key]; pending != nil {
		m.mu.Unlock()
		<-pending.ready
		return pending.db, pending.err
	}
	pending := &poolOpening{ready: make(chan struct{})}
	m.opening[key] = pending
	m.mu.Unlock()
	var tunnel *ssh.Client
	var db *sql.DB
	var err error
	if connection.SSH.enabled() {
		tunnel, err = dialSSH(context.Background(), connection.SSH, nil)
	}
	if err == nil {
		open := m.openPool
		if open == nil {
			open = openDatabase
		}
		db, err = open(connection, tunnel)
	}
	if err == nil {
		poolSize := connection.PoolSize
		if poolSize <= 0 {
			poolSize = 10
		}
		if FileEngine(connection.Engine) {
			poolSize = 1
		}
		db.SetMaxOpenConns(poolSize)
		db.SetMaxIdleConns(poolSize)
		db.SetConnMaxIdleTime(30 * time.Minute)
		db.SetConnMaxLifetime(2 * time.Hour)
	}
	m.mu.Lock()
	if m.closed || m.opening[key] != pending {
		err = errors.New("database connection was invalidated during setup")
	} else if err == nil {
		m.pools[key] = db
		if tunnel != nil {
			m.tunnels[key] = tunnel
		}
	}
	if m.opening[key] == pending {
		delete(m.opening, key)
	}
	if err == nil {
		pending.db = db
	} else {
		pending.err = err
	}
	close(pending.ready)
	m.mu.Unlock()
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return nil, err
	}
	return db, nil
}

// openDatabase hands every driver the same *tls.Config through a connector;
// DSN flags meant different verification guarantees on each engine.
func openDatabase(connection Connection, tunnel *ssh.Client) (*sql.DB, error) {
	if FileEngine(connection.Engine) || connection.Engine == "clickhouse" {
		return openAdditional(connection, tunnel)
	}
	switch connection.Engine {
	case "mongodb", "redis", "valkey", "cassandra", "elasticsearch":
		return nil, fmt.Errorf("%s does not use a pooled SQL connection", connection.Engine)
	}
	config, err := tlsConfig(connection.TLS, connection.Host)
	if err != nil {
		return nil, err
	}
	_, dsn, err := connectionString(connection)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(connection.Engine) {
	case "postgres", "postgresql", "cockroachdb":
		parsed, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, err
		}
		parsed.TLSConfig, parsed.Fallbacks = config, nil
		if tunnel != nil {
			parsed.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return tunnelDial(ctx, tunnel, network, addr)
			}
		}
		return stdlib.OpenDB(*parsed), nil
	case "mysql", "mariadb":
		parsed, err := gosqlmysql.ParseDSN(dsn)
		if err != nil {
			return nil, err
		}
		parsed.TLS = config
		if tunnel != nil {
			// A per-connection network name so each tunnel dials its own server.
			network := "rowset-ssh-" + connection.ID
			gosqlmysql.RegisterDialContext(network, func(ctx context.Context, addr string) (net.Conn, error) {
				return tunnelDial(ctx, tunnel, "tcp", addr)
			})
			parsed.Net = network
		}
		connector, err := gosqlmysql.NewConnector(parsed)
		if err != nil {
			return nil, err
		}
		return sql.OpenDB(connector), nil
	case "mssql", "sqlserver":
		parsed, err := msdsn.Parse(dsn)
		if err != nil {
			return nil, err
		}
		if config != nil {
			parsed.Encryption, parsed.TLSConfig, parsed.HostInCertificateProvided = msdsn.EncryptionRequired, config, true
		}
		connector := mssql.NewConnectorConfig(parsed)
		if tunnel != nil {
			connector.Dialer = sshMSSQLDialer{tunnel}
		}
		return sql.OpenDB(connector), nil
	default:
		return nil, fmt.Errorf("unsupported engine: %s", connection.Engine)
	}
}

// sshMSSQLDialer lets the SQL Server driver dial through the tunnel.
type sshMSSQLDialer struct{ client *ssh.Client }

func (d sshMSSQLDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return tunnelDial(ctx, d.client, network, addr)
}

// connectionString returns a plaintext DSN; openDatabase layers TLS on top.
func connectionString(connection Connection) (string, string, error) {
	hostPort := net.JoinHostPort(connection.Host, fmt.Sprint(connection.Port))
	switch strings.ToLower(connection.Engine) {
	case "postgres", "postgresql", "cockroachdb":
		u := &url.URL{Scheme: "postgres", User: url.UserPassword(connection.Username, connection.Password), Host: hostPort, Path: "/" + connection.Database}
		q := u.Query()
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
		return "pgx", u.String(), nil
	case "mysql", "mariadb":
		cfg := gosqlmysql.NewConfig()
		cfg.User = connection.Username
		cfg.Passwd = connection.Password
		cfg.Net = "tcp"
		cfg.Addr = hostPort
		cfg.DBName = connection.Database
		cfg.ParseTime = true
		cfg.Timeout = 10 * time.Second
		cfg.ReadTimeout = 24 * time.Hour
		cfg.WriteTimeout = 24 * time.Hour
		return "mysql", cfg.FormatDSN(), nil
	case "mssql", "sqlserver":
		u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(connection.Username, connection.Password), Host: hostPort}
		q := u.Query()
		q.Set("database", connection.Database)
		q.Set("encrypt", "disable")
		u.RawQuery = q.Encode()
		return "sqlserver", u.String(), nil
	default:
		return "", "", fmt.Errorf("unsupported engine: %s", connection.Engine)
	}
}

func poolKey(connection Connection) string {
	digest := sha256.Sum256([]byte(connection.Password))
	ssh := sha256.Sum256([]byte(connection.SSH.Host + "|" + fmt.Sprint(connection.SSH.Port) + "|" + connection.SSH.User + "|" + connection.SSH.AuthMethod + "|" + connection.SSH.Password + "|" + connection.SSH.PrivateKey + "|" + connection.SSH.Passphrase + "|" + connection.SSH.KnownHost))
	return fmt.Sprintf("%s|%s:%s:%d/%s/%s/%x/%s/%d/%x", connection.ID, connection.Engine, connection.Host, connection.Port, connection.Database, connection.Username, digest[:8], connection.TLS.digest(), connection.PoolSize, ssh[:8])
}
