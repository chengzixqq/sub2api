package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2Timeline_CustomBucketsReadRetainedRollups(t *testing.T) {
	for _, bucket := range []time.Duration{6 * time.Hour, 9 * time.Hour, 36 * time.Hour, 30 * 24 * time.Hour} {
		f := service.ChannelMonitorV2Filter{Bucket: bucket, RollupBucket: time.Hour}
		require.Equal(t, 3600, channelMonitorV2FixedBucketSeconds(f))
		require.Equal(t, "channel_monitor_v2_metrics_rollup", channelMonitorV2MetricsTable(f))
		require.Equal(t, "channel_monitor_v2_latency_histograms_rollup", channelMonitorV2HistogramTable(f))
		require.Equal(t, "channel_monitor_v2_error_metrics_rollup", channelMonitorV2ErrorMetricsTable(f))
	}
	require.Equal(t, 0, channelMonitorV2FixedBucketSeconds(service.ChannelMonitorV2Filter{Bucket: 35 * time.Minute, RollupBucket: time.Minute}))
	require.Equal(t, "channel_monitor_v2_metrics_1m", channelMonitorV2MetricsTable(service.ChannelMonitorV2Filter{Bucket: time.Hour, RollupBucket: time.Minute}))
	// Default callers still select the existing precomputed tier.
	require.Equal(t, 43200, channelMonitorV2FixedBucketSeconds(service.ChannelMonitorV2Filter{Bucket: 12 * time.Hour}))
}

