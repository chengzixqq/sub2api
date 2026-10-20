package admin

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestReauthStash(t *testing.T, max int) (*reauthCredentialStash, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	stash := newReauthCredentialStash(5*time.Minute, max)
	stash.now = func() time.Time { return now }
	return stash, &now
}

func TestReauthCredentialStashLifecycle(t *testing.T) {
	stash, now := newTestReauthStash(t, 256)
	key := reauthStashKey("anthropic", "sess-1", 7)
	stash.put(key, reauthStashEntry{
		accountType: "oauth",
		credentials: map[string]any{"access_token": "at-1"},
		extra:       map[string]any{"email_address": "a@example.com"},
	})

	got, ok := stash.lookup(key)
	require.True(t, ok)
	require.False(t, got.done)
	require.Equal(t, "oauth", got.accountType)
	require.Equal(t, "at-1", got.credentials["access_token"])
	got.credentials["access_token"] = "mutated"
	again, ok := stash.lookup(key)
	require.True(t, ok)
	require.Equal(t, "at-1", again.credentials["access_token"], "lookup must return a copy")

	_, ok = stash.lookup(reauthStashKey("anthropic", "sess-1", 8))
	require.False(t, ok, "entries are bound to the account")

	stash.markDone(key)
	done, ok := stash.lookup(key)
	require.True(t, ok)
	require.True(t, done.done)
	require.Empty(t, done.credentials, "tokens are dropped once saved")
	require.Empty(t, done.extra)

	*now = now.Add(5*time.Minute + time.Second)
	_, ok = stash.lookup(key)
	require.False(t, ok, "entries expire after the ttl")
}

func TestReauthCredentialStashEvictsEarliestWhenFull(t *testing.T) {
	stash, now := newTestReauthStash(t, 3)
	for i := 0; i < 3; i++ {
		stash.put(reauthStashKey("openai", fmt.Sprintf("s-%d", i), 1), reauthStashEntry{accountType: "oauth"})
		*now = now.Add(time.Second)
	}
	stash.put(reauthStashKey("openai", "s-3", 1), reauthStashEntry{accountType: "oauth"})

	_, ok := stash.lookup(reauthStashKey("openai", "s-0", 1))
	require.False(t, ok, "the entry expiring first is evicted")
	for i := 1; i <= 3; i++ {
		_, ok := stash.lookup(reauthStashKey("openai", fmt.Sprintf("s-%d", i), 1))
		require.True(t, ok, "s-%d", i)
	}
	require.Len(t, stash.entries, 3)
}

func TestReauthCredentialStashPutPrunesExpired(t *testing.T) {
	stash, now := newTestReauthStash(t, 256)
	stash.put(reauthStashKey("grok", "old", 1), reauthStashEntry{accountType: "oauth"})
	*now = now.Add(6 * time.Minute)
	stash.put(reauthStashKey("grok", "new", 1), reauthStashEntry{accountType: "oauth"})
	require.Len(t, stash.entries, 1)
}

func TestReauthCredentialStashMarkDoneWithoutEntry(t *testing.T) {
	stash, _ := newTestReauthStash(t, 256)
	key := reauthStashKey("gemini", "sess", 3)
	stash.markDone(key)
	got, ok := stash.lookup(key)
	require.True(t, ok)
	require.True(t, got.done)
}

func TestReauthCredentialStashNilReceiverIsSafe(t *testing.T) {
	var stash *reauthCredentialStash
	key := reauthStashKey("anthropic", "sess", 1)
	require.NotPanics(t, func() {
		stash.put(key, reauthStashEntry{accountType: "oauth"})
		stash.markDone(key)
	})
	_, ok := stash.lookup(key)
	require.False(t, ok)
}
