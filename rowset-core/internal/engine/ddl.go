package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ObjectDDL returns the statement that creates an object: the database's own
// definition for views, routines and triggers, and a CREATE TABLE built from
// the catalog where the engine has no such function.
func (m *Manager) ObjectDDL(ctx context.Context, connection Connection, kind, schemaName, name string) (string, error) {
	if connection.Engine == "cassandra" {
		return cassandraDDL(ctx, connection, kind, schemaName, name)
	}
	db, err := m.database(connection)
	if err != nil {
		return "", err
	}
	if AdditionalEngine(connection.Engine) {
		return additionalDDL(ctx, db, connection, kind, schemaName, name)
	}
	engine := strings.ToLower(connection.Engine)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if name = strings.TrimSpace(name); name == "" {
		return "", errors.New("object name is required")
	}
	switch engine {
	case "mysql", "mariadb":
		return mysqlDDL(ctx, db, kind, schemaName, name)
	case "mssql", "sqlserver":
		switch kind {
		case "table":
			return sqlServerTableDDL(ctx, db, schemaName, name)
		case "sequence":
			return sqlServerSequenceDDL(ctx, db, schemaName, name)
		}
		return sqlServerDefinition(ctx, db, schemaName, name)
	default:
		return postgresDDL(ctx, db, kind, schemaName, name)
	}
}

func qualified(engine, schemaName, name string) string {
	quoted := quoteObject(engine, name)
	if schemaName != "" {
		quoted = quoteObject(engine, schemaName) + "." + quoted
	}
	return quoted
}

