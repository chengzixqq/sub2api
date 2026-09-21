package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestObservationRawIntervalsIncludeUnsealedTrailingBucket(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 37, 42, 0, time.UTC)
	filter := service.ChannelMonitorV2Filter{
		Start:  now.Add(-2 * time.Hour),
		End:    now.Truncate(time.Minute),
		Bucket: time.Hour,
	}
	sourceBucket := time.Hour
	startFull := filter.Start.Truncate(sourceBucket)
	if startFull.Before(filter.Start) {
		startFull = startFull.Add(sourceBucket)
	}
	endFull := filter.End.Truncate(sourceBucket)
	intervals, aggregateEnd := observationRawIntervals(filter, startFull, endFull, sourceBucket, now)
	require.Equal(t, now.Truncate(sourceBucket), aggregateEnd)
	require.Equal(t, [][2]time.Time{{filter.Start, startFull}, {now.Truncate(sourceBucket), filter.End}}, intervals)
}

func TestObservationRawIntervalsDoNotAddTrailingBucketForSealedRange(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 37, 42, 0, time.UTC)
	filter := service.ChannelMonitorV2Filter{
		Start:  now.Add(-3 * time.Hour),
		End:    now.Truncate(time.Hour),
		Bucket: time.Hour,
	}
	startFull := filter.Start.Truncate(time.Hour)
	if startFull.Before(filter.Start) {
		startFull = startFull.Add(time.Hour)
	}
	endFull := filter.End.Truncate(time.Hour)
	intervals, aggregateEnd := observationRawIntervals(filter, startFull, endFull, time.Hour, now)
	require.Equal(t, endFull, aggregateEnd)
	require.Equal(t, [][2]time.Time{{filter.Start, startFull}}, intervals)
}
