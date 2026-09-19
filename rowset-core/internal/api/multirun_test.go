package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func multiRunCall(t *testing.T, s *Server, identity domain.Identity, input any) (*httptest.ResponseRecorder, []multiRunEvent) {
	t.Helper()
	body, _ := json.Marshal(input)
	w := httptest.NewRecorder()
	s.runOnConnections(w, personalRequest(identity, string(body)))
	var events []multiRunEvent
	scanner := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var event multiRunEvent
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type != "" {
			events = append(events, event)
		}
	}
	return w, events
}

// finalEvents keeps the last event of each target.
func finalEvents(events []multiRunEvent) map[int]multiRunEvent {
	final := map[int]multiRunEvent{}
	for _, event := range events {
		if event.Type == "target" {
			final[event.Index] = event
		}
	}
	return final
}

func saveConnection(t *testing.T, s *Server, identity domain.Identity, id string) {
	t.Helper()
	ctx := context.Background()
	if err := s.store.CreateSecret(ctx, domain.Secret{ID: "secret-" + id, Ciphertext: []byte("x"), Nonce: []byte("n")}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateConnection(ctx, domain.Connection{ID: id, OrgID: identity.OrgID, Name: id, Engine: "postgres", Host: "127.0.0.1", Port: 1, Database: "app", Environment: "development", SecretID: "secret-" + id, CreatedAt: store.NowString()}); err != nil {
		t.Fatal(err)
	}
}

func TestMultiRunRejectsWhatItCannotRun(t *testing.T) {
	s, identity := personalServer(t)
	for _, input := range []map[string]any{
		{"targets": []map[string]string{}, "statements": []string{"SELECT 1"}},
		{"targets": []map[string]string{{"connectionId": "a"}}, "statements": []string{"  "}},
		{"targets": []map[string]string{{"connectionId": ""}}, "statements": []string{"SELECT 1"}},
		{"targets": []map[string]string{{"connectionId": "a"}}, "statements": []string{"SELECT 1"}, "concurrency": 11},
	} {
		w, _ := multiRunCall(t, s, identity, input)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%v: %d %s", input, w.Code, w.Body.String())
		}
	}
}

func TestMultiRunReportsEachConnectionOnItsOwn(t *testing.T) {
	s, identity := personalServer(t)
	w, events := multiRunCall(t, s, identity, map[string]any{
		"targets":    []map[string]string{{"connectionId": "missing-1"}, {"connectionId": "missing-2"}},
		"statements": []string{"SELECT 1", "SELECT 2"},
	})
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if len(events) == 0 || events[len(events)-1].Type != "done" {
		t.Fatalf("stream did not end with done: %+v", events)
	}
	final := finalEvents(events)
	for index := range 2 {
		event := final[index]
		if event.Status != "error" || event.Completed != 0 || event.FailedStatement != "SELECT 1" || !strings.Contains(event.Error, "not found") {
			t.Fatalf("target %d: %+v", index, event)
		}
	}
}

func TestMultiRunRoutesCassandraToCQLHandler(t *testing.T) {
	s, identity := personalServer(t)
	if err := s.store.CreateConnection(context.Background(), domain.Connection{ID: "cql-target", OrgID: identity.OrgID, Name: "Cassandra", Engine: "cassandra", Host: "127.0.0.1", Port: 9042, Database: "app", Environment: "development"}); err != nil {
		t.Fatal(err)
	}
	_, events := multiRunCall(t, s, identity, map[string]any{
		"targets":    []map[string]string{{"connectionId": "cql-target", "database": "app"}},
		"statements": []string{"USE app"},
	})
	event := finalEvents(events)[0]
	if event.Status != "error" || !strings.Contains(event.Error, "session-control CQL") {
		t.Fatalf("Cassandra statement did not use the CQL handler: %+v", event)
	}
}

