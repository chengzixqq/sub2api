//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCompactObservationIntegration_PolicyMigrationStartsShadowOnce(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `DELETE FROM channel_monitor_compact_config; UPDATE channel_monitor_observation_config SET config=config || '{"enabled":false,"mode":"live","minimum_sample":123,"live_group_ids":[7]}'::jsonb WHERE id=TRUE`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("239_channel_monitor_compact.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT config FROM channel_monitor_compact_config WHERE id=TRUE`).Scan(&raw))
	var cfg service.ChannelMonitorObservationConfig
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.Equal(t, "shadow", cfg.Mode)
	require.False(t, cfg.Enabled)
	require.Equal(t, int64(123), cfg.MinimumSample)
	require.Equal(t, 24, cfg.DetailRetentionHours)
	require.False(t, cfg.ProbeEnabled)
	require.True(t, cfg.QuotaEnabled)
	require.Empty(t, cfg.LiveGroupIDs)
	_, err = tx.ExecContext(ctx, `UPDATE channel_monitor_compact_config SET config=config || '{"mode":"live"}'::jsonb WHERE id=TRUE`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var mode string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT config->>'mode' FROM channel_monitor_compact_config WHERE id=TRUE`).Scan(&mode))
	require.Equal(t, "live", mode)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT config->>'mode' FROM channel_monitor_observation_config WHERE id=TRUE`).Scan(&mode))
	require.Equal(t, "live", mode)
}

func TestCompactObservationIntegration_AtomicConcurrentReplayAndSourceIsolation(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewChannelMonitorObservationRepository(integrationDB).(*compactObservationRepository)
	require.True(t, ok)
	now := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	groupID := now.UnixNano()
	e := service.ChannelMonitorEvent{RequestID: uuid.NewString(), SessionID: uuid.NewString(), StartedAt: now.Add(-time.Second), CompletedAt: now, Platform: "openai", GroupID: groupID, RequestedModel: "compact-test", Protocol: "openai_chat", Outcome: "success", DurationMs: 1000, Source: "traffic", Attempts: []service.ChannelMonitorAttempt{{Sequence: 1, AccountID: 11, Outcome: "channel_error", DurationMs: 100}, {Sequence: 2, AccountID: 12, Outcome: "success", DurationMs: 900}}}
	probe := e
	probe.RequestID = uuid.NewString()
	probe.Source = "probe"
	t.Cleanup(func() {
		for _, table := range []string{"channel_monitor_compact_minute", "channel_monitor_compact_hour", "channel_monitor_compact_attempt_minute", "channel_monitor_compact_attempt_hour", "channel_monitor_compact_samples"} {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE group_id=$1", groupID)
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_dedup WHERE request_id IN ($1,$2)`, e.RequestID, probe.RequestID)
	})
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- repo.StoreBatch(ctx, []service.ChannelMonitorEvent{e, probe}) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	filter := service.ChannelMonitorV2Filter{Start: now.Add(-time.Minute), End: now.Add(time.Minute), Bucket: time.Minute, GroupIDs: []int64{groupID}, AllowedGroupIDs: []int64{groupID}, RestrictGroups: true}
	out, err := repo.Query(ctx, filter)
	require.NoError(t, err)
	require.Len(t, out.Facts, 1)
	require.Equal(t, int64(1), out.Facts[0].SuccessRequests)
	require.Equal(t, "traffic", out.Facts[0].Source)
	probes, err := repo.QuerySource(ctx, filter, "probe")
	require.NoError(t, err)
	require.Len(t, probes.Facts, 1)
	require.Equal(t, int64(1), probes.Facts[0].SuccessRequests)
	accounts, err := repo.QueryAccounts(ctx, filter)
	require.NoError(t, err)
	require.Len(t, accounts, 2)
	var success, channelErrors, unclassified int64
	for _, a := range accounts {
		success += a.SuccessAttempts
		channelErrors += a.ChannelErrorAttempts
		unclassified += a.UnclassifiedAttempts
		require.Zero(t, a.FailedAttempts, "new attempts must not use the legacy unclassified counter")
		require.Zero(t, a.ClientErrorAttempts)
		require.Zero(t, a.CancelledAttempts)
	}
	require.Equal(t, int64(1), success)
	require.Equal(t, int64(1), channelErrors)
	require.Zero(t, unclassified)
	var oldCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_observation_events WHERE request_id=$1`, e.RequestID).Scan(&oldCount))
	require.Zero(t, oldCount)
	conflict := e
	conflict.Outcome = "channel_error"
	require.NoError(t, repo.StoreBatch(ctx, []service.ChannelMonitorEvent{conflict}))
	var conflicts int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_gaps WHERE session_id=$1 AND reason='terminal_conflict'`, e.SessionID).Scan(&conflicts))
	require.Equal(t, 1, conflicts)
	_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_gaps WHERE session_id=$1`, e.SessionID)
}

