package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
	"github.com/gocql/gocql"
	"golang.org/x/crypto/ssh"
)

type cassandraSSHDialer struct{ client *ssh.Client }

func (d cassandraSSHDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return tunnelDial(ctx, d.client, network, addr)
}

func cassandraSession(ctx context.Context, connection Connection, keyspace string) (*gocql.Session, func(), error) {
	hosts := connection.ContactPoints
	if len(hosts) == 0 {
		hosts = []string{connection.Host}
	}
	cluster := gocql.NewCluster(hosts...)
	cluster.Port = connection.Port
	consistency := connection.CassandraConsistency
	if consistency == "" {
		consistency = "QUORUM"
	}
	parsedConsistency, err := gocql.ParseConsistencyWrapper(consistency)
	if err != nil {
		return nil, nil, fmt.Errorf("Cassandra consistency: %w", err)
	}
	cluster.Consistency = parsedConsistency
	if connection.CassandraPageSize > 0 {
		cluster.PageSize = connection.CassandraPageSize
	}
	cluster.ConnectTimeout = 10 * time.Second
	cluster.Timeout = 24 * time.Hour
	if keyspace != "" {
		cluster.Keyspace = keyspace
	}
	if connection.Username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{Username: connection.Username, Password: connection.Password}
	}
	if connection.TLS.Mode != TLSDisable && connection.TLS.Mode != "" {
		config, err := tlsConfig(connection.TLS, connection.Host)
		if err != nil {
			return nil, nil, err
		}
		cluster.SslOpts = &gocql.SslOptions{Config: config}
	}
	var tunnel *ssh.Client
	if connection.SSH.enabled() {
		tunnel, err = dialSSH(ctx, connection.SSH, nil)
		if err != nil {
			return nil, nil, err
		}
		cluster.Dialer = cassandraSSHDialer{client: tunnel}
	}
	session, err := cluster.CreateSession()
	if err != nil {
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return nil, nil, err
	}
	cleanup := func() {
		session.Close()
		if tunnel != nil {
			_ = tunnel.Close()
		}
	}
	return session, cleanup, nil
}

func cassandraTest(ctx context.Context, connection Connection) error {
	session, cleanup, err := cassandraSession(ctx, connection, "")
	if err != nil {
		return err
	}
	defer cleanup()
	return session.Query("SELECT cluster_name FROM system.local").WithContext(ctx).Exec()
}

func cassandraDatabases(ctx context.Context, connection Connection) ([]string, error) {
	session, cleanup, err := cassandraSession(ctx, connection, "")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	iter := session.Query("SELECT keyspace_name FROM system_schema.keyspaces").WithContext(ctx).Iter()
	var result []string
	var name string
	for iter.Scan(&name) {
		result = append(result, name)
	}
	return result, iter.Close()
}

// cassandraSchema reports each keyspace's tables and columns as one flat
// schema; Cassandra has no cross-keyspace catalog view, so every keyspace's
// tables are read and merged the way Rowset already merges schemas elsewhere.
func cassandraSchema(ctx context.Context, connection Connection) (Schema, error) {
	result := Schema{Tables: map[string][]Column{}, Indexes: map[string][]Index{}, Views: map[string]bool{}}
	keyspace := strings.TrimSpace(connection.Database)
	if keyspace == "" || keyspace == "system" {
		return result, errors.New("choose a keyspace (Database field) to browse its tables")
	}
	session, cleanup, err := cassandraSession(ctx, connection, "")
	if err != nil {
		return result, err
	}
	defer cleanup()
	iter := session.Query("SELECT table_name, column_name, type, kind FROM system_schema.columns WHERE keyspace_name = ?", keyspace).WithContext(ctx).Iter()
	var table, column, kind, colKind string
	for iter.Scan(&table, &column, &kind, &colKind) {
		key := tableKey(keyspace, table)
		result.Tables[key] = append(result.Tables[key], Column{Schema: keyspace, Table: table, Name: column, DataType: kind, PrimaryKey: colKind == "partition_key" || colKind == "clustering"})
	}
	if err := iter.Close(); err != nil {
		return result, err
	}
	indexIter := session.Query("SELECT table_name, index_name, options FROM system_schema.indexes WHERE keyspace_name = ?", keyspace).WithContext(ctx).Iter()
	var idxTable, idxName string
	var idxOptions map[string]string
	for indexIter.Scan(&idxTable, &idxName, &idxOptions) {
		// A CUSTOM index (e.g. a SASI/SAI index) may target an expression
		// rather than a plain column, or have no "target" at all; skip those
		// rather than showing a misleading column list.
		target := strings.Trim(idxOptions["target"], `"`)
		if target == "" || strings.ContainsAny(target, "(),") {
			continue
		}
		key := tableKey(keyspace, idxTable)
		result.Indexes[key] = append(result.Indexes[key], Index{Name: idxName, Columns: []string{target}})
	}
	if err := indexIter.Close(); err != nil {
		return result, err
	}
	// Materialized views are regular tables under the hood (their columns
	// were already picked up by the query above under their own name); this
	// just marks which table names are actually views.
	viewIter := session.Query("SELECT view_name FROM system_schema.views WHERE keyspace_name = ?", keyspace).WithContext(ctx).Iter()
	var viewName string
	for viewIter.Scan(&viewName) {
		result.Views[tableKey(keyspace, viewName)] = true
	}
	return result, viewIter.Close()
}

