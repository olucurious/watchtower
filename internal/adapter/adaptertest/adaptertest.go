// Package adaptertest provides in-memory adapter dependencies for tests.
package adaptertest

import (
	"context"
	"sync"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/event"
)

// Keys resolves credentials from a fixed table keyed by adapter+"/"+key.
type Keys map[string]adapter.Project

func (k Keys) ResolveKey(_ context.Context, adapterName, key string) (adapter.Project, error) {
	p, ok := k[adapterName+"/"+key]
	if !ok {
		return adapter.Project{}, adapter.ErrUnknownKey
	}
	return p, nil
}

// Sink records accepted events; set Err to make Accept fail.
type Sink struct {
	mu     sync.Mutex
	Events []event.Event
	Err    error
}

func (s *Sink) Accept(_ context.Context, events []event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	s.Events = append(s.Events, events...)
	return nil
}

// Metrics records counts by adapter/outcome.
type Metrics struct {
	mu sync.Mutex
	m  map[string]int
}

func (m *Metrics) Count(adapterName, outcome string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]int{}
	}
	m.m[adapterName+"/"+outcome] += n
}

func (m *Metrics) Get(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.m[key]
}

// Limits are generous defaults for tests.
var Limits = adapter.Limits{MaxBodyBytes: 1 << 20, MaxDecompressedBytes: 4 << 20, MaxEventBytes: 256 << 10}
