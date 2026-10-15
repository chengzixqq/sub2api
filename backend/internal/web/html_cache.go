//go:build embed

package web

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// HTMLCache manages the cached index.html with injected settings
type HTMLCache struct {
	mu              sync.RWMutex
	cachedHTML      []byte
	etag            string
	baseHTMLHash    string // Hash of the original index.html (immutable after build)
	settingsVersion uint64 // Incremented when settings change
}

// CachedHTML represents the cache state
type CachedHTML struct {
	Content []byte
	ETag    string
}

// NewHTMLCache creates a new HTML cache instance
func NewHTMLCache() *HTMLCache {
	return &HTMLCache{}
}

// SetBaseHTML initializes the cache with the base HTML template
func (c *HTMLCache) SetBaseHTML(baseHTML []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	hash := sha256.Sum256(baseHTML)
	c.baseHTMLHash = hex.EncodeToString(hash[:8]) // First 8 bytes for brevity
}

// Invalidate marks the cache as stale
func (c *HTMLCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.settingsVersion++
	c.cachedHTML = nil
	c.etag = ""
}

// Get returns the cached HTML or nil if cache is stale
func (c *HTMLCache) Get() *CachedHTML {
	cached, _ := c.snapshot()
	return cached
}

// snapshot binds a cache read to the settings generation that produced it.
func (c *HTMLCache) snapshot() (*CachedHTML, uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.cachedHTML == nil {
		return nil, c.settingsVersion
	}
	return &CachedHTML{
		Content: c.cachedHTML,
		ETag:    c.etag,
	}, c.settingsVersion
}

// publish rejects settings fetched before an invalidation. A slow old read must
// never replace newer settings or make a stale cache valid again.
func (c *HTMLCache) publish(html, settingsJSON []byte, version uint64) *CachedHTML {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settingsVersion != version {
		return nil
	}
	c.cachedHTML = html
	c.etag = c.generateETag(settingsJSON)
	return &CachedHTML{Content: c.cachedHTML, ETag: c.etag}
}

// Set updates the cache with new rendered HTML
func (c *HTMLCache) Set(html []byte, settingsJSON []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cachedHTML = html
	c.etag = c.generateETag(settingsJSON)
}

// generateETag creates an ETag from base HTML hash + settings hash
func (c *HTMLCache) generateETag(settingsJSON []byte) string {
	settingsHash := sha256.Sum256(settingsJSON)
	return `"` + c.baseHTMLHash + "-" + hex.EncodeToString(settingsHash[:8]) + `"`
}
