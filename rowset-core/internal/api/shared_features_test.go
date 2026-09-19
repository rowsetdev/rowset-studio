package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

type memberAccess struct{}

func (memberAccess) Role(_ context.Context, userID string) (AccessRole, error) {
	if userID == "member" {
		return AccessRole{ID: "member-role", Name: "analyst"}, nil
	}
	return AccessRole{Name: "admin"}, nil
}

func (memberAccess) Connection(context.Context, domain.Identity, AccessRole, string) (ConnectionGrant, bool, error) {
	return ConnectionGrant{NodePolicy: "primary_only", DefaultNodeRole: "primary"}, true, nil
}

func sharedServer(t *testing.T) (*Server, domain.Identity, domain.Identity) {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	data, err := store.Open(ctx, filepath.Join(directory, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	owner := domain.Identity{OrgID: "org", UserID: "owner", Role: "admin", Email: "owner@example.com"}
	if err := data.CreateOrganizationWithAdmin(ctx,
		domain.Organization{ID: "org", Name: "Org", CreatedAt: store.NowString()},
		domain.Role{ID: "admin", OrgID: "org", Name: "admin"},
		domain.User{ID: "owner", OrgID: "org", Email: owner.Email, PasswordHash: "unused", Status: "active", CreatedAt: store.NowString()},
	); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateUser(ctx, domain.User{ID: "member", OrgID: "org", Email: "member@example.com", PasswordHash: "unused", Status: "active", CreatedAt: store.NowString()}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Shared: true, DBPath: filepath.Join(directory, "control.db"), RequestBodyLimitBytes: 1 << 20, EncryptionKey: bytes.Repeat([]byte{9}, 32)}
	s := New(cfg, data, auth.NewIssuer(strings.Repeat("s", 32), ""), nil)
	s.SetAccess(memberAccess{})
	t.Cleanup(func() { s.Close() })
	return s, owner, domain.Identity{OrgID: "org", UserID: "member", Role: "analyst", Email: "member@example.com"}
}

// A shared server offers what a personal workspace does: scheduled queries
// write into a server folder per user and are downloaded, and a member can
// restore their row backups but not read a backup's raw script.
func TestSharedServerOffersScheduledQueriesAndRowBackups(t *testing.T) {
	s, owner, member := sharedServer(t)
	path := filepath.Join(t.TempDir(), "shared.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE people(id INTEGER PRIMARY KEY, email TEXT); INSERT INTO people VALUES (1,'ada@example.com')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	body, _ := json.Marshal(map[string]any{"name": "shared", "engine": "sqlite", "database": path, "tlsMode": "disable"})
	w := httptest.NewRecorder()
	s.createConnection(w, personalRequest(owner, string(body)))
	var created struct{ ID string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("connection: %d %s", w.Code, w.Body.String())
	}

	schedule, _ := json.Marshal(map[string]any{"name": "people", "connectionId": created.ID, "sql": "SELECT id, email FROM people WHERE id > 0", "schedule": map[string]any{"kind": "daily", "time": "10:00", "timezone": "UTC"}, "outputDir": "/etc", "format": "csv", "enabled": true})
	w = httptest.NewRecorder()
	s.createScheduled(w, personalRequest(member, string(schedule)))
	var saved struct {
		ID        string `json:"id"`
		OutputDir string `json:"outputDir"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatalf("schedule: %d %s", w.Code, w.Body.String())
	}
	if saved.OutputDir != s.userOutputDir("member") {
		t.Fatalf("output folder %q, want the server folder", saved.OutputDir)
	}
	r := personalRequest(member, "")
	r.SetPathValue("id", saved.ID)
	w = httptest.NewRecorder()
	s.runScheduledNow(w, r)
	if w.Code >= 300 {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	var runs struct {
		Runs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"runs"`
	}
	for attempt := 0; attempt < 50; attempt++ {
		w = httptest.NewRecorder()
		s.listScheduledRuns(w, r)
		_ = json.Unmarshal(w.Body.Bytes(), &runs)
		if len(runs.Runs) > 0 && runs.Runs[0].Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(runs.Runs) == 0 || runs.Runs[0].Status != "success" {
		t.Fatalf("runs: %s", w.Body.String())
	}
	r.SetPathValue("run_id", runs.Runs[0].ID)
	w = httptest.NewRecorder()
	s.scheduledRunFile(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ada@example.com") {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	if entries, _ := os.ReadDir(s.userOutputDir("member")); len(entries) != 1 {
		t.Fatalf("server folder holds %d files", len(entries))
	}

	query, _ := json.Marshal(map[string]any{"sql": "UPDATE people SET email = 'x@example.com' WHERE id = 1", "backup": true})
	w = importCall(t, s, member, s.runQuery, "POST", created.ID, "", string(query))
	var result struct {
		Backup struct{ ID string } `json:"backup"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Backup.ID == "" {
		t.Fatalf("backed-up update: %d %s", w.Code, w.Body.String())
	}
	r = personalRequest(member, "")
	r.SetPathValue("id", result.Backup.ID)
	w = httptest.NewRecorder()
	s.rowBackupRestore(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member read a backup script: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.applyRowBackup(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("member restore: %d %s", w.Code, w.Body.String())
	}
}
