package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

type policyDefinition struct {
	Key, Description, Risk, Detail string
	HasValue                       bool
	Default                        bool
	// Shared definitions only exist in shared installations.
	Shared bool
}

var policyCatalog = []policyDefinition{
	{"deny_select_without_where", "Block table SELECT statements without a WHERE clause", "medium", "Blocks unbounded reads from physical tables. Constant queries such as SELECT 1 remain available.", false, false, false},
	{"deny_delete_without_where", "Block DELETE statements without a WHERE clause", "critical", "Blocks DELETEs with no effective WHERE clause.", false, true, false},
	{"deny_update_without_where", "Block UPDATE statements without a WHERE clause", "critical", "Blocks UPDATEs with no effective WHERE clause.", false, true, false},
	{"deny_drop", "Block DROP statements", "critical", "Blocks DROP on any object.", false, true, false},
	{"deny_truncate", "Block TRUNCATE statements", "high", "Blocks full-table TRUNCATE operations.", false, true, false},
	{"limit_rows", "Limit how many rows a result returns", "low", "Rowset stops reading after this many rows and cancels the rest; your statement is never changed. New workspaces cap results at 10,000 rows.", true, true, false},
	{"query_timeout_seconds", "Stop queries after a fixed number of seconds", "medium", "Applies the shortest of this policy, the saved connection timeout and the server stream timeout.", true, false, false},
	{"deny_unclassified", "Block SQL statements the parser cannot classify", "high", "Fails closed on unusual SQL syntax.", false, false, false},
}

func policyKnown(key string) (policyDefinition, bool) {
	for _, item := range policyCatalog {
		if item.Key == key {
			return item, true
		}
	}
	return policyDefinition{}, false
}
func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	items, err := s.store.ListPolicyOverrides(r.Context(), identity.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "policies could not be read")
		return
	}
	connectionID := strings.TrimSpace(r.URL.Query().Get("connectionId"))
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	out := make([]map[string]any, 0, len(policyCatalog))
	for _, definition := range policyCatalog {
		if definition.Shared && !s.config.Shared {
			continue
		}
		enabled, value, scope := definition.Default, (*string)(nil), "global"
		var global *domain.PolicyOverride
		for index := range items {
			if items[index].Key == definition.Key {
				global = &items[index]
				break
			}
		}
		if global != nil {
			enabled, value = global.Enabled, global.Config
		}
		globalEnabled := enabled
		if connectionID != "" && connectionID != "*" {
			for _, item := range items {
				if item.Key == definition.Key+"@"+connectionID {
					enabled, value, scope = item.Enabled, item.Config, "connection"
				}
			}
		}
		if role != "" && role != "*" {
			for _, item := range items {
				if item.Key == definition.Key+"@role:"+role {
					enabled, value, scope = item.Enabled, item.Config, "role"
				}
			}
		}
		overrides := []map[string]any{}
		for _, item := range items {
			base, target, ok := strings.Cut(item.Key, "@")
			if !ok || base != definition.Key {
				continue
			}
			kind := "connection"
			if strings.HasPrefix(target, "role:") {
				kind = "role"
				target = strings.TrimPrefix(target, "role:")
			}
			overrides = append(overrides, map[string]any{"scope": kind, "target": target, "enabled": item.Enabled, "value": item.Config})
		}
		out = append(out, map[string]any{"key": definition.Key, "description": definition.Description, "risk": definition.Risk, "detail": definition.Detail, "enabled": enabled, "scope": scope, "value": value, "hasValue": definition.HasValue, "globalEnabled": globalEnabled, "overrides": overrides})
	}
	writeJSON(w, 200, map[string]any{"policies": out})
}

type togglePolicyInput struct {
	Enabled      bool    `json:"enabled"`
	ConnectionID *string `json:"connectionId"`
	Role         *string `json:"role"`
	Value        *string `json:"value"`
}

