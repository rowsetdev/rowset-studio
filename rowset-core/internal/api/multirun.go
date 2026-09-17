package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A script run on several connections is carried out here rather than by the
// browser: a browser opens at most six connections to one address, and those
// are shared with everything else Studio loads, so running ten connections
// from the page would really run six and stall the rest of the app until they
// finished. One request streams the progress of every connection instead.

const (
	defaultMultiRunConcurrency = 4
	maxMultiRunConcurrency     = 10
)

type multiRunTarget struct {
	ConnectionID string `json:"connectionId"`
	Database     string `json:"database"`
}

type multiRunInput struct {
	Targets     []multiRunTarget `json:"targets"`
	Statements  []string         `json:"statements"`
	Concurrency int              `json:"concurrency"`
	Backup      bool             `json:"backup"`
}

// multiRunEvent reports one connection's progress. Result is the response of
// its last statement that returned columns, passed on unchanged so values
// such as large integers keep every digit.
type multiRunEvent struct {
	Type            string          `json:"type"`
	Index           int             `json:"index"`
	Status          string          `json:"status,omitempty"`
	Completed       int             `json:"completed"`
	Total           int             `json:"total"`
	RowCount        *int64          `json:"rowCount,omitempty"`
	Error           string          `json:"error,omitempty"`
	FailedStatement string          `json:"failedStatement,omitempty"`
	DurationMS      int64           `json:"durationMs"`
	Result          json.RawMessage `json:"result,omitempty"`
}

func (s *Server) runOnConnections(w http.ResponseWriter, r *http.Request) {
	var input multiRunInput
	if !decodeJSON(w, r, &input) {
		return
	}
	statements := make([]string, 0, len(input.Statements))
	for _, statement := range input.Statements {
		if statement = strings.TrimSpace(statement); statement != "" {
			statements = append(statements, statement)
		}
	}
	if len(input.Targets) == 0 || len(statements) == 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "choose at least one connection and one statement")
		return
	}
	for _, target := range input.Targets {
		if strings.TrimSpace(target.ConnectionID) == "" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "every target needs a connectionId")
			return
		}
	}
	concurrency := input.Concurrency
	if concurrency <= 0 {
		concurrency = defaultMultiRunConcurrency
	}
	if concurrency > maxMultiRunConcurrency {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "concurrency must be between 1 and 10")
		return
	}
	concurrency = min(concurrency, len(input.Targets))

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	var writeMu sync.Mutex
	emit := func(event multiRunEvent) {
		writeMu.Lock()
		defer writeMu.Unlock()
		line, _ := json.Marshal(event)
		if _, err := w.Write(append(line, '\n')); err == nil {
			_ = controller.Flush()
		}
	}

	ctx := r.Context()
	var next atomic.Int64
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				index := int(next.Add(1)) - 1
				if index >= len(input.Targets) {
					return
				}
				s.runTarget(ctx, r, index, input.Targets[index], statements, input.Backup, emit)
			}
		}()
	}
	workers.Wait()
	emit(multiRunEvent{Type: "done", Total: len(statements)})
}

// runTarget runs the statements on one connection in order and stops at the
// first error; other connections carry on.
func (s *Server) runTarget(ctx context.Context, r *http.Request, index int, target multiRunTarget, statements []string, backup bool, emit func(multiRunEvent)) {
	started := time.Now()
	event := multiRunEvent{Type: "target", Index: index, Total: len(statements)}
	send := func(status string) {
		event.Status = status
		event.DurationMS = time.Since(started).Milliseconds()
		emit(event)
		event.Result = nil
	}
	if ctx.Err() != nil {
		send("cancelled")
		return
	}
	send("running")
	var last json.RawMessage
	for _, sql := range statements {
		if ctx.Err() != nil {
			event.FailedStatement = sql
			event.Error = stoppedMessage
			send("cancelled")
			return
		}
		outcome := runStatement(s, ctx, r, target, sql, backup)
		if outcome.err != "" {
			event.FailedStatement = sql
			event.Error = outcome.err
			event.Result = last
			if ctx.Err() != nil {
				event.Error = stoppedMessage
				send("cancelled")
				return
			}
			send("error")
			return
		}
		event.Completed++
		event.RowCount = outcome.rowCount
		if outcome.hasColumns {
			last = outcome.body
		}
		send("running")
	}
	event.Result = last
	send("success")
}

// runStatement is the one statement a target runs; tests replace it.
var runStatement = (*Server).runStatementFor

const stoppedMessage = "Stopped. A write that was already running may have completed; check the database."

type statementOutcome struct {
	body       json.RawMessage
	rowCount   *int64
	hasColumns bool
	err        string
}

// runStatementFor sends one statement through the same handler a single run
// uses, as the same person, so policies, row backups, timeouts and the audit
// trail apply to each connection exactly as they would on their own.
func (s *Server) runStatementFor(ctx context.Context, r *http.Request, target multiRunTarget, sql string, backup bool) statementOutcome {
	connection, err := s.store.Connection(ctx, target.ConnectionID)
	if err != nil {
		return statementOutcome{err: err.Error()}
	}
	endpoint := "/api/connections/" + target.ConnectionID + "/query"
	input := map[string]any{"sql": sql, "database": target.Database, "backup": backup}
	handler := s.runQuery
	switch connection.Engine {
	case "cassandra":
		endpoint = "/api/connections/" + target.ConnectionID + "/cassandra/query"
		input = map[string]any{"query": sql, "keyspace": target.Database}
		handler = s.cassandraQuery
	case "mongodb", "redis", "valkey", "elasticsearch":
		return statementOutcome{err: "run on several connections is not available for this engine"}
	}
	body, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return statementOutcome{err: err.Error()}
	}
	request.SetPathValue("id", target.ConnectionID)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", r.UserAgent())
	request.RemoteAddr = r.RemoteAddr
	response := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	handler(response, request)

	raw := response.body.Bytes()
	var summary struct {
		Columns      []string        `json:"columns"`
		RowCount     *int64          `json:"rowCount"`
		RowsAffected *int64          `json:"rowsAffected"`
		Message      string          `json:"message"`
		Error        json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(raw, &summary)
	if response.status == http.StatusAccepted {
		message := summary.Message
		if message == "" {
			message = "The statement is waiting and has not run."
		}
		return statementOutcome{err: message}
	}
	if response.status >= 300 || (len(summary.Error) > 0 && string(summary.Error) != "null") {
		return statementOutcome{err: errorText(summary.Error, response.status)}
	}
	count := summary.RowCount
	if count == nil {
		count = summary.RowsAffected
	}
	return statementOutcome{body: append(json.RawMessage(nil), raw...), rowCount: count, hasColumns: len(summary.Columns) > 0}
}

// errorText reads either {"message": ...} or a plain string, the two shapes a
// failed statement is reported in.
func errorText(raw json.RawMessage, status int) string {
	var detail struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &detail) == nil && detail.Message != "" {
		return detail.Message
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}
	return http.StatusText(status)
}

// bufferedResponse collects a handler's response in memory.
type bufferedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
	wrote  bool
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(status int) {
	if !b.wrote {
		b.status, b.wrote = status, true
	}
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.wrote = true
	return b.body.Write(p)
}
func (b *bufferedResponse) Flush() {}