type CassandraQueryInput struct {
	Keyspace string `json:"keyspace"`
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
}

// CassandraQuery runs a policy-cleared CQL statement and returns SELECT rows
// in the same shape as a SQL result grid.
func (m *Manager) CassandraQuery(ctx context.Context, connection Connection, input CassandraQueryInput) (Result, error) {
	if input.Limit <= 0 || input.Limit > 10000 {
		input.Limit = 1000
	}
	session, cleanup, err := cassandraSession(ctx, connection, input.Keyspace)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	started := time.Now()
	info, err := sqlguard.ParseCQL(input.Query)
	if err != nil {
		return Result{}, err
	}
	if info.Kind != sqlguard.Select {
		err = session.Query(input.Query).WithContext(ctx).Exec()
		return Result{DurationMS: time.Since(started).Milliseconds()}, err
	}
	pageSize := connection.CassandraPageSize
	if pageSize <= 0 || pageSize > input.Limit {
		pageSize = input.Limit
	}
	iter := session.Query(input.Query).WithContext(ctx).PageSize(pageSize).Iter()
	columns := iter.Columns()
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.Name
	}
	result := Result{Columns: names}
	row := make(map[string]any)
	for iter.MapScan(row) {
		values := make([]any, len(names))
		for i, name := range names {
			values[i] = jsonSafe(row[name])
		}
		result.Rows = append(result.Rows, values)
		row = make(map[string]any)
		if len(result.Rows) >= input.Limit {
			result.Truncated = true
			break
		}
	}
	result.DurationMS = time.Since(started).Milliseconds()
	if err := iter.Close(); err != nil && !result.Truncated {
		return result, err
	}
	return result, nil
}

// jsonSafe converts Cassandra-specific types (gocql.UUID, []byte, time.Time)
// into values the existing JSON/CSV export path already knows how to render.
func jsonSafe(v any) any {
	switch x := v.(type) {
	case gocql.UUID:
		return x.String()
	case []byte:
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%x", x)
		}
		return string(raw)
	default:
		return v
	}
}

// ErrCQLExportUnsupported identifies tables that cannot be restored with
// Cassandra's INSERT JSON syntax (notably counter tables and views).
var ErrCQLExportUnsupported = errors.New("CQL INSERT export is not available for this table")

func (m *Manager) ValidateCassandraCQLExport(ctx context.Context, connection Connection, keyspace, table string) error {
	session, cleanup, err := cassandraSession(ctx, connection, keyspace)
	if err != nil {
		return err
	}
	defer cleanup()
	metadata, err := session.KeyspaceMetadata(keyspace)
	if err != nil {
		return err
	}
	item := metadata.Tables[table]
	if item == nil {
		return fmt.Errorf("%w: %s is not a base table", ErrCQLExportUnsupported, table)
	}
	for _, column := range item.Columns {
		if column.Type.Type() == gocql.TypeCounter {
			return fmt.Errorf("%w: counter tables require UPDATE rather than INSERT", ErrCQLExportUnsupported)
		}
	}
	return nil
}

