package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func tableKey(schema, table string) string {
	if schema == "" {
		return table
	}
	return schema + "." + table
}

func (m *Manager) Databases(ctx context.Context, connection Connection) ([]string, error) {
	if FileEngine(connection.Engine) {
		return []string{connection.Database}, nil
	}
	if connection.Engine == "mongodb" {
		return mongoDatabases(ctx, connection)
	}
	if connection.Engine == "redis" || connection.Engine == "valkey" {
		return redisDatabases(ctx, connection)
	}
	if connection.Engine == "cassandra" {
		return cassandraDatabases(ctx, connection)
	}
	if connection.Engine == "elasticsearch" {
		return elasticsearchDatabases(ctx, connection)
	}
	db, err := m.database(connection)
	if err != nil {
		return nil, err
	}
	query := ""
	switch strings.ToLower(connection.Engine) {
	case "clickhouse":
		// INFORMATION_SCHEMA is a case-variant alias ClickHouse keeps for MySQL
		// compatibility; information_schema is the canonical one, so only that
		// one is listed to avoid showing the same schema twice.
		query = "SELECT name FROM system.databases WHERE name != 'INFORMATION_SCHEMA' ORDER BY name"
	case "postgres", "postgresql", "cockroachdb":
		query = "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname"
	case "mysql", "mariadb":
		query = "SELECT CAST(schema_name AS CHAR) FROM information_schema.schemata ORDER BY schema_name"
	case "mssql", "sqlserver":
		query = "SELECT name FROM sys.databases WHERE state_desc='ONLINE' ORDER BY name"
	default:
		return nil, fmt.Errorf("unsupported engine: %s", connection.Engine)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name != "" {
			result = append(result, name)
		}
	}
	return result, rows.Err()
}