func TestCompactObservationIntegration_SampleBudgetAndMaintenance(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewChannelMonitorObservationRepository(integrationDB).(*compactObservationRepository)
	require.True(t, ok)
	now := time.Now().UTC()
	groupID := now.UnixNano()
	session := uuid.NewString()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_samples WHERE group_id=$1`, groupID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_writer_sessions WHERE id=$1`, session)
	})
	day := now.Format("2006-01-02")
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_compact_sample_budgets(day,kind,used) VALUES($1,'failure',7999) ON CONFLICT(day,kind) DO UPDATE SET used=7999`, day)
	require.NoError(t, err)
	first := service.ChannelMonitorEvent{RequestID: uuid.NewString(), CompletedAt: now, GroupID: groupID, Platform: "openai", RequestedModel: "model", Protocol: "openai_chat", Outcome: "channel_error"}
	second := first
	second.RequestID = uuid.NewString()
	require.NoError(t, repo.storeSamples(ctx, []service.ChannelMonitorEvent{first, second}))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_samples WHERE group_id=$1`, groupID).Scan(&count))
	require.Equal(t, 1, count)
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT facts FROM channel_monitor_compact_samples WHERE group_id=$1`, groupID).Scan(&raw))
	var sample map[string]any
	require.NoError(t, json.Unmarshal(raw, &sample))
	require.NotContains(t, sample, "user_id")
	require.NoError(t, repo.Heartbeat(ctx, service.ChannelMonitorObservationSession{ID: session, StartedAt: now.Add(-time.Hour), HeartbeatAt: now}))
	require.NoError(t, repo.Maintain(ctx, now))
	var through time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT data_through FROM channel_monitor_compact_writer_sessions WHERE id=$1`, session).Scan(&through))
	require.WithinDuration(t, now, through, time.Microsecond)
	_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_sample_budgets WHERE day=$1 AND kind='failure'`, day)
}

func TestCompactObservationIntegration_RollbackLeavesReplayableEvent(t *testing.T) {
	ctx := context.Background()
	repo := NewChannelMonitorObservationRepository(integrationDB)
	e := compactTestEvent()
	e.GroupID = time.Now().UnixNano()
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION compact_test_reject_hour() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.group_id=%d THEN RAISE EXCEPTION 'injected aggregate failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER compact_test_reject_hour BEFORE INSERT ON channel_monitor_compact_hour FOR EACH ROW EXECUTE FUNCTION compact_test_reject_hour()`, e.GroupID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS compact_test_reject_hour ON channel_monitor_compact_hour; DROP FUNCTION IF EXISTS compact_test_reject_hour()`)
		for _, table := range []string{"channel_monitor_compact_minute", "channel_monitor_compact_hour", "channel_monitor_compact_samples"} {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE group_id=$1", e.GroupID)
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_dedup WHERE request_id=$1`, e.RequestID)
	})
	require.ErrorContains(t, repo.StoreBatch(ctx, []service.ChannelMonitorEvent{e}), "injected aggregate failure")
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_dedup WHERE request_id=$1`, e.RequestID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_minute WHERE group_id=$1`, e.GroupID).Scan(&count))
	require.Zero(t, count)
	_, err = integrationDB.ExecContext(ctx, `DROP TRIGGER compact_test_reject_hour ON channel_monitor_compact_hour; DROP FUNCTION compact_test_reject_hour()`)
	require.NoError(t, err)
	require.NoError(t, repo.StoreBatch(ctx, []service.ChannelMonitorEvent{e}))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_dedup WHERE request_id=$1`, e.RequestID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestCompactObservationIntegration_RetentionAndUTCPartitions(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewChannelMonitorObservationRepository(integrationDB).(*compactObservationRepository)
	require.True(t, ok)
	now := time.Now().UTC()
	groupID := now.UnixNano()
	requestID := uuid.NewString()
	old := now.Add(-4 * 24 * time.Hour).Truncate(time.Hour)
	_, err := integrationDB.ExecContext(ctx, `SELECT channel_monitor_compact_ensure_partitions($1)`, old.In(time.FixedZone("UTC+8", 8*3600)))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `SELECT channel_monitor_compact_ensure_partitions($1)`, time.Now().UTC())
		for _, table := range []string{"channel_monitor_compact_minute", "channel_monitor_compact_hour", "channel_monitor_compact_samples"} {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE group_id=$1", groupID)
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_dedup WHERE request_id=$1`, requestID)
	})
	for _, table := range []string{"channel_monitor_compact_minute", "channel_monitor_compact_hour"} {
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO `+table+`(bucket_start,source,platform,group_id,model,protocol,facts) VALUES($1,'traffic','openai',$2,'model','openai_chat','{"success_requests":1}')`, old, groupID)
		require.NoError(t, err)
	}
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_compact_dedup(request_id,fingerprint,received_at) VALUES($1,decode(repeat('00',32),'hex'),$2)`, requestID, now.Add(-26*time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_compact_samples(request_id,completed_at,source,group_id,kind,facts) VALUES($1,$2,'traffic',$3,'failure','{}')`, requestID, now.Add(-25*time.Hour), groupID)
	require.NoError(t, err)
	require.NoError(t, repo.Maintain(ctx, now))
	var count int
	for _, table := range []string{"channel_monitor_compact_minute"} {
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE group_id=$1", groupID).Scan(&count))
		require.Zero(t, count)
	}
	samples, err := repo.QuerySamples(ctx, service.ChannelMonitorV2Filter{Start: now.Add(-48 * time.Hour), End: now, Bucket: time.Minute, GroupIDs: []int64{groupID}})
	require.NoError(t, err)
	require.Empty(t, samples)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_hour WHERE group_id=$1`, groupID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_dedup WHERE request_id=$1`, requestID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, repo.Maintain(ctx, now.Add(31*24*time.Hour)))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_hour WHERE group_id=$1`, groupID).Scan(&count))
	require.Zero(t, count)
}

