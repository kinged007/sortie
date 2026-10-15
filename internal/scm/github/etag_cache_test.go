package github

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func etagCacheLen(t *testing.T, c *etagCache) int {
	t.Helper()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func TestETagCache_Put_UpdatesExisting(t *testing.T) {
	t.Parallel()

	c := newETagCache(10)
	c.put("/path/x", "etag-v1", []string{"v1"}, false)
	c.put("/path/x", "etag-v2", []string{"v2"}, true)

	etag, labels, closed, ok := c.lookup("/path/x")
	if !ok || etag != "etag-v2" || !slices.Equal(labels, []string{"v2"}) || !closed {
		t.Errorf("lookup = (%q, %v, %v, %v), want (%q, %v, true, true)", etag, labels, closed, ok, "etag-v2", []string{"v2"})
	}
	// Second put must not create a duplicate entry.
	if n := etagCacheLen(t, c); n != 1 {
		t.Errorf("cache len = %d, want 1 (no duplicate)", n)
	}
}

func TestETagCache_Eviction_RemovesLRU(t *testing.T) {
	t.Parallel()

	c := newETagCache(2)

	c.put("/path/a", "etag-a", nil, false)
	// Force /path/a to the oldest timestamp so it is definitely evicted.
	c.mu.Lock()
	ea := c.entries["/path/a"]
	ea.accessedAt = 1
	c.entries["/path/a"] = ea
	c.mu.Unlock()

	c.put("/path/b", "etag-b", nil, false) // newer
	c.put("/path/c", "etag-c", nil, false) // triggers eviction of /path/a (oldest)

	if _, _, _, ok := c.lookup("/path/a"); ok {
		t.Error("lookup(/path/a) = hit, want miss (LRU evicted)")
	}
	if _, _, _, ok := c.lookup("/path/b"); !ok {
		t.Error("lookup(/path/b) = miss, want hit")
	}
	if _, _, _, ok := c.lookup("/path/c"); !ok {
		t.Error("lookup(/path/c) = miss, want hit")
	}
}

func TestETagCache_Touch_UpdatesAccessTime(t *testing.T) {
	t.Parallel()

	c := newETagCache(2)

	c.put("/path/a", "etag-a", nil, false)
	// Backdate /path/a so it starts as the oldest entry.
	c.mu.Lock()
	ea := c.entries["/path/a"]
	ea.accessedAt = 1
	c.entries["/path/a"] = ea
	c.mu.Unlock()

	c.put("/path/b", "etag-b", nil, false)
	// Backdate /path/b to a slightly newer but still old epoch time.
	c.mu.Lock()
	eb := c.entries["/path/b"]
	eb.accessedAt = 2
	c.entries["/path/b"] = eb
	c.mu.Unlock()

	// Touch /path/a; its accessedAt is updated to time.Now() (much larger than 2).
	c.touch("/path/a")

	// Adding /path/c triggers eviction; /path/b has the smallest accessedAt (2).
	c.put("/path/c", "etag-c", nil, false)

	if _, _, _, ok := c.lookup("/path/a"); !ok {
		t.Error("lookup(/path/a) = miss, want hit (was touched, should survive)")
	}
	if _, _, _, ok := c.lookup("/path/b"); ok {
		t.Error("lookup(/path/b) = hit, want miss (LRU evicted after touch updated /path/a)")
	}
	if _, _, _, ok := c.lookup("/path/c"); !ok {
		t.Error("lookup(/path/c) = miss, want hit")
	}
}

func TestETagCache_MaxSizeZero_Disabled(t *testing.T) {
	t.Parallel()

	c := newETagCache(0)

	c.put("/path/a", "etag-a", nil, false)

	if n := etagCacheLen(t, c); n != 0 {
		t.Errorf("cache len = %d after put, want 0 (cache disabled)", n)
	}
	if _, _, _, ok := c.lookup("/path/a"); ok {
		t.Error("lookup = hit, want miss (cache disabled)")
	}

	// touch on a disabled cache must not panic.
	c.touch("/path/a")
}

func TestETagCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	const goroutines = 50
	c := newETagCache(10)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/path/%d", i%5) // shared paths to stress lock contention
			c.put(path, fmt.Sprintf("etag-%d", i), nil, false)
			c.lookup(path)
			c.touch(path)
		}(i)
	}
	wg.Wait()
}
