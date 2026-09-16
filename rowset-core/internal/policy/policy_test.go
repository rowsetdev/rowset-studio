package policy

import (
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
	"testing"
)

func statement(t *testing.T, sql string) sqlguard.Info {
	t.Helper()
	value, err := sqlguard.Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGuardrailsMatchLegacySemantics(t *testing.T) {
	for _, role := range []string{"admin", "developer"} {
		got := Evaluate(Input{Statement: statement(t, "update users set active=false"), Role: role, Disabled: map[string]bool{}, Enabled: map[string]bool{}})
		if got.Effect != Deny || got.PolicyID != "deny_update_without_where" {
			t.Fatalf("role %s bypassed: %#v", role, got)
		}
	}
	constant := Evaluate(Input{Statement: statement(t, "select 1"), Disabled: map[string]bool{}, Enabled: map[string]bool{}})
	if constant.Effect != Allow {
		t.Fatalf("constant select denied: %#v", constant)
	}
}

func TestReadOnlyWriteRunsOnlyOnceCleared(t *testing.T) {
	direct := Evaluate(Input{Statement: statement(t, "delete from users where id=1"), ReadOnly: true, Disabled: map[string]bool{}, Enabled: map[string]bool{}})
	if direct.Effect != Deny || direct.PolicyID != "read_only_role" {
		t.Fatalf("direct read-only write was not denied: %#v", direct)
	}
	cleared := Evaluate(Input{Statement: statement(t, "delete from users where id=1"), ReadOnly: true, Cleared: true, Disabled: map[string]bool{}, Enabled: map[string]bool{}})
	if cleared.Effect != Allow {
		t.Fatalf("cleared exact write was not allowed: %#v", cleared)
	}
}

func TestUnclassifiedDenialIsOptIn(t *testing.T) {
	base := Input{Statement: statement(t, "WAITFOR DELAY '00:00:01'"), Disabled: map[string]bool{}, Enabled: map[string]bool{}}
	if got := Evaluate(base); got.Effect != Allow {
		t.Fatalf("default-off policy denied statement: %#v", got)
	}
	base.Enabled["deny_unclassified"] = true
	if got := Evaluate(base); got.Effect != Deny || got.PolicyID != "deny_unclassified" {
		t.Fatalf("enabled policy did not deny statement: %#v", got)
	}
}

func TestCassandraStatementsUseGuardrails(t *testing.T) {
	for _, query := range []string{"UPDATE users SET name='x'", "BEGIN BATCH INSERT INTO users (id) VALUES (1); UPDATE users SET name='x'; APPLY BATCH;"} {
		info, err := sqlguard.ParseCQL(query)
		if err != nil {
			t.Fatal(err)
		}
		decision := Evaluate(Input{Statement: info, Disabled: map[string]bool{}, Enabled: map[string]bool{}})
		if decision.Effect != Deny || decision.PolicyID != "deny_update_without_where" {
			t.Fatalf("%q: %+v", query, decision)
		}
	}
	info, err := sqlguard.ParseCQL("INSERT INTO users (id) VALUES (1)")
	if err != nil {
		t.Fatal(err)
	}
	decision := Evaluate(Input{Statement: info, ReadOnly: true, Disabled: map[string]bool{}, Enabled: map[string]bool{}})
	if decision.Effect != Deny || decision.PolicyID != "read_only_role" {
		t.Fatalf("read-only insert: %+v", decision)
	}
}
