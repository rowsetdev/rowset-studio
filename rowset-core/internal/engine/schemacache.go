package engine

import (
	"context"
	"strings"
	"sync"
	"time"
)

// schemaTTL is how long a loaded schema answers further requests. It is short
// on purpose: long enough that the explorer, autocomplete, the diagram and the
// assistant opening together cost one read of the catalog, short enough that
// an object created from another tool appears on its own. Refresh and any
// DDL run through Rowset skip it.
const schemaTTL = 30 * time.Second

// loadSchema reads the catalog; tests replace it to count reads.
var loadSchema = func(m *Manager, ctx context.Context, connection Connection) (Schema, error) {
	return m.Schema(ctx, connection)
}

type schemaEntry struct {
	ready  chan struct{}
	schema Schema
	err    error
	loaded time.Time
}

type schemaCache struct {
	mu      sync.Mutex
	entries map[string]*schemaEntry
}

// CachedSchema returns the schema of a connection, loading it at most once at
// a time per connection and database. Callers share the result and must not
// modify it. With refresh set the catalog is read again whatever is cached.
func (m *Manager) CachedSchema(ctx context.Context, connection Connection, refresh bool) (Schema, error) {
	key := poolKey(connection)
	m.schemas.mu.Lock()
	if m.schemas.entries == nil {
		m.schemas.entries = map[string]*schemaEntry{}
	}
	entry := m.schemas.entries[key]
	if entry != nil {
		select {
		case <-entry.ready:
			if refresh || entry.err != nil || time.Since(entry.loaded) > schemaTTL {
				entry = nil
			}
		default:
			// A load that is still running may have started before whatever
			// the refresh is for, so a refresh starts its own; everyone else
			// waits for the running one.
			if refresh {
				entry = nil
			}
		}
	}
	if entry == nil {
		entry = &schemaEntry{ready: make(chan struct{})}
		m.schemas.entries[key] = entry
		m.schemas.mu.Unlock()
		// The load belongs to everyone waiting on it, so one caller going
		// away does not cancel it for the others; it keeps the caller's
		// deadline.
		loadCtx := context.WithoutCancel(ctx)
		cancel := context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			loadCtx, cancel = context.WithDeadline(loadCtx, deadline)
		}
		entry.schema, entry.err = loadSchema(m, loadCtx, connection)
		cancel()
		entry.loaded = time.Now()
		close(entry.ready)
		if entry.err != nil {
			m.schemas.mu.Lock()
			if m.schemas.entries[key] == entry {
				delete(m.schemas.entries, key)
			}
			m.schemas.mu.Unlock()
		}
		return entry.schema, entry.err
	}
	m.schemas.mu.Unlock()
	select {
	case <-entry.ready:
		return entry.schema, entry.err
	case <-ctx.Done():
		return Schema{}, ctx.Err()
	}
}

// InvalidateSchema forgets every cached schema of a saved connection, for
// all of its databases, so the next request reads the catalog again. Column
// lineage describes the same objects, so it goes with it.
func (m *Manager) InvalidateSchema(connectionID string) {
	if connectionID == "" {
		return
	}
	m.origins.forget(connectionID)
	prefix := connectionID + "|"
	m.schemas.mu.Lock()
	defer m.schemas.mu.Unlock()
	for key := range m.schemas.entries {
		if strings.HasPrefix(key, prefix) {
			delete(m.schemas.entries, key)
		}
	}
}
