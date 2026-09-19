package activity

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

type Store interface {
	Ping(context.Context) error
	WriteAudit(context.Context, domain.AuditLog) error
	CreateQueryHistory(context.Context, domain.QueryHistory) error
	ListQueryHistory(context.Context, string, string, *string, *string) ([]domain.QueryHistory, error)
	Purge(context.Context, *uint32, *uint32) (uint64, uint64, error)
	VerifyAuditChain(context.Context) ([]store.ChainBreak, int, error)
}

type SQLite struct{ Data *store.Store }

func (s SQLite) Ping(ctx context.Context) error { return s.Data.Ping(ctx) }
func (s SQLite) WriteAudit(ctx context.Context, item domain.AuditLog) error {
	return s.Data.WriteAudit(ctx, item)
}
func (s SQLite) CreateQueryHistory(ctx context.Context, item domain.QueryHistory) error {
	return s.Data.CreateQueryHistory(ctx, item)
}
func (s SQLite) ListQueryHistory(ctx context.Context, userID, connectionID string, from, to *string) ([]domain.QueryHistory, error) {
	return s.Data.ListQueryHistory(ctx, userID, connectionID, from, to)
}
func (s SQLite) Purge(ctx context.Context, auditDays, historyDays *uint32) (uint64, uint64, error) {
	return s.Data.PurgeActivity(ctx, auditDays, historyDays)
}
func (s SQLite) VerifyAuditChain(ctx context.Context) ([]store.ChainBreak, int, error) {
	return s.Data.VerifyAuditChain(ctx)
}

// WriteBatch stores a whole batch in one transaction.
func (s SQLite) WriteBatch(ctx context.Context, audits []domain.AuditLog, histories []domain.QueryHistory) error {
	return s.Data.WriteActivity(ctx, audits, histories)
}

// A record carries an audit entry, a history row, or neither: a record with
// only flushed set is a barrier that is released once everything queued
// before it has been stored.
type queuedRecord struct {
	audit   *domain.AuditLog
	history *domain.QueryHistory
	flushed chan struct{}
}

// Buffered takes activity off the request path: statements are recorded by
// one writer that stores them in batches, and reads wait until what was
// queued before them is stored, so a statement shows up in history as soon as
// it has run. It never drops a record. When the queue is full a request waits
// for room instead of writing around the writer: many writers competing for
// SQLite's write lock are not served in order, and under load some would wait
// past busy_timeout and fail.
type Buffered struct {
	backend Store
	queue   chan queuedRecord
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
	// closing guards the queue: senders hold it for reading, Close takes it
	// for writing before closing the channel, so nothing is sent on a closed
	// channel by a request that finished after shutdown began.
	closing sync.RWMutex
	closed  bool
}

// BatchWriter is implemented by a backend that can store many records in
// one write; Buffered then hands it each batch, audit entries in order.
type BatchWriter interface {
	WriteBatch(ctx context.Context, audits []domain.AuditLog, histories []domain.QueryHistory) error
}

const maxBatch = 256

