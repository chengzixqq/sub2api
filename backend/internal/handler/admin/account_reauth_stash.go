package admin

import (
	"maps"
	"strconv"
	"sync"
	"time"
)

const (
	reauthStashTTL        = 5 * time.Minute
	reauthStashMaxEntries = 256
)

// reauthCredentialStash keeps credentials obtained by POST /accounts/:id/reauthorize
// in process memory for a short while. An authorization code can be exchanged
// only once, so when saving fails after a successful exchange the request can be
// retried with the same session_id and the stashed credentials are saved without
// a new code. After a successful save only a "done" marker is kept, for a full
// ttl from the save (tokens are dropped), so replays return the account instead
// of failing on a used session.
//
// Like the OAuth sessions themselves the stash is per instance. All methods are
// safe on a nil receiver: lookups miss and writes are no-ops.
type reauthCredentialStash struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	now     func() time.Time
	entries map[string]reauthStashEntry
}

type reauthStashEntry struct {
	accountType string
	credentials map[string]any
	extra       map[string]any
	done        bool
	expiresAt   time.Time
}

func newReauthCredentialStash(ttl time.Duration, max int) *reauthCredentialStash {
	return &reauthCredentialStash{ttl: ttl, max: max, now: time.Now, entries: map[string]reauthStashEntry{}}
}

// reauthStashKey binds an entry to the platform, the OAuth session and the
// account, so one session's credentials can never be saved onto another account.
func reauthStashKey(platform, sessionID string, accountID int64) string {
	return platform + "\x00" + sessionID + "\x00" + strconv.FormatInt(accountID, 10)
}

func (e reauthStashEntry) clone() reauthStashEntry {
	e.credentials = maps.Clone(e.credentials)
	e.extra = maps.Clone(e.extra)
	return e
}

func (s *reauthCredentialStash) lookup(key string) (reauthStashEntry, bool) {
	if s == nil {
		return reauthStashEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || !s.now().Before(entry.expiresAt) {
		return reauthStashEntry{}, false
	}
	return entry.clone(), true
}

func (s *reauthCredentialStash) put(key string, entry reauthStashEntry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeLocked(key, entry)
}

// markDone drops the tokens and keeps a "done" marker for a full ttl from the
// successful save, so a replay within that window gets the account even when
// the save came late in the stash window.
func (s *reauthCredentialStash) markDone(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeLocked(key, reauthStashEntry{done: true})
}

func (s *reauthCredentialStash) storeLocked(key string, entry reauthStashEntry) {
	now := s.now()
	for k, existing := range s.entries {
		if !now.Before(existing.expiresAt) {
			delete(s.entries, k)
		}
	}
	if _, exists := s.entries[key]; !exists && s.max > 0 && len(s.entries) >= s.max {
		s.evictEarliestLocked()
	}
	entry = entry.clone()
	entry.expiresAt = now.Add(s.ttl)
	s.entries[key] = entry
}

func (s *reauthCredentialStash) evictEarliestLocked() {
	var (
		victim   string
		earliest time.Time
		found    bool
	)
	for k, entry := range s.entries {
		if !found || entry.expiresAt.Before(earliest) {
			victim, earliest, found = k, entry.expiresAt, true
		}
	}
	if found {
		delete(s.entries, victim)
	}
}
