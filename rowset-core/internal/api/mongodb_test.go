package api

import (
	"encoding/json"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

func TestMongoFilterPolicyPredicate(t *testing.T) {
	for _, tt := range []struct {
		filter     string
		restricted bool
	}{
		{`{}`, false},
		{`{"name":"sample"}`, true},
		{`{"$and":[{}, {"name":"sample"}]}`, true},
		{`{"$or":[{}, {"name":"sample"}]}`, false},
		{`{"$or":[{"name":"sample"}, {"count":{"$gt":5}}]}`, true},
		{`{"$expr":{"$eq":[1,1]}}`, false},
	} {
		filter, err := engine.ParseMongoFilter(json.RawMessage(tt.filter))
		if err != nil {
			t.Fatal(err)
		}
		if got := mongoFilterHasPredicate(filter); got != tt.restricted {
			t.Fatalf("%s: got %v", tt.filter, got)
		}
	}
	info, err := sqlguard.ParseDialect(sqlguard.DialectPostgres, `SELECT * FROM "db"."items" WHERE "__rowset_document_filter__" IS NOT NULL`)
	if err != nil || !info.HasWhere {
		t.Fatalf("Mongo predicate must survive SQL policy normalization: %v %v", info, err)
	}
}
