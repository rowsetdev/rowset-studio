package policy

import (
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

type Effect string
type Risk string

const (
	Allow    Effect = "allow"
	Deny     Effect = "deny"
	Low      Risk   = "low"
	Medium   Risk   = "medium"
	High     Risk   = "high"
	Critical Risk   = "critical"
)

type Decision struct {
	Effect   Effect `json:"effect"`
	PolicyID string `json:"policyId,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Risk     Risk   `json:"risk"`
}

type Input struct {
	Statement   sqlguard.Info
	Role        string
	ReadOnly    bool
	Environment string
	// Cleared marks a statement already cleared for this caller; guardrail
	// denials no longer apply to it.
	Cleared  bool
	Disabled map[string]bool
	Enabled  map[string]bool
}

// Rule is an additional check evaluated after the built-in guardrails.
type Rule func(Input) (Decision, bool)

var rules []Rule

// AddRule registers a rule. Call during program initialization.
func AddRule(rule Rule) { rules = append(rules, rule) }

func Evaluate(input Input) Decision {
	deny := func(key, reason string, risk Risk) (Decision, bool) {
		if input.Cleared || input.Disabled[key] {
			return Decision{}, false
		}
		return Decision{Effect: Deny, PolicyID: key, Reason: reason, Risk: risk}, true
	}
	stmt := input.Statement
	if stmt.Kind == sqlguard.Multi {
		return Decision{Effect: Deny, PolicyID: "multi_statement", Reason: "multiple SQL statements are forbidden", Risk: Critical}
	}
	if stmt.Kind == sqlguard.Unknown {
		return Decision{Effect: Deny, PolicyID: "unknown_statement", Reason: "unclassifiable SQL is forbidden", Risk: Critical}
	}
	if stmt.Kind == sqlguard.Other {
		if input.ReadOnly && !input.Cleared {
			return Decision{Effect: Deny, PolicyID: "read_only_unclassified", Reason: "Rowset does not recognise this statement's shape, and a read-only role may only run statements it can judge", Risk: High}
		}
		if input.Enabled["deny_unclassified"] {
			if d, ok := deny("deny_unclassified", "Rowset does not recognise this statement's shape and the guardrail blocks what it cannot judge; it can be turned off in policies, and the statement reported so a later release recognises it", High); ok {
				return d
			}
		}
	}
	if stmt.Kind == sqlguard.Select && len(stmt.Tables) > 0 && !stmt.HasWhere {
		if d, ok := deny("deny_select_without_where", "SELECT from a table without WHERE is forbidden", Medium); ok {
			return d
		}
	}
	if stmt.Kind == sqlguard.Delete && !stmt.HasWhere {
		if d, ok := deny("deny_delete_without_where", "DELETE without WHERE is forbidden", Critical); ok {
			return d
		}
	}
	if stmt.Kind == sqlguard.Update && !stmt.HasWhere {
		if d, ok := deny("deny_update_without_where", "UPDATE without WHERE is forbidden", Critical); ok {
			return d
		}
	}
	if stmt.IsDrop {
		if d, ok := deny("deny_drop", "DROP is forbidden", Critical); ok {
			return d
		}
	}
	if stmt.IsTruncate {
		if d, ok := deny("deny_truncate", "TRUNCATE is forbidden", High); ok {
			return d
		}
	}
	if input.ReadOnly && sqlguard.IsWrite(stmt.Kind) && !input.Cleared {
		return Decision{Effect: Deny, PolicyID: "read_only_role", Reason: "read-only role may not run write statements", Risk: High}
	}
	for _, rule := range rules {
		if d, ok := rule(input); ok {
			return d
		}
	}
	return Decision{Effect: Allow, Risk: Low}
}
