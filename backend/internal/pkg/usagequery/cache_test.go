package usagequery

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheIndependentWaiterCancellation(t *testing.T) {
	c := NewCache(time.Minute)
	first, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var loads atomic.Int32
	load := func(ctx context.Context) (any, error) {
		loads.Add(1)
		close(started)
		select {
		case <-release:
			return "ready", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	firstResult := make(chan error, 1)
	go func() { _, _, err := c.GetOrLoad(first, "same", false, load); firstResult <- err }()
	<-started
	secondResult := make(chan CacheEntry, 1)
	secondErr := make(chan error, 1)
	go func() {
		entry, _, err := c.GetOrLoad(context.Background(), "same", false, load)
		secondResult <- entry
		secondErr <- err
	}()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.calls["same"] != nil && c.calls["same"].waiters == 2
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-firstResult, context.Canceled)
	close(release)
	require.NoError(t, <-secondErr)
	require.Equal(t, "ready", (<-secondResult).Payload)
	require.Equal(t, int32(1), loads.Load())
}

func TestCacheCancelsAbandonedWorkAndDoesNotStoreErrors(t *testing.T) {
	c := NewCache(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	started, stopped := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, _, err := c.GetOrLoad(ctx, "abandoned", false, func(work context.Context) (any, error) {
			close(started)
			<-work.Done()
			close(stopped)
			return nil, work.Err()
		})
		result <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("abandoned query did not stop")
	}
	_, hit := c.Get("abandoned")
	require.False(t, hit)
	_, _, err := c.GetOrLoad(context.Background(), "abandoned", false, func(context.Context) (any, error) { return nil, errors.New("failed") })
	require.Error(t, err)
	_, hit = c.Get("abandoned")
	require.False(t, hit)
}

func TestCacheBoundedAndRefreshBypasses(t *testing.T) {
	c := NewCache(time.Minute)
	for i := 0; i < 1100; i++ {
		c.Set(fmt.Sprint(i), i)
	}
	require.LessOrEqual(t, len(c.items), 1024)
	c.Set("key", "old")
	entry, hit, err := c.GetOrLoad(context.Background(), "key", true, func(context.Context) (any, error) { return "fresh", nil })
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, "fresh", entry.Payload)
	entry, hit = c.Get("key")
	require.True(t, hit)
	require.Equal(t, "fresh", entry.Payload)
}

func TestCacheSharedWorkRetainsScopeAndHasDeadline(t *testing.T) {
	c := NewCache(time.Minute)
	ctx := WithOptions(context.Background(), Options{Identity: "user:66", Timezone: "Asia/Shanghai"})
	_, _, err := c.GetOrLoad(ctx, "key", false, func(work context.Context) (any, error) {
		require.Equal(t, "user:66", OptionsFrom(work).Identity)
		deadline, ok := work.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 30*time.Second)
		return "ok", nil
	})
	require.NoError(t, err)
}

func TestCacheRefreshSupersedesOlderInflightResult(t *testing.T) {
	c := NewCache(time.Minute)
	started, release := make(chan struct{}), make(chan struct{})
	oldDone := make(chan error, 1)
	go func() {
		_, _, err := c.GetOrLoad(context.Background(), "key", false, func(context.Context) (any, error) { close(started); <-release; return "old", nil })
		oldDone <- err
	}()
	<-started
	entry, hit, err := c.GetOrLoad(context.Background(), "key", true, func(context.Context) (any, error) { return "fresh", nil })
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, "fresh", entry.Payload)
	close(release)
	require.NoError(t, <-oldDone)
	entry, hit = c.Get("key")
	require.True(t, hit)
	require.Equal(t, "fresh", entry.Payload)
}

func TestCacheFailedRefreshDoesNotExposePreviousEntry(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("key", "old")
	_, _, err := c.GetOrLoad(context.Background(), "key", true, func(context.Context) (any, error) { return nil, errors.New("refresh failed") })
	require.Error(t, err)
	_, hit := c.Get("key")
	require.False(t, hit)
}

