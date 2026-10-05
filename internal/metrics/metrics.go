// Package metrics keeps in-process counters and exposes them in the
// Prometheus text format.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu       sync.Mutex
	adapter  map[[2]string]int64 // (adapter, outcome) -> count
	counters map[string]int64    // worker_<outcome> -> count
}

func New() *Registry {
	return &Registry{adapter: map[[2]string]int64{}, counters: map[string]int64{}}
}

// Count implements adapter.Metrics.
func (r *Registry) Count(adapter, outcome string, n int) {
	r.mu.Lock()
	r.adapter[[2]string{adapter, outcome}] += int64(n)
	r.mu.Unlock()
}

// Add increments a named counter, e.g. Add("worker_stored", 3).
func (r *Registry) Add(name string, n int) {
	if n == 0 {
		return
	}
	r.mu.Lock()
	r.counters[name] += int64(n)
	r.mu.Unlock()
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	b.WriteString("# TYPE watchtower_adapter_requests_total counter\n")
	keys := make([][2]string, 0, len(r.adapter))
	for k := range r.adapter {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1] })
	for _, k := range keys {
		fmt.Fprintf(&b, "watchtower_adapter_requests_total{adapter=%q,outcome=%q} %d\n", k[0], k[1], r.adapter[k])
	}
	names := make([]string, 0, len(r.counters))
	for n := range r.counters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "# TYPE watchtower_%s_total counter\nwatchtower_%s_total %d\n", n, n, r.counters[n])
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(b.String()))
}
