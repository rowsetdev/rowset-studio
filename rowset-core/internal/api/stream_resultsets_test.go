package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

// fakeBatch stands in for a batch of several SELECTs: each inner slice is one
// result, with its own columns.
type fakeBatch struct {
	columns [][]string
	rows    [][][]any
	set     int
	row     int
}

func (f *fakeBatch) Columns() []string { return f.columns[f.set] }
func (f *fakeBatch) DurationMS() int64 { return 1 }
func (f *fakeBatch) Next() ([]any, bool, error) {
	if f.row >= len(f.rows[f.set]) {
		return nil, false, nil
	}
	row := f.rows[f.set][f.row]
	f.row++
	return row, true, nil
}
func (f *fakeBatch) NextResultSet() (bool, error) {
	if f.set+1 >= len(f.columns) {
		return false, nil
	}
	f.set++
	f.row = 0
	return true, nil
}

// A batch returns a result per SELECT, and the stream must carry every one of
// them: a client that saw only the first would show part of what it ran.
func TestStreamCarriesEveryResultOfABatch(t *testing.T) {
	s, identity := personalServer(t)
	batch := &fakeBatch{
		columns: [][]string{{"first_set"}, {"b", "c"}},
		rows:    [][][]any{{{int64(1)}}, {{int64(2), int64(3)}, {int64(4), int64(5)}}},
	}
	r := httptest.NewRequest("POST", "/query", nil)
	r.Header.Set("Accept", "application/x-ndjson")
	r = r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
	w := httptest.NewRecorder()
	s.streamNDJSON(w, r, domain.Connection{ID: "c", OrgID: "org"}, "SELECT 1 SELECT 2,3", "", "", "", batch, ResultTransforms{}, Annotations{}, false, 0)
	body := w.Body.String()
	for _, want := range []string{`"type":"columns"`, `"first_set"`, `"type":"result"`, `"b"`, `"c"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream is missing %s:\n%s", want, body)
		}
	}
	if got := strings.Count(body, `"type":"complete"`); got != 2 {
		t.Fatalf("got %d complete frames, want one per result:\n%s", got, body)
	}
}

// One statement returns one result and must look exactly as it did before.
func TestStreamOfOneResultHasNoExtraFrames(t *testing.T) {
	s, identity := personalServer(t)
	batch := &fakeBatch{columns: [][]string{{"n"}}, rows: [][][]any{{{int64(1)}}}}
	r := httptest.NewRequest("POST", "/query", nil)
	r.Header.Set("Accept", "application/x-ndjson")
	r = r.WithContext(context.WithValue(r.Context(), contextKey{}, identity))
	w := httptest.NewRecorder()
	s.streamNDJSON(w, r, domain.Connection{ID: "c", OrgID: "org"}, "SELECT 1", "", "", "", batch, ResultTransforms{}, Annotations{}, false, 0)
	body := w.Body.String()
	if strings.Contains(body, `"type":"result"`) {
		t.Fatalf("a single result announced a further one:\n%s", body)
	}
	if got := strings.Count(body, `"type":"complete"`); got != 1 {
		t.Fatalf("got %d complete frames, want 1:\n%s", got, body)
	}
}
