package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func TestRetentionDeletesNothingFromAPersonalWorkspaceUnlessAsked(t *testing.T) {
	days := uint32(30)
	for _, item := range []struct {
		name       string
		cfg        config.Config
		wantPeriod bool
	}{
		{name: "personal, defaults", cfg: config.Config{AuditRetentionDays: &days, QueryHistoryRetentionDays: &days}},
		{name: "personal, set explicitly", cfg: config.Config{AuditRetentionDays: &days, QueryHistoryRetentionDays: &days, RetentionConfigured: true}, wantPeriod: true},
		{name: "shared, defaults", cfg: config.Config{Shared: true, AuditRetentionDays: &days, QueryHistoryRetentionDays: &days}, wantPeriod: true},
	} {
		s := &Server{config: item.cfg}
		audit, history := s.retentionPeriods()
		if (audit != nil && history != nil) != item.wantPeriod {
			t.Fatalf("%s: audit=%v history=%v", item.name, audit, history)
		}
	}
}

func TestRetentionRemovesOnlyEntriesOlderThanThePeriod(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, t.TempDir()+"/rowset.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	old := time.Now().UTC().AddDate(0, 0, -45).Format(time.RFC3339Nano)
	recent := time.Now().UTC().AddDate(0, 0, -2).Format(time.RFC3339Nano)
	// Enough old rows to need several delete chunks.
	for i := 0; i < 4500; i++ {
		if err := data.CreateQueryHistory(ctx, domain.QueryHistory{ID: fmt.Sprintf("old-%d", i), UserID: "u", ConnectionID: "c", SQL: "select 1", CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.CreateQueryHistory(ctx, domain.QueryHistory{ID: "recent", UserID: "u", ConnectionID: "c", SQL: "select 1", CreatedAt: recent}); err != nil {
		t.Fatal(err)
	}
	days := uint32(30)
	_, removed, err := data.PurgeActivity(ctx, nil, &days)
	if err != nil {
		t.Fatal(err)
	}
	items, err := data.ListQueryHistory(ctx, "u", "c", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 4500 || len(items) != 1 || items[0].ID != "recent" {
		t.Fatalf("removed=%d remaining=%d", removed, len(items))
	}
}
