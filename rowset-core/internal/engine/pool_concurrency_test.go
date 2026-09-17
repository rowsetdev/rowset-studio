package engine

import (
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestPoolSetupDoesNotBlockOtherConnections(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	started := make(chan struct{})
	release := make(chan struct{})
	var opens atomic.Int32
	m.openPool = func(c Connection, _ *ssh.Client) (*sql.DB, error) {
		opens.Add(1)
		if c.ID == "slow" {
			close(started)
			<-release
		}
		return sql.Open("sqlite", ":memory:")
	}
	slow := Connection{ID: "slow", Engine: "sqlite", Database: "/tmp/slow.sqlite"}
	result := make(chan error, 1)
	go func() { _, err := m.database(slow); result <- err }()
	<-started
	fast := make(chan error, 1)
	go func() {
		_, err := m.database(Connection{ID: "fast", Engine: "sqlite", Database: "/tmp/fast.sqlite"})
		fast <- err
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a slow pool setup blocked an independent connection")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 2 {
		t.Fatalf("opened %d pools", opens.Load())
	}
}

func TestPoolInvalidationDiscardsStaleOpen(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	started := make(chan struct{})
	release := make(chan struct{})
	var opens atomic.Int32
	m.openPool = func(Connection, *ssh.Client) (*sql.DB, error) {
		if opens.Add(1) == 1 {
			close(started)
			<-release
		}
		return sql.Open("sqlite", ":memory:")
	}
	c := Connection{ID: "saved", Engine: "sqlite", Database: "/tmp/saved.sqlite"}
	first := make(chan error, 1)
	go func() { _, err := m.database(c); first <- err }()
	<-started
	if err := m.Invalidate("saved"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-first; err == nil {
		t.Fatal("invalidated setup was published")
	}
	if len(m.pools) != 0 {
		t.Fatal("invalidated pool survived")
	}
	if _, err := m.database(c); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 2 {
		t.Fatalf("fresh setup count=%d", opens.Load())
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.database(c); err == nil {
		t.Fatal("closed manager accepted a pool")
	}
}
