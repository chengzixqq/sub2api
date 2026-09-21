package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCompactObservationConfig_UsesIndependentPolicy(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := NewChannelMonitorObservationRepository(db)
	mock.ExpectQuery("SELECT version, config FROM channel_monitor_compact_config").WillReturnError(sql.ErrNoRows)
	cfg, err := repo.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "shadow", cfg.Mode)
	cfg.Mode = "live"
	mock.ExpectQuery("UPDATE channel_monitor_compact_config").WithArgs(sqlmock.AnyArg(), 1).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(2))
	updated, err := repo.UpdateConfig(context.Background(), *cfg, 1)
	require.NoError(t, err)
	require.Equal(t, 2, updated.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCompactObservationAggregate_SeparatesTrafficProbeAndAttempts(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 12, 30, 0, time.UTC)
	event := service.ChannelMonitorEvent{RequestID: uuid.NewString(), SessionID: uuid.NewString(), StartedAt: now.Add(-time.Second), CompletedAt: now, Platform: "openai", GroupID: 7, RequestedModel: "gpt-test", Protocol: "openai_chat", Outcome: "success", DurationMs: 1000, Attempts: []service.ChannelMonitorAttempt{{Sequence: 1, AccountID: 11, Outcome: "channel_error", DurationMs: 100}, {Sequence: 2, AccountID: 12, Outcome: "success", DurationMs: 900}}}
	rows, attempts, err := compactObservationRows([]service.ChannelMonitorEvent{event})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Len(t, attempts, 4)
	for _, row := range rows {
		require.Equal(t, "traffic", row.Source)
		var facts service.ChannelMonitorObservationFact
		require.NoError(t, json.Unmarshal(row.Facts, &facts))
		require.Equal(t, int64(1), facts.SuccessRequests)
		require.Equal(t, int64(0), facts.ChannelErrors)
	}
}

func TestCompactObservationQueryRanges_UsesExactMinuteEdgesAndHourlyHistory(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 12, 30, 0, time.UTC)
	f := service.ChannelMonitorV2Filter{Start: now.Truncate(time.Minute).Add(-24 * time.Hour), End: now.Truncate(time.Minute), Bucket: 5 * time.Minute}
	parts, partial, err := compactObservationQueryRanges(f, now)
	require.NoError(t, err)
	require.False(t, partial)
	require.Equal(t, []compactObservationRange{{Table: "channel_monitor_compact_minute", Start: f.Start, End: f.End}}, parts)
	f.Start = f.End.Add(-30 * 24 * time.Hour)
	parts, partial, err = compactObservationQueryRanges(f, now)
	require.NoError(t, err)
	require.True(t, partial)
	require.Len(t, parts, 2)
	require.Equal(t, parts[0].End, parts[1].Start)
	require.Equal(t, "channel_monitor_compact_hour", parts[0].Table)
	require.Equal(t, "channel_monitor_compact_minute", parts[1].Table)
}

func TestCompactObservationSample_IsDeterministicAndSanitized(t *testing.T) {
	event := service.ChannelMonitorEvent{RequestID: "00000000-0000-0000-0000-000000000000", UserID: 123, APIKeyID: 456, SessionID: "private", Outcome: "channel_error", RequestedModel: "model", Attempts: []service.ChannelMonitorAttempt{{AccountID: 7}}}
	first, selected := compactObservationSample(event)
	require.True(t, selected)
	second, selectedAgain := compactObservationSample(event)
	require.True(t, selectedAgain)
	require.Equal(t, first, second)
	raw, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "user_id")
	require.NotContains(t, string(raw), "api_key_id")
	require.NotContains(t, string(raw), "session_id")
	require.NotContains(t, string(raw), "attempts")
}

func TestCompactObservationRows_KeepSourceAndProtocolIndependent(t *testing.T) {
	now := time.Now().UTC()
	traffic := service.ChannelMonitorEvent{Source: "traffic", CompletedAt: now, Platform: "openai", GroupID: 7, RequestedModel: "model", Protocol: "openai_chat", Outcome: "success"}
	probe := traffic
	probe.Source = "probe"
	response := traffic
	response.Protocol = "openai_responses"
	rows, _, err := compactObservationRows([]service.ChannelMonitorEvent{traffic, probe, response})
	require.NoError(t, err)
	require.Len(t, rows, 6)
	for _, row := range rows {
		var fact service.ChannelMonitorObservationFact
		require.NoError(t, json.Unmarshal(row.Facts, &fact))
		require.Equal(t, int64(1), fact.SuccessRequests)
	}
}

func TestCompactObservationRows_KeepAttemptOutcomeClassesSeparate(t *testing.T) {
	now := time.Now().UTC()
	event := service.ChannelMonitorEvent{
		CompletedAt: now, Platform: "openai", GroupID: 7, RequestedModel: "model", Protocol: "openai_chat", Outcome: "channel_error",
		Attempts: []service.ChannelMonitorAttempt{
			{AccountID: 1, Outcome: "channel_error"},
			{AccountID: 1, Outcome: "client_error"},
			{AccountID: 1, Outcome: "cancelled"},
		},
	}
	_, rows, err := compactObservationRows([]service.ChannelMonitorEvent{event})
	require.NoError(t, err)
	for _, row := range rows {
		if row.Seconds != 60 {
			continue
		}
		var fact compactObservationAttemptFact
		require.NoError(t, json.Unmarshal(row.Facts, &fact))
		require.Zero(t, fact.FailedAttempts, "new client and cancellation attempts are not channel failures")
		require.EqualValues(t, 1, fact.ChannelErrorAttempts)
		require.EqualValues(t, 1, fact.ClientErrorAttempts)
		require.EqualValues(t, 1, fact.CancelledAttempts)
	}
}

