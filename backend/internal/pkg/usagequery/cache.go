package usagequery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

type CacheEntry struct {
	ETag      string
	Payload   any
	ExpiresAt time.Time
}

type cacheCall struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	entry   CacheEntry
	err     error
	refresh bool
}

// Cache bounds retained results and outstanding distinct queries. Waiters share work, not cancellation.
type Cache struct {
	mu     sync.Mutex
	ttl    time.Duration
	items  map[string]CacheEntry
	calls  map[string]*cacheCall
	active int
}

func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Cache{ttl: ttl, items: map[string]CacheEntry{}, calls: map[string]*cacheCall{}}
}

func CacheETag(payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (c *Cache) Get(key string) (CacheEntry, bool) {
	if c == nil || key == "" {
		return CacheEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if ok && !time.Now().Before(entry.ExpiresAt) {
		delete(c.items, key)
		return CacheEntry{}, false
	}
	return entry, ok
}

func (c *Cache) Set(key string, payload any) CacheEntry {
	if c == nil {
		return CacheEntry{}
	}
	entry := CacheEntry{ETag: CacheETag(payload), Payload: payload, ExpiresAt: time.Now().Add(c.ttl)}
	if key == "" {
		return entry
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeLocked(key, entry)
	return entry
}

func (c *Cache) storeLocked(key string, entry CacheEntry) {
	if len(c.items) >= 1024 {
		oldestKey := ""
		var oldest time.Time
		for k, v := range c.items {
			if oldestKey == "" || v.ExpiresAt.Before(oldest) {
				oldestKey, oldest = k, v.ExpiresAt
			}
		}
		delete(c.items, oldestKey)
	}
	c.items[key] = entry
}

func (c *Cache) GetOrLoad(ctx context.Context, key string, bypass bool, load func(context.Context) (any, error)) (CacheEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return CacheEntry{}, false, err
	}
	if load == nil {
		return CacheEntry{}, false, nil
	}
	if !bypass {
		if entry, ok := c.Get(key); ok {
			return entry, true, nil
		}
	}
	if c == nil || key == "" {
		work, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		payload, err := load(work)
		if err != nil {
			return CacheEntry{}, false, err
		}
		return CacheEntry{ETag: CacheETag(payload), Payload: payload, ExpiresAt: time.Now()}, false, nil
	}
	c.mu.Lock()
	if entry, ok := c.items[key]; !bypass && ok && time.Now().Before(entry.ExpiresAt) {
		c.mu.Unlock()
		return entry, true, nil
	}
	call, exists := c.calls[key]
	if bypass {
		delete(c.items, key)
		// A refresh supersedes ordinary work, while concurrent refresh waiters
		// share the same fresh generation. Old waiters may finish but cannot publish.
		if exists && !call.refresh {
			delete(c.calls, key)
			exists = false
		}
	}
	if !exists {
		if c.active >= 128 {
			c.mu.Unlock()
			return CacheEntry{}, false, errors.New("statistics query capacity reached")
		}
		// Retain identity/scope values, but only the last waiter cancels shared work.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		call = &cacheCall{done: make(chan struct{}), cancel: cancel, refresh: bypass}
		c.calls[key] = call
		c.active++
		go func() {
			payload, err := load(work)
			if err == nil {
				err = work.Err()
			}
			var entry CacheEntry
			if err == nil {
				entry = CacheEntry{ETag: CacheETag(payload), Payload: payload, ExpiresAt: time.Now().Add(c.ttl)}
			}
			c.mu.Lock()
			c.active--
			call.entry, call.err = entry, err
			if c.calls[key] == call {
				if err == nil {
					c.storeLocked(key, entry)
				}
				delete(c.calls, key)
			}
			close(call.done)
			c.mu.Unlock()
			cancel()
		}()
	}
	call.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		call.waiters--
		if call.waiters == 0 {
			if c.calls[key] == call {
				delete(c.calls, key)
			}
			call.cancel()
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return CacheEntry{}, false, ctx.Err()
	case <-call.done:
		return call.entry, false, call.err
	}
}
