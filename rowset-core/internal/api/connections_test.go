package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
)

func TestConnectionUsernameSupportsNewAndLegacyPayloads(t *testing.T) {
	base := connectionInput{Name: "prod", Engine: "postgres", Host: "db", Port: 5432, Password: "secret"}
	base.ConnectionUsername = "new-user"
	connection, _, message := normalizeConnectionInput(base, nil, "org")
	if message != "" || connection.ConnectionUsername != "new-user" {
		t.Fatalf("new field: %#v %q", connection, message)
	}
	base.ConnectionUsername, base.TechnicalUsername = "", "legacy-user"
	connection, _, message = normalizeConnectionInput(base, nil, "org")
	if message != "" || connection.ConnectionUsername != "legacy-user" {
		t.Fatalf("legacy field: %#v %q", connection, message)
	}
}

func TestConnectionTLSModeDefaultsAndLegacyFlag(t *testing.T) {
	base := connectionInput{Name: "prod", Engine: "postgres", Host: "db", Port: 5432, Password: "secret", ConnectionUsername: "user"}
	connection, _, message := normalizeConnectionInput(base, nil, "org")
	if message != "" || connection.TLSMode != engine.TLSVerifyFull || !connection.TLSRequired {
		t.Fatalf("new connections must verify by default: %#v %q", connection, message)
	}
	off, on := false, true
	base.TLSRequired = &off
	if connection, _, _ = normalizeConnectionInput(base, nil, "org"); connection.TLSMode != engine.TLSDisable || connection.TLSRequired {
		t.Fatalf("legacy off flag: %#v", connection)
	}
	existing := domain.Connection{ID: "c", Engine: "postgres", TLSMode: engine.TLSVerifyCA, TLSRequired: true, CreatedAt: "t", QueryTimeoutSeconds: 600}
	base.TLSRequired = &on
	if connection, _, _ = normalizeConnectionInput(base, &existing, "org"); connection.TLSMode != engine.TLSVerifyCA {
		t.Fatalf("legacy on flag weakened a stored mode: %s", connection.TLSMode)
	}
	prefer := "prefer"
	base.TLSMode = &prefer
	if _, _, message = normalizeConnectionInput(base, nil, "org"); message == "" {
		t.Fatal("unsupported TLS mode accepted")
	}
	full, ca := "verify-full", "not a certificate"
	base.TLSMode, base.TLSCAPEM = &full, &ca
	if _, _, message = normalizeConnectionInput(base, nil, "org"); !strings.Contains(message, "CA certificate") {
		t.Fatalf("invalid CA accepted: %q", message)
	}
	if (domain.Connection{Engine: "postgres", TLSRequired: true}).EffectiveTLSMode() != engine.TLSRequire ||
		(domain.Connection{Engine: "mssql", TLSRequired: true}).EffectiveTLSMode() != engine.TLSRequire ||
		(domain.Connection{Engine: "mysql", TLSRequired: true}).EffectiveTLSMode() != engine.TLSVerifyFull {
		t.Fatal("stored rows without a mode changed their historical verification")
	}
}

func TestSchemaJSONUsesStableLowercaseIndexFields(t *testing.T) {
	payload := schemaJSON(engine.Schema{
		Tables:   map[string][]engine.Column{"dbo.customers": {{Schema: "dbo", Table: "customers", Name: "id"}}},
		Indexes:  map[string][]engine.Index{"dbo.customers": {{Name: "pk_customers", Columns: []string{"id"}, Unique: true, Primary: true}}},
		Warnings: []string{"indexes metadata unavailable: permission denied"},
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{`"indexes":[{`, `"name":"pk_customers"`, `"columns":["id"]`, `"unique":true`, `"primary":true`, `"warnings":["indexes metadata unavailable: permission denied"]`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("schema JSON missing %s: %s", expected, text)
		}
	}
	if strings.Contains(text, `"Columns"`) || strings.Contains(text, `"Name"`) {
		t.Fatalf("schema JSON leaked legacy Go field names: %s", text)
	}
}