// Policies decide before any database is contacted, so this runs without one:
// a script on several connections is refused on each, and each refusal is in
// that connection's history, just as on a single run.
func TestMultiRunAppliesPoliciesToEveryConnection(t *testing.T) {
	s, identity := personalServer(t)
	saveConnection(t, s, identity, "east")
	saveConnection(t, s, identity, "west")
	w := httptest.NewRecorder()
	s.createCustomPolicy(w, personalRequest(identity, `{"name":"Protect customers","kind":"deny_table","config":"customers"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("policy: %d %s", w.Code, w.Body.String())
	}
	_, events := multiRunCall(t, s, identity, map[string]any{
		"targets":    []map[string]string{{"connectionId": "east"}, {"connectionId": "west"}},
		"statements": []string{"UPDATE customers SET name = 'x' WHERE id = 1"},
	})
	for index, event := range finalEvents(events) {
		if event.Status != "error" || !strings.Contains(event.Error, "Protect customers") {
			t.Fatalf("target %d was not refused by the policy: %+v", index, event)
		}
	}
	for _, id := range []string{"east", "west"} {
		items, err := s.activity.ListQueryHistory(context.Background(), identity.UserID, id, nil, nil)
		if err != nil || len(items) != 1 || items[0].Status == "success" {
			t.Fatalf("%s history: %+v %v", id, items, err)
		}
	}
}

func TestMultiRunKeepsToItsConcurrencyAndStopsOnlyTheFailingConnection(t *testing.T) {
	s, identity := personalServer(t)
	var running, peak atomic.Int64
	var mu sync.Mutex
	seen := map[string][]string{}
	original := runStatement
	runStatement = func(_ *Server, _ context.Context, _ *http.Request, target multiRunTarget, sql string, _ bool) statementOutcome {
		now := running.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		running.Add(-1)
		mu.Lock()
		seen[target.ConnectionID] = append(seen[target.ConnectionID], sql)
		mu.Unlock()
		if target.ConnectionID == "c3" && sql == "two" {
			return statementOutcome{err: "relation does not exist"}
		}
		count := int64(1)
		return statementOutcome{body: json.RawMessage(`{"columns":["n"],"rows":[[9007199254740993]],"rowCount":1}`), rowCount: &count, hasColumns: true}
	}
	t.Cleanup(func() { runStatement = original })

	targets := []map[string]string{}
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", "c11", "c12"} {
		targets = append(targets, map[string]string{"connectionId": id})
	}
	_, events := multiRunCall(t, s, identity, map[string]any{"targets": targets, "statements": []string{"one", "two", "three"}, "concurrency": 10})
	if peak.Load() > 10 || peak.Load() < 2 {
		t.Fatalf("ran %d connections at once", peak.Load())
	}
	final := finalEvents(events)
	if len(final) != 12 {
		t.Fatalf("expected 12 targets, got %d", len(final))
	}
	for index, event := range final {
		id := targets[index]["connectionId"]
		if id == "c3" {
			if event.Status != "error" || event.Completed != 1 || event.FailedStatement != "two" {
				t.Fatalf("c3: %+v", event)
			}
			if got := seen["c3"]; len(got) != 2 {
				t.Fatalf("c3 kept running after its error: %v", got)
			}
			continue
		}
		if event.Status != "success" || event.Completed != 3 || strings.Join(seen[id], ",") != "one,two,three" {
			t.Fatalf("%s: %+v %v", id, event, seen[id])
		}
		// The result is passed on as the handler wrote it, digits and all.
		if !strings.Contains(string(event.Result), "9007199254740993") {
			t.Fatalf("%s result changed: %s", id, event.Result)
		}
	}
}

func TestLiveMultiRunOnEveryEngine(t *testing.T) {
	c := newE2EClient(t)
	var targets []map[string]string
	engines := map[string]string{}
	for _, engine := range importEngines {
		password := os.Getenv(engine.passwordEnv)
		if password == "" {
			continue
		}
		id := c.connection(t, engine, password)
		targets = append(targets, map[string]string{"connectionId": id, "database": "rowset_e2e"})
		engines[id] = engine.engine
	}
	if len(targets) < 2 {
		t.Skip("needs at least two live engines")
	}
	run := func(ctx context.Context, statements []string) ([]multiRunEvent, time.Duration) {
		body, _ := json.Marshal(map[string]any{"targets": targets, "statements": statements, "concurrency": 10})
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/multirun", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+c.token)
		started := time.Now()
		response, err := c.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var events []multiRunEvent
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 1<<20), 64<<20)
		for scanner.Scan() {
			var event multiRunEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events = append(events, event)
			}
		}
		return events, time.Since(started)
	}

	events, _ := run(context.Background(), []string{"SELECT 7 AS n, 9007199254740993 AS big"})
	for index, event := range finalEvents(events) {
		if event.Status != "success" || !strings.Contains(string(event.Result), "9007199254740993") {
			t.Fatalf("%s: %+v %s", engines[targets[index]["connectionId"]], event, event.Result)
		}
	}

	// Stopping must end the statements on the servers, not only the request:
	// once stopped, none of them may still be running on its database.
	slow := map[string]string{
		"postgres":  "SELECT pg_sleep(30) AS rowset_multirun_probe",
		"mysql":     "SELECT SLEEP(30) AS rowset_multirun_probe",
		"mariadb":   "SELECT SLEEP(30) AS rowset_multirun_probe",
		"sqlserver": "SELECT COUNT_BIG(*) AS rowset_multirun_probe FROM sys.all_objects a CROSS JOIN sys.all_objects b WHERE HASHBYTES('SHA2_512', CONCAT(a.name, b.name, REPLICATE(CAST('x' AS varchar(max)), 4000))) IS NOT NULL",
	}
	// Each check leaves out its own session, whose text mentions the probe too.
	stillRunning := map[string]string{
		"postgres":  "SELECT COUNT(*) AS n FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%rowset_multirun_probe%' AND pid <> pg_backend_pid()",
		"mysql":     "SELECT COUNT(*) AS n FROM information_schema.processlist WHERE info LIKE '%rowset_multirun_probe%' AND id <> CONNECTION_ID()",
		"mariadb":   "SELECT COUNT(*) AS n FROM information_schema.processlist WHERE info LIKE '%rowset_multirun_probe%' AND id <> CONNECTION_ID()",
		"sqlserver": "SELECT COUNT(*) AS n FROM sys.dm_exec_requests r CROSS APPLY sys.dm_exec_sql_text(r.sql_handle) q WHERE q.text LIKE '%rowset_multirun_probe%' AND r.session_id <> @@SPID",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for _, target := range targets {
			wg.Add(1)
			go func(target map[string]string) {
				defer wg.Done()
				one, _ := json.Marshal(map[string]any{"targets": []map[string]string{target}, "statements": []string{slow[engines[target["connectionId"]]]}})
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/multirun", bytes.NewReader(one))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+c.token)
				if response, err := c.client.Do(request); err == nil {
					_, _ = bytes.NewBuffer(nil).ReadFrom(response.Body)
					response.Body.Close()
				}
			}(target)
		}
		wg.Wait()
	}()
	count := func(target map[string]string) int {
		code, _, body := c.do(t, "POST", "/api/connections/"+target["connectionId"]+"/query", map[string]any{"sql": stillRunning[engines[target["connectionId"]]], "database": "rowset_e2e"})
		var result struct{ Rows [][]any }
		if code != http.StatusOK || json.Unmarshal(body, &result) != nil || len(result.Rows) != 1 {
			t.Fatalf("%s process check: %d %.300s", engines[target["connectionId"]], code, body)
		}
		var n int
		switch value := result.Rows[0][0].(type) {
		case float64:
			n = int(value)
		case string:
			_, _ = fmt.Sscan(value, &n)
		}
		return n
	}
	// The probes have to be running before stopping means anything.
	deadline := time.Now().Add(10 * time.Second)
	for _, target := range targets {
		for count(target) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%s probe never started", engines[target["connectionId"]])
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	cancel()
	<-done
	deadline = time.Now().Add(5 * time.Second)
	for _, target := range targets {
		for n := count(target); n != 0; n = count(target) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d probe statements still running after stop", engines[target["connectionId"]], n)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}
