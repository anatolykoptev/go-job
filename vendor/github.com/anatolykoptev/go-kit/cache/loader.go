package cache

import (
	"context"
	"time"
)

// GetOrLoad returns the value for key, loading it via loader on cache miss.
// Concurrent loads for the same key are deduplicated (singleflight).
// The loaded value is stored in L1.
func (c *Cache) GetOrLoad(ctx context.Context, key string, loader func(context.Context) ([]byte, error)) ([]byte, error) {
	if data, ok := c.Get(ctx, key); ok {
		return data, nil
	}

	data, err := c.flight.do(key, func() ([]byte, error) {
		return loader(ctx)
	})
	if err != nil {
		return nil, err
	}

	c.Set(ctx, key, data)
	return data, nil
}

// GetOrLoadWithTTL is like GetOrLoad but stores the loaded value with a custom TTL.
func (c *Cache) GetOrLoadWithTTL(ctx context.Context, key string, ttl time.Duration,
	loader func(context.Context) ([]byte, error),
) ([]byte, error) {
	if data, ok := c.Get(ctx, key); ok {
		return data, nil
	}

	data, err := c.flight.do(key, func() ([]byte, error) {
		return loader(ctx)
	})
	if err != nil {
		return nil, err
	}

	c.SetWithTTL(ctx, key, data, ttl)
	return data, nil
}
