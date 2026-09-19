package engine

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

func TestLiveThreeHundredDialectEdgeQueries(t *testing.T) {
	tests := []struct {
		name, passwordEnv, user, database, table, quotedID string
		port                                               int
		dialect                                            sqlguard.Dialect
		setup                                              []string
	}{
		{
			name: "postgres", passwordEnv: "ROWSET_MATRIX_POSTGRES_PASSWORD", user: "postgres", database: "rowset_e2e", port: 55432, dialect: sqlguard.DialectPostgres,
			table: "public.rowset_query_corpus", quotedID: `"id"`,
			setup: []string{`DROP TABLE IF EXISTS public.rowset_query_corpus`, `CREATE TABLE public.rowset_query_corpus(id int primary key,tenant_id int not null,value_num int not null,value_text varchar(40),nullable_num int)`},
		},
		{
			name: "mysql", passwordEnv: "ROWSET_MATRIX_MYSQL_PASSWORD", user: "root", database: "rowset_e2e", port: 53306, dialect: sqlguard.DialectMySQL,
			table: "rowset_query_corpus", quotedID: "`id`",
			setup: []string{`DROP TABLE IF EXISTS rowset_query_corpus`, `CREATE TABLE rowset_query_corpus(id int primary key,tenant_id int not null,value_num int not null,value_text varchar(40),nullable_num int)`},
		},
		{
			name: "mssql", passwordEnv: "ROWSET_MATRIX_MSSQL_PASSWORD", user: "sa", database: "rowset_e2e", port: 51433, dialect: sqlguard.DialectMSSQL,
			table: "dbo.rowset_query_corpus", quotedID: "[id]",
			setup: []string{`IF OBJECT_ID('dbo.rowset_query_corpus','U') IS NOT NULL DROP TABLE dbo.rowset_query_corpus`, `CREATE TABLE dbo.rowset_query_corpus(id int primary key,tenant_id int not null,value_num int not null,value_text varchar(40),nullable_num int)`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			password := os.Getenv(test.passwordEnv)
			if password == "" {
				t.Skip(test.passwordEnv + " is not configured")
			}
			manager := NewManager()
			defer manager.Close()
			connection := Connection{ID: "corpus-" + test.name, Engine: test.name, Host: "127.0.0.1", Port: test.port, Database: test.database, Username: test.user, Password: password, PoolSize: 4}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			for _, statement := range test.setup {
				if _, err := manager.Execute(ctx, connection, statement, 0); err != nil {
					t.Fatal(err)
				}
			}
			values := ""
			for id := 1; id <= 20; id++ {
				if id > 1 {
					values += ","
				}
				nullable := "NULL"
				if id%2 == 0 {
					nullable = fmt.Sprint(id * 3)
				}
				values += fmt.Sprintf("(%d,%d,%d,'value-%d',%s)", id, 1+id%3, id*10, id, nullable)
			}
			if _, err := manager.Execute(ctx, connection, "INSERT INTO "+test.table+"(id,tenant_id,value_num,value_text,nullable_num) VALUES "+values, 0); err != nil {
				t.Fatal(err)
			}

			for index := 0; index < 100; index++ {
				query := corpusQuery(test.table, test.quotedID, index)
				info, err := sqlguard.ParseDialect(test.dialect, query)
				if err != nil || info.Command != sqlguard.Select || len(info.Tables) == 0 {
					t.Fatalf("parser rejected corpus query %d: kind=%s tables=%v error=%v\n%s", index, info.Command, info.Tables, err, query)
				}
				stream, err := manager.Query(ctx, connection, query)
				if err != nil {
					t.Fatalf("engine rejected corpus query %d: %v\n%s", index, err, query)
				}
				rows := 0
				for {
					_, ok, err := stream.Next()
					if err != nil {
						stream.Close()
						t.Fatalf("corpus query %d row %d: %v", index, rows, err)
					}
					if !ok {
						break
					}
					rows++
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if rows == 0 {
					t.Fatalf("corpus query %d unexpectedly returned no rows: %s", index, query)
				}
			}
		})
	}
}

func corpusQuery(table, quotedID string, index int) string {
	value := index/10 + 1
	switch index % 10 {
	case 0:
		return fmt.Sprintf("SELECT id,value_text FROM %s WHERE id>=%d ORDER BY id", table, value)
	case 1:
		return fmt.Sprintf("SELECT %s AS quoted_id,COALESCE(nullable_num,value_num) AS chosen FROM %s WHERE id BETWEEN %d AND 20 ORDER BY quoted_id", quotedID, table, value)
	case 2:
		return fmt.Sprintf("WITH scoped AS (SELECT id,tenant_id FROM %s WHERE tenant_id=%d AND id>=%d) SELECT id FROM scoped ORDER BY id", table, 1+(index/10)%3, value)
	case 3:
		return fmt.Sprintf("SELECT d.id,d.value_num FROM (SELECT id,value_num FROM %s WHERE id>=%d) d ORDER BY d.id", table, value)
	case 4:
		return fmt.Sprintf("SELECT a.id,b.value_text FROM %s a LEFT JOIN %s b ON b.id=a.id AND b.tenant_id=a.tenant_id WHERE a.id>=%d ORDER BY a.id", table, table, value)
	case 5:
		return fmt.Sprintf("SELECT a.id FROM %s a WHERE EXISTS (SELECT 1 FROM %s b WHERE b.id=a.id AND b.value_num>=%d) ORDER BY a.id", table, table, value*10)
	case 6:
		return fmt.Sprintf("SELECT id FROM %s WHERE tenant_id IN (SELECT tenant_id FROM %s WHERE id=%d) ORDER BY id", table, table, value)
	case 7:
		return fmt.Sprintf("SELECT tenant_id,COUNT(*) AS amount,SUM(value_num) AS total FROM %s WHERE id>=%d GROUP BY tenant_id HAVING COUNT(*)>=1 ORDER BY tenant_id", table, value)
	case 8:
		return fmt.Sprintf("SELECT id,ROW_NUMBER() OVER (PARTITION BY tenant_id ORDER BY value_num,id) AS rn FROM %s WHERE id>=%d ORDER BY id", table, value)
	default:
		return fmt.Sprintf("SELECT id FROM %s WHERE id=%d UNION ALL SELECT id FROM %s WHERE id=%d ORDER BY id", table, value, table, value+10)
	}
}
