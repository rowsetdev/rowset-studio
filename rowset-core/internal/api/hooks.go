package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// StatementRequest describes a statement that passed policy checks and is
// about to run.
type StatementRequest struct {
	Identity   domain.Identity
	RoleID     string
	Connection domain.Connection
	Statement  sqlguard.Info
}

// StatementHook may return a replacement statement. Returning the statement
// unchanged means no change; an error refuses the statement.
type StatementHook func(ctx context.Context, request StatementRequest) (string, error)

// ResultRequest describes a result set before its rows are streamed.
type ResultRequest struct {
	Identity   domain.Identity
	Connection domain.Connection
	Statement  sqlguard.Info
	Columns    []string
	Origins    []domain.ColumnOrigin
}

// ResultTransforms maps a column index to a function applied to every
// non-NULL value of that column.
type ResultTransforms map[int]func(any) any

// ResultHook plans per-column transforms for a result set.
type ResultHook func(ctx context.Context, request ResultRequest) (ResultTransforms, error)

// Annotations are extra fields a query response carries on behalf of hooks.
type Annotations map[string]any

type statementHook struct {
	run        StatementHook
	annotation string
}

type resultHook struct {
	run        ResultHook
	annotation string
}

// AddStatementHook registers a hook; hooks run in registration order and each
// sees the previous hook's output. When the hook changes the statement and
// annotation is not empty, the query response carries annotation: true.
func (s *Server) AddStatementHook(hook StatementHook, annotation string) {
	s.statementHooks = append(s.statementHooks, statementHook{run: hook, annotation: annotation})
}

// AddResultHook registers a hook; later hooks win for the same column. When
// annotation is not empty, the query response carries it with the names of
// the columns the hook transformed.
func (s *Server) AddResultHook(hook ResultHook, annotation string) {
	s.resultHooks = append(s.resultHooks, resultHook{run: hook, annotation: annotation})
}

// prepareStatement runs the statement hooks.
func (s *Server) prepareStatement(ctx context.Context, request StatementRequest) (sqlguard.Info, Annotations, error) {
	info, notes := request.Statement, Annotations{}
	for _, hook := range s.statementHooks {
		request.Statement = info
		next, err := hook.run(ctx, request)
		if err != nil {
			return info, notes, err
		}
		if next == info.Raw {
			continue
		}
		parsed, err := sqlguard.ParseDialect(sqlguard.DialectForEngine(request.Connection.Engine), next)
		if err != nil {
			return info, notes, err
		}
		info = parsed
		if hook.annotation != "" {
			notes[hook.annotation] = true
		}
	}
	return info, notes, nil
}

// prepareResult merges the result hooks.
func (s *Server) prepareResult(ctx context.Context, request ResultRequest) (ResultTransforms, Annotations, error) {
	transforms, notes := ResultTransforms{}, Annotations{}
	for _, hook := range s.resultHooks {
		planned, err := hook.run(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		indexes := make([]int, 0, len(planned))
		for index, transform := range planned {
			if index >= 0 && index < len(request.Columns) && transform != nil {
				indexes = append(indexes, index)
			}
		}
		sort.Ints(indexes)
		names := make([]string, 0, len(indexes))
		for _, index := range indexes {
			transforms[index] = planned[index]
			names = append(names, request.Columns[index])
		}
		if hook.annotation != "" {
			notes[hook.annotation] = names
		}
	}
	return transforms, notes, nil
}

func (a Annotations) merge(other Annotations) Annotations {
	for key, value := range other {
		a[key] = value
	}
	return a
}

func (a Annotations) addTo(target map[string]any) map[string]any {
	for key, value := range a {
		target[key] = value
	}
	return target
}

// fields renders the annotations as leading JSON object members, each followed
// by a comma, in a stable order.
func (a Annotations) fields() string {
	keys := make([]string, 0, len(a))
	for key := range a {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		name, _ := json.Marshal(key)
		value, err := json.Marshal(a[key])
		if err != nil {
			continue
		}
		out.Write(name)
		out.WriteByte(':')
		out.Write(value)
		out.WriteByte(',')
	}
	return out.String()
}

func (t ResultTransforms) apply(row []any) {
	for index, transform := range t {
		if index < len(row) && row[index] != nil {
			row[index] = transform(row[index])
		}
	}
}
