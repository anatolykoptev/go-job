package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker"
)

// Sentinel errors for L2 cache operations.
var (
	// ErrCacheMiss indicates the requested key was not found in the cache.
	ErrCacheMiss = errors.New("cache: miss")

	// ErrL2Unavailable indicates the L2 store is not available (nil receiver or closed).
	ErrL2Unavailable = errors.New("cache: L2 unavailable")
)

// L2 is an optional second-tier cache (typically Redis).
// Get returns the value and nil error on hit, ErrCacheMiss on miss.
// Implementations must be safe for concurrent use.
type L2 interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, data []byte, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	Close() error
}

const (
	pingTimeout      = 3 * time.Second
	cbTimeout        = 30 * time.Second
	cbFailThreshold  = 3
)

// RedisL2 implements L2 using Redis.
type RedisL2 struct {
	rdb    *redis.Client
	prefix string
	cb     *gobreaker.TwoStepCircuitBreaker
}

// NewRedisL2 connects to Redis and returns an L2 store.
// Returns nil if the URL is empty or Redis is unreachable (logs a warning).
func NewRedisL2(redisURL string, db int, prefix string) *RedisL2 {
	if redisURL == "" {
		return nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		slog.Warn("cache: invalid redis URL, L2 disabled", slog.Any("error", err))
		return nil
	}
	opts.DB = db

	rdb := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Warn("cache: redis unreachable, L2 disabled", slog.Any("error", err))
		rdb.Close()
		return nil
	}

	cb := gobreaker.NewTwoStepCircuitBreaker(gobreaker.Settings{
		Name:    "cache-l2",
		Timeout: cbTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures > cbFailThreshold
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Warn("cache: circuit breaker state change",
				slog.String("name", name),
				slog.String("from", from.String()),
				slog.String("to", to.String()),
			)
		},
	})

	return &RedisL2{rdb: rdb, prefix: prefix, cb: cb}
}

func (r *RedisL2) key(k string) string {
	if r.prefix == "" {
		return k
	}
	return r.prefix + k
}

// Get retrieves a value from Redis by key.
// Returns ErrCacheMiss if the key does not exist or receiver is nil.
// Returns ErrL2Unavailable if the circuit breaker is open.
func (r *RedisL2) Get(ctx context.Context, key string) ([]byte, error) {
	if r == nil {
		return nil, ErrCacheMiss
	}
	done, err := r.cb.Allow()
	if err != nil {
		return nil, ErrL2Unavailable
	}
	data, err := r.rdb.Get(ctx, r.key(key)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			done(true) // miss is not a failure
			return nil, ErrCacheMiss
		}
		done(false)
		return nil, fmt.Errorf("cache: L2 get %q: %w", key, err)
	}
	done(true)
	return data, nil
}

// Set stores a value in Redis with the given TTL.
// Returns ErrL2Unavailable if receiver is nil or circuit breaker is open.
func (r *RedisL2) Set(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	if r == nil {
		return ErrL2Unavailable
	}
	done, err := r.cb.Allow()
	if err != nil {
		return ErrL2Unavailable
	}
	if err := r.rdb.Set(ctx, r.key(key), data, ttl).Err(); err != nil {
		done(false)
		return fmt.Errorf("cache: L2 set %q: %w", key, err)
	}
	done(true)
	return nil
}

// Del removes a key from Redis.
// Returns ErrL2Unavailable if receiver is nil or circuit breaker is open.
func (r *RedisL2) Del(ctx context.Context, key string) error {
	if r == nil {
		return ErrL2Unavailable
	}
	done, err := r.cb.Allow()
	if err != nil {
		return ErrL2Unavailable
	}
	if err := r.rdb.Del(ctx, r.key(key)).Err(); err != nil {
		done(false)
		return fmt.Errorf("cache: L2 del %q: %w", key, err)
	}
	done(true)
	return nil
}

// Close closes the underlying Redis client. No-op if receiver is nil.
func (r *RedisL2) Close() error {
	if r == nil {
		return nil
	}
	return r.rdb.Close()
}
