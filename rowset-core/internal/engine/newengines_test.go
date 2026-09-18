package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gocql/gocql"
	goredis "github.com/redis/go-redis/v9"
)

// TestLiveCockroachDB exercises CockroachDB through the same path as
// PostgreSQL (wire-compatible), including a real schema read.
func TestLiveCockroachDB(t *testing.T) {
	host := os.Getenv("ROWSET_TEST_COCKROACHDB_HOST")
	if host == "" {
		t.Skip("cockroachdb test server not configured")
	}
	port := 26257
	if v := os.Getenv("ROWSET_TEST_COCKROACHDB_PORT"); v != "" {
		var err error
		if port, err = parsePort(v); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := Connection{ID: "crdb", Engine: "cockroachdb", Host: host, Port: port, Database: "defaultdb", Username: "root", TLS: TLSSettings{Mode: TLSDisable}}
	m := NewManager()
	defer m.Close()
	if err := m.Test(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Databases(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Execute(ctx, c, "CREATE TABLE IF NOT EXISTS rowset_crdb_probe (id INT PRIMARY KEY, label TEXT)", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Execute(ctx, c, "INSERT INTO rowset_crdb_probe VALUES (1,'hello') ON CONFLICT (id) DO NOTHING", 10); err != nil {
		t.Fatal(err)
	}
	r, err := m.Execute(ctx, c, "SELECT id, label FROM rowset_crdb_probe WHERE id=1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 {
		t.Fatalf("rows: %#v", r)
	}
	schema, err := m.Schema(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Tables) == 0 {
		t.Fatal("expected at least one table from CockroachDB's schema catalog")
	}
	// The capability map advertises Transactions for CockroachDB; this was
	// never exercised live before, only inferred from PostgreSQL wire
	// compatibility.
	tx, err := m.Begin(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Execute(ctx, "UPDATE rowset_crdb_probe SET label='changed' WHERE id=1", 10); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if r, err = m.Execute(ctx, c, "SELECT label FROM rowset_crdb_probe WHERE id=1", 10); err != nil || r.Rows[0][0] != "hello" {
		t.Fatalf("rollback did not revert the update: %#v %v", r, err)
	}
	tx, err = m.Begin(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Execute(ctx, "DELETE FROM rowset_crdb_probe WHERE id=1", 10); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if r, err = m.Execute(ctx, c, "SELECT count(*) FROM rowset_crdb_probe", 10); err != nil || r.Rows[0][0] != int64(0) {
		t.Fatalf("commit did not persist the delete: %#v %v", r, err)
	}
}

// TestLiveRedis exercises key scanning across string, hash, list, set and
// zset types, plus TTL and pattern filtering.
func TestLiveRedis(t *testing.T) {
	host := os.Getenv("ROWSET_TEST_REDIS_HOST")
	if host == "" {
		t.Skip("redis test server not configured")
	}
	port := 6379
	if v := os.Getenv("ROWSET_TEST_REDIS_PORT"); v != "" {
		var err error
		if port, err = parsePort(v); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := Connection{ID: "redis", Engine: "redis", Host: host, Port: port, Database: "0", TLS: TLSSettings{Mode: TLSDisable}}
	if err := redisTest(ctx, c); err != nil {
		t.Fatal(err)
	}
	client, cleanup, err := redisClient(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, key := range []string{"rowset:probe:string", "rowset:probe:hash", "rowset:probe:list", "rowset:probe:set", "rowset:probe:zset"} {
		if err := client.Del(ctx, key).Err(); err != nil {
			t.Fatal(err)
		}
	}
	client.Set(ctx, "rowset:probe:string", "hello", 0)
	client.Expire(ctx, "rowset:probe:string", time.Hour)
	client.HSet(ctx, "rowset:probe:hash", "field", "value")
	client.LPush(ctx, "rowset:probe:list", "a", "b")
	client.SAdd(ctx, "rowset:probe:set", "x", "y")
	client.ZAdd(ctx, "rowset:probe:zset", goredis.Z{Score: 1, Member: "z"})
	entries, _, err := NewManager().RedisScan(ctx, c, RedisScanInput{Database: "0", Pattern: "rowset:probe:*", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 keys, got %d: %#v", len(entries), entries)
	}
	types := map[string]bool{}
	for _, e := range entries {
		types[e.Type] = true
		if e.Type == "string" && e.TTL <= 0 {
			t.Fatalf("expected string key to report a TTL, got %d", e.TTL)
		}
	}
	for _, want := range []string{"string", "hash", "list", "set", "zset"} {
		if !types[want] {
			t.Fatalf("missing type %s in scan results: %#v", want, types)
		}
	}
	dbs, err := redisDatabases(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) == 0 {
		t.Fatal("expected at least the connection's own DB index")
	}
	schema, err := redisSchema(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Tables) == 0 {
		t.Fatal("expected redis schema to report at least one key-type grouping")
	}
}

// TestLiveCassandra exercises keyspace/table discovery and a read-only CQL
// query; anything other than SELECT must be rejected before it ever reaches
// this layer (the API handler enforces that), but the engine itself must
// still run a real SELECT correctly.
func TestLiveCassandra(t *testing.T) {
	host := os.Getenv("ROWSET_TEST_CASSANDRA_HOST")
	if host == "" {
		t.Skip("cassandra test server not configured")
	}
	port := 9042
	if v := os.Getenv("ROWSET_TEST_CASSANDRA_PORT"); v != "" {
		var err error
		if port, err = parsePort(v); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := Connection{ID: "cassandra", Engine: "cassandra", Host: host, Port: port, Database: "rowset_probe", TLS: TLSSettings{Mode: TLSDisable}}
	if err := cassandraTest(ctx, c); err != nil {
		t.Fatal(err)
	}
	session, cleanup, err := cassandraSession(ctx, c, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Query("CREATE KEYSPACE IF NOT EXISTS rowset_probe WITH replication = {'class':'SimpleStrategy','replication_factor':1}").WithContext(ctx).Exec(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := session.Query("CREATE TABLE IF NOT EXISTS rowset_probe.items (id int PRIMARY KEY, label text)").WithContext(ctx).Exec(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := session.Query("INSERT INTO rowset_probe.items (id, label) VALUES (1, 'hello')").WithContext(ctx).Exec(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	cleanup()
	dbs, err := cassandraDatabases(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range dbs {
		if name == "rowset_probe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected rowset_probe keyspace in %v", dbs)
	}
	schema, err := cassandraSchema(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Tables) == 0 {
		t.Fatal("expected at least one table in rowset_probe keyspace")
	}
	result, err := NewManager().CassandraQuery(ctx, c, CassandraQueryInput{Keyspace: "rowset_probe", Query: "SELECT id, label FROM items WHERE id=1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows: %#v", result)
	}
	manager := NewManager()
	for _, query := range []string{
		"INSERT INTO items (id, label) VALUES (2, 'write')",
		"UPDATE items SET label='updated' WHERE id=2",
		"BEGIN UNLOGGED BATCH INSERT INTO items (id, label) VALUES (3, 'batch-a'); INSERT INTO items (id, label) VALUES (4, 'batch-b'); APPLY BATCH;",
		"CREATE TABLE IF NOT EXISTS ddl_probe (id int PRIMARY KEY, value text)",
	} {
		if _, err := manager.CassandraQuery(ctx, c, CassandraQueryInput{Keyspace: "rowset_probe", Query: query, Limit: 10}); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if err := manager.CassandraImport(ctx, c, "rowset_probe", "items", []string{"id", "label"}, [][]any{{"5", "imported"}}); err != nil {
		t.Fatalf("import: %v", err)
	}
	result, err = manager.CassandraQuery(ctx, c, CassandraQueryInput{Keyspace: "rowset_probe", Query: "SELECT id, label FROM items WHERE id IN (2,3,4,5)", Limit: 10})
	if err != nil || len(result.Rows) != 4 {
		t.Fatalf("writes: %#v err=%v", result, err)
	}
	ddl, err := cassandraDDL(ctx, c, "table", "rowset_probe", "items")
	if err != nil || !strings.Contains(ddl, "CREATE TABLE") || !strings.Contains(ddl, "PRIMARY KEY") {
		t.Fatalf("ddl: %q err=%v", ddl, err)
	}
	if err := manager.ValidateCassandraCQLExport(ctx, c, "rowset_probe", "items"); err != nil {
		t.Fatalf("CQL export validation: %v", err)
	}
	jsonRows, err := manager.CassandraQuery(ctx, c, CassandraQueryInput{Keyspace: "rowset_probe", Query: `SELECT JSON * FROM "rowset_probe"."items" WHERE id=1`, Limit: 10})
	if err != nil || len(jsonRows.Rows) != 1 || len(jsonRows.Rows[0]) != 1 {
		t.Fatalf("SELECT JSON: %#v err=%v", jsonRows, err)
	}
	jsonRow, ok := jsonRows.Rows[0][0].(string)
	if !ok {
		t.Fatalf("SELECT JSON returned %T", jsonRows.Rows[0][0])
	}
	insertJSON := `INSERT INTO "rowset_probe"."items" JSON '` + strings.ReplaceAll(jsonRow, "'", "''") + `' DEFAULT UNSET`
	if _, err := manager.CassandraQuery(ctx, c, CassandraQueryInput{Keyspace: "rowset_probe", Query: insertJSON, Limit: 10}); err != nil {
		t.Fatalf("replaying SELECT JSON as INSERT JSON: %v", err)
	}
	if _, err := manager.CassandraQuery(ctx, c, CassandraQueryInput{Keyspace: "rowset_probe", Query: "CREATE TABLE IF NOT EXISTS counter_probe (id int PRIMARY KEY, total counter)", Limit: 10}); err != nil {
		t.Fatalf("creating counter table: %v", err)
	}
	if err := manager.ValidateCassandraCQLExport(ctx, c, "rowset_probe", "counter_probe"); !errors.Is(err, ErrCQLExportUnsupported) {
		t.Fatalf("counter table should reject INSERT export: %v", err)
	}
}

func TestCassandraImportValue(t *testing.T) {
	tests := []struct {
		value string
		typ   gocql.Type
		want  any
	}{
		{"42", gocql.TypeInt, int(42)}, {"true", gocql.TypeBoolean, true}, {"1.5", gocql.TypeDouble, 1.5}, {"hello", gocql.TypeText, "hello"},
	}
	for _, test := range tests {
		got, err := cassandraImportValue(test.value, test.typ)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s: got %#v err=%v", test.value, got, err)
		}
	}
	if _, err := cassandraImportValue("not-an-int", gocql.TypeInt); err == nil {
		t.Fatal("invalid integer accepted")
	}
}

// TestLiveElasticsearch exercises index discovery and _search.
func TestLiveElasticsearch(t *testing.T) {
	host := os.Getenv("ROWSET_TEST_ELASTICSEARCH_HOST")
	if host == "" {
		t.Skip("elasticsearch test server not configured")
	}
	port := 9200
	if v := os.Getenv("ROWSET_TEST_ELASTICSEARCH_PORT"); v != "" {
		var err error
		if port, err = parsePort(v); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := Connection{ID: "es", Engine: "elasticsearch", Host: host, Port: port, Database: "elasticsearch", TLS: TLSSettings{Mode: TLSDisable}}
	if err := elasticsearchTest(ctx, c); err != nil {
		t.Fatal(err)
	}
	client, cleanup, err := elasticsearchClient(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	body := `{"label":"hello"}`
	res, err := client.Index("rowset-probe", strings.NewReader(body), client.Index.WithContext(ctx), client.Index.WithRefresh("true"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.IsError() {
		t.Fatalf("index: %s", res.Status())
	}
	schema, err := elasticsearchSchema(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Tables["elasticsearch.rowset-probe"]; !ok {
		t.Fatalf("expected rowset-probe index in schema: %#v", schema.Tables)
	}
	result, err := NewManager().ElasticsearchSearch(ctx, c, ElasticsearchSearchInput{Index: "rowset-probe", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Documents) != 1 {
		t.Fatalf("expected 1 document, got %d", len(result.Documents))
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(result.Documents[0], &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["_source"]; !ok {
		t.Fatalf("expected _source in hit: %s", result.Documents[0])
	}
}

func parsePort(v string) (int, error) { return strconv.Atoi(v) }