func cassandraDDL(ctx context.Context, connection Connection, kind, keyspace, name string) (string, error) {
	if kind != "table" {
		return "", fmt.Errorf("no definition available for Cassandra %s objects", kind)
	}
	if keyspace == "" {
		keyspace = connection.Database
	}
	session, cleanup, err := cassandraSession(ctx, connection, keyspace)
	if err != nil {
		return "", err
	}
	defer cleanup()
	metadata, err := session.KeyspaceMetadata(keyspace)
	if err != nil {
		return "", err
	}
	table := metadata.Tables[name]
	if table == nil {
		return "", errors.New("object not found")
	}
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	columns := make([]string, 0, len(table.OrderedColumns)+1)
	for _, columnName := range table.OrderedColumns {
		column := table.Columns[columnName]
		columns = append(columns, "  "+quote(column.Name)+" "+cqlTypeName(column.Type))
	}
	partition := make([]string, len(table.PartitionKey))
	for i, column := range table.PartitionKey {
		partition[i] = quote(column.Name)
	}
	clustering := make([]string, len(table.ClusteringColumns))
	for i, column := range table.ClusteringColumns {
		clustering[i] = quote(column.Name)
	}
	primary := strings.Join(partition, ", ")
	if len(partition) > 1 {
		primary = "(" + primary + ")"
	}
	if len(clustering) > 0 {
		primary += ", " + strings.Join(clustering, ", ")
	}
	columns = append(columns, "  PRIMARY KEY ("+primary+")")
	return "CREATE TABLE " + quote(keyspace) + "." + quote(name) + " (\n" + strings.Join(columns, ",\n") + "\n);", nil
}

func cqlTypeName(info gocql.TypeInfo) string {
	switch value := info.(type) {
	case gocql.CollectionType:
		if value.Type() == gocql.TypeMap {
			return "map<" + cqlTypeName(value.Key) + ", " + cqlTypeName(value.Elem) + ">"
		}
		return value.Type().String() + "<" + cqlTypeName(value.Elem) + ">"
	case gocql.TupleTypeInfo:
		parts := make([]string, len(value.Elems))
		for i, item := range value.Elems {
			parts[i] = cqlTypeName(item)
		}
		return "tuple<" + strings.Join(parts, ", ") + ">"
	case gocql.UDTTypeInfo:
		return `"` + strings.ReplaceAll(value.Name, `"`, `""`) + `"`
	default:
		return info.Type().String()
	}
}

func (m *Manager) CassandraImport(ctx context.Context, connection Connection, keyspace, table string, columns []string, rows [][]any) error {
	session, cleanup, err := cassandraSession(ctx, connection, keyspace)
	if err != nil {
		return err
	}
	defer cleanup()
	metadata, err := session.KeyspaceMetadata(keyspace)
	if err != nil {
		return err
	}
	tableMetadata := metadata.Tables[table]
	if tableMetadata == nil {
		return errors.New("import table not found")
	}
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	quoted := make([]string, len(columns))
	placeholders := make([]string, len(columns))
	for i, column := range columns {
		if tableMetadata.Columns[column] == nil {
			return fmt.Errorf("import column %q not found", column)
		}
		quoted[i], placeholders[i] = quote(column), "?"
	}
	statement := "INSERT INTO " + quote(keyspace) + "." + quote(table) + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	batch := session.NewBatch(gocql.LoggedBatch).WithContext(ctx)
	for rowIndex, row := range rows {
		values := make([]any, len(row))
		for i, raw := range row {
			if raw == nil {
				continue
			}
			value, convertErr := cassandraImportValue(fmt.Sprint(raw), tableMetadata.Columns[columns[i]].Type.Type())
			if convertErr != nil {
				return fmt.Errorf("row %d column %s: %w", rowIndex+1, columns[i], convertErr)
			}
			values[i] = value
		}
		batch.Query(statement, values...)
	}
	return session.ExecuteBatch(batch)
}

func cassandraImportValue(value string, typ gocql.Type) (any, error) {
	switch typ {
	case gocql.TypeInt:
		parsed, err := strconv.ParseInt(value, 10, 32)
		return int(parsed), err
	case gocql.TypeBigInt, gocql.TypeCounter:
		return strconv.ParseInt(value, 10, 64)
	case gocql.TypeSmallInt:
		parsed, err := strconv.ParseInt(value, 10, 16)
		return int16(parsed), err
	case gocql.TypeTinyInt:
		parsed, err := strconv.ParseInt(value, 10, 8)
		return int8(parsed), err
	case gocql.TypeFloat:
		parsed, err := strconv.ParseFloat(value, 32)
		return float32(parsed), err
	case gocql.TypeDouble:
		return strconv.ParseFloat(value, 64)
	case gocql.TypeBoolean:
		return strconv.ParseBool(value)
	case gocql.TypeTimestamp:
		return time.Parse(time.RFC3339Nano, value)
	case gocql.TypeUUID, gocql.TypeTimeUUID:
		return gocql.ParseUUID(value)
	case gocql.TypeBlob:
		return []byte(value), nil
	default:
		return value, nil
	}
}
