//go:build cgo

package api

import (
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
)

func TestRowBackupRestoresDuckDB(t *testing.T) {
	fileRowBackupRoundTrip(t, "duckdb", "duckdb", "CREATE TABLE items(id INTEGER PRIMARY KEY, name VARCHAR, paid BOOLEAN, data BLOB); INSERT INTO items VALUES (1, 'Ayşe O''Neil', true, '\\x61\\x62\\x63'::BLOB), (2, NULL, false, NULL), (3, 'keep', true, NULL)")
}
