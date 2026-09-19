package store

import (
	"context"
	"strconv"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestVerifyAuditChainDetectsTampering(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, t.TempDir()+"/rowset.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()

	for i, sql := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
		entry := domain.AuditLog{ID: "audit-" + strconv.Itoa(i), OrgID: "org-1", UserID: "user-1", SQL: sql, StartedAt: NowString(), CreatedAt: NowString(), PolicyDecision: "allow"}
		if err := data.WriteAudit(ctx, entry); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	breaks, checked, err := data.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatalf("verify before tampering: %v", err)
	}
	if len(breaks) != 0 {
		t.Fatalf("expected no breaks before tampering, got %v", breaks)
	}
	if checked != 3 {
		t.Fatalf("expected 3 entries checked, got %d", checked)
	}

	// Tamper with one entry directly, the way an out-of-band database edit
	// would: change the recorded statement without touching its hash.
	if _, err := data.db.ExecContext(ctx, "UPDATE audit_logs SET sql = 'SELECT tampered' WHERE org_id = 'org-1' AND sql = 'SELECT 2'"); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	breaks, _, err = data.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatalf("verify after tampering: %v", err)
	}
	if len(breaks) != 1 || breaks[0].OrgID != "org-1" {
		t.Fatalf("expected exactly one break for org-1, got %v", breaks)
	}
}

// Entries whose hash covers a reference kept in an extension's column verify
// as intact, and a changed reference is caught.
func TestVerifyAuditChainCoversTheReferenceColumn(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, t.TempDir()+"/rowset.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.db.ExecContext(ctx, "ALTER TABLE audit_logs ADD COLUMN reference_id TEXT"); err != nil {
		t.Fatal(err)
	}
	auditReferenceColumn = "reference_id"
	defer func() { auditReferenceColumn = "" }()
	for i, reference := range []string{"", "ref-1"} {
		entry := domain.AuditLog{ID: "audit-" + strconv.Itoa(i), OrgID: "org-1", UserID: "user-1", SQL: "SELECT 1", StartedAt: NowString(), CreatedAt: NowString(), PolicyDecision: "allow", Reference: reference}
		if err := data.WriteAudit(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	if breaks, checked, err := data.VerifyAuditChain(ctx); err != nil || len(breaks) != 0 || checked != 2 {
		t.Fatalf("intact chain: breaks=%v checked=%d err=%v", breaks, checked, err)
	}
	if _, err := data.db.ExecContext(ctx, "UPDATE audit_logs SET reference_id = 'ref-2' WHERE id = 'audit-1'"); err != nil {
		t.Fatal(err)
	}
	if breaks, _, err := data.VerifyAuditChain(ctx); err != nil || len(breaks) != 1 {
		t.Fatalf("changed reference: breaks=%v err=%v", breaks, err)
	}
}
