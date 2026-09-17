package api

import (
	"bytes"
	"strings"
	"testing"

	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

func TestCassandraCQLExportUsesServerJSONAndEscapesLiteral(t *testing.T) {
	query := `SELECT JSON * FROM "CaseSpace"."People"`
	info, err := sqlguard.ParseCQL(query)
	if err != nil || info.Kind != sqlguard.Select || len(info.Tables) != 1 || !strings.EqualFold(info.Tables[0].Name, "People") {
		t.Fatalf("CQL export query must remain a governed SELECT: %+v, %v", info, err)
	}
	var out bytes.Buffer
	rows := [][]any{{`{"id":"3f06ec2e-8d20-4c2a-9d30-98e98eb2673c","name":"O'Reilly","tags":["a","b"]}`}}
	if err := writeCassandraCQL(&out, "CaseSpace", "People", rows, true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, `INSERT INTO "CaseSpace"."People" JSON '{"id":"3f06ec2e-8d20-4c2a-9d30-98e98eb2673c","name":"O''Reilly","tags":["a","b"]}' DEFAULT UNSET;`) {
		t.Fatalf("wrong CQL export: %q", text)
	}
	if !strings.Contains(text, "Truncated at the configured row limit") || !strings.Contains(text, "TTL and write timestamps are not preserved") {
		t.Fatalf("missing export limits: %q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "INSERT INTO ") {
			continue
		}
		parsed, err := sqlguard.ParseCQL(line)
		if err != nil || parsed.Kind != sqlguard.Insert {
			t.Fatalf("exported statement cannot run through the CQL editor: %+v, %v", parsed, err)
		}
	}
}

func TestCassandraCQLExportRejectsInvalidRowsBeforeWriting(t *testing.T) {
	for _, row := range []any{`not json`, `[]`, `null`, 42} {
		var out bytes.Buffer
		if err := writeCassandraCQL(&out, "ks", "items", [][]any{{row}}, false); err == nil || out.Len() != 0 {
			t.Fatalf("row %#v produced %q, error %v", row, out.String(), err)
		}
	}
}
