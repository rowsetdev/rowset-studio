package engine

import (
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

// originCacheLimit bounds how many statements keep a resolved lineage. The
// entries are a handful of names each, and a person re-runs the same handful
// of statements, so a small cache already spares most of the round trips.
const originCacheLimit = 512

// originCache remembers which table and column each column of a result came
// from, keyed by pool and statement text. It is dropped with the schema it
// describes, so an ALTER or a DROP cannot leave a stale lineage behind.
type originCache struct {
	mu      sync.Mutex
	entries map[string][]domain.ColumnOrigin
	order   []string
	next    int
}

// get returns a copy: the caller hands the slice to a row stream, and result
// hooks are free to rewrite what they are given.
func (c *originCache) get(key string) ([]domain.ColumnOrigin, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	origins, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return append([]domain.ColumnOrigin(nil), origins...), true
}

func (c *originCache) put(key string, origins []domain.ColumnOrigin) {
	stored := append([]domain.ColumnOrigin(nil), origins...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string][]domain.ColumnOrigin, originCacheLimit)
	}
	if _, exists := c.entries[key]; exists {
		c.entries[key] = stored
		return
	}
	if len(c.order) < originCacheLimit {
		c.order = append(c.order, key)
	} else {
		delete(c.entries, c.order[c.next])
		c.order[c.next] = key
		c.next = (c.next + 1) % originCacheLimit
	}
	c.entries[key] = stored
}

// forget drops every entry of a saved connection, whatever its database,
// user or pool settings.
func (c *originCache) forget(connectionID string) {
	prefix := connectionID + "|"
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
}

// tunnel returns the SSH tunnel a connection's pool was opened through, so a
// metadata connection of its own reaches the database the same way.
func (m *Manager) tunnel(connection Connection) *ssh.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tunnels[poolKey(connection)]
}
