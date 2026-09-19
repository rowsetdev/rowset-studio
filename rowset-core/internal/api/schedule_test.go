package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestScheduleNextRuns(t *testing.T) {
	at := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for _, test := range []struct {
		name  string
		spec  scheduleSpec
		after string
		want  string
	}{
		{"daily later today", scheduleSpec{Kind: "daily", Time: "10:00", Timezone: "Europe/Istanbul"}, "2026-09-11T06:00:00Z", "2026-09-11T07:00:00Z"},
		{"daily already passed", scheduleSpec{Kind: "daily", Time: "10:00", Timezone: "Europe/Istanbul"}, "2026-09-11T07:00:00Z", "2026-09-12T07:00:00Z"},
		{"weekdays skip the weekend", scheduleSpec{Kind: "weekly", Time: "09:30", Days: []int{1, 2, 3, 4, 5}, Timezone: "UTC"}, "2026-09-11T10:00:00Z", "2026-09-14T09:30:00Z"},
		{"interval", scheduleSpec{Kind: "interval", EveryMinutes: 90, Timezone: "UTC"}, "2026-09-11T10:00:00Z", "2026-09-11T11:30:00Z"},
		{"clock skipped by DST moves forward", scheduleSpec{Kind: "daily", Time: "02:30", Timezone: "America/New_York"}, "2026-03-08T05:00:00Z", "2026-03-08T07:30:00Z"},
	} {
		next, err := test.spec.next(at(test.after))
		if err != nil || !next.Equal(at(test.want)) {
			t.Errorf("%s: next=%s err=%v, want %s", test.name, next.UTC().Format(time.RFC3339), err, test.want)
		}
	}
	for _, bad := range []scheduleSpec{
		{Kind: "daily", Time: "25:00", Timezone: "UTC"},
		{Kind: "weekly", Time: "09:00", Timezone: "UTC"},
		{Kind: "interval", EveryMinutes: 1, Timezone: "UTC"},
		{Kind: "daily", Time: "09:00", Timezone: "Mars/Olympus"},
		{Kind: "hourly", Timezone: "UTC"},
	} {
		if bad.validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// scheduleConnection stores a connection through the API; PostgreSQL details
// come from the live-test environment when it is configured.
func scheduleConnection(t *testing.T, s *Server, identity domain.Identity) string {
	t.Helper()
	password := os.Getenv("ROWSET_MATRIX_POSTGRES_PASSWORD")
	if password == "" {
		password = "unused"
	}
	port := 55432
	body, _ := json.Marshal(map[string]any{"name": "pg", "engine": "postgres", "host": "127.0.0.1", "port": port, "database": "rowset_e2e", "connectionUsername": "postgres", "password": password, "tlsMode": "disable"})
	w := httptest.NewRecorder()
	s.createConnection(w, personalRequest(identity, string(body)))
	var created struct{ ID string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("connection: %d %s", w.Code, w.Body.String())
	}
	return created.ID
}

func scheduleRequest(t *testing.T, s *Server, identity domain.Identity, method, id, body string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := personalRequest(identity, body)
	r.Method = method
	if id != "" {
		r.SetPathValue("id", id)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func scheduleBody(connectionID, sql, dir string, extra map[string]any) string {
	body := map[string]any{"name": "Daily sales", "connectionId": connectionID, "sql": sql, "schedule": map[string]any{"kind": "daily", "time": "10:00", "timezone": "Europe/Istanbul"}, "outputDir": dir, "format": "csv", "catchUp": true, "enabled": true}
	for key, value := range extra {
		body[key] = value
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func TestScheduledQueriesValidateAndPersist(t *testing.T) {
	s, identity := personalServer(t)
	connectionID := scheduleConnection(t, s, identity)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"write":          scheduleBody(connectionID, "DELETE FROM sales WHERE id = 1", dir, nil),
		"two statements": scheduleBody(connectionID, "SELECT 1; SELECT 2", dir, nil),
		"relative dir":   scheduleBody(connectionID, "SELECT 1", "exports", nil),
		"format":         scheduleBody(connectionID, "SELECT 1", dir, map[string]any{"format": "xlsx"}),
		"time zone":      scheduleBody(connectionID, "SELECT 1", dir, map[string]any{"schedule": map[string]any{"kind": "daily", "time": "10:00", "timezone": "Nowhere/City"}}),
		"connection":     scheduleBody("missing", "SELECT 1", dir, nil),
	} {
		if w := scheduleRequest(t, s, identity, "POST", "", body, s.createScheduled); w.Code != http.StatusBadRequest {
			t.Errorf("%s accepted: %d %s", name, w.Code, w.Body.String())
		}
	}
	w := scheduleRequest(t, s, identity, "POST", "", scheduleBody(connectionID, "SELECT 1 AS one", dir, nil), s.createScheduled)
	var created struct {
		ID        string  `json:"id"`
		SQL       string  `json:"sql"`
		NextRunAt *string `json:"nextRunAt"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.NextRunAt == nil || created.SQL != "SELECT 1 AS one" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	stored, _ := s.store.ScheduledQuery(context.Background(), created.ID, identity.UserID)
	if strings.Contains(string(stored.Ciphertext), "SELECT") {
		t.Fatal("scheduled SQL stored in plaintext")
	}
	w = scheduleRequest(t, s, identity, "PUT", created.ID, scheduleBody(connectionID, "SELECT 2 AS two", dir, map[string]any{"enabled": false}), s.updateScheduled)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"nextRunAt":null`) || !strings.Contains(w.Body.String(), "SELECT 2 AS two") {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if w = scheduleRequest(t, s, identity, "GET", "", "", s.listScheduled); !strings.Contains(w.Body.String(), created.ID) {
		t.Fatal(w.Body.String())
	}
	if w = scheduleRequest(t, s, identity, "DELETE", created.ID, "", s.deleteScheduled); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
}

func TestScheduleRejectsEngineWithoutSQLScheduler(t *testing.T) {
	s, identity := personalServer(t)
	ctx := context.Background()
	if err := s.store.CreateConnection(ctx, domain.Connection{ID: "cassandra-schedule", OrgID: identity.OrgID, Name: "Cassandra", Engine: "cassandra", Host: "127.0.0.1", Port: 9042, Database: "app", Environment: "development"}); err != nil {
		t.Fatal(err)
	}
	w := scheduleRequest(t, s, identity, http.MethodPost, "", scheduleBody("cassandra-schedule", "SELECT * FROM items", t.TempDir(), nil), s.createScheduled)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not available for this engine") {
		t.Fatalf("schedule accepted Cassandra: %d %s", w.Code, w.Body.String())
	}
}

func TestMissedScheduledRunsHonourCatchUp(t *testing.T) {
	s, identity := personalServer(t)
	connectionID := scheduleConnection(t, s, identity)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	ids := map[bool]string{}
	for _, catchUp := range []bool{false, true} {
		w := scheduleRequest(t, s, identity, "POST", "", scheduleBody(connectionID, "SELECT 1", t.TempDir(), map[string]any{"catchUp": catchUp}), s.createScheduled)
		var created struct{ ID string }
		_ = json.Unmarshal(w.Body.Bytes(), &created)
		ids[catchUp] = created.ID
		if err := s.store.SetScheduledNextRun(context.Background(), created.ID, &past); err != nil {
			t.Fatal(err)
		}
	}
	s.startDueSchedules(context.Background(), time.Now())
	s.scheduleRuns.Wait()
	for catchUp, id := range ids {
		runs, _ := s.store.ListScheduledRuns(context.Background(), id, 10)
		if (len(runs) == 1) != catchUp {
			t.Errorf("catchUp=%v: %d runs", catchUp, len(runs))
		}
		item, _ := s.store.ScheduledQuery(context.Background(), id, identity.UserID)
		if item.NextRunAt == nil || *item.NextRunAt <= past {
			t.Errorf("catchUp=%v: next run not advanced: %v", catchUp, item.NextRunAt)
		}
	}
}

func TestLiveScheduledQueryWritesFiles(t *testing.T) {
	if os.Getenv("ROWSET_MATRIX_POSTGRES_PASSWORD") == "" {
		t.Skip("ROWSET_MATRIX_POSTGRES_PASSWORD is not configured")
	}
	s, identity := personalServer(t)
	connectionID := scheduleConnection(t, s, identity)
	dir := t.TempDir()
	for format, want := range map[string]string{
		"csv":  "\ufeffone,two\n1,\"a,b\"\n",
		"json": "[\n{\"one\":1,\"two\":\"a,b\"}\n]\n",
	} {
		w := scheduleRequest(t, s, identity, "POST", "", scheduleBody(connectionID, "SELECT 1 AS one, 'a,b' AS two", dir, map[string]any{"format": format, "name": "Report " + format}), s.createScheduled)
		var created struct{ ID string }
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		item, _ := s.store.ScheduledQuery(context.Background(), created.ID, identity.UserID)
		s.runScheduled(context.Background(), item, "manual")
		runs, _ := s.store.ListScheduledRuns(context.Background(), created.ID, 1)
		if len(runs) != 1 || runs[0].Status != "success" || runs[0].Rows != 1 || runs[0].OutputPath == nil {
			t.Fatalf("%s run: %+v", format, runs)
		}
		content, err := os.ReadFile(*runs[0].OutputPath)
		if err != nil || string(content) != want || filepath.Dir(*runs[0].OutputPath) != dir || !strings.HasPrefix(filepath.Base(*runs[0].OutputPath), "report-"+format+"_") {
			t.Fatalf("%s file %s: %q %v", format, *runs[0].OutputPath, content, err)
		}
	}
	history, _ := s.activity.ListQueryHistory(context.Background(), identity.UserID, connectionID, nil, nil)
	if len(history) != 2 {
		t.Fatalf("scheduled runs missing from activity: %d", len(history))
	}
}
