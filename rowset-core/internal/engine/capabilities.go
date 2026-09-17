package engine

// Capabilities is the single backend authority for controls whose support
// differs by engine. API handlers still validate independently; Studio uses
// this map so it never advertises an operation the engine will reject.
type Capabilities struct {
	Transactions   bool `json:"transactions"`
	Explain        bool `json:"explain"`
	ExplainAnalyze bool `json:"explainAnalyze"`
	DDL            bool `json:"ddl"`
	CSVImport      bool `json:"csvImport"`
	CSVExport      bool `json:"csvExport"`
	JSONExport     bool `json:"jsonExport"`
	SQLExport      bool `json:"sqlExport"`
	CQLExport      bool `json:"cqlExport"`
	SSH            bool `json:"ssh"`
	Shared         bool `json:"shared"`
	MultiNode      bool `json:"multiNode"`
	DocumentWrite  bool `json:"documentWrite"`
	KeyWrite       bool `json:"keyWrite"`
}

func EngineCapabilities(name string) Capabilities {
	switch name {
	case "postgres", "mysql", "mariadb":
		return Capabilities{Transactions: true, Explain: true, ExplainAnalyze: true, DDL: true, CSVImport: true, CSVExport: true, JSONExport: true, SQLExport: true, SSH: true, Shared: true, MultiNode: true}
	case "mssql", "sqlserver":
		return Capabilities{Transactions: true, Explain: true, ExplainAnalyze: true, DDL: true, CSVImport: true, CSVExport: true, JSONExport: true, SQLExport: true, SSH: true, Shared: true, MultiNode: true}
	case "cockroachdb":
		return Capabilities{Transactions: true, Explain: true, ExplainAnalyze: true, DDL: true, CSVImport: true, CSVExport: true, JSONExport: true, SQLExport: true, SSH: true, Shared: true, MultiNode: true}
	case "sqlite", "duckdb":
		return Capabilities{Transactions: true, DDL: true, CSVImport: true, CSVExport: true, JSONExport: true}
	case "clickhouse":
		return Capabilities{Explain: true, DDL: true, CSVExport: true, JSONExport: true, SSH: true}
	case "cassandra":
		return Capabilities{DDL: true, CSVImport: true, CSVExport: true, JSONExport: true, CQLExport: true, SSH: true, Shared: true, MultiNode: true}
	case "mongodb", "elasticsearch":
		return Capabilities{DocumentWrite: true}
	case "redis", "valkey":
		return Capabilities{KeyWrite: true}
	default:
		return Capabilities{}
	}
}

func AllEngineCapabilities() map[string]Capabilities {
	result := map[string]Capabilities{}
	for _, name := range []string{"postgres", "mysql", "mariadb", "mssql", "cockroachdb", "sqlite", "duckdb", "clickhouse", "mongodb", "redis", "valkey", "cassandra", "elasticsearch"} {
		result[name] = EngineCapabilities(name)
	}
	return result
}

// Scheduled SELECTs use the pooled SQL query path. File databases and
// ClickHouse use that path too; document and CQL engines have separate APIs.
func SupportsScheduledSQL(name string) bool {
	switch name {
	case "mongodb", "redis", "valkey", "cassandra", "elasticsearch":
		return false
	default:
		return true
	}
}
