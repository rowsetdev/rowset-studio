package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

func TestStatementHooksComposeRefuseAndDefaultToUnchanged(t *testing.T) {
	ctx := context.Background()
	info, err := sqlguard.Parse("SELECT id, email FROM users WHERE id > 0")
	if err != nil {
		t.Fatal(err)
	}

	unchanged, notes, err := (&Server{}).prepareStatement(ctx, StatementRequest{Statement: info})
	if err != nil || len(notes) != 0 || unchanged.Raw != info.Raw {
		t.Fatalf("no hooks must leave the statement alone: %q notes=%v err=%v", unchanged.Raw, notes, err)
	}

	s := &Server{}
	s.AddStatementHook(func(_ context.Context, request StatementRequest) (string, error) {
		return request.Statement.Raw + " AND tenant_id = 7", nil
	}, "rewritten")
	s.AddStatementHook(func(_ context.Context, request StatementRequest) (string, error) {
		if !strings.Contains(request.Statement.Raw, "tenant_id = 7") {
			t.Error("second hook did not receive the first hook's output")
		}
		return request.Statement.Raw, nil
	}, "untouched")
	prepared, notes, err := s.prepareStatement(ctx, StatementRequest{Statement: info})
	if err != nil || notes["rewritten"] != true || notes["untouched"] != nil || !strings.HasSuffix(prepared.Raw, "tenant_id = 7") || prepared.Kind != sqlguard.Select {
		t.Fatalf("composed statement: %q notes=%v err=%v", prepared.Raw, notes, err)
	}

	refused := &Server{}
	refused.AddStatementHook(func(context.Context, StatementRequest) (string, error) { return "", errors.New("not allowed") }, "")
	if _, _, err := refused.prepareStatement(ctx, StatementRequest{Statement: info}); err == nil {
		t.Fatal("hook error did not refuse the statement")
	}
}

func TestResultHooksMergeInColumnOrderAndSkipNulls(t *testing.T) {
	s := &Server{}
	s.AddResultHook(func(context.Context, ResultRequest) (ResultTransforms, error) {
		return ResultTransforms{2: func(any) any { return "first" }, 9: func(any) any { return "out of range" }}, nil
	}, "first")
	s.AddResultHook(func(context.Context, ResultRequest) (ResultTransforms, error) {
		return ResultTransforms{0: func(any) any { return "***" }, 2: func(any) any { return "later wins" }}, nil
	}, "second")
	transforms, notes, err := s.prepareResult(context.Background(), ResultRequest{Columns: []string{"email", "id", "phone"}})
	if err != nil || len(transforms) != 2 || !reflect.DeepEqual(notes["first"], []string{"phone"}) || !reflect.DeepEqual(notes["second"], []string{"email", "phone"}) {
		t.Fatalf("transforms=%d notes=%v err=%v", len(transforms), notes, err)
	}
	row := []any{"a@example.com", int64(1), nil}
	transforms.apply(row)
	if row[0] != "***" || row[1] != int64(1) || row[2] != nil {
		t.Fatalf("applied row: %#v", row)
	}
	row = []any{"b@example.com", int64(2), "555"}
	transforms.apply(row)
	if row[2] != "later wins" {
		t.Fatalf("later hook must win for a column: %#v", row)
	}
}