func (m *Manager) Schema(ctx context.Context, connection Connection) (Schema, error) {
	if connection.Engine == "mongodb" {
		return mongoSchema(ctx, connection)
	}
	if connection.Engine == "redis" || connection.Engine == "valkey" {
		return redisSchema(ctx, connection)
	}
	if connection.Engine == "cassandra" {
		return cassandraSchema(ctx, connection)
	}
	if connection.Engine == "elasticsearch" {
		return elasticsearchSchema(ctx, connection)
	}
	db, err := m.database(connection)
	if err != nil {
		return Schema{}, err
	}
	if AdditionalEngine(connection.Engine) {
		return additionalSchema(ctx, db, connection)
	}
	result := Schema{Tables: map[string][]Column{}, Indexes: map[string][]Index{}, Views: map[string]bool{}}
	engine := strings.ToLower(connection.Engine)
	if engine == "cockroachdb" {
		// CockroachDB emulates PostgreSQL's wire protocol and system catalogs
		// closely enough that the same catalog queries work unmodified.
		engine = "postgres"
	}
	query, err := schemaColumnsQuery(engine)
	if err != nil {
		return result, err
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var schemaName, table, name, dataType, nullable string
		var defaultValue, generated, comment sql.NullString
		if err := rows.Scan(&schemaName, &table, &name, &dataType, &nullable, &defaultValue, &generated, &comment); err != nil {
			rows.Close()
			return result, err
		}
		key := tableKey(schemaName, table)
		result.Tables[key] = append(result.Tables[key], Column{Schema: schemaName, Table: table, Name: name, DataType: dataType, Nullable: strings.EqualFold(nullable, "YES"), Default: defaultValue.String, Generated: generated.String, Comment: comment.String})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	// Tables and columns are the explorer's core payload. Load optional object
	// metadata concurrently and cap the whole enrichment phase: a database user
	// may have SELECT without VIEW DEFINITION, and one slow/denied object kind
	// must not hide every table in a large database.
	enrichmentCtx, cancelEnrichment := context.WithTimeout(ctx, 15*time.Second)
	defer cancelEnrichment()
	enrichments := loadSchemaEnrichments(enrichmentCtx, db, engine)
	// One read per loader in loadSchemaEnrichments: primary keys, foreign
	// keys, indexes, views, routines, triggers and sequences.
	for range 7 {
		enrichment := <-enrichments
		if enrichment.err != nil {
			result.Warnings = append(result.Warnings, metadataWarning(enrichment.name, enrichment.err))
			continue
		}
		switch enrichment.name {
		case "primary keys":
			applyPrimaryKeys(&result, enrichment.primaryKeys)
		case "foreign keys":
			applyForeignKeys(&result, enrichment.foreignKeys)
		case "indexes":
			result.Indexes = enrichment.indexes
		case "views":
			result.Views = enrichment.views
		case "routines":
			result.Routines = enrichment.routines
		case "triggers":
			result.Triggers = enrichment.triggers
		case "sequences":
			result.Sequences = enrichment.sequences
		}
	}
	return result, nil
}

func schemaColumnsQuery(engine string) (string, error) {
	query := `SELECT table_schema,table_name,column_name,data_type,is_nullable FROM information_schema.columns`
	orderBy := " ORDER BY table_schema,table_name,ordinal_position"
	switch engine {
	case "postgres":
		query = `SELECT cols.table_schema,cols.table_name,cols.column_name,pg_catalog.format_type(a.atttypid,a.atttypmod),cols.is_nullable,cols.column_default,CASE WHEN cols.is_identity='YES' THEN 'identity '||cols.identity_generation WHEN cols.is_generated='ALWAYS' THEN 'generated: '||COALESCE(cols.generation_expression,'') ELSE '' END,pg_catalog.col_description(c.oid,a.attnum)
		FROM information_schema.columns cols
		JOIN pg_catalog.pg_namespace n ON n.nspname=cols.table_schema
		JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=cols.table_name
		JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attname=cols.column_name
		WHERE cols.table_schema NOT IN ('pg_catalog','information_schema')`
		orderBy = " ORDER BY cols.table_schema,cols.table_name,cols.ordinal_position"
	case "mysql", "mariadb":
		query = `SELECT CAST(table_schema AS CHAR),CAST(table_name AS CHAR),CAST(column_name AS CHAR),CAST(column_type AS CHAR),CAST(is_nullable AS CHAR),CAST(column_default AS CHAR),CAST(extra AS CHAR),CAST(column_comment AS CHAR) FROM information_schema.columns WHERE table_schema=DATABASE()`
	case "mssql", "sqlserver":
		// INFORMATION_SCHEMA object-name resolution can follow a database's
		// catalog collation. Use SQL Server's native catalogs so metadata
		// discovery works consistently across case-sensitive/custom catalogs.
		query = `SELECT s.name,o.name,c.name,ty.name + CASE
			WHEN ty.name IN ('varchar','nvarchar','varbinary','char','nchar','binary') THEN '('+CASE WHEN c.max_length=-1 THEN 'max' ELSE CAST(CASE WHEN ty.name IN ('nvarchar','nchar') THEN c.max_length/2 ELSE c.max_length END AS varchar(10)) END+')'
			WHEN ty.name IN ('decimal','numeric') THEN '('+CAST(c.precision AS varchar(10))+','+CAST(c.scale AS varchar(10))+')'
			WHEN ty.name IN ('datetime2','datetimeoffset','time') THEN '('+CAST(c.scale AS varchar(10))+')' ELSE '' END,
			CASE WHEN c.is_nullable=1 THEN 'YES' ELSE 'NO' END,dc.definition,
			CASE WHEN c.is_identity=1 THEN 'identity' WHEN c.is_computed=1 THEN 'computed: '+COALESCE(cc.definition,'') ELSE '' END,CAST(ep.value AS nvarchar(4000))
			FROM sys.objects o
			JOIN sys.schemas s ON s.schema_id=o.schema_id
			JOIN sys.columns c ON c.object_id=o.object_id
			JOIN sys.types ty ON ty.user_type_id=c.user_type_id
			LEFT JOIN sys.default_constraints dc ON dc.object_id=c.default_object_id
			LEFT JOIN sys.computed_columns cc ON cc.object_id=c.object_id AND cc.column_id=c.column_id
			LEFT JOIN sys.extended_properties ep ON ep.class=1 AND ep.major_id=c.object_id AND ep.minor_id=c.column_id AND ep.name='MS_Description'
			WHERE o.type IN ('U','V') AND o.is_ms_shipped=0`
		orderBy = " ORDER BY s.name,o.name,c.column_id"
	default:
		return "", fmt.Errorf("unsupported engine: %s", engine)
	}
	query += orderBy
	return query, nil
}

type schemaEnrichment struct {
	name        string
	primaryKeys map[string]bool
	foreignKeys map[string]string
	indexes     map[string][]Index
	views       map[string]bool
	routines    []Routine
	triggers    []Trigger
	sequences   []Sequence
	err         error
}

func loadSchemaEnrichments(ctx context.Context, db *sql.DB, engine string) <-chan schemaEnrichment {
	output := make(chan schemaEnrichment, 7)
	// At most two catalog reads run together, so a slow object kind does not
	// consume the entire deadline while small database pools remain usable.
	limit := make(chan struct{}, 2)
	loaders := []func() schemaEnrichment{
		func() schemaEnrichment {
			items, err := loadPrimaryKeys(ctx, db, engine)
			return schemaEnrichment{name: "primary keys", primaryKeys: items, err: err}
		},
		func() schemaEnrichment {
			items, err := loadForeignKeys(ctx, db, engine)
			return schemaEnrichment{name: "foreign keys", foreignKeys: items, err: err}
		},
		func() schemaEnrichment {
			value := Schema{Indexes: map[string][]Index{}}
			err := loadIndexes(ctx, db, engine, &value)
			return schemaEnrichment{name: "indexes", indexes: value.Indexes, err: err}
		},
		func() schemaEnrichment {
			value := Schema{Views: map[string]bool{}}
			err := loadViews(ctx, db, engine, &value)
			return schemaEnrichment{name: "views", views: value.Views, err: err}
		},
		func() schemaEnrichment {
			value := Schema{}
			err := loadRoutines(ctx, db, engine, &value)
			return schemaEnrichment{name: "routines", routines: value.Routines, err: err}
		},
		func() schemaEnrichment {
			value := Schema{}
			err := loadTriggers(ctx, db, engine, &value)
			return schemaEnrichment{name: "triggers", triggers: value.Triggers, err: err}
		},
		func() schemaEnrichment {
			value := Schema{}
			err := loadSequences(ctx, db, engine, &value)
			return schemaEnrichment{name: "sequences", sequences: value.Sequences, err: err}
		},
	}
	for _, loader := range loaders {
		go func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			output <- loader()
		}()
	}
	return output
}

