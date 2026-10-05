package symbolicate

import (
	"container/list"
	"sync"

	"github.com/go-sourcemap/sourcemap"
)

// cacheBudget bounds the parsed maps kept in memory. A parsed map takes
// about as much memory as its JSON (measured on a 3.7 MB Vite map), so the
// budget is in source-map bytes.
const cacheBudget = 64 << 20

// mapCache keeps recently used parsed source maps within a memory budget,
// evicting the least recently used.
type mapCache struct {
	mu     sync.Mutex
	budget int
	used   int
	order  *list.List // most recently used first; values are *cached
	byID   map[int64]*list.Element
}

type cached struct {
	id   int64
	c    *sourcemap.Consumer
	size int
}

func newMapCache(budget int) *mapCache {
	return &mapCache{budget: budget, order: list.New(), byID: map[int64]*list.Element{}}
}

func (m *mapCache) get(id int64) (*sourcemap.Consumer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.byID[id]
	if !ok {
		return nil, false
	}
	m.order.MoveToFront(el)
	return el.Value.(*cached).c, true
}

// put caches a map of size bytes. A map larger than the whole budget is
// not cached: it is used for the event at hand and then released.
func (m *mapCache) put(id int64, c *sourcemap.Consumer, size int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if size > m.budget {
		return
	}
	if el, ok := m.byID[id]; ok {
		m.order.MoveToFront(el)
		return
	}
	for m.used+size > m.budget {
		oldest := m.order.Back()
		e := oldest.Value.(*cached)
		m.order.Remove(oldest)
		delete(m.byID, e.id)
		m.used -= e.size
	}
	m.byID[id] = m.order.PushFront(&cached{id: id, c: c, size: size})
	m.used += size
}
