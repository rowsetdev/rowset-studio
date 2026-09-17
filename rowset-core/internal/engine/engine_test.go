package engine

import (
	"strings"
	"testing"
)

func TestConnectionStringsDoNotLoseCredentials(t *testing.T) {
	for _, connection := range []Connection{
		{Engine: "postgres", Host: "db", Port: 5432, Database: "app", Username: "user@org", Password: "p@ss/word", TLS: TLSSettings{Mode: TLSRequire}},
		{Engine: "mysql", Host: "db", Port: 3306, Database: "app", Username: "user", Password: "p@ss/word", TLS: TLSSettings{Mode: TLSRequire}},
		{Engine: "mssql", Host: "db", Port: 1433, Database: "app", Username: "user", Password: "p@ss/word", TLS: TLSSettings{Mode: TLSRequire}},
		{Engine: "cockroachdb", Host: "db", Port: 26257, Database: "app", Username: "user@org", Password: "p@ss/word", TLS: TLSSettings{Mode: TLSRequire}},
	} {
		driver, dsn, err := connectionString(connection)
		if err != nil {
			t.Fatal(err)
		}
		if driver == "" || dsn == "" || strings.Contains(dsn, " ") {
			t.Fatalf("bad dsn: %s %q", driver, dsn)
		}
	}
}

func TestStatementResultClassification(t *testing.T) {
	for _, query := range []string{"SELECT 1", "with x as (select 1) select * from x", "SHOW TABLES", "UPDATE x SET y=1 RETURNING y"} {
		if !returnsRows(query) {
			t.Fatalf("should return rows: %s", query)
		}
	}
	for _, query := range []string{"INSERT INTO x VALUES (1)", "UPDATE x SET y=1", "DELETE FROM x"} {
		if returnsRows(query) {
			t.Fatalf("should be command: %s", query)
		}
	}
	if returnsRows("WITH x AS (SELECT id FROM users WHERE id=1) UPDATE users SET ok=true WHERE id IN (SELECT id FROM x)") {
		t.Fatal("CTE UPDATE was classified as a row result")
	}
}

func TestPoolKeyChangesWhenPasswordRotates(t *testing.T) {
	connection := Connection{ID: "saved-connection", Engine: "postgres", Host: "db", Port: 5432, Database: "app", Username: "user", Password: "one", PoolSize: 10}
	one := poolKey(connection)
	connection.Password = "two"
	two := poolKey(connection)
	if one == two || strings.Contains(one, "one") || strings.Contains(two, "two") {
		t.Fatal("unsafe pool key")
	}
	connection.Password = "one"
	connection.PoolSize = 20
	if one == poolKey(connection) {
		t.Fatal("pool configuration change reused the old pool")
	}
}

func TestPoolIsBoundedAndInvalidatedBySavedConnection(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() { _ = manager.Close() })
	connection := Connection{ID: "saved", Engine: "postgres", Host: "127.0.0.1", Port: 1, Database: "app", Username: "user", Password: "secret", PoolSize: 7}
	db, err := manager.database(connection)
	if err != nil {
		t.Fatal(err)
	}
	if db.Stats().MaxOpenConnections != 7 {
		t.Fatalf("pool is not bounded: %#v", db.Stats())
	}
	if err := manager.Invalidate(connection.ID); err != nil {
		t.Fatal(err)
	}
	if len(manager.pools) != 0 {
		t.Fatal("stale saved-connection pool survived invalidation")
	}
}