func TestDetachedCacheFinishesAbandonedWorkAndStoresResult(t *testing.T) {
	c := NewDetachedCache(time.Minute, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 2)
	var loads atomic.Int32
	load := func(work context.Context) (any, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			finished <- nil
			return "history", nil
		case <-work.Done():
			finished <- work.Err()
			return nil, work.Err()
		}
	}
	result := make(chan error, 1)
	go func() { _, _, err := c.GetOrLoad(ctx, "key", false, load); result <- err }()
	<-started
	cancel()
	require.ErrorIs(t, <-result, context.Canceled, "the leaving waiter still returns its own cancellation")

	// A later waiter joins the abandoned generation instead of starting another load.
	joined := make(chan CacheEntry, 1)
	joinedErr := make(chan error, 1)
	go func() {
		entry, _, err := c.GetOrLoad(context.Background(), "key", false, load)
		joined <- entry
		joinedErr <- err
	}()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.calls["key"] != nil && c.calls["key"].waiters == 1
	}, time.Second, time.Millisecond)
	close(release)
	require.NoError(t, <-finished, "shared work must not be cancelled when its last waiter leaves")
	require.NoError(t, <-joinedErr)
	require.Equal(t, "history", (<-joined).Payload)

	entry, hit, err := c.GetOrLoad(context.Background(), "key", false, func(context.Context) (any, error) {
		loads.Add(1)
		return "reloaded", nil
	})
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, "history", entry.Payload)
	require.Equal(t, int32(1), loads.Load())
}

func TestDetachedCacheStoresResultWithoutAnyRemainingWaiter(t *testing.T) {
	c := NewDetachedCache(time.Minute, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, _, err := c.GetOrLoad(ctx, "key", false, func(work context.Context) (any, error) {
			close(started)
			select {
			case <-release:
				return "history", nil
			case <-work.Done():
				return nil, work.Err()
			}
		})
		result <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	close(release)
	require.Eventually(t, func() bool {
		entry, hit := c.Get("key")
		return hit && entry.Payload == "history"
	}, time.Second, time.Millisecond)
}

func TestDetachedCacheWorkTimeoutCancelsAndDoesNotStore(t *testing.T) {
	c := NewDetachedCache(time.Minute, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan time.Duration, 1)
	stopped := make(chan error, 1)
	result := make(chan error, 1)
	go func() {
		_, _, err := c.GetOrLoad(ctx, "slow", false, func(work context.Context) (any, error) {
			deadline, _ := work.Deadline()
			started <- time.Until(deadline)
			<-work.Done()
			stopped <- work.Err()
			return "late", nil
		})
		result <- err
	}()
	require.LessOrEqual(t, <-started, 20*time.Millisecond, "work deadline must follow workTimeout")
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	select {
	case err := <-stopped:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("detached work did not stop at its deadline")
	}
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.calls["slow"] == nil && c.active == 0
	}, time.Second, time.Millisecond)
	_, hit := c.Get("slow")
	require.False(t, hit, "timed-out work must not be cached")
}

func TestDetachedCacheDoesNotStoreErrors(t *testing.T) {
	c := NewDetachedCache(time.Minute, time.Minute)
	var loads atomic.Int32
	failing := func(context.Context) (any, error) { loads.Add(1); return nil, errors.New("failed") }
	_, _, err := c.GetOrLoad(context.Background(), "key", false, failing)
	require.Error(t, err)
	_, hit := c.Get("key")
	require.False(t, hit)
	entry, hit, err := c.GetOrLoad(context.Background(), "key", false, func(context.Context) (any, error) { loads.Add(1); return "ok", nil })
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, "ok", entry.Payload)
	require.Equal(t, int32(2), loads.Load())
}

func TestDetachedCacheRefreshSupersedesAndCancelsAbandonedWork(t *testing.T) {
	c := NewDetachedCache(time.Minute, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	oldStopped := make(chan error, 1)
	oldResult := make(chan error, 1)
	go func() {
		_, _, err := c.GetOrLoad(ctx, "key", false, func(work context.Context) (any, error) {
			close(started)
			<-work.Done()
			oldStopped <- work.Err()
			return "old", nil
		})
		oldResult <- err
	}()
	<-started
	entry, hit, err := c.GetOrLoad(context.Background(), "key", true, func(context.Context) (any, error) { return "fresh", nil })
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, "fresh", entry.Payload)
	cancel()
	require.ErrorIs(t, <-oldResult, context.Canceled)
	select {
	case err := <-oldStopped:
		require.ErrorIs(t, err, context.Canceled, "superseded work can never publish, so it stops with its last waiter")
	case <-time.After(time.Second):
		t.Fatal("superseded detached work kept running")
	}
	entry, hit = c.Get("key")
	require.True(t, hit)
	require.Equal(t, "fresh", entry.Payload)
}