func applyPrimaryKeys(schema *Schema, keys map[string]bool) {
	for tableKey, columns := range schema.Tables {
		for index := range columns {
			coord := tableKey + "." + columns[index].Name
			columns[index].PrimaryKey = keys[coord]
		}
		schema.Tables[tableKey] = columns
	}
}

func applyForeignKeys(schema *Schema, keys map[string]string) {
	for tableKey, columns := range schema.Tables {
		for index := range columns {
			coord := tableKey + "." + columns[index].Name
			columns[index].References = keys[coord]
		}
		schema.Tables[tableKey] = columns
	}
}

func metadataWarning(kind string, err error) string {
	return fmt.Sprintf("%s metadata unavailable: %v", kind, err)
}

func loadPrimaryKeys(ctx context.Context, db *sql.DB, engine string) (map[string]bool, error) {
	query := `SELECT tc.table_schema,tc.table_name,kcu.column_name FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage kcu ON kcu.constraint_name=tc.constraint_name AND kcu.table_schema=tc.table_schema AND kcu.table_name=tc.table_name WHERE tc.constraint_type='PRIMARY KEY'`
	if engine == "postgres" {
		query += " AND tc.table_schema NOT IN ('pg_catalog','information_schema')"
	}
	if engine == "mysql" || engine == "mariadb" {
		query += " AND tc.table_schema=DATABASE()"
	}
	if engine == "mssql" || engine == "sqlserver" {
		query = `SELECT s.name,t.name,c.name
			FROM sys.indexes i
			JOIN sys.tables t ON t.object_id=i.object_id
			JOIN sys.schemas s ON s.schema_id=t.schema_id
			JOIN sys.index_columns ic ON ic.object_id=i.object_id AND ic.index_id=i.index_id
			JOIN sys.columns c ON c.object_id=ic.object_id AND c.column_id=ic.column_id
			WHERE i.is_primary_key=1`
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var schema, table, column string
		if err := rows.Scan(&schema, &table, &column); err != nil {
			return nil, err
		}
		result[tableKey(schema, table)+"."+column] = true
	}
	return result, rows.Err()
}

