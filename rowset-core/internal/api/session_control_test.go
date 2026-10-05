package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
)

// Rowset opens and ends transactions itself, and picks the database per
// request, so writing either as a statement is refused and the control that
// does the job is named. Everything else a session leaves behind is allowed:
// it runs on a connection of its own, which is discarded afterwards.
func TestTransactionAndDatabaseControlStayRowsetsOwn(t *testing.T) {
	s, identity := personalServer(t)
	refused := map[string]string{
		"BEGIN":                "TRANSACTION_CONTROL_UNSUPPORTED",
		"BEGIN TRANSACTION":    "TRANSACTION_CONTROL_UNSUPPORTED",
		"START TRANSACTION":    "TRANSACTION_CONTROL_UNSUPPORTED",
		"COMMIT":               "TRANSACTION_CONTROL_UNSUPPORTED",
		"ROLLBACK":             "TRANSACTION_CONTROL_UNSUPPORTED",
		"SAVEPOINT p":          "TRANSACTION_CONTROL_UNSUPPORTED",
		"RELEASE SAVEPOINT p":  "TRANSACTION_CONTROL_UNSUPPORTED",
		"/* comment */ COMMIT": "TRANSACTION_CONTROL_UNSUPPORTED",
		"USE other_db":         "DATABASE_SWITCH_UNSUPPORTED",
	}
	for sql, code := range refused {
		for _, tx := range []*engine.Transaction{nil, {}} {
			r := httptest.NewRequest("POST", "/query", nil)
			r = r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
			w := httptest.NewRecorder()
			s.executeQuery(w, r, domain.Connection{ID: "c", OrgID: "org"}, queryInput{SQL: sql}, tx)
			if w.Code != 400 || !strings.Contains(w.Body.String(), code) {
				t.Fatalf("%q: %d %s", sql, w.Code, w.Body.String())
			}
		}
	}
}

// A maintenance script opens with SET and DECLARE. These used to be refused
// outright; they now pass the guardrail and reach the database, so the
// failure a caller sees is the connection's, not a blanket refusal.
func TestSessionStateSQLIsNoLongerRefusedOutright(t *testing.T) {
	s, identity := personalServer(t)
	for _, sql := range []string{
		"SET NOCOUNT ON",
		"SET QUOTED_IDENTIFIER OFF",
		"DECLARE @BatchSize int = 5000",
		"PRINT 'done'",
		"WAITFOR DELAY '00:00:00.010'",
		"CREATE TEMPORARY TABLE t (id int)",
	} {
		r := httptest.NewRequest("POST", "/query", nil)
		r = r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
		w := httptest.NewRecorder()
		s.executeQuery(w, r, domain.Connection{ID: "c", OrgID: "org"}, queryInput{SQL: sql}, nil)
		body := w.Body.String()
		for _, gone := range []string{"SESSION_CONTROL_UNSUPPORTED", "TRANSACTION_CONTROL_UNSUPPORTED", "DATABASE_SWITCH_UNSUPPORTED", "POLICY_DENIED"} {
			if strings.Contains(body, gone) {
				t.Fatalf("%q was refused by %s: %d %s", sql, gone, w.Code, body)
			}
		}
	}
}
