package cache

import "sync"

// group deduplicates concurrent loads for the same key.
type group struct {
	mu    sync.Mutex
	calls map[string]*groupCall
}

type groupCall struct {
	wg  sync.WaitGroup
	val []byte
	err error
}

func (g *group) do(key string, fn func() ([]byte, error)) ([]byte, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*groupCall)
	}
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &groupCall{}
	c.wg.Add(1)
	g.calls[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()

	return c.val, c.err
}
