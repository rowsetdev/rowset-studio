package activity

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/store"
)

func TestBufferedFlushesOnClose(t *testing.T) {
	data, err := store.Open(context.Background(), t.TempDir()+"/rowset.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	buffer := NewBuffered(SQLite{Data: data}, 4)
	item := domain.QueryHistory{ID: "history", UserID: "user", ConnectionID: "conn", SQL: "select 1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := buffer.CreateQueryHistory(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	buffer.Close()
	items, err := data.ListQueryHistory(context.Background(), "user", "conn", nil, nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestBufferedHistoryReadsIncludeWhatWasJustQueued(t *testing.T) {
	data, err := store.Open(context.Background(), t.TempDir()+"/rowset.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	buffer := NewBuffered(SQLite{Data: data}, 64)
	defer buffer.Close()
	for i := 0; i < 20; i++ {
		item := domain.QueryHistory{ID: fmt.Sprintf("h%d", i), UserID: "user", ConnectionID: "conn", SQL: "select 1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := buffer.CreateQueryHistory(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	// Read straight after queueing: the statement a person just ran must be
	// in their history without waiting for the writer.
	items, err := buffer.ListQueryHistory(context.Background(), "user", "conn", nil, nil)
	if err != nil || len(items) != 20 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

// slowStore holds every write until released, so the queue can be filled.
type slowStore struct {
	release chan struct{}
	mu      sync.Mutex
	direct  int
	stored  int
}

func (s *slowStore) Ping(context.Context) error { return nil }
func (s *slowStore) WriteAudit(ctx context.Context, _ domain.AuditLog) error {
	return s.record(ctx)
}
func (s *slowStore) CreateQueryHistory(ctx context.Context, _ domain.QueryHistory) error {
	return s.record(ctx)
}
func (s *slowStore) record(context.Context) error {
	<-s.release
	s.mu.Lock()
	s.stored++
	s.mu.Unlock()
	return nil
}
func (s *slowStore) ListQueryHistory(context.Context, string, string, *string, *string) ([]domain.QueryHistory, error) {
	return nil, nil
}
func (s *slowStore) Purge(context.Context, *uint32, *uint32) (uint64, uint64, error) {
	return 0, 0, nil
}
func (s *slowStore) VerifyAuditChain(context.Context) ([]store.ChainBreak, int, error) {
	return nil, 0, nil
}

func TestBufferedNeverDropsARecordWhenTheQueueIsFull(t *testing.T) {
	backend := &slowStore{release: make(chan struct{})}
	buffer := NewBuffered(backend, 2)
	const total = 12
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := buffer.CreateQueryHistory(context.Background(), domain.QueryHistory{ID: fmt.Sprint(i)}); err != nil {
				t.Errorf("record %d rejected: %v", i, err)
			}
		}(i)
	}
	// Let the writes through only once the queue has had time to fill, so
	// some of them must take the direct path.
	time.Sleep(50 * time.Millisecond)
	close(backend.release)
	wg.Wait()
	buffer.Close()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.stored != total {
		t.Fatalf("stored %d of %d records", backend.stored, total)
	}
}

func TestBufferedWritesAfterCloseAreStoredDirectly(t *testing.T) {
	data, err := store.Open(context.Background(), t.TempDir()+"/rowset.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	buffer := NewBuffered(SQLite{Data: data}, 4)
	buffer.Close()
	// A request that finishes after shutdown began must neither panic on the
	// closed queue nor lose its record.
	item := domain.QueryHistory{ID: "late", UserID: "user", ConnectionID: "conn", SQL: "select 1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := buffer.CreateQueryHistory(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := data.ListQueryHistory(context.Background(), "user", "conn", nil, nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestBufferedConcurrentWritersKeepEveryRecord(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, t.TempDir()+"/rowset.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	buffer := NewBuffered(SQLite{Data: data}, 4096)
	const workers, each = 48, 200
	started := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				now := time.Now().UTC().Format(time.RFC3339Nano)
				id := fmt.Sprintf("%d-%d", worker, i)
				if err := buffer.CreateQueryHistory(ctx, domain.QueryHistory{ID: "h" + id, UserID: "user", ConnectionID: "conn", SQL: "select 1", Status: "success", CreatedAt: now}); err != nil {
					t.Error(err)
				}
				if err := buffer.WriteAudit(ctx, domain.AuditLog{ID: "a" + id, OrgID: "org", UserID: "user", SQL: "select 1", CreatedAt: now, StartedAt: now}); err != nil {
					t.Error(err)
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := buffer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	buffer.Close()
	var histories, audits, broken int
	_ = data.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM query_history").Scan(&histories)
	_ = data.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&audits)
	// Every entry but the first names the entry stored just before it.
	_ = data.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs a WHERE a.rowid > (SELECT MIN(rowid) FROM audit_logs)
		AND a.prev_hash IS NOT (SELECT b.entry_hash FROM audit_logs b WHERE b.rowid < a.rowid ORDER BY b.rowid DESC LIMIT 1)`).Scan(&broken)
	if histories != workers*each || audits != workers*each || broken != 0 {
		t.Fatalf("histories=%d audits=%d broken chain links=%d", histories, audits, broken)
	}
	t.Logf("%d records in %s (%.0f records/s)", histories+audits, elapsed.Round(time.Millisecond), float64(histories+audits)/elapsed.Seconds())
}

// A backend that stores nothing until released, and still answers reads.
type stuckStore struct{ slowStore }

func (s *stuckStore) ListQueryHistory(context.Context, string, string, *string, *string) ([]domain.QueryHistory, error) {
	return []domain.QueryHistory{{ID: "already stored"}}, nil
}

func TestHistoryReadsDoNotWaitForeverOnAStuckBackend(t *testing.T) {
	backend := &stuckStore{slowStore{release: make(chan struct{})}}
	buffer := NewBuffered(backend, 8)
	original := flushBeforeReadWait
	flushBeforeReadWait = 100 * time.Millisecond
	t.Cleanup(func() {
		flushBeforeReadWait = original
		close(backend.release)
		buffer.Close()
	})
	if err := buffer.CreateQueryHistory(context.Background(), domain.QueryHistory{ID: "queued"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	items, err := buffer.ListQueryHistory(context.Background(), "user", "conn", nil, nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("history read waited %s for a backend that is not writing", elapsed)
	}
}
