package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type accountRPMRecordingCache struct {
	RPMCache
	ids      []int64
	deadline time.Time
	err      error
}

func (c *accountRPMRecordingCache) IncrementRPM(ctx context.Context, id int64) (int, error) {
	c.ids = append(c.ids, id)
	c.deadline, _ = ctx.Deadline()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return len(c.ids), c.err
}

func TestAccountRPMRecording_AllGatewayServicesUseSameCounter(t *testing.T) {
	cache := &accountRPMRecordingCache{}
	legacy := &GatewayService{rpmCache: cache}
	openAI := &OpenAIGatewayService{}
	openAI.SetRPMCache(cache)
	for _, increment := range []func(context.Context, int64) error{legacy.IncrementAccountRPM, openAI.IncrementAccountRPM} {
		require.NoError(t, increment(context.Background(), 42))
		require.WithinDuration(t, time.Now().Add(time.Second), cache.deadline, 100*time.Millisecond)
		require.NoError(t, increment(context.Background(), 0))
	}
	require.Equal(t, []int64{42, 42}, cache.ids)

	cache.err = errors.New("redis unavailable")
	require.ErrorIs(t, openAI.IncrementAccountRPM(context.Background(), 42), cache.err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, legacy.IncrementAccountRPM(ctx, 42), context.Canceled)
	require.NoError(t, (&GatewayService{}).IncrementAccountRPM(context.Background(), 42))
	require.NoError(t, (&OpenAIGatewayService{}).IncrementAccountRPM(context.Background(), 42))
}
