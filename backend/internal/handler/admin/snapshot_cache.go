package admin

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
)

type snapshotCacheEntry = usagequery.CacheEntry

type snapshotCache struct {
	cache *usagequery.Cache
	ttl   time.Duration
}

func newSnapshotCache(ttl time.Duration) *snapshotCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &snapshotCache{cache: usagequery.NewCache(ttl), ttl: ttl}
}

func (c *snapshotCache) Get(key string) (snapshotCacheEntry, bool) {
	if c == nil {
		return snapshotCacheEntry{}, false
	}
	return c.cache.Get(key)
}
func (c *snapshotCache) Set(key string, payload any) snapshotCacheEntry {
	if c == nil {
		return snapshotCacheEntry{}
	}
	return c.cache.Set(key, payload)
}
func (c *snapshotCache) GetOrLoad(key string, load func() (any, error)) (snapshotCacheEntry, bool, error) {
	if load == nil {
		return snapshotCacheEntry{}, false, nil
	}
	return c.GetOrLoadContext(context.Background(), key, func(context.Context) (any, error) { return load() })
}
func (c *snapshotCache) GetOrLoadContext(ctx context.Context, key string, load func(context.Context) (any, error)) (snapshotCacheEntry, bool, error) {
	var cache *usagequery.Cache
	if c != nil {
		cache = c.cache
	}
	return cache.GetOrLoad(ctx, key, usagequery.OptionsFrom(ctx).ForceRefresh, load)
}

func parseBoolQueryWithDefault(raw string, def bool) bool {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return def
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