func NewBuffered(backend Store, capacity int) *Buffered {
	if capacity < 1 {
		capacity = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	buffer := &Buffered{backend: backend, queue: make(chan queuedRecord, capacity), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go buffer.run()
	return buffer
}

func (b *Buffered) run() {
	defer close(b.done)
	for first := range b.queue {
		records := []queuedRecord{first}
		flushWindow := time.NewTimer(time.Millisecond)
	collect:
		for len(records) < maxBatch {
			select {
			case record, ok := <-b.queue:
				if !ok {
					break collect
				}
				records = append(records, record)
			case <-flushWindow.C:
				break collect
			}
		}
		if !flushWindow.Stop() {
			select {
			case <-flushWindow.C:
			default:
			}
		}
		b.store(records)
		for _, record := range records {
			if record.flushed != nil {
				close(record.flushed)
			}
		}
	}
}

// store retries a failed batch with backoff until it is written or the
// writer is cancelled at shutdown; a batch is one transaction, so a retry
// never stores part of it twice.
func (b *Buffered) store(records []queuedRecord) {
	if !hasData(records) {
		return
	}
	delay := 100 * time.Millisecond
	for {
		ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
		err := b.write(ctx, records)
		cancel()
		if err == nil {
			return
		}
		if b.ctx.Err() != nil {
			slog.Error("activity shutdown abandoned a batch after the graceful flush deadline", "error", err, "records", len(records))
			return
		}
		slog.Error("activity batch write failed; retrying without dropping records", "error", err, "records", len(records), "retry_ms", delay.Milliseconds())
		select {
		case <-time.After(delay):
		case <-b.ctx.Done():
			return
		}
		if delay < 10*time.Second {
			delay = min(delay*2, 10*time.Second)
		}
	}
}

func (b *Buffered) write(ctx context.Context, records []queuedRecord) error {
	if batch, ok := b.backend.(BatchWriter); ok {
		var audits []domain.AuditLog
		var histories []domain.QueryHistory
		for _, record := range records {
			if record.audit != nil {
				audits = append(audits, *record.audit)
			}
			if record.history != nil {
				histories = append(histories, *record.history)
			}
		}
		return batch.WriteBatch(ctx, audits, histories)
	}
	for _, record := range records {
		var err error
		if record.audit != nil {
			err = b.backend.WriteAudit(ctx, *record.audit)
		} else if record.history != nil {
			err = b.backend.CreateQueryHistory(ctx, *record.history)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func hasData(records []queuedRecord) bool {
	for _, record := range records {
		if record.audit != nil || record.history != nil {
			return true
		}
	}
	return false
}

// enqueueWait bounds how long a request waits for room in a full queue before
// writing its record itself.
var enqueueWait = 30 * time.Second

// enqueue hands a record to the writer, or reports false when it has to be
// written directly: the writer is closed, or the queue stayed full.
func (b *Buffered) enqueue(record queuedRecord) bool {
	b.closing.RLock()
	defer b.closing.RUnlock()
	if b.closed {
		return false
	}
	select {
	case b.queue <- record:
		return true
	default:
	}
	// The request's own context is not used: a person closing the tab does
	// not make the statement they ran any less worth recording.
	timer := time.NewTimer(enqueueWait)
	defer timer.Stop()
	select {
	case b.queue <- record:
		return true
	case <-timer.C:
		return false
	}
}

func (b *Buffered) Close() {
	b.once.Do(func() {
		b.closing.Lock()
		b.closed = true
		close(b.queue)
		b.closing.Unlock()
		select {
		case <-b.done:
			b.cancel()
		case <-time.After(10 * time.Second):
			slog.Error("activity graceful flush deadline exceeded; cancelling the backend write")
			b.cancel()
			select {
			case <-b.done:
			case <-time.After(time.Second):
				slog.Error("activity writer did not stop after cancellation")
			}
		}
	})
}

// Flush waits until every record queued before the call has been stored.
func (b *Buffered) Flush(ctx context.Context) error {
	barrier := make(chan struct{})
	if !b.enqueueBarrier(ctx, barrier) {
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A barrier must not be skipped when the queue is full, so it waits for room.
func (b *Buffered) enqueueBarrier(ctx context.Context, barrier chan struct{}) bool {
	b.closing.RLock()
	defer b.closing.RUnlock()
	if b.closed {
		// Close drains the queue before it returns, so nothing is pending.
		close(barrier)
		return true
	}
	select {
	case b.queue <- queuedRecord{flushed: barrier}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (b *Buffered) Ping(ctx context.Context) error { return b.backend.Ping(ctx) }

func (b *Buffered) WriteAudit(ctx context.Context, item domain.AuditLog) error {
	if b.enqueue(queuedRecord{audit: &item}) {
		return nil
	}
	if err := b.backend.WriteAudit(context.WithoutCancel(ctx), item); err != nil {
		slog.Error("activity audit record could not be written", "error", err, "id", item.ID)
		return err
	}
	return nil
}

func (b *Buffered) CreateQueryHistory(ctx context.Context, item domain.QueryHistory) error {
	if b.enqueue(queuedRecord{history: &item}) {
		return nil
	}
	if err := b.backend.CreateQueryHistory(context.WithoutCancel(ctx), item); err != nil {
		slog.Error("activity query-history record could not be written", "error", err, "id", item.ID)
		return err
	}
	return nil
}

// flushBeforeReadWait bounds how long a history read waits for the records
// queued before it. A local database stores them in milliseconds; a remote
// backend that is slow or down must not leave the history page waiting, so
// after this the read shows what is already stored.
var flushBeforeReadWait = 2 * time.Second

func (b *Buffered) ListQueryHistory(ctx context.Context, userID, connectionID string, from, to *string) ([]domain.QueryHistory, error) {
	waitCtx, cancel := context.WithTimeout(ctx, flushBeforeReadWait)
	err := b.Flush(waitCtx)
	cancel()
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return b.backend.ListQueryHistory(ctx, userID, connectionID, from, to)
}

func (b *Buffered) Purge(ctx context.Context, auditDays, historyDays *uint32) (uint64, uint64, error) {
	return b.backend.Purge(ctx, auditDays, historyDays)
}

func (b *Buffered) VerifyAuditChain(ctx context.Context) ([]store.ChainBreak, int, error) {
	waitCtx, cancel := context.WithTimeout(ctx, flushBeforeReadWait)
	err := b.Flush(waitCtx)
	cancel()
	if err != nil && ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	return b.backend.VerifyAuditChain(ctx)
}
