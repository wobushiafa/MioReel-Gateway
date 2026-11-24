package cache

import (
	"container/list"
	"sync"
	"time"
)

type cacheEntry[V any] struct {
	key       string
	value     V
	expiresAt time.Time
	elem      *list.Element
}

// TTLCache is a thread-safe bounded LRU + TTL cache.
type TTLCache[V any] struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*cacheEntry[V]
	order    *list.List // front = most recently used
	now      func() time.Time
}

// NewTTLCache creates a bounded LRU+TTL cache. If capacity <= 0, capacity is unlimited.
func NewTTLCache[V any](capacity int) *TTLCache[V] {
	return &TTLCache[V]{
		capacity: capacity,
		items:    make(map[string]*cacheEntry[V]),
		order:    list.New(),
		now:      time.Now,
	}
}

func (c *TTLCache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ent, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	if !ent.expiresAt.IsZero() && !c.now().Before(ent.expiresAt) {
		c.removeEntry(ent)
		var zero V
		return zero, false
	}
	c.order.MoveToFront(ent.elem)
	return ent.value, true
}

func (c *TTLCache[V]) Set(key string, value V, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	exp := c.now().Add(ttl)
	if ent, ok := c.items[key]; ok {
		ent.value = value
		ent.expiresAt = exp
		c.order.MoveToFront(ent.elem)
		return
	}

	ent := &cacheEntry[V]{key: key, value: value, expiresAt: exp}
	ent.elem = c.order.PushFront(ent)
	c.items[key] = ent

	if c.capacity > 0 {
		for len(c.items) > c.capacity {
			back := c.order.Back()
			if back == nil {
				break
			}
			c.removeEntry(back.Value.(*cacheEntry[V]))
		}
	}
}

func (c *TTLCache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ent, ok := c.items[key]; ok {
		c.removeEntry(ent)
	}
}

func (c *TTLCache[V]) removeEntry(ent *cacheEntry[V]) {
	c.order.Remove(ent.elem)
	delete(c.items, ent.key)
}