func TestCompactObservationIntegration_SamplesRetentionUsesUTCPartitions(t *testing.T) {
	ctx := context.Background()
	var partitioned bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_partitioned_table WHERE partrelid='channel_monitor_compact_samples'::regclass)`).Scan(&partitioned))
	require.True(t, partitioned)
	now := time.Now().UTC()
	expired := now.Add(-24 * time.Hour).Truncate(24 * time.Hour)
	groupID := now.UnixNano()
	_, err := integrationDB.ExecContext(ctx, `SELECT channel_monitor_compact_ensure_partitions($1)`, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `SELECT channel_monitor_compact_ensure_partitions($1)`, time.Now().UTC())
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_samples WHERE group_id=$1`, groupID)
	})
	for _, timestamp := range []time.Time{expired, now.Add(-time.Minute)} {
		raw, err := json.Marshal(compactObservationSampleRow{CompletedAt: timestamp, Model: "sample-test"})
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_compact_samples(request_id,completed_at,source,group_id,kind,facts) VALUES($1,$2,'traffic',$3,'failure',$4)`, uuid.NewString(), timestamp, groupID, raw)
		require.NoError(t, err)
	}
	repo, ok := NewChannelMonitorObservationRepository(integrationDB).(*compactObservationRepository)
	require.True(t, ok)
	require.NoError(t, repo.Maintain(ctx, now))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_compact_samples WHERE group_id=$1`, groupID).Scan(&count))
	require.Equal(t, 1, count, "bounded cleanup removes expired rows from the boundary day without touching fresh partitions")
	samples, err := repo.QuerySamples(ctx, service.ChannelMonitorV2Filter{Start: now.Add(-48 * time.Hour), End: now, Bucket: time.Minute, GroupIDs: []int64{groupID}})
	require.NoError(t, err)
	require.Len(t, samples, 1, "logical sample retention must remain exactly 24 hours")
	require.NoError(t, repo.Maintain(ctx, now.Truncate(24*time.Hour).Add(24*time.Hour+time.Minute)))
	var oldPartitionExists bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, "channel_monitor_compact_samples_"+expired.Format("20060102")).Scan(&oldPartitionExists))
	require.False(t, oldPartitionExists)
}

