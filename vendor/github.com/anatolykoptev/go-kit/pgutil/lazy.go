package pgutil

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anatolykoptev/go-kit/retry"
)

// ticker abstracts time.Ticker for testability.
type ticker struct {
	C    <-chan time.Time
	stop func()
}

// Lazy manages a lazily-connected resource with background reconnection.
// It starts a background goroutine that connects with retry and monitors
// health, allowing the service to start immediately without blocking on
// database availability.
//
// Use Get to obtain the current value — it returns the zero value if not
// yet connected. Use Ready to check connection state.
type Lazy[T any] struct {
	mu        sync.RWMutex
	value     T
	ready     bool
	connectFn func(ctx context.Context) (T, error)
	closeFn   func(T)
	healthFn  func(context.Context, T) error
	opts      Options
	cancel    context.CancelFunc
	done      chan struct{}
	tickerFn  func(d time.Duration) ticker // override for tests
}

// NewLazy creates a Lazy connector. Call Start to begin the background
// connect/health loop.
//
//   - connectFn: creates and pings the resource; should clean up on error.
//   - closeFn: closes the resource (called on reconnect and Close).
//   - healthFn: checks resource health; returning error triggers reconnect.
func NewLazy[T any](
	opts Options,
	connectFn func(ctx context.Context) (T, error),
	closeFn func(T),
	healthFn func(context.Context, T) error,
) *Lazy[T] {
	opts.applyDefaults()
	return &Lazy[T]{
		connectFn: connectFn,
		closeFn:   closeFn,
		healthFn:  healthFn,
		opts:      opts,
		done:      make(chan struct{}),
	}
}

// Start begins the background connect and health-check loop.
// Returns immediately. The loop runs until ctx is cancelled or Close is called.
func (l *Lazy[T]) Start(ctx context.Context) {
	ctx, l.cancel = context.WithCancel(ctx)
	go l.loop(ctx)
}

// Get returns the current value. Returns the zero value of T if not connected.
func (l *Lazy[T]) Get() T {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.value
}

// Ready reports whether a healthy connection is available.
func (l *Lazy[T]) Ready() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ready
}

// Close stops the background loop and closes the current connection.
// Blocks until the background goroutine exits.
func (l *Lazy[T]) Close() {
	if l.cancel != nil {
		l.cancel()
	}
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ready {
		l.closeFn(l.value)
		l.ready = false
	}
}

func (l *Lazy[T]) loop(ctx context.Context) {
	defer close(l.done)
	logger := l.opts.Logger

	for {
		if err := ctx.Err(); err != nil {
			return
		}

		val, err := l.connect(ctx)
		if err != nil {
			// Context cancelled during connect — exit.
			return
		}

		l.set(val)
		logger.Info(l.opts.Name + " connected")

		if l.healthLoop(ctx, val) {
			// Context cancelled — exit.
			return
		}

		// Health failed — close current value and reconnect.
		logger.Warn(l.opts.Name + " health check failed, reconnecting")
		l.mu.Lock()
		l.closeFn(l.value)
		l.ready = false
		l.mu.Unlock()
	}
}

// connect retries in cycles until successful or ctx is cancelled.
// Each cycle tries MaxAttempts times; cycles repeat indefinitely.
func (l *Lazy[T]) connect(ctx context.Context) (T, error) {
	for {
		val, err := retry.Do(ctx, retry.Options{
			MaxAttempts:  l.opts.MaxAttempts,
			InitialDelay: l.opts.InitDelay,
			MaxDelay:     l.opts.MaxDelay,
			Jitter:       true,
			OnRetry: func(attempt int, err error) {
				l.opts.Logger.Warn(l.opts.Name+" not ready, retrying",
					slog.Int("attempt", attempt),
					slog.Any("error", err))
			},
		}, func() (T, error) {
			return l.connectFn(ctx)
		})
		if err == nil {
			return val, nil
		}
		if ctx.Err() != nil {
			return val, err
		}
		l.opts.Logger.Warn(l.opts.Name+" connect cycle exhausted, restarting",
			slog.Any("error", err))
	}
}

func (l *Lazy[T]) newTicker(d time.Duration) ticker {
	if l.tickerFn != nil {
		return l.tickerFn(d)
	}
	t := time.NewTicker(d)
	return ticker{C: t.C, stop: t.Stop}
}

// healthLoop checks health at HealthInterval. Returns true if ctx was cancelled.
func (l *Lazy[T]) healthLoop(ctx context.Context, val T) bool {
	t := l.newTicker(l.opts.HealthInterval)
	defer t.stop()

	for {
		select {
		case <-ctx.Done():
			return true
		case <-t.C:
			if err := l.healthFn(ctx, val); err != nil {
				if ctx.Err() != nil {
					return true
				}
				return false
			}
		}
	}
}

func (l *Lazy[T]) set(val T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.value = val
	l.ready = true
}
