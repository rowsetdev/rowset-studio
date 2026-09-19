package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/activity"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

type fakeQueryStream struct {
	columns []string
	count   int
	index   int
}

func (s *fakeQueryStream) Columns() []string { return s.columns }
func (s *fakeQueryStream) DurationMS() int64 { return 12 }
func (s *fakeQueryStream) Next() ([]any, bool, error) {
	if s.index >= s.count {
		return nil, false, nil
	}
	s.index++
	return []any{"secret", s.index}, true, nil
}

func streamTestServer(t *testing.T) *Server {
	t.Helper()
	data, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rowset.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	return &Server{store: data, activity: activity.SQLite{Data: data}, logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
}

func TestStreamQueryResponseHasNoLegacyTenThousandRowCeiling(t *testing.T) {
	server := streamTestServer(t)
	request := httptest.NewRequest("POST", "/api/connections/c/query", nil)
	response := httptest.NewRecorder()
	server.streamQueryResponse(response, request, domain.Connection{ID: "c"}, "select", "SELECT", "hash", "", &fakeQueryStream{columns: []string{"value", "n"}, count: 12_345}, nil, Annotations{"rewritten": true}, false, 0)
	var body struct {
		Rows      [][]any `json:"rows"`
		RowCount  int     `json:"rowCount"`
		Truncated bool    `json:"truncated"`
		Rewritten bool    `json:"rewritten"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RowCount != 12_345 || len(body.Rows) != 12_345 || body.Truncated || !body.Rewritten {
		t.Fatalf("rowCount=%d rows=%d truncated=%v", body.RowCount, len(body.Rows), body.Truncated)
	}
}

func TestStreamQueryResponseMasksAndUsesProbeRowForExactTruncation(t *testing.T) {
	server := streamTestServer(t)
	request := httptest.NewRequest("POST", "/api/connections/c/query", nil)
	response := httptest.NewRecorder()
	server.streamQueryResponse(response, request, domain.Connection{ID: "c"}, "select", "SELECT", "hash", "", &fakeQueryStream{columns: []string{"value", "n"}, count: 3}, ResultTransforms{0: func(any) any { return "******" }}, Annotations{}, true, 2)
	var body struct {
		Rows         [][]any `json:"rows"`
		RowCount     int     `json:"rowCount"`
		Truncated    bool    `json:"truncated"`
		PolicyNotice string  `json:"policyNotice"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RowCount != 2 || len(body.Rows) != 2 || !body.Truncated || body.Rows[0][0] != "******" || body.PolicyNotice == "" {
		t.Fatalf("unexpected response: %#v", body)
	}
}