func loadForeignKeys(ctx context.Context, db *sql.DB, engine string) (map[string]string, error) {
	var query string
	switch engine {
	case "postgres":
		query = `SELECT n.nspname,c.relname,a.attname,rn.nspname,rc.relname,ra.attname
		FROM pg_constraint fk JOIN pg_class c ON c.oid=fk.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_class rc ON rc.oid=fk.confrelid JOIN pg_namespace rn ON rn.oid=rc.relnamespace
		CROSS JOIN LATERAL unnest(fk.conkey,fk.confkey) AS pair(local_column,remote_column)
		JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=pair.local_column
		JOIN pg_attribute ra ON ra.attrelid=rc.oid AND ra.attnum=pair.remote_column
		WHERE fk.contype='f' AND n.nspname NOT IN ('pg_catalog','information_schema')`
	case "mysql", "mariadb":
		query = `SELECT CAST(table_schema AS CHAR),CAST(table_name AS CHAR),CAST(column_name AS CHAR),CAST(referenced_table_schema AS CHAR),CAST(referenced_table_name AS CHAR),CAST(referenced_column_name AS CHAR) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND referenced_table_name IS NOT NULL`
	case "mssql", "sqlserver":
		query = `SELECT s.name,t.name,c.name,rs.name,rt.name,rc.name FROM sys.foreign_key_columns fkc JOIN sys.tables t ON t.object_id=fkc.parent_object_id JOIN sys.schemas s ON s.schema_id=t.schema_id JOIN sys.columns c ON c.object_id=fkc.parent_object_id AND c.column_id=fkc.parent_column_id JOIN sys.tables rt ON rt.object_id=fkc.referenced_object_id JOIN sys.schemas rs ON rs.schema_id=rt.schema_id JOIN sys.columns rc ON rc.object_id=fkc.referenced_object_id AND rc.column_id=fkc.referenced_column_id`
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var schema, table, column, refSchema, refTable, refColumn string
		if err := rows.Scan(&schema, &table, &column, &refSchema, &refTable, &refColumn); err != nil {
			return nil, err
		}
		result[tableKey(schema, table)+"."+column] = refSchema + "." + refTable + "." + refColumn
	}
	return result, rows.Err()
}

func loadIndexes(ctx context.Context, db *sql.DB, engine string, schema *Schema) error {
	var query string
	switch engine {
	case "postgres":
		query = `SELECT n.nspname,t.relname,i.relname,ix.indisunique,ix.indisprimary,pg_get_indexdef(ix.indexrelid,pos.ordinality::int,true)
		FROM pg_index ix JOIN pg_class t ON t.oid=ix.indrelid JOIN pg_class i ON i.oid=ix.indexrelid JOIN pg_namespace n ON n.oid=t.relnamespace
		CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS pos(attnum,ordinality)
		WHERE n.nspname NOT IN ('pg_catalog','information_schema') ORDER BY n.nspname,t.relname,i.relname,pos.ordinality`
	case "mysql", "mariadb":
		query = `SELECT CAST(table_schema AS CHAR),CAST(table_name AS CHAR),CAST(index_name AS CHAR),(non_unique=0),(index_name='PRIMARY'),CAST(column_name AS CHAR) FROM information_schema.statistics WHERE table_schema=DATABASE() ORDER BY table_schema,table_name,index_name,seq_in_index`
	case "mssql", "sqlserver":
		query = `SELECT s.name,t.name,i.name,i.is_unique,i.is_primary_key,c.name,ic.is_included_column,i.filter_definition FROM sys.indexes i JOIN sys.tables t ON t.object_id=i.object_id JOIN sys.schemas s ON s.schema_id=t.schema_id JOIN sys.index_columns ic ON ic.object_id=i.object_id AND ic.index_id=i.index_id JOIN sys.columns c ON c.object_id=ic.object_id AND c.column_id=ic.column_id WHERE i.name IS NOT NULL ORDER BY s.name,t.name,i.name,ic.is_included_column,ic.key_ordinal,ic.index_column_id`
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schemaName, table, name, column string
		var unique, primary bool
		var included bool
		var filter sql.NullString
		var err error
		if engine == "mssql" || engine == "sqlserver" {
			err = rows.Scan(&schemaName, &table, &name, &unique, &primary, &column, &included, &filter)
		} else {
			err = rows.Scan(&schemaName, &table, &name, &unique, &primary, &column)
		}
		if err != nil {
			return err
		}
		key := tableKey(schemaName, table)
		found := false
		for index := range schema.Indexes[key] {
			if schema.Indexes[key][index].Name == name {
				if included {
					schema.Indexes[key][index].IncludedColumns = append(schema.Indexes[key][index].IncludedColumns, column)
				} else {
					schema.Indexes[key][index].Columns = append(schema.Indexes[key][index].Columns, column)
				}
				found = true
				break
			}
		}
		if !found {
			item := Index{Name: name, Unique: unique, Primary: primary, Filter: filter.String}
			if included {
				item.IncludedColumns = []string{column}
			} else {
				item.Columns = []string{column}
			}
			schema.Indexes[key] = append(schema.Indexes[key], item)
		}
	}
	return rows.Err()
}

