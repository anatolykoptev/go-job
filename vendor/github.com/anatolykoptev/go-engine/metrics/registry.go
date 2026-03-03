// Package metrics provides atomic counter registry for service telemetry.
//
// Counters are registered by name and incremented atomically.
// [Registry.Format] returns a text representation for /metrics endpoints.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry is a thread-safe collection of named atomic counters.
// Zero value is ready to use.
type Registry struct {
	counters sync.Map // string -> *atomic.Int64
}

// New creates a new empty Registry.
func New() *Registry {
	return &Registry{}
}

// counter returns or creates an *atomic.Int64 for the given name.
func (r *Registry) counter(name string) *atomic.Int64 {
	if v, ok := r.counters.Load(name); ok {
		return v.(*atomic.Int64)
	}
	v, _ := r.counters.LoadOrStore(name, &atomic.Int64{})
	return v.(*atomic.Int64)
}

// Incr increments the named counter by 1.
func (r *Registry) Incr(name string) {
	r.counter(name).Add(1)
}

// Add adds delta to the named counter.
func (r *Registry) Add(name string, delta int64) {
	r.counter(name).Add(delta)
}

// Get returns the current value of the named counter.
// Returns 0 for unknown counters.
func (r *Registry) Get(name string) int64 {
	if v, ok := r.counters.Load(name); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// Snapshot returns a copy of all counter values.
func (r *Registry) Snapshot() map[string]int64 {
	m := make(map[string]int64)
	r.counters.Range(func(key, value any) bool {
		m[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	return m
}

// Format returns all counters as sorted "name value\n" text.
func (r *Registry) Format() string {
	snap := r.Snapshot()
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s %d\n", k, snap[k])
	}
	return sb.String()
}
