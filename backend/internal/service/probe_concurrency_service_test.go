//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type probeConcurrencyCacheForTest struct {
	stubConcurrencyCacheForTest
	probeCounts       map[int64]int
	probeErr          error
	trackedAccountID  []int64
	trackedRequestID  []string
	releasedAccountID []int64
	releasedRequestID []string
}

func (c *probeConcurrencyCacheForTest) TrackProbeAccountSlot(_ context.Context, accountID int64, requestID string) error {
	c.trackedAccountID = append(c.trackedAccountID, accountID)
	c.trackedRequestID = append(c.trackedRequestID, requestID)
	return nil
}

func (c *probeConcurrencyCacheForTest) ReleaseProbeAccountSlot(_ context.Context, accountID int64, requestID string) error {
	c.releasedAccountID = append(c.releasedAccountID, accountID)
	c.releasedRequestID = append(c.releasedRequestID, requestID)
	return nil
}

func (c *probeConcurrencyCacheForTest) GetProbeAccountConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	if c.probeErr != nil {
		return nil, c.probeErr
	}
	result := make(map[int64]int, len(accountIDs))
	for _, accountID := range accountIDs {
		result[accountID] = c.probeCounts[accountID]
	}
	return result, nil
}

var _ ProbeConcurrencyCache = (*probeConcurrencyCacheForTest)(nil)

func TestGetAccountConcurrencyBreakdownBatchSeparatesProbeLeaders(t *testing.T) {
	cache := &probeConcurrencyCacheForTest{
		stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{concurrency: 5},
		probeCounts:                 map[int64]int{42: 2},
	}
	svc := NewConcurrencyService(cache)

	breakdown, err := svc.GetAccountConcurrencyBreakdownBatch(context.Background(), []int64{42, 43})

	require.NoError(t, err)
	require.Equal(t, AccountConcurrencyBreakdown{Current: 3, Probe: 2}, breakdown[42])
	require.Equal(t, AccountConcurrencyBreakdown{Current: 5, Probe: 0}, breakdown[43])
}

// A failed probe read must not be reported as probe=0: the ordinary count
// still includes the probe slots, so the split is unknown as a whole.
func TestGetAccountConcurrencyBreakdownBatchFailsWhenProbeReadFails(t *testing.T) {
	cache := &probeConcurrencyCacheForTest{
		stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{concurrency: 5},
		probeCounts:                 map[int64]int{42: 2},
		probeErr:                    errors.New("redis TIME: i/o timeout"),
	}
	svc := NewConcurrencyService(cache)

	breakdown, err := svc.GetAccountConcurrencyBreakdownBatch(context.Background(), []int64{42, 43})

	require.Error(t, err)
	require.ErrorContains(t, err, "probe")
	require.Nil(t, breakdown)
}

func TestAcquireAccountSlotTracksOnlyMarkedProbeLeaders(t *testing.T) {
	cache := &probeConcurrencyCacheForTest{
		stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: true},
	}
	svc := NewConcurrencyService(cache)

	ordinary, err := svc.AcquireAccountSlot(context.Background(), 7, 5)
	require.NoError(t, err)
	require.Empty(t, cache.trackedAccountID)
	ordinary.ReleaseFunc()
	require.Empty(t, cache.releasedAccountID)

	probeCtx := WithProbeAccountConcurrency(context.Background(), true)
	probe, err := svc.AcquireAccountSlot(probeCtx, 7, 5)
	require.NoError(t, err)
	require.True(t, probe.Acquired)
	require.Equal(t, []int64{7}, cache.trackedAccountID)
	require.Len(t, cache.trackedRequestID, 1)
	require.NotEmpty(t, cache.trackedRequestID[0])

	probe.ReleaseFunc()
	require.Equal(t, []int64{7}, cache.releasedAccountID)
	require.Equal(t, cache.trackedRequestID, cache.releasedRequestID)
}