func compactTestEvent() service.ChannelMonitorEvent {
	now := time.Now().UTC()
	e := service.ChannelMonitorEvent{RequestID: uuid.NewString(), SessionID: uuid.NewString(), StartedAt: now.Add(-time.Second), CompletedAt: now, Platform: "openai", GroupID: 1, RequestedModel: "model", Protocol: "openai_chat", Outcome: "success", DurationMs: 1000}
	for {
		if _, selected := compactObservationSample(e); !selected {
			return e
		}
		e.RequestID = uuid.NewString()
	}
}

func TestCompactObservationStore_DuplicateDoesNotIncrementAggregates(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := NewChannelMonitorObservationRepository(db)
	e := compactTestEvent()
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO channel_monitor_compact_dedup").WillReturnRows(sqlmock.NewRows([]string{"request_id"}))
	mock.ExpectQuery("SELECT x.request_id::text").WillReturnRows(sqlmock.NewRows([]string{"request_id"}))
	mock.ExpectCommit()
	require.NoError(t, repo.StoreBatch(context.Background(), []service.ChannelMonitorEvent{e}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCompactObservationStore_AggregationFailureRollsBackDedup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := NewChannelMonitorObservationRepository(db)
	e := compactTestEvent()
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO channel_monitor_compact_dedup").WillReturnRows(sqlmock.NewRows([]string{"request_id"}).AddRow(e.RequestID))
	mock.ExpectQuery("SELECT x.request_id::text").WillReturnRows(sqlmock.NewRows([]string{"request_id"}))
	mock.ExpectExec("INSERT INTO channel_monitor_compact_minute").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO channel_monitor_compact_hour").WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	require.ErrorContains(t, repo.StoreBatch(context.Background(), []service.ChannelMonitorEvent{e}), "database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCompactObservationStore_CommittedBatchDoesNotWriteLegacyDetails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := NewChannelMonitorObservationRepository(db)
	e := compactTestEvent()
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO channel_monitor_compact_dedup").WillReturnRows(sqlmock.NewRows([]string{"request_id"}).AddRow(e.RequestID))
	mock.ExpectQuery("SELECT x.request_id::text").WillReturnRows(sqlmock.NewRows([]string{"request_id"}))
	mock.ExpectExec("INSERT INTO channel_monitor_compact_minute").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO channel_monitor_compact_hour").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.StoreBatch(context.Background(), []service.ChannelMonitorEvent{e}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCompactObservationCoverage_HealthyIdleUsesWriterProgress(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	now := time.Now().UTC()
	f := service.ChannelMonitorV2Filter{Start: now.Add(-24 * time.Hour), End: now.Truncate(time.Minute)}
	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)
	mock.ExpectQuery("WITH spans AS").WillReturnRows(sqlmock.NewRows([]string{"started", "through", "ingested", "stale", "failed", "live", "in_flight", "unobserved", "pending"}).AddRow(now.Add(-25*time.Hour), now, nil, 0, 0, 1, 0, 0, 0))
	mock.ExpectQuery("SELECT reason,COUNT").WillReturnRows(sqlmock.NewRows([]string{"reason", "count", "lost"}))
	var coverage service.ChannelMonitorObservationCoverage
	require.NoError(t, compactObservationCoverage(context.Background(), tx, f, now, &coverage))
	require.Equal(t, "complete", coverage.State)
	require.Equal(t, "healthy", coverage.CollectorState)
	require.Nil(t, coverage.LastIngestedAt)
	require.Equal(t, now, coverage.DataThrough)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCompactObservationQuery_EmptyScopeDoesNotReadFacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	now := time.Now().UTC()
	f := service.ChannelMonitorV2Filter{Start: now.Truncate(time.Minute).Add(-24 * time.Hour), End: now.Truncate(time.Minute), Bucket: time.Minute, RestrictGroups: true}
	mock.ExpectBegin()
	mock.ExpectQuery("WITH spans AS").WillReturnRows(sqlmock.NewRows([]string{"started", "through", "ingested", "stale", "failed", "live", "in_flight", "unobserved", "pending"}).AddRow(now.Add(-25*time.Hour), now, now, 0, 0, 1, 0, 0, 0))
	mock.ExpectQuery("SELECT reason,COUNT").WillReturnRows(sqlmock.NewRows([]string{"reason", "count", "lost"}))
	mock.ExpectCommit()
	out, err := NewChannelMonitorObservationRepository(db).Query(context.Background(), f)
	require.NoError(t, err)
	require.Empty(t, out.Facts)
	require.NoError(t, mock.ExpectationsWereMet())
}
