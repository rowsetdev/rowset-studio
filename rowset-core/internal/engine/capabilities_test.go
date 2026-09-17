package engine

import "testing"

func TestEngineCapabilitiesDoNotAdvertiseUnsupportedControls(t *testing.T) {
	if capability := EngineCapabilities("cockroachdb"); !capability.Explain || !capability.ExplainAnalyze {
		t.Fatalf("CockroachDB plan capabilities are inconsistent: %+v", capability)
	}
	if capability := EngineCapabilities("clickhouse"); !capability.Explain || capability.ExplainAnalyze {
		t.Fatalf("ClickHouse plan capabilities are inconsistent: %+v", capability)
	}
	if capability := EngineCapabilities("mongodb"); !capability.DocumentWrite {
		t.Fatalf("MongoDB should advertise document writes: %+v", capability)
	}
	if capability := EngineCapabilities("elasticsearch"); !capability.DocumentWrite {
		t.Fatalf("Elasticsearch should advertise document writes: %+v", capability)
	}
	if capability := EngineCapabilities("redis"); !capability.KeyWrite {
		t.Fatalf("Redis should advertise key writes: %+v", capability)
	}
	if capability := EngineCapabilities("valkey"); !capability.KeyWrite {
		t.Fatalf("Valkey should advertise key writes: %+v", capability)
	}
	if capability := EngineCapabilities("sqlite"); !capability.CSVImport {
		t.Fatalf("SQLite should advertise CSV import: %+v", capability)
	}
	if capability := EngineCapabilities("duckdb"); !capability.CSVImport {
		t.Fatalf("DuckDB should advertise CSV import: %+v", capability)
	}
	if capability := EngineCapabilities("cassandra"); !capability.DDL || !capability.CSVImport || !capability.CQLExport || capability.SQLExport || capability.Transactions {
		t.Fatalf("Cassandra capabilities are inconsistent: %+v", capability)
	}
	if capability := EngineCapabilities("mssql"); !capability.Explain || !capability.ExplainAnalyze {
		t.Fatalf("SQL Server plan capabilities are inconsistent: %+v", capability)
	}
}

func TestScheduledSQLUsesOnlyPooledEngines(t *testing.T) {
	for _, name := range []string{"postgres", "sqlite", "duckdb", "clickhouse"} {
		if !SupportsScheduledSQL(name) {
			t.Errorf("%s has a pooled SQL query path", name)
		}
	}
	for _, name := range []string{"mongodb", "redis", "valkey", "cassandra", "elasticsearch"} {
		if SupportsScheduledSQL(name) {
			t.Errorf("%s has no pooled SQL query path", name)
		}
	}
}

func TestAllEngineCapabilitiesIncludesEveryConnectionEngine(t *testing.T) {
	capabilities := AllEngineCapabilities()
	for _, name := range []string{"postgres", "mysql", "mariadb", "mssql", "cockroachdb", "sqlite", "duckdb", "clickhouse", "mongodb", "redis", "valkey", "cassandra", "elasticsearch"} {
		if _, ok := capabilities[name]; !ok {
			t.Errorf("missing %s", name)
		}
	}
}