func loadViews(ctx context.Context, db *sql.DB, engine string, schema *Schema) error {
	query := "SELECT table_schema,table_name FROM information_schema.views"
	if engine == "postgres" {
		query += " WHERE table_schema NOT IN ('pg_catalog','information_schema') UNION ALL SELECT schemaname,matviewname FROM pg_matviews"
	} else if engine == "mysql" || engine == "mariadb" {
		query += " WHERE table_schema=DATABASE()"
	} else if engine == "mssql" || engine == "sqlserver" {
		query = "SELECT s.name,v.name FROM sys.views v JOIN sys.schemas s ON s.schema_id=v.schema_id WHERE v.is_ms_shipped=0"
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var owner, name string
		if err := rows.Scan(&owner, &name); err != nil {
			return err
		}
		schema.Views[tableKey(owner, name)] = true
	}
	return rows.Err()
}

// loadSequences lists sequence objects. MySQL has none.
func loadSequences(ctx context.Context, db *sql.DB, engine string, schema *Schema) error {
	var query string
	switch engine {
	case "postgres":
		query = "SELECT sequence_schema,sequence_name FROM information_schema.sequences WHERE sequence_schema NOT IN ('pg_catalog','information_schema')"
	case "mariadb":
		query = "SELECT table_schema,table_name FROM information_schema.tables WHERE table_type='SEQUENCE' AND table_schema=DATABASE()"
	case "mssql", "sqlserver":
		query = "SELECT s.name,q.name FROM sys.sequences q JOIN sys.schemas s ON s.schema_id=q.schema_id"
	default:
		return nil
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Sequence
		if err := rows.Scan(&item.Schema, &item.Name); err != nil {
			return err
		}
		schema.Sequences = append(schema.Sequences, item)
	}
	return rows.Err()
}

func loadRoutines(ctx context.Context, db *sql.DB, engine string, schema *Schema) error {
	var query string
	if engine == "mssql" || engine == "sqlserver" {
		query = `SELECT s.name,o.name,CASE WHEN RTRIM(o.type)='P' THEN 'procedure' ELSE 'function' END FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id WHERE o.type IN ('P','FN','IF','TF') ORDER BY s.name,o.name`
	} else {
		query = "SELECT routine_schema,routine_name,LOWER(routine_type) FROM information_schema.routines"
		if engine == "postgres" {
			query += " WHERE routine_schema NOT IN ('pg_catalog','information_schema')"
		} else {
			query += " WHERE routine_schema=DATABASE()"
		}
		query += " ORDER BY routine_schema,routine_name"
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Routine
		if err := rows.Scan(&item.Schema, &item.Name, &item.Kind); err != nil {
			return err
		}
		schema.Routines = append(schema.Routines, item)
	}
	return rows.Err()
}

func loadTriggers(ctx context.Context, db *sql.DB, engine string, schema *Schema) error {
	var query string
	if engine == "mssql" || engine == "sqlserver" {
		query = `SELECT s.name,tr.name,t.name,CASE WHEN tr.is_instead_of_trigger=1 THEN 'INSTEAD OF' ELSE 'AFTER' END,te.type_desc FROM sys.triggers tr JOIN sys.tables t ON t.object_id=tr.parent_id JOIN sys.schemas s ON s.schema_id=t.schema_id JOIN sys.trigger_events te ON te.object_id=tr.object_id ORDER BY s.name,t.name,tr.name`
	} else {
		query = "SELECT trigger_schema,trigger_name,event_object_table,action_timing,event_manipulation FROM information_schema.triggers"
		if engine == "postgres" {
			query += " WHERE trigger_schema NOT IN ('pg_catalog','information_schema')"
		} else {
			query += " WHERE trigger_schema=DATABASE()"
		}
		query += " ORDER BY trigger_schema,event_object_table,trigger_name"
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Trigger
		if err := rows.Scan(&item.Schema, &item.Name, &item.Table, &item.Timing, &item.Event); err != nil {
			return err
		}
		merged := false
		for index := range schema.Triggers {
			existing := &schema.Triggers[index]
			if existing.Schema == item.Schema && existing.Name == item.Name && existing.Table == item.Table {
				if !strings.Contains(existing.Event, item.Event) {
					existing.Event += ", " + item.Event
				}
				merged = true
				break
			}
		}
		if !merged {
			schema.Triggers = append(schema.Triggers, item)
		}
	}
	return rows.Err()
}
