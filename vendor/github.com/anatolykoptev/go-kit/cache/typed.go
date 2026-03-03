package cache

import (
	"context"
	"encoding/json"
)

// SetJSON marshals val as JSON and stores it in the cache.
func SetJSON[T any](c *Cache, ctx context.Context, key string, val T) error {
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	c.Set(ctx, key, data)
	return nil
}

// GetJSON retrieves a value from the cache and unmarshals it from JSON.
// Returns the zero value, false, nil on cache miss.
func GetJSON[T any](c *Cache, ctx context.Context, key string) (T, bool, error) {
	var zero T
	data, ok := c.Get(ctx, key)
	if !ok {
		return zero, false, nil
	}
	var val T
	if err := json.Unmarshal(data, &val); err != nil {
		return zero, false, err
	}
	return val, true, nil
}

// GetOrLoadJSON retrieves a typed value, calling loader on cache miss.
// The loaded value is marshaled to JSON and stored in the cache.
func GetOrLoadJSON[T any](c *Cache, ctx context.Context, key string,
	loader func(context.Context) (T, error),
) (T, error) {
	var zero T
	data, err := c.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		val, err := loader(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(val)
	})
	if err != nil {
		return zero, err
	}
	var val T
	if err := json.Unmarshal(data, &val); err != nil {
		return zero, err
	}
	return val, nil
}