func scopedPolicyKey(key string, connectionID, role *string) string {
	if role != nil && strings.TrimSpace(*role) != "" && *role != "*" {
		return key + "@role:" + strings.TrimSpace(*role)
	}
	if connectionID != nil && strings.TrimSpace(*connectionID) != "" && *connectionID != "*" {
		return key + "@" + strings.TrimSpace(*connectionID)
	}
	return key
}
func (s *Server) togglePolicy(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	definition, ok := policyKnown(key)
	if !ok {
		writeError(w, 404, "NOT_FOUND", "unknown policy")
		return
	}
	var input togglePolicyInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.personalPolicyAllowed(w, key, input.Role) {
		return
	}
	var value *string
	if input.Value != nil && strings.TrimSpace(*input.Value) != "" {
		trimmed := strings.TrimSpace(*input.Value)
		value = &trimmed
	}
	if definition.HasValue && input.Enabled {
		number, err := strconv.Atoi(stringValue(value))
		if err != nil || number <= 0 || number > 86400 {
			writeError(w, 400, "BAD_REQUEST", "this policy requires a numeric value between 1 and 86400")
			return
		}
	} else if !definition.HasValue && value != nil {
		writeError(w, 400, "BAD_REQUEST", "this policy does not take a value")
		return
	}
	identity := identityFromContext(r.Context())
	stored := scopedPolicyKey(key, input.ConnectionID, input.Role)
	if err := s.store.SetPolicyOverride(r.Context(), id.New(), identity.OrgID, stored, input.Enabled, value); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"key": key, "enabled": input.Enabled, "value": value})
}
func (s *Server) deletePolicyScope(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, ok := policyKnown(key); !ok {
		writeError(w, 404, "NOT_FOUND", "unknown policy")
		return
	}
	var connectionID, role *string
	if value := strings.TrimSpace(r.URL.Query().Get("connectionId")); value != "" {
		connectionID = &value
	}
	if value := strings.TrimSpace(r.URL.Query().Get("role")); value != "" {
		role = &value
	}
	identity := identityFromContext(r.Context())
	if err := s.store.DeletePolicyOverride(r.Context(), identity.OrgID, scopedPolicyKey(key, connectionID, role)); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(204)
}

var customKinds = map[string]bool{"deny_table": true, "deny_schema": true, "deny_statement": true, "limit_rows": true, "deny_write_outside_hours": true}

// customRule adds a custom policy kind, evaluated when a statement is not
// otherwise blocked.
type customRule struct {
	statementConfig bool // the config names a statement kind
	shared          bool // only available in shared installations
	evaluate        func(item domain.CustomPolicy, info sqlguard.Info, matchTable bool, decision policy.Decision) (policy.Decision, bool)
}

var customRules = map[string]customRule{}

// personalPolicyAllowed refuses role scopes and shared-only policies in a
// personal workspace.
func (s *Server) personalPolicyAllowed(w http.ResponseWriter, kind string, role *string) bool {
	if s.config.Shared {
		return true
	}
	definition, builtIn := policyKnown(kind)
	rule, custom := customRules[kind]
	scoped := role != nil && strings.TrimSpace(*role) != "" && strings.TrimSpace(*role) != "*"
	if scoped || builtIn && definition.Shared || custom && rule.shared {
		writeError(w, http.StatusForbidden, "PERSONAL_WORKSPACE", "This policy is not available in a personal workspace")
		return false
	}
	return true
}

type customPolicyInput struct {
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	Config       string  `json:"config"`
	ConnectionID *string `json:"connectionId"`
	Role         *string `json:"role"`
}

