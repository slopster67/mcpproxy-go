package toolsig

import (
	"sync"
)

// Cache memoizes compiled Signatures keyed by the indexed per-tool hash
// (ToolMetadata.Hash — the SHA-256 covering description + schemas, so
// definition changes and index rebuilds naturally invalidate entries by
// changing the key). Process-local, never persisted; re-warms on restart via
// the normal reindex.
//
// Exactly ONE instance must exist per process: Runtime owns it, the indexing
// path warms it, and the MCP request path reads it. A read-mostly RWMutex
// suffices: this is a pure memoized derivation of an immutable input.
type Cache struct {
	mu sync.RWMutex
	m  map[string]Signature

	// live is the last reconciled live-hash set (nil until the first
	// RetainHashes). Once set, Get memoizes ONLY live hashes: a request that
	// captured a search result before an eviction can still render its stale
	// hash (compute-through), but can no longer re-populate the cache with
	// it — otherwise a Get racing RetainHashes would leak the evicted entry
	// until the next reconcile.
	live map[string]struct{}
}

// NewCache creates an empty signature cache.
func NewCache() *Cache {
	return &Cache{m: make(map[string]Signature)}
}

// Peek returns the Signature for hash if it is already cached, and reports
// whether it was. It never compiles and never memoizes.
//
// This is deliberately NOT Get with different arguments. Get compiles on a
// miss, which the deferred direct listing cannot use for two reasons: it would
// put per-request compilation on the tools/list path, and — worse — it would
// make the miss invisible, since a caller that
// always receives a Signature cannot tell "warmed at index time" from "compiled
// just now for you". The deferred renderer needs to SEE the miss so it can list
// the entry without a signature suffix rather than dropping or delaying it.
func (c *Cache) Peek(hash string) (Signature, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sig, ok := c.m[hash]
	return sig, ok
}

// Get returns the Signature for hash, compiling (and memoizing) it from
// paramsJSON/description on a miss. Concurrent callers may compile
// redundantly under contention, but exactly one result is stored and counted.
func (c *Cache) Get(hash, paramsJSON, description string) Signature {
	c.mu.RLock()
	sig, ok := c.m[hash]
	c.mu.RUnlock()
	if ok {
		return sig
	}

	// Compile outside the lock — rendering is pure and deterministic, so a
	// lost race stores an identical value anyway.
	compiled, _ := Render(paramsJSON, description)

	c.mu.Lock()
	defer c.mu.Unlock()
	if sig, ok := c.m[hash]; ok {
		return sig
	}
	if c.live != nil {
		if _, ok := c.live[hash]; !ok {
			// Stale hash (evicted by a reconcile that raced this request):
			// serve the computed value without storing it.
			return compiled
		}
	}
	c.m[hash] = compiled

	return compiled
}

// Warm pre-compiles the Signature for hash so later Gets are pure cache hits.
// Called from the indexing path (signatures are compiled at index time) — the
// authoritative source of fresh hashes, so unlike Get it always memoizes and
// admits the hash to the live set (indexing may warm a new hash before the
// post-update reconcile runs; without this, that warm would be dropped by the
// Get-side stale gate).
func (c *Cache) Warm(hash, paramsJSON, description string) {
	c.mu.RLock()
	_, ok := c.m[hash]
	c.mu.RUnlock()
	if ok {
		return
	}

	compiled, _ := Render(paramsJSON, description)

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[hash]; ok {
		return
	}
	c.m[hash] = compiled

	if c.live != nil {
		c.live[hash] = struct{}{}
	}
}

// RetainHashes reconciles the cache to the live tool set: every entry whose
// hash is NOT in live is evicted, and the number of evictions is returned.
// The indexing path calls this after index rebuilds/differential updates so
// stale hashes (removed or redefined tools) do not accumulate for the life of
// the process — Warm only ever adds. A nil/empty live set clears the cache.
// Safe for concurrent use with Get/Warm: eviction holds the write lock, and a
// racing Get for an evicted hash simply recompiles (rendering is pure).
func (c *Cache) RetainHashes(live map[string]struct{}) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Snapshot the live set so post-reconcile Gets cannot re-memoize evicted
	// hashes (they compute-through instead — see Get).
	c.live = make(map[string]struct{}, len(live))
	for h := range live {
		c.live[h] = struct{}{}
	}
	evicted := 0
	for hash := range c.m {
		if _, ok := live[hash]; !ok {
			delete(c.m, hash)
			evicted++
		}
	}
	return evicted
}

// Len reports the number of cached signatures (bounded by live tool count).
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}