func BenchmarkCompactObservationStore(b *testing.B) {
	ctx := context.Background()
	repo := NewChannelMonitorObservationRepository(integrationDB)
	now := time.Now().UTC()
	groupID := now.UnixNano()
	session := uuid.NewString()
	events := make([]service.ChannelMonitorEvent, 128)
	for i := range events {
		events[i] = service.ChannelMonitorEvent{RequestID: uuid.NewString(), SessionID: session, StartedAt: now.Add(-time.Second), CompletedAt: now, Source: "traffic", Platform: "openai", GroupID: groupID, RequestedModel: fmt.Sprintf("model-%d", i%8), Protocol: "openai_chat", Outcome: "success", DurationMs: 1000, Attempts: []service.ChannelMonitorAttempt{{AccountID: int64(i%8 + 1), Sequence: 1, Outcome: "success", DurationMs: 900}}}
	}
	b.Cleanup(func() {
		for _, table := range []string{"channel_monitor_compact_minute", "channel_monitor_compact_hour", "channel_monitor_compact_attempt_minute", "channel_monitor_compact_attempt_hour", "channel_monitor_compact_samples"} {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE group_id=$1", groupID)
		}
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range events {
			events[j].RequestID = uuid.NewString()
		}
		if err := repo.StoreBatch(ctx, events); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N*len(events))/b.Elapsed().Seconds(), "events/s")
}

