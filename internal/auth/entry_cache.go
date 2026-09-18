package auth

import (
	"container/list"
	"sync"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/token"
)

// cacheable is what the auth caches hold: a token record or a membership
// set. The union keeps the cache concrete rather than open to any value.
type cacheable interface {
	*token.Token | []uuid.UUID
}

// entryCache is a bounded cache with a lifetime per entry. When it is full
// the entry untouched for longest goes first. It is safe for concurrent use.
type entryCache[V cacheable] struct {
	mu      sync.Mutex
	max     int
	order   *list.List
	entries map[string]*list.Element
}

type cacheEntry[V cacheable] struct {
	key     string
	value   V
	expires time.Time
}

func newEntryCache[V cacheable](limit int) *entryCache[V] {
	return &entryCache[V]{
		mu:      sync.Mutex{},
		max:     limit,
		order:   list.New(),
		entries: make(map[string]*list.Element, limit),
	}
}

// get returns the live entry for key and marks it recently used. An entry
// past its lifetime is removed and reported absent.
func (c *entryCache[V]) get(key string, now time.Time) (V, bool) {
	var none V
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return none, false
	}
	entry, ok := element.Value.(*cacheEntry[V])
	if !ok {
		return none, false
	}
	if !entry.expires.After(now) {
		c.order.Remove(element)
		delete(c.entries, key)
		return none, false
	}
	c.order.MoveToFront(element)
	return entry.value, true
}

// put stores value under key until expires, evicting the least recently
// used entry when the cache is full.
func (c *entryCache[V]) put(key string, value V, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		if entry, isEntry := element.Value.(*cacheEntry[V]); isEntry {
			entry.value = value
			entry.expires = expires
		}
		c.order.MoveToFront(element)
		return
	}
	for c.order.Len() >= c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		if entry, isEntry := oldest.Value.(*cacheEntry[V]); isEntry {
			delete(c.entries, entry.key)
		}
		c.order.Remove(oldest)
	}
	c.entries[key] = c.order.PushFront(&cacheEntry[V]{key: key, value: value, expires: expires})
}

// remove forgets key, if present.
func (c *entryCache[V]) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.order.Remove(element)
		delete(c.entries, key)
	}
}
