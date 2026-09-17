package engine

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveMSSQLCompositeFKAndCoveringIndexDDL(t *testing.T) {
	password := os.Getenv("ROWSET_MATRIX_MSSQL_PASSWORD")
	if password == "" {
		t.Skip("ROWSET_MATRIX_MSSQL_PASSWORD is not configured")
	}
	port := 51433
	if raw := os.Getenv("ROWSET_MATRIX_MSSQL_PORT"); raw != "" {
		var err error
		port, err = strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := NewManager()
	defer m.Close()
	c := Connection{ID: "mssql-index-ddl", Engine: "mssql", Host: "127.0.0.1", Port: port, Database: "rowset_e2e", Username: "sa", Password: password}
	db, err := m.database(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rowset_meta_child_copy", "rowset_meta_child", "rowset_meta_parent"} {
		if _, err := db.ExecContext(ctx, "IF OBJECT_ID('dbo."+name+"','U') IS NOT NULL DROP TABLE dbo."+name); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`IF EXISTS (SELECT 1 FROM sys.partition_schemes WHERE name='rowset_meta_ps') DROP PARTITION SCHEME rowset_meta_ps`,
		`IF EXISTS (SELECT 1 FROM sys.partition_functions WHERE name='rowset_meta_pf') DROP PARTITION FUNCTION rowset_meta_pf`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, name := range []string{"rowset_meta_child_copy", "rowset_meta_child", "rowset_meta_parent"} {
			_, _ = db.ExecContext(context.Background(), "IF OBJECT_ID('dbo."+name+"','U') IS NOT NULL DROP TABLE dbo."+name)
		}
		_, _ = db.ExecContext(context.Background(), `IF EXISTS (SELECT 1 FROM sys.partition_schemes WHERE name='rowset_meta_ps') DROP PARTITION SCHEME rowset_meta_ps`)
		_, _ = db.ExecContext(context.Background(), `IF EXISTS (SELECT 1 FROM sys.partition_functions WHERE name='rowset_meta_pf') DROP PARTITION FUNCTION rowset_meta_pf`)
	})
	for _, statement := range []string{
		`CREATE PARTITION FUNCTION rowset_meta_pf (int) AS RANGE LEFT FOR VALUES (10)`,
		`CREATE PARTITION SCHEME rowset_meta_ps AS PARTITION rowset_meta_pf ALL TO ([PRIMARY])`,
		`CREATE TABLE dbo.rowset_meta_parent (a int NOT NULL, b int NOT NULL, CONSTRAINT PK_rowset_meta_parent PRIMARY KEY (a,b))`,
		`CREATE TABLE dbo.rowset_meta_child (id int NOT NULL, a int, b int, payload nvarchar(20), flag bit, CONSTRAINT PK_rowset_meta_child PRIMARY KEY (id), CONSTRAINT FK_rowset_meta_child FOREIGN KEY (a,b) REFERENCES dbo.rowset_meta_parent(a,b) ON DELETE CASCADE)`,
		`CREATE NONCLUSTERED INDEX IX_rowset_meta_child ON dbo.rowset_meta_child (a DESC,b ASC) INCLUDE (payload) WHERE flag=1 WITH (FILLFACTOR=80,PAD_INDEX=ON,ALLOW_PAGE_LOCKS=OFF,STATISTICS_NORECOMPUTE=ON,OPTIMIZE_FOR_SEQUENTIAL_KEY=ON,DATA_COMPRESSION=PAGE)`,
		`CREATE NONCLUSTERED INDEX IX_rowset_meta_partitioned ON dbo.rowset_meta_child (id) WITH (DATA_COMPRESSION=ROW) ON rowset_meta_ps(id)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := m.Schema(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	var index *Index
	for i := range schema.Indexes["dbo.rowset_meta_child"] {
		item := &schema.Indexes["dbo.rowset_meta_child"][i]
		if item.Name == "IX_rowset_meta_child" {
			index = item
		}
	}
	if index == nil || !reflect.DeepEqual(index.Columns, []string{"a", "b"}) || !reflect.DeepEqual(index.IncludedColumns, []string{"payload"}) || !strings.Contains(strings.ToLower(index.Filter), "flag") {
		t.Fatalf("index metadata: %#v", index)
	}
	ddl, err := m.ObjectDDL(ctx, c, "table", "dbo", "rowset_meta_child")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FOREIGN KEY ([a], [b])", "REFERENCES [dbo].[rowset_meta_parent] ([a], [b]) ON DELETE CASCADE", "([a] DESC, [b]) INCLUDE ([payload])", "WHERE ", "FILLFACTOR = 80", "ALLOW_PAGE_LOCKS = OFF", "STATISTICS_NORECOMPUTE = ON", "OPTIMIZE_FOR_SEQUENTIAL_KEY = ON", "DATA_COMPRESSION = PAGE", "DATA_COMPRESSION = ROW", "ON [rowset_meta_ps]([id])"} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL missing %q:\n%s", want, ddl)
		}
	}
	if strings.Count(ddl, "CONSTRAINT [FK_rowset_meta_child]") != 1 {
		t.Fatalf("composite FK split into multiple constraints:\n%s", ddl)
	}
	replay := strings.ReplaceAll(ddl, "rowset_meta_child", "rowset_meta_child_copy")
	if _, err := db.ExecContext(ctx, replay); err != nil {
		t.Fatalf("generated DDL cannot be replayed: %v\n%s", err, replay)
	}
}
