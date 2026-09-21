//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type compactLoadRepository struct {
	service.ChannelMonitorObservationRepository
	mu   sync.Mutex
	lags []time.Duration
}

func (r *compactLoadRepository) StoreBatch(ctx context.Context, events []service.ChannelMonitorEvent) error {
	if err := r.ChannelMonitorObservationRepository.StoreBatch(ctx, events); err != nil {
		return err
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range events {
		r.lags = append(r.lags, now.Sub(event.CompletedAt))
	}
	return nil
}

// Run explicitly with -benchtime=120x for a two-minute test. One iteration
// produces one second of traffic; ordinary integration tests do not run it.
func BenchmarkCompactObservationSustained(b *testing.B) {
	const eventsPerSecond = 110
	ctx := context.Background()
	base := NewChannelMonitorObservationRepository(integrationDB)
	r := &compactLoadRepository{ChannelMonitorObservationRepository: base}
	collector := service.NewChannelMonitorCollector(r, service.ChannelMonitorCollectorOptions{})
	started := time.Now().UTC()
	groupBase := started.UnixNano()
	space := func() int64 {
		var bytes int64
		require.NoError(b, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(SUM(pg_total_relation_size(c.oid)),0) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind='r' AND c.relname LIKE 'channel_monitor_compact_%'`).Scan(&bytes))
		return bytes
	}
	before := space()
	expected := map[string]*service.ChannelMonitorObservationFact{"traffic": {}, "probe": {}}
	var submitted atomic.Int64
	var rejected atomic.Int64
	maxDepth := 0
	collector.Start()
	b.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = collector.Stop(stopCtx)
	})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		now := time.Now().UTC()
		events := make([]service.ChannelMonitorEvent, eventsPerSecond)
		for i := range events {
			e := service.ChannelMonitorEvent{RequestID: uuid.NewString(), Source: "traffic", StartedAt: now.Add(-100 * time.Millisecond), CompletedAt: now, Platform: "openai", GroupID: groupBase + int64(i%8), RequestedModel: fmt.Sprintf("load-model-%d", i%16), Protocol: "openai_responses", Outcome: "success", HTTPStatus: 200, OutputSeen: true, TerminalSeen: true, DurationMs: 100, InputTokens: 100, OutputTokens: 10}
			if i%7 == 0 {
				e.Source = "probe"
			}
			if i%2 == 0 {
				e.Protocol = "openai_chat"
			}
			switch i % 10 {
			case 0:
				e.Outcome, e.ErrorCategory, e.HTTPStatus = "channel_error", "upstream_error", 502
			case 1:
				e.Outcome, e.ErrorCategory, e.HTTPStatus = "client_error", "client_request", 400
			case 2:
				e.Outcome, e.ErrorCategory = "cancelled", "client_cancelled"
			case 3:
				e.Outcome, e.ErrorCategory = "unknown", "incomplete_terminal"
			}
			e.Attempts = []service.ChannelMonitorAttempt{{Sequence: 1, AccountID: int64(i%16 + 1), Outcome: e.Outcome, ErrorCategory: e.ErrorCategory, HTTPStatus: e.HTTPStatus, DurationMs: 100}}
			if i%4 == 0 {
				e.Attempts = append(e.Attempts, service.ChannelMonitorAttempt{Sequence: 2, AccountID: int64(i%16 + 17), Outcome: "channel_error", ErrorCategory: "upstream_capacity", HTTPStatus: 429, DurationMs: 20})
			}
			events[i] = e
			expected[e.Source].Add(service.ChannelMonitorObservationFactFromEvent(e, time.Minute))
		}
		var workers sync.WaitGroup
		for worker := 0; worker < 5; worker++ {
			workers.Add(1)
			go func(worker int) {
				defer workers.Done()
				for i, event := range events {
					if (worker < 4 && i%4 == worker) || (worker == 4 && i%4 == 0) {
						if collector.Submit(event) {
							submitted.Add(1)
						} else {
							rejected.Add(1)
						}
					}
				}
			}(worker)
		}
		workers.Wait()
		maxDepth = max(maxDepth, collector.QueueDepth())
		<-ticker.C
	}
	stopCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	require.NoError(b, collector.Stop(stopCtx))
	b.StopTimer()
	require.Zero(b, rejected.Load())
	for _, table := range []string{"channel_monitor_compact_minute", "channel_monitor_compact_hour"} {
		actual := map[string]*service.ChannelMonitorObservationFact{"traffic": {}, "probe": {}}
		rows, err := integrationDB.QueryContext(ctx, "SELECT source,facts FROM "+table+" WHERE group_id BETWEEN $1 AND $2", groupBase, groupBase+7)
		require.NoError(b, err)
		for rows.Next() {
			var source string
			var raw []byte
			require.NoError(b, rows.Scan(&source, &raw))
			var fact service.ChannelMonitorObservationFact
			require.NoError(b, json.Unmarshal(raw, &fact))
			actual[source].Add(fact)
		}
		require.NoError(b, rows.Err())
		require.NoError(b, rows.Close())
		for source, want := range expected {
			require.Equal(b, want, actual[source], table+" "+source+" must preserve exact counters and histograms")
		}
	}
	var accounts, samples, lost int64
	require.NoError(b, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(SUM((facts->>'attempt_count')::bigint),0) FROM channel_monitor_compact_attempt_minute WHERE group_id BETWEEN $1 AND $2`, groupBase, groupBase+7).Scan(&accounts))
	require.Equal(b, expected["traffic"].AttemptCount+expected["probe"].AttemptCount, accounts)
	require.NoError(b, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_samples WHERE group_id BETWEEN $1 AND $2`, groupBase, groupBase+7).Scan(&samples))
	require.NoError(b, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(SUM(lost_events),0) FROM channel_monitor_compact_gaps WHERE started_at>=$1`, started).Scan(&lost))
	require.Zero(b, lost)
	r.mu.Lock()
	sort.Slice(r.lags, func(i, j int) bool { return r.lags[i] < r.lags[j] })
	require.NotEmpty(b, r.lags)
	maximum, p95 := r.lags[len(r.lags)-1], r.lags[(len(r.lags)-1)*95/100]
	r.mu.Unlock()
	require.Less(b, maximum, 120*time.Second, "completed event to committed aggregate latency gate")
	bytes := space() - before
	b.ReportMetric(float64(eventsPerSecond), "unique_events/s")
	b.ReportMetric(float64(p95.Milliseconds()), "p95_commit_lag_ms")
	b.ReportMetric(float64(maximum.Milliseconds()), "max_commit_lag_ms")
	b.ReportMetric(float64(bytes), "allocated_growth_bytes")
	b.Logf("iterations=%d unique=%d accepted_including_duplicates=%d rejected=%d lost=%d attempts=%d samples=%d max_queue=%d before_bytes=%d after_bytes=%d p95_lag=%s max_lag=%s", b.N, b.N*eventsPerSecond, submitted.Load(), rejected.Load(), lost, accounts, samples, maxDepth, before, before+bytes, p95, maximum)
}
