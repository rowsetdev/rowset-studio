package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/microsoft/go-mssqldb"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
)

func databaseDiagnostic(err error, sql string) map[string]any {
	detail := map[string]any{"code": "EXEC_ERROR", "message": err.Error()}
	if errors.Is(err, context.Canceled) {
		detail["code"] = "QUERY_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		detail["code"] = "QUERY_TIMEOUT"
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		detail["sqlstate"] = pg.Code
		detail["message"] = pg.Message
		if pg.Position > 0 {
			runes := []rune(sql)
			end := int(pg.Position) - 1
			if end > len(runes) {
				end = len(runes)
			}
			prefix := string(runes[:end])
			detail["line"] = strings.Count(prefix, "\n") + 1
			last := strings.LastIndex(prefix, "\n")
			detail["column"] = len([]rune(prefix[last+1:])) + 1
		}
	}
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		detail["sqlstate"] = string(my.SQLState[:])
		detail["number"] = my.Number
		detail["message"] = my.Message
	}
	var ms mssql.Error
	if errors.As(err, &ms) {
		detail["number"] = ms.Number
		detail["line"] = ms.LineNo
		detail["message"] = ms.Message
	}
	return detail
}

func writeDatabaseError(w http.ResponseWriter, err error, sql string) {
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": databaseDiagnostic(err, sql)})
}

// writeStatementError reports whether a manual transaction survived the
// failed statement so Studio never offers Commit for work already discarded.
func writeStatementError(w http.ResponseWriter, err error, sql string, transaction *engine.Transaction) {
	detail := databaseDiagnostic(err, sql)
	// A deadline is Rowset's own query timeout, not a database error; say so
	// and where to change it instead of showing "context deadline exceeded".
	if errors.Is(err, context.DeadlineExceeded) {
		detail["code"] = "QUERY_TIMEOUT"
		detail["message"] = "The statement was stopped because it ran longer than the query timeout. Raise the connection's query timeout in Connections to let it run longer."
	}
	if transaction != nil {
		detail["transactionState"] = string(transaction.State())
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": detail})
}
