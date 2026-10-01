package service

import "sync/atomic"

const accountMappingCacheSlots = 512

// Account snapshots are shared by requests and copied by value. Keep lazy cache
// writes outside Account so both concurrent reads and snapshot copies stay safe.
// Each slot publishes a complete, immutable entry; collisions only cost a miss.
// The fixed capacity avoids retaining every account/configuration ever observed.
type accountMappingCache struct {
	slots [accountMappingCacheSlots]atomic.Pointer[accountMappingCacheEntry]
}

type accountMappingCacheKey struct {
	rawPtr         uintptr
	rawLen         int
	rawSignature   uint64
	platform       string
	googleOne      bool
	runtimeVersion uint64
}

type accountMappingCacheEntry struct {
	key accountMappingCacheKey
	// Keep the source map alive while its pointer identifies this entry, so a
	// recycled map address can never hit an unrelated cached configuration.
	source  map[string]any
	mapping map[string]string
}

var modelMappingSnapshots accountMappingCache
var headerOverrideSnapshots accountMappingCache

func (key accountMappingCacheKey) slotIndex() uint64 {
	hash := uint64(key.rawPtr>>4) ^ key.rawSignature ^ key.runtimeVersion
	for i := range len(key.platform) {
		hash = hash*1099511628211 ^ uint64(key.platform[i])
	}
	if key.googleOne {
		hash ^= 0x9e3779b97f4a7c15
	}
	return hash % accountMappingCacheSlots
}

func (cache *accountMappingCache) load(key accountMappingCacheKey) (map[string]string, bool) {
	entry := cache.slots[key.slotIndex()].Load()
	if entry == nil || entry.key != key {
		return nil, false
	}
	return entry.mapping, true
}

func (cache *accountMappingCache) store(key accountMappingCacheKey, source map[string]any, mapping map[string]string) {
	cache.slots[key.slotIndex()].Store(&accountMappingCacheEntry{
		key: key, source: source, mapping: mapping,
	})
}