func TestChannelMonitorV2Timeline_PartialStoredHoursStayOutsideSampleWindow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	start := time.Date(2026, 9, 12, 0, 7, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	f := service.ChannelMonitorV2Filter{Start: start, End: end, Bucket: 6 * time.Hour, RollupBucket: time.Hour}
	mock.ExpectQuery("SELECT usage_coverage_start").WillReturnRows(sqlmock.NewRows([]string{"usage", "error", "through", "computed", "backfill"}).AddRow(start.Add(-time.Hour), start.Add(-time.Hour), end, time.Now().UTC(), start.Add(-time.Hour)))
	coverage, err := (&channelMonitorV2Repository{db: db}).loadCoverage(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, start.Truncate(time.Hour).Add(time.Hour), coverage.CoverageStart)
	require.Equal(t, end.Truncate(time.Hour), coverage.DataThrough)
	require.Equal(t, start, coverage.RequestedStart)
	require.Equal(t, end, coverage.RequestedEnd)
	require.False(t, coverage.CoverageComplete)
	effective := channelMonitorV2CommonCoverageFilter(f, *coverage)
	require.Equal(t, coverage.CoverageStart, effective.Start)
	require.Equal(t, coverage.DataThrough, effective.End)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorV2Timeline_SQLExcludesPartialSourceHoursWithoutCoveragePreflight(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	start := time.Date(2026, 9, 12, 0, 7, 0, 0, time.UTC)
	end := start.Add(30*24*time.Hour + 23*time.Minute)
	f := service.ChannelMonitorV2Filter{Start: start, End: end, Bucket: 6 * time.Hour, RollupBucket: time.Hour}
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}
	mock.ExpectQuery("SELECT date_bin").WithArgs("21600 seconds", start.Truncate(time.Hour).Add(time.Hour), end.Truncate(time.Hour), pq.Array([]string{"openai"}), 3600).WillReturnRows(sqlmock.NewRows([]string{"bucket", "platform", "group", "name", "model", "success", "errors", "upstream_affected", "attempts", "input", "output", "cache_creation", "cache_read", "ttft_sum", "ttft_count", "duration_sum", "duration_count"}))
	_, err = (&channelMonitorV2Repository{db: db}).loadFacts(context.Background(), f, cfg, true)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorV2Timeline_FactsHistogramsAndIgnoredErrorsShareAggregation(t *testing.T) {
	start := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	f := service.ChannelMonitorV2Filter{Start: start, End: start.Add(30 * 24 * time.Hour), Bucket: 6 * time.Hour, RollupBucket: time.Hour}
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}, IgnoredErrorCategories: []string{"authentication"}}
	for _, kind := range []string{"facts", "histograms", "ignored", "matrix ignored"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
				require.Contains(t, actual, "date_bin($1::interval,")
				require.Contains(t, actual, channelMonitorV2DateBinOrigin)
				require.Contains(t, actual, "bucket_seconds = $5")
				require.Contains(t, actual, "GROUP BY date_bin(")
				require.NotContains(t, actual, "_1m")
				require.Contains(t, strings.ToUpper(actual), "SUM(")
				return nil
			})))
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			repo := &channelMonitorV2Repository{db: db}
			switch kind {
			case "facts":
				mock.ExpectQuery("facts").WithArgs("21600 seconds", f.Start, f.End, pq.Array([]string{"openai"}), 3600).WillReturnRows(sqlmock.NewRows([]string{"bucket", "platform", "group", "name", "model", "success", "errors", "upstream_affected", "attempts", "input", "output", "cache_creation", "cache_read", "ttft_sum", "ttft_count", "duration_sum", "duration_count"}).AddRow(start, "openai", 7, "test", "m", 100, 20, 20, 120, 1000, 200, 0, 900, 109000, 100, 500000, 100))
				facts, err := repo.loadFacts(context.Background(), f, cfg, true)
				require.NoError(t, err)
				require.Len(t, facts, 1)
				require.EqualValues(t, 100, facts[0].Success)
				require.EqualValues(t, 20, facts[0].Errors)
			case "histograms":
				mock.ExpectQuery("histograms").WithArgs("21600 seconds", f.Start, f.End, pq.Array([]string{"openai"}), 3600, int64(0)).WillReturnRows(sqlmock.NewRows([]string{"bucket", "platform", "group", "model", "user", "metric", "upper", "count"}).AddRow(start, "openai", 7, "m", 0, "ttft", 100, 90).AddRow(start, "openai", 7, "m", 0, "ttft", 10000, 10))
				hist, err := repo.loadHistograms(context.Background(), f, cfg, 0, true)
				require.NoError(t, err)
				require.Len(t, hist, 2)
				acc := newMetricAccumulator()
				for _, h := range hist {
					acc.addHistogram(h)
				}
				m := acc.metric(360, true)
				require.EqualValues(t, 100, *m.TTFT.P50Ms)
				require.EqualValues(t, 10000, *m.TTFT.P95Ms)
			case "ignored":
				mock.ExpectQuery("ignored").WithArgs("21600 seconds", f.Start, f.End, pq.Array([]string{"openai"}), 3600, pq.Array([]string{"authentication"}), service.ChannelMonitorV2TaxonomyVersion).WillReturnRows(sqlmock.NewRows([]string{"bucket", "platform", "model", "errors"}).AddRow(start, "openai", "m", 4))
				by, total, err := repo.loadIgnoredErrorCounts(context.Background(), f, cfg)
				require.NoError(t, err)
				require.EqualValues(t, 4, total)
				require.EqualValues(t, 4, by[start.Format(time.RFC3339Nano)])
			case "matrix ignored":
				mock.ExpectQuery("matrix ignored").WithArgs("21600 seconds", f.Start, f.End, pq.Array([]string{"openai"}), 3600, pq.Array([]string{"authentication"}), service.ChannelMonitorV2TaxonomyVersion).WillReturnRows(sqlmock.NewRows([]string{"bucket", "platform", "group", "model", "errors"}).AddRow(start, "openai", 7, "m", 4))
				by, total, err := repo.loadIgnoredErrorCountsByMatrixKey(context.Background(), f, cfg, service.ChannelMonitorV2GroupByPlatformGroupModel)
				require.NoError(t, err)
				key := channelMonitorV2MatrixKey{platform: "openai", groupID: 7, model: "m"}
				require.EqualValues(t, 4, total[key])
				require.EqualValues(t, 4, by[key][start.Format(time.RFC3339Nano)])
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
