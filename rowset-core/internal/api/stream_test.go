package api

import (
	"encoding/json"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNDJSONTransformsBeforeStreamingAndHonorsLimit(t *testing.T) {
	s := streamTestServer(t)
	r := httptest.NewRequest("POST", "/api/connections/c/query", nil)
	r.Header.Set("Accept", "application/x-ndjson")
	w := httptest.NewRecorder()
	s.streamQueryResponse(w, r, domain.Connection{ID: "c"}, "select", "SELECT", "hash", "", &fakeQueryStream{columns: []string{"value", "n"}, count: 3}, ResultTransforms{0: func(any) any { return "****" }}, Annotations{"transformed": []string{"value"}}, true, 2)
	if !strings.Contains(w.Header().Get("Content-Type"), "ndjson") {
		t.Fatal(w.Header())
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 3 || strings.Contains(w.Body.String(), "secret") || !strings.Contains(lines[0], `"transformed":["value"]`) {
		t.Fatal(w.Body.String())
	}
	var complete struct {
		Type       string
		RowCount   int
		Truncated  bool
		DurationMS int64
	}
	if err := json.Unmarshal([]byte(lines[2]), &complete); err != nil {
		t.Fatal(err)
	}
	if complete.Type != "complete" || complete.RowCount != 2 || !complete.Truncated || complete.DurationMS != 12 {
		t.Fatal(complete)
	}
}
