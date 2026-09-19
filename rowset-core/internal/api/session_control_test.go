package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
)

func TestWebSessionControlCannotEscapeManagedLifecycle(t *testing.T) {
	s, identity := personalServer(t)
	for _, sql := range []string{"BEGIN", "COMMIT", "ROLLBACK", "SET QUOTED_IDENTIFIER OFF", "SAVEPOINT p", "USE other_db", "/* comment */ COMMIT"} {
		for _, tx := range []*engine.Transaction{nil, {}} {
			r := httptest.NewRequest("POST", "/query", nil)
			r = r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
			w := httptest.NewRecorder()
			s.executeQuery(w, r, domain.Connection{ID: "c", OrgID: "org"}, queryInput{SQL: sql}, tx)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "SESSION_CONTROL_UNSUPPORTED") {
				t.Fatalf("%q: %d %s", sql, w.Code, w.Body.String())
			}
		}
	}
}
