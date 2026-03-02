package pool

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"
)

// Pool manages a set of items with round-robin scheduling and health-based deactivation.
type Pool[T Identity] struct {
	items  []T
	mu     sync.Mutex
	idx    int
	config Config
}

// New creates a pool from a slice of items.
func New[T Identity](items []T, cfg Config) *Pool[T] {
	return &Pool[T]{items: items, config: cfg}
}

// Next returns the next available item that passes the filter.
// The filter function lets consumers apply custom checks (e.g. rate limits).
// Pass nil for no additional filtering.
func (p *Pool[T]) Next(filter func(T) bool) (T, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var zero T
	n := len(p.items)
	if n == 0 {
		return zero, fmt.Errorf("pool is empty")
	}

	now := time.Now()

	for i := 0; i < n; i++ {
		idx := (p.idx + i) % n
		item := p.items[idx]

		// Auto-reactivate: if cooldown has expired, restore the item
		if !item.IsActive() && !item.ReactivateAt().IsZero() && now.After(item.ReactivateAt()) {
			item.SetActive(true)
			item.SetReactivateAt(time.Time{})
			slog.Info("pool item auto-reactivated", slog.String("id", item.ID()))
		}

		if !item.IsActive() {
			continue
		}

		if filter != nil && !filter(item) {
			continue
		}

		p.idx = (idx + 1) % n
		return item, nil
	}

	return zero, fmt.Errorf("all %d items are unavailable", n)
}

// NextWithWait tries to get an item, and if all are unavailable, waits up to maxWait.
func (p *Pool[T]) NextWithWait(ctx context.Context, filter func(T) bool, maxWait time.Duration) (T, error) {
	item, err := p.Next(filter)
	if err == nil {
		return item, nil
	}

	wait := p.AvailableIn()
	if wait <= 0 || wait > maxWait {
		var zero T
		return zero, err
	}

	slog.Debug("pool exhausted, waiting", slog.Duration("wait", wait))
	select {
	case <-time.After(wait):
		return p.Next(filter)
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// DeactivateItem permanently removes an item from the active pool.
func (p *Pool[T]) DeactivateItem(item T) {
	p.mu.Lock()
	item.SetActive(false)
	item.SetReactivateAt(time.Time{})
	p.mu.Unlock()
	if p.config.AlertHook != nil {
		p.config.AlertHook("alerts.item_deactivated", map[string]string{
			"id":     item.ID(),
			"reason": "permanent",
		})
	}
}

// SoftDeactivate temporarily disables an item with automatic reactivation.
func (p *Pool[T]) SoftDeactivate(item T, cooldown time.Duration) {
	p.mu.Lock()
	item.SetActive(false)
	item.SetReactivateAt(time.Now().Add(cooldown))
	p.mu.Unlock()
	slog.Warn("pool item soft-deactivated",
		slog.String("id", item.ID()),
		slog.Duration("cooldown", cooldown))
	if p.config.AlertHook != nil {
		p.config.AlertHook("alerts.item_deactivated", map[string]string{
			"id":     item.ID(),
			"reason": fmt.Sprintf("soft-deactivated, cooldown %s", cooldown),
		})
	}
}

// Add appends an item to the pool.
func (p *Pool[T]) Add(item T) {
	p.mu.Lock()
	p.items = append(p.items, item)
	p.mu.Unlock()
}

// Size returns the number of items in the pool.
func (p *Pool[T]) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

// Healthy returns the number of active items that pass the filter.
func (p *Pool[T]) Healthy(filter func(T) bool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, item := range p.items {
		if item.IsActive() && (filter == nil || filter(item)) {
			n++
		}
	}
	return n
}

// Items returns a snapshot of all items in the pool.
func (p *Pool[T]) Items() []T {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]T, len(p.items))
	copy(out, p.items)
	return out
}

// AvailableIn returns how long until at least one item might become available.
// Returns 0 if an item is available now.
func (p *Pool[T]) AvailableIn() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	var earliest time.Time

	for _, item := range p.items {
		if !item.IsActive() {
			ra := item.ReactivateAt()
			if !ra.IsZero() && ra.After(now) {
				if earliest.IsZero() || ra.Before(earliest) {
					earliest = ra
				}
			}
			continue
		}
		return 0 // at least one active item
	}

	if earliest.IsZero() {
		return 0
	}
	wait := time.Until(earliest)
	if wait <= 0 {
		return 0
	}
	return wait
}

// backoffDuration computes exponential backoff duration for the given attempt.
func backoffDuration(cfg BackoffConfig, attempt int) time.Duration {
	base := float64(cfg.InitialWait) * math.Pow(cfg.Multiplier, float64(attempt))
	if base > float64(cfg.MaxWait) {
		base = float64(cfg.MaxWait)
	}
	jitter := base * cfg.JitterPct * (2*rand.Float64() - 1)
	return max(time.Duration(base+jitter), 0)
}
