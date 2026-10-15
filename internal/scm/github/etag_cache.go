package github

import (
	"sync"
	"sync/atomic"
)

// etagEntry holds the cached ETag value and the label names and closed flag
// of a single GitHub issue endpoint response. It keeps no derived state, so a
// 304 is derived under the state vocabulary in force when it arrives.
type etagEntry struct {
	etag       string   // ETag header value (opaque string from GitHub)
	labels     []string // label names from the cached response
	closed     bool     // whether the cached response was a closed issue
	accessedAt uint64   // monotonic counter value at last access (for LRU eviction)
}

// etagCache provides bounded, concurrency-safe ETag caching for
// GitHub API conditional requests. Safe for concurrent use.
type etagCache struct {
	mu      sync.RWMutex
	entries map[string]etagEntry // keyed by request path
	maxSize int                  // maximum number of entries; 0 disables caching
	clock   atomic.Uint64        // monotonic counter for LRU ordering
}

// newETagCache creates a cache with the given maximum entry count.
// A maxSize of 0 disables caching (lookup always misses, put is a no-op).
func newETagCache(maxSize int) *etagCache {
	return &etagCache{
		entries: make(map[string]etagEntry),
		maxSize: maxSize,
	}
}

// lookup returns the cached ETag, label names, and closed flag for the
// given path. Returns ok false on cache miss.
func (c *etagCache) lookup(path string) (etag string, labels []string, closed bool, ok bool) {
	if c.maxSize == 0 {
		return "", nil, false, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[path]
	if !ok {
		return "", nil, false, false
	}
	return e.etag, e.labels, e.closed, true
}

// put inserts or updates a cache entry. If the cache is at capacity,
// the least-recently-accessed entry is evicted first.
func (c *etagCache) put(path, etag string, labels []string, closed bool) {
	if c.maxSize == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Add(1)

	if _, exists := c.entries[path]; exists {
		c.entries[path] = etagEntry{etag: etag, labels: labels, closed: closed, accessedAt: now}
		return
	}

	// Evict the least-recently-accessed entry when at capacity.
	if len(c.entries) >= c.maxSize {
		var oldestKey string
		oldestSeq := ^uint64(0)
		for k, e := range c.entries {
			if e.accessedAt < oldestSeq {
				oldestSeq = e.accessedAt
				oldestKey = k
			}
		}
		delete(c.entries, oldestKey)
	}

	c.entries[path] = etagEntry{etag: etag, labels: labels, closed: closed, accessedAt: now}
}

// touch updates the accessedAt timestamp for an existing entry,
// called on 304 hits to refresh LRU ordering. No-op if the entry
// does not exist.
func (c *etagCache) touch(path string) {
	if c.maxSize == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[path]; ok {
		e.accessedAt = c.clock.Add(1)
		c.entries[path] = e
	}
}