func TestCompactObservationIntegration_IdleProgressAndProbeProtocol(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewChannelMonitorObservationRepository(integrationDB).(*compactObservationRepository)
	require.True(t, ok)
	now := time.Now().UTC()
	session := uuid.NewString()
	groupID := now.UnixNano()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_writer_sessions WHERE id=$1`, session)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_gaps WHERE session_id=$1`, session)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_minute WHERE group_id=$1`, groupID)
	})
	state := service.ChannelMonitorObservationSession{ID: session, StartedAt: now.Add(-25 * time.Hour), HeartbeatAt: now}
	require.NoError(t, repo.Heartbeat(ctx, state))
	filter := service.ChannelMonitorV2Filter{Start: now.Truncate(time.Minute).Add(-24 * time.Hour), End: now.Truncate(time.Minute), Bucket: time.Minute, GroupIDs: []int64{groupID}}
	out, err := repo.Query(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, "complete", out.Coverage.State)
	require.Equal(t, "healthy", out.Coverage.CollectorState)
	require.Nil(t, out.Coverage.LastIngestedAt)
	activity, err := repo.ProbeActivity(ctx, groupID, "model", "openai_chat")
	require.NoError(t, err)
	require.True(t, activity.CollectionHealthy)
	require.Nil(t, activity.LastTrafficAt)
	state.PendingEvents = 20
	state.HeartbeatAt = now.Add(time.Second)
	require.NoError(t, repo.Heartbeat(ctx, state))
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_chat")
	require.NoError(t, err)
	require.True(t, activity.CollectionHealthy, "a small flush-sized burst is not a collector backlog")
	backlogged, err := repo.Query(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, "healthy", backlogged.Coverage.CollectorState)
	require.Equal(t, int64(20), backlogged.Coverage.PendingEvents)
	require.WithinDuration(t, now, backlogged.Coverage.DataThrough, time.Microsecond)
	state.PendingEvents = compactObservationBacklogThreshold
	state.HeartbeatAt = now.Add(2 * time.Second)
	require.NoError(t, repo.Heartbeat(ctx, state))
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_chat")
	require.NoError(t, err)
	require.False(t, activity.CollectionHealthy, "a full pending batch is a collector backlog")
	backlogged, err = repo.Query(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, "backlogged", backlogged.Coverage.CollectorState)
	require.Equal(t, compactObservationBacklogThreshold, backlogged.Coverage.PendingEvents)
	state.PendingEvents = 0
	state.HeartbeatAt = now
	require.NoError(t, repo.Heartbeat(ctx, state))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_compact_minute(bucket_start,source,platform,group_id,model,protocol,facts) VALUES($1,'traffic','openai',$2,'model','openai_responses','{"unknown_requests":1}')`, now.Truncate(time.Minute), groupID)
	require.NoError(t, err)
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_responses")
	require.NoError(t, err)
	require.Nil(t, activity.LastTrafficAt, "unknown-only records are not completed traffic")
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_minute WHERE bucket_start=$1 AND source='traffic' AND group_id=$2 AND model='model' AND protocol='openai_responses'`, now.Truncate(time.Minute), groupID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO channel_monitor_compact_minute(bucket_start,source,platform,group_id,model,protocol,facts) VALUES($1,'traffic','openai',$2,'model','openai_responses','{"success_requests":1}')`, now.Truncate(time.Minute), groupID)
	require.NoError(t, err)
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_chat")
	require.NoError(t, err)
	require.Nil(t, activity.LastTrafficAt)
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_responses")
	require.NoError(t, err)
	require.NotNil(t, activity.LastTrafficAt)
	_, err = integrationDB.ExecContext(ctx, `UPDATE channel_monitor_compact_minute SET protocol='responses_websocket' WHERE group_id=$1`, groupID)
	require.NoError(t, err)
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_responses")
	require.NoError(t, err)
	require.NotNil(t, activity.LastTrafficAt, "Responses WS is active Responses traffic")
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_chat")
	require.NoError(t, err)
	require.Nil(t, activity.LastTrafficAt)
	state.LastWriteError = "database unavailable"
	state.HeartbeatAt = now.Add(time.Second)
	require.NoError(t, repo.Heartbeat(ctx, state))
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_chat")
	require.NoError(t, err)
	require.False(t, activity.CollectionHealthy)
	var through time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT data_through FROM channel_monitor_compact_writer_sessions WHERE id=$1`, session).Scan(&through))
	require.WithinDuration(t, now, through, time.Microsecond)
	state.LastWriteError = ""
	require.NoError(t, repo.Heartbeat(ctx, state))
	require.NoError(t, repo.RecordGaps(ctx, session, []service.ChannelMonitorObservationGap{{StartedAt: now.Add(-time.Minute), EndedAt: now, Reason: "write_failed", LostEvents: 1}}))
	activity, err = repo.ProbeActivity(ctx, groupID, "model", "openai_chat")
	require.NoError(t, err)
	require.False(t, activity.CollectionHealthy)
}

func TestCompactObservationIntegration_CoverageDetectsRestartDowntime(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewChannelMonitorObservationRepository(integrationDB).(*compactObservationRepository)
	require.True(t, ok)
	now := time.Now().UTC()
	first, second := uuid.NewString(), uuid.NewString()
	ended := now.Add(-time.Hour)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channel_monitor_compact_writer_sessions WHERE id IN ($1,$2)`, first, second)
	})
	require.NoError(t, repo.Heartbeat(ctx, service.ChannelMonitorObservationSession{ID: first, StartedAt: now.Add(-25 * time.Hour), HeartbeatAt: ended, EndedAt: &ended}))
	require.NoError(t, repo.Heartbeat(ctx, service.ChannelMonitorObservationSession{ID: second, StartedAt: now.Add(-30 * time.Minute), HeartbeatAt: now}))
	filter := service.ChannelMonitorV2Filter{Start: now.Truncate(time.Minute).Add(-24 * time.Hour), End: now.Truncate(time.Minute), Bucket: time.Minute, RestrictGroups: true}
	out, err := repo.Query(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, "partial", out.Coverage.State)
	require.Contains(t, out.Coverage.GapReasons, "collector_unobserved")
}