func customPolicyJSON(item domain.CustomPolicy) map[string]any {
	return map[string]any{"id": item.ID, "name": item.Name, "kind": item.Kind, "config": item.Config, "connectionId": stringValue(item.ConnectionID), "role": stringValue(item.RoleName), "enabled": item.Enabled, "createdAt": item.CreatedAt}
}
func validateCustom(kind, config string) string {
	rule, extra := customRules[kind]
	if !customKinds[kind] && !extra {
		return "unknown policy kind"
	}
	if kind == "deny_statement" || extra && rule.statementConfig {
		if config != "insert" && config != "update" && config != "delete" && config != "ddl" {
			return "statement config must be one of insert, update, delete, ddl"
		}
	}
	switch kind {
	case "limit_rows":
		value, err := strconv.Atoi(config)
		if err != nil || value <= 0 {
			return "limit_rows requires a positive row count"
		}
	case "deny_write_outside_hours":
		parts := strings.Split(config, "-")
		if len(parts) != 2 {
			return "time window must look like 09:00-18:00"
		}
		for _, part := range parts {
			if _, err := time.Parse("15:04", strings.TrimSpace(part)); err != nil {
				return "time window must look like 09:00-18:00"
			}
		}
	}
	return ""
}
func (s *Server) listCustomPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListCustomPolicies(r.Context(), identityFromContext(r.Context()).OrgID)
	if err != nil {
		writeError(w, 500, "INTERNAL", "failed to list custom policies")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, customPolicyJSON(item))
	}
	writeJSON(w, 200, map[string]any{"policies": out})
}
func (s *Server) createCustomPolicy(w http.ResponseWriter, r *http.Request) {
	s.putCustomPolicy(w, r, true)
}
func (s *Server) updateCustomPolicy(w http.ResponseWriter, r *http.Request) {
	s.putCustomPolicy(w, r, false)
}
func (s *Server) putCustomPolicy(w http.ResponseWriter, r *http.Request, create bool) {
	var input customPolicyInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name, input.Kind, input.Config = strings.TrimSpace(input.Name), strings.TrimSpace(input.Kind), strings.TrimSpace(input.Config)
	if !s.personalPolicyAllowed(w, input.Kind, input.Role) {
		return
	}
	if input.Name == "" || input.Config == "" {
		writeError(w, 400, "BAD_REQUEST", "name and config are required")
		return
	}
	if message := validateCustom(input.Kind, input.Config); message != "" {
		writeError(w, 400, "BAD_REQUEST", message)
		return
	}
	identity := identityFromContext(r.Context())
	connectionID := input.ConnectionID
	if connectionID != nil && (strings.TrimSpace(*connectionID) == "" || *connectionID == "*") {
		connectionID = nil
	}
	if connectionID != nil {
		connection, err := s.store.Connection(r.Context(), *connectionID)
		if err != nil || connection.OrgID != identity.OrgID {
			writeError(w, 400, "BAD_REQUEST", "unknown connection")
			return
		}
	}
	roleName := input.Role
	if roleName != nil && (strings.TrimSpace(*roleName) == "" || *roleName == "*") {
		roleName = nil
	}
	if roleName != nil {
		if _, err := s.store.RoleByName(r.Context(), identity.OrgID, *roleName); err != nil {
			writeError(w, 400, "BAD_REQUEST", "unknown role")
			return
		}
	}
	item := domain.CustomPolicy{ID: r.PathValue("id"), OrgID: identity.OrgID, Name: input.Name, Kind: input.Kind, Config: input.Config, ConnectionID: connectionID, RoleName: roleName, Enabled: true, CreatedAt: store.NowString()}
	var err error
	if create {
		item.ID = id.New()
		err = s.store.CreateCustomPolicy(r.Context(), item)
	} else {
		err = s.store.UpdateCustomPolicy(r.Context(), item)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, customPolicyJSON(item))
}

type customPolicyPatch struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) patchCustomPolicy(w http.ResponseWriter, r *http.Request) {
	var input customPolicyPatch
	if !decodeJSON(w, r, &input) {
		return
	}
	identity := identityFromContext(r.Context())
	if err := s.store.SetCustomPolicyEnabled(r.Context(), r.PathValue("id"), identity.OrgID, input.Enabled); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("id"), "enabled": input.Enabled})
}
func (s *Server) deleteCustomPolicy(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	if err := s.store.DeleteCustomPolicy(r.Context(), r.PathValue("id"), identity.OrgID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(204)
}

// PolicyDefinition is a toggle policy added to the built-in catalog of a
// shared installation.
type PolicyDefinition struct {
	Key, Description, Risk, Detail string
	// HasValue policies carry a value, such as a limit.
	HasValue bool
	// Default is whether a workspace has the policy on before anyone
	// changes it.
	Default bool
}

// RegisterPolicy adds a policy to the catalog of shared installations.
// Call before any server is created.
func RegisterPolicy(definition PolicyDefinition) {
	policyCatalog = append(policyCatalog, policyDefinition{Key: definition.Key, Description: definition.Description, Risk: definition.Risk, Detail: definition.Detail, HasValue: definition.HasValue, Default: definition.Default, Shared: true})
}

// CustomPolicyKind is a kind of custom policy a shared installation offers.
// Evaluate runs when a statement is not otherwise blocked; matchTable
// reports whether the statement reads or writes the policy's table.
type CustomPolicyKind struct {
	Kind string
	// StatementConfig kinds take a statement kind (insert, update, delete,
	// ddl) as their configuration.
	StatementConfig bool
	Evaluate        func(item domain.CustomPolicy, statement sqlguard.Info, matchTable bool, decision policy.Decision) (policy.Decision, bool)
}

// RegisterCustomPolicyKind adds a custom policy kind to shared
// installations. Call before any server is created.
func RegisterCustomPolicyKind(kind CustomPolicyKind) {
	customRules[kind.Kind] = customRule{statementConfig: kind.StatementConfig, shared: true, evaluate: kind.Evaluate}
}
