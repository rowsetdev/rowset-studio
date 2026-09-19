package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestSharedModeCannotBeReversed(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, shared := range []bool{false, false, true, true} {
		if err := s.ClaimMode(ctx, shared); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClaimMode(ctx, false); err == nil {
		t.Fatal("shared database reopened as personal")
	}
}

func TestPersonalModeKeepsItsOwner(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ClaimMode(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, domain.User{ID: "u", OrgID: "o", Email: "owner@example.com", PasswordHash: "unused", Status: "active", CreatedAt: NowString()}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimMode(ctx, false); err != nil {
		t.Fatal(err)
	}
}

func TestUnmarkedDatabaseWithUsersIsShared(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateUser(ctx, domain.User{ID: "u", OrgID: "o", Email: "owner@example.com", PasswordHash: "unused", Status: "active", CreatedAt: NowString()}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimMode(ctx, false); err == nil {
		t.Fatal("unmarked database with users opened as personal")
	}
	if err := s.ClaimMode(ctx, true); err != nil {
		t.Fatal(err)
	}
}
