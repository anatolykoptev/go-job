package connectors

import (
	"context"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// Capability flags for a Source.
type Capability uint32

const (
	// NeedsAPIKey indicates the source requires an API key to operate.
	NeedsAPIKey Capability = 1 << iota
	// OptIn indicates the source is excluded from platform=all (e.g. UN scrapers).
	OptIn
	// SupportsPagination indicates the source supports offset-based pagination.
	SupportsPagination
)

// Source is the single interface a new job connector must implement.
type Source interface {
	Name() string
	Capabilities() Capability
	Groups() []string
	Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error)
	SiteScope() string
}

// Query holds all search parameters passed to a connector.
type Query struct {
	Query, Location, Experience, JobType, Remote, TimeRange, Salary, Language string
	Limit, Offset                                                              int
	EasyApply                                                                  bool
}

// groupAll is the sentinel group name matched by platform=all fan-out.
const groupAll = "all"

// Registry maps platform names to Sources, preserving insertion order.
type Registry struct {
	byName map[string]Source
	order  []Source
}

// New creates an empty Registry.
func New() *Registry {
	return &Registry{byName: make(map[string]Source)}
}

// Register adds a Source to the registry. Panics on duplicate name (init-time guard).
func (r *Registry) Register(s Source) {
	if _, exists := r.byName[s.Name()]; exists {
		panic("connectors: duplicate source name: " + s.Name())
	}
	r.byName[s.Name()] = s
	r.order = append(r.order, s)
}

// Known reports whether platform is a recognized connector name or meta-group.
func (r *Registry) Known(platform string) bool {
	if platform == groupAll {
		return true
	}
	if _, ok := r.byName[platform]; ok {
		return true
	}
	for _, s := range r.order {
		for _, g := range s.Groups() {
			if g == platform {
				return true
			}
		}
	}
	return false
}

// Select returns the ordered list of Sources to fan out to for the given platform.
func (r *Registry) Select(platform string) []Source {
	var out []Source
	for _, s := range r.order {
		if sourceMatches(s, platform) {
			out = append(out, s)
		}
	}
	return out
}

// All returns all registered sources in insertion order.
func (r *Registry) All() []Source { return r.order }

func sourceMatches(s Source, platform string) bool {
	if platform == groupAll {
		return s.Capabilities()&OptIn == 0
	}
	if s.Name() == platform {
		return true
	}
	for _, g := range s.Groups() {
		if g == platform {
			return true
		}
	}
	return false
}