func quoteObject(engine, name string) string {
	switch engine {
	case "mysql", "mariadb":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case "mssql", "sqlserver":
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

func literal(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// mysqlDDL asks MySQL and MariaDB for their own SHOW CREATE text.
func mysqlDDL(ctx context.Context, db *sql.DB, kind, schemaName, name string) (string, error) {
	statement := map[string]string{
		"table":     "SHOW CREATE TABLE ",
		"view":      "SHOW CREATE VIEW ",
		"procedure": "SHOW CREATE PROCEDURE ",
		"function":  "SHOW CREATE FUNCTION ",
		"trigger":   "SHOW CREATE TRIGGER ",
		"sequence":  "SHOW CREATE SEQUENCE ",
	}[kind]
	if statement == "" {
		return "", fmt.Errorf("no definition available for %s objects", kind)
	}
	rows, err := db.QueryContext(ctx, statement+qualified("mysql", schemaName, name))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		return "", errors.New("object not found")
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	if err := rows.Scan(pointers...); err != nil {
		return "", err
	}
	// SHOW CREATE names the column after the object ("Create Table") or,
	// for triggers, "SQL Original Statement". "Created" is a timestamp.
	for index, column := range columns {
		lower := strings.ToLower(column)
		if lower == "create "+kind || lower == "sql original statement" {
			return text(values[index]), rows.Err()
		}
	}
	for index := range columns {
		if value := text(values[index]); strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "CREATE") {
			return value, rows.Err()
		}
	}
	return "", errors.New("the database returned no definition")
}

func text(value any) string {
	switch v := value.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// postgresDDL uses PostgreSQL's definition functions, and builds CREATE TABLE
// from the catalog because PostgreSQL has no function for it.
func postgresDDL(ctx context.Context, db *sql.DB, kind, schemaName, name string) (string, error) {
	if schemaName == "" {
		schemaName = "public"
	}
	target := literal(qualified("postgres", schemaName, name))
	switch kind {
	case "view":
		var definition string
		query := "SELECT 'CREATE OR REPLACE VIEW ' || " + target + " || E' AS\\n' || pg_get_viewdef(" + target + "::regclass, true)"
		if err := db.QueryRowContext(ctx, query).Scan(&definition); err != nil {
			return "", err
		}
		return definition, nil
	case "procedure", "function":
		var definition string
		query := "SELECT string_agg(pg_get_functiondef(p.oid), E';\\n\\n') FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = " + literal(schemaName) + " AND p.proname = " + literal(name)
		if err := db.QueryRowContext(ctx, query).Scan(&definition); err != nil {
			return "", err
		}
		return definition, nil
	case "trigger":
		var definition string
		query := "SELECT string_agg(pg_get_triggerdef(t.oid, true), E';\\n\\n') FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE NOT t.tgisinternal AND n.nspname = " + literal(schemaName) + " AND t.tgname = " + literal(name)
		if err := db.QueryRowContext(ctx, query).Scan(&definition); err != nil {
			return "", err
		}
		return definition, nil
	case "sequence":
		var increment, minimum, maximum, start int64
		var cycle bool
		query := "SELECT increment_by, min_value, max_value, start_value, cycle FROM pg_sequences WHERE schemaname = " + literal(schemaName) + " AND sequencename = " + literal(name)
		if err := db.QueryRowContext(ctx, query).Scan(&increment, &minimum, &maximum, &start, &cycle); err != nil {
			return "", err
		}
		definition := fmt.Sprintf("CREATE SEQUENCE %s\n  INCREMENT BY %d\n  MINVALUE %d\n  MAXVALUE %d\n  START WITH %d", qualified("postgres", schemaName, name), increment, minimum, maximum, start)
		if cycle {
			definition += "\n  CYCLE"
		}
		return definition + ";", nil
	case "table":
		return postgresTableDDL(ctx, db, schemaName, name)
	}
	return "", fmt.Errorf("no definition available for %s objects", kind)
}

func postgresTableDDL(ctx context.Context, db *sql.DB, schemaName, name string) (string, error) {
	target := literal(qualified("postgres", schemaName, name))
	columns, err := queryStrings(ctx, db, `SELECT '  ' || quote_ident(a.attname) || ' ' || format_type(a.atttypid, a.atttypmod)
		|| CASE WHEN a.attidentity = 'a' THEN ' GENERATED ALWAYS AS IDENTITY' WHEN a.attidentity = 'd' THEN ' GENERATED BY DEFAULT AS IDENTITY' ELSE '' END
		|| CASE WHEN a.attgenerated <> '' THEN ' GENERATED ALWAYS AS (' || pg_get_expr(d.adbin, d.adrelid) || ') STORED'
		        WHEN d.adbin IS NOT NULL THEN ' DEFAULT ' || pg_get_expr(d.adbin, d.adrelid) ELSE '' END
		|| CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
		FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = `+target+`::regclass AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`)
	if err != nil {
		return "", err
	}
	if len(columns) == 0 {
		return "", errors.New("table not found")
	}
	constraints, err := queryStrings(ctx, db, "SELECT '  CONSTRAINT ' || quote_ident(conname) || ' ' || pg_get_constraintdef(oid, true) FROM pg_constraint WHERE conrelid = "+target+"::regclass ORDER BY contype DESC, conname")
	if err != nil {
		return "", err
	}
	indexes, err := queryStrings(ctx, db, "SELECT pg_get_indexdef(i.indexrelid) || ';' FROM pg_index i WHERE i.indrelid = "+target+"::regclass AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid) ORDER BY i.indexrelid")
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("CREATE TABLE " + qualified("postgres", schemaName, name) + " (\n")
	out.WriteString(strings.Join(append(columns, constraints...), ",\n"))
	out.WriteString("\n);\n")
	for _, index := range indexes {
		out.WriteString("\n" + index + "\n")
	}
	return out.String(), nil
}

// sqlServerDefinition returns the stored text of a view, routine or trigger.
func sqlServerDefinition(ctx context.Context, db *sql.DB, schemaName, name string) (string, error) {
	if schemaName == "" {
		schemaName = "dbo"
	}
	var definition sql.NullString
	query := "SELECT OBJECT_DEFINITION(OBJECT_ID(" + literal(qualified("mssql", schemaName, name)) + "))"
	if err := db.QueryRowContext(ctx, query).Scan(&definition); err != nil {
		return "", err
	}
	if !definition.Valid || strings.TrimSpace(definition.String) == "" {
		return "", errors.New("the database returned no definition; it may be encrypted")
	}
	return definition.String, nil
}

// sqlServerSequenceDDL builds the statement, since OBJECT_DEFINITION has
// nothing to say about sequences.
func sqlServerSequenceDDL(ctx context.Context, db *sql.DB, schemaName, name string) (string, error) {
	if schemaName == "" {
		schemaName = "dbo"
	}
	query := `SELECT 'CREATE SEQUENCE ' + QUOTENAME(s.name) + '.' + QUOTENAME(q.name) + CHAR(10) + '  AS ' + UPPER(t.name)
		+ CHAR(10) + '  START WITH ' + CAST(q.start_value AS varchar(40))
		+ CHAR(10) + '  INCREMENT BY ' + CAST(q.increment AS varchar(40))
		+ CHAR(10) + '  MINVALUE ' + CAST(q.minimum_value AS varchar(40))
		+ CHAR(10) + '  MAXVALUE ' + CAST(q.maximum_value AS varchar(40))
		+ CASE WHEN q.is_cycling = 1 THEN CHAR(10) + '  CYCLE' ELSE '' END + ';'
		FROM sys.sequences q JOIN sys.schemas s ON s.schema_id = q.schema_id JOIN sys.types t ON t.user_type_id = q.user_type_id
		WHERE s.name = ` + literal(schemaName) + ` AND q.name = ` + literal(name)
	var definition sql.NullString
	if err := db.QueryRowContext(ctx, query).Scan(&definition); err != nil {
		return "", err
	}
	if !definition.Valid {
		return "", errors.New("sequence not found")
	}
	return definition.String, nil
}

func sqlServerTableDDL(ctx context.Context, db *sql.DB, schemaName, name string) (string, error) {
	if schemaName == "" {
		schemaName = "dbo"
	}
	target := literal(qualified("mssql", schemaName, name))
	columns, err := queryStrings(ctx, db, `SELECT '  ' + QUOTENAME(c.name) + ' ' +
		CASE WHEN c.is_computed = 1 THEN 'AS ' + ISNULL(cc.definition, '')
		ELSE UPPER(t.name)
			+ CASE WHEN t.name IN ('varchar','char','varbinary','binary','nvarchar','nchar')
				THEN '(' + CASE WHEN c.max_length = -1 THEN 'MAX' WHEN t.name IN ('nvarchar','nchar') THEN CAST(c.max_length / 2 AS varchar(10)) ELSE CAST(c.max_length AS varchar(10)) END + ')'
				WHEN t.name IN ('decimal','numeric') THEN '(' + CAST(c.precision AS varchar(10)) + ',' + CAST(c.scale AS varchar(10)) + ')'
				WHEN t.name IN ('datetime2','time','datetimeoffset') THEN '(' + CAST(c.scale AS varchar(10)) + ')'
				ELSE '' END
			+ CASE WHEN c.is_identity = 1 THEN ' IDENTITY(' + CAST(ISNULL(ic.seed_value, 1) AS varchar(20)) + ',' + CAST(ISNULL(ic.increment_value, 1) AS varchar(20)) + ')' ELSE '' END
			+ CASE WHEN dc.definition IS NOT NULL THEN ' DEFAULT ' + dc.definition ELSE '' END
			+ CASE WHEN c.is_nullable = 1 THEN ' NULL' ELSE ' NOT NULL' END END
		FROM sys.columns c
		JOIN sys.types t ON t.user_type_id = c.user_type_id
		LEFT JOIN sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
		LEFT JOIN sys.identity_columns ic ON ic.object_id = c.object_id AND ic.column_id = c.column_id
		LEFT JOIN sys.default_constraints dc ON dc.object_id = c.default_object_id
		WHERE c.object_id = OBJECT_ID(`+target+`) ORDER BY c.column_id`)
	if err != nil {
		return "", err
	}
	if len(columns) == 0 {
		return "", errors.New("table not found")
	}
	keys, err := queryStrings(ctx, db, `SELECT '  CONSTRAINT ' + QUOTENAME(k.name) + ' ' + REPLACE(REPLACE(k.type_desc COLLATE DATABASE_DEFAULT, '_CONSTRAINT', ''), '_', ' ') + ' (' +
		STUFF((SELECT ', ' + QUOTENAME(c.name) + CASE WHEN ic.is_descending_key = 1 THEN ' DESC' ELSE '' END
			FROM sys.index_columns ic JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
			WHERE ic.object_id = k.parent_object_id AND ic.index_id = k.unique_index_id ORDER BY ic.key_ordinal FOR XML PATH('')), 1, 2, '') + ')'
		FROM sys.key_constraints k WHERE k.parent_object_id = OBJECT_ID(`+target+`)`)
	if err != nil {
		return "", err
	}
	checks, err := queryStrings(ctx, db, "SELECT '  CONSTRAINT ' + QUOTENAME(name) + ' CHECK ' + definition FROM sys.check_constraints WHERE parent_object_id = OBJECT_ID("+target+")")
	if err != nil {
		return "", err
	}
	foreign, err := queryStrings(ctx, db, `SELECT '  CONSTRAINT ' + QUOTENAME(f.name) + ' FOREIGN KEY (' + QUOTENAME(pc.name) + ') REFERENCES ' + QUOTENAME(rs.name) + '.' + QUOTENAME(rt.name) + ' (' + QUOTENAME(rc.name) + ')'
		FROM sys.foreign_keys f
		JOIN sys.foreign_key_columns fc ON fc.constraint_object_id = f.object_id
		JOIN sys.columns pc ON pc.object_id = fc.parent_object_id AND pc.column_id = fc.parent_column_id
		JOIN sys.tables rt ON rt.object_id = fc.referenced_object_id
		JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
		JOIN sys.columns rc ON rc.object_id = fc.referenced_object_id AND rc.column_id = fc.referenced_column_id
		WHERE f.parent_object_id = OBJECT_ID(`+target+`)`)
	if err != nil {
		return "", err
	}
	indexes, err := queryStrings(ctx, db, `SELECT 'CREATE ' + CASE WHEN i.is_unique = 1 THEN 'UNIQUE ' ELSE '' END + 'INDEX ' + QUOTENAME(i.name) + ' ON ' + QUOTENAME(s.name) + '.' + QUOTENAME(o.name) + ' (' +
		STUFF((SELECT ', ' + QUOTENAME(c.name) + CASE WHEN ic.is_descending_key = 1 THEN ' DESC' ELSE '' END
			FROM sys.index_columns ic JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
			WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 0 ORDER BY ic.key_ordinal FOR XML PATH('')), 1, 2, '') + ');'
		FROM sys.indexes i JOIN sys.objects o ON o.object_id = i.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE i.object_id = OBJECT_ID(`+target+`) AND i.is_primary_key = 0 AND i.is_unique_constraint = 0 AND i.type_desc <> 'HEAP'`)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("CREATE TABLE " + qualified("mssql", schemaName, name) + " (\n")
	out.WriteString(strings.Join(append(append(append(columns, keys...), checks...), foreign...), ",\n"))
	out.WriteString("\n);\n")
	for _, index := range indexes {
		out.WriteString("\n" + index + "\n")
	}
	return out.String(), nil
}

func queryStrings(ctx context.Context, db *sql.DB, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if value.Valid && strings.TrimSpace(value.String) != "" {
			out = append(out, value.String)
		}
	}
	return out, rows.Err()
}
