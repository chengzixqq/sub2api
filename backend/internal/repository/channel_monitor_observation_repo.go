package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type channelMonitorObservationRepository struct {
	db            *sql.DB
	capacity      atomic.Bool
	maintenanceAt atomic.Int64
}

func NewChannelMonitorObservationRepository(db *sql.DB) service.ChannelMonitorObservationRepository {
	return &channelMonitorObservationRepository{db: db}
}

func (r *channelMonitorObservationRepository) GetConfig(ctx context.Context) (*service.ChannelMonitorObservationConfig, error) {
	var raw []byte
	var version int
	err := r.db.QueryRowContext(ctx, `SELECT version, config FROM channel_monitor_observation_config WHERE id=TRUE`).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		cfg := service.DefaultChannelMonitorObservationConfig()
		return &cfg, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg service.ChannelMonitorObservationConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	cfg.Version = version
	return &cfg, nil
}

func (r *channelMonitorObservationRepository) UpdateConfig(ctx context.Context, cfg service.ChannelMonitorObservationConfig, expected int) (*service.ChannelMonitorObservationConfig, error) {
	if err := service.ValidateChannelMonitorObservationConfig(cfg); err != nil {
		return nil, err
	}
	cfg.Version = expected + 1
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	err = r.db.QueryRowContext(ctx, `UPDATE channel_monitor_observation_config SET config=$1::jsonb,version=version+1,updated_at=NOW() WHERE id=TRUE AND version=$2 RETURNING version`, raw, expected).Scan(&cfg.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrChannelMonitorV2ConfigConflict
	}
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

type observationEventRow struct {
	RequestID   string          `json:"request_id"`
	SessionID   string          `json:"session_id"`
	CompletedAt time.Time       `json:"completed_at"`
	Platform    string          `json:"platform"`
	GroupID     int64           `json:"group_id"`
	Model       string          `json:"model"`
	Fingerprint string          `json:"fingerprint"`
	Facts       json.RawMessage `json:"facts"`
}
type observationAttemptRow struct {
	RequestID string          `json:"request_id"`
	Sequence  int             `json:"sequence"`
	Facts     json.RawMessage `json:"facts"`
}
type observationAggregateRow struct {
	BucketSeconds int64           `json:"bucket_seconds"`
	BucketStart   time.Time       `json:"bucket_start"`
	Platform      string          `json:"platform"`
	GroupID       int64           `json:"group_id"`
	Model         string          `json:"model"`
	Facts         json.RawMessage `json:"facts"`
}

var observationRetention = []struct {
	seconds   int64
	retention time.Duration
}{
	{60, 7 * 24 * time.Hour}, {300, 7 * 24 * time.Hour}, {3600, 30 * 24 * time.Hour}, {43200, 45 * 24 * time.Hour}, {86400, 90 * 24 * time.Hour},
}

func (r *channelMonitorObservationRepository) StoreBatch(ctx context.Context, events []service.ChannelMonitorEvent) error {
	if len(events) == 0 {
		return nil
	}
	if len(events) > 512 {
		return errors.New("observation batch exceeds limit")
	}
	if r.capacity.Load() {
		return service.ErrChannelMonitorObservationCapacity
	}
	now := time.Now().UTC()
	rows := make([]observationEventRow, 0, len(events))
	byID := make(map[string]service.ChannelMonitorEvent, len(events))
	var localGaps []service.ChannelMonitorObservationGap
	for _, original := range events {
		e, err := service.NormalizeChannelMonitorEvent(original, now)
		if err != nil {
			return err
		}
		if _, err = uuid.Parse(e.SessionID); err != nil {
			return errors.New("invalid observation session UUID")
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(raw)
		if previous, exists := byID[e.RequestID]; exists {
			prev, _ := json.Marshal(previous)
			if string(prev) != string(raw) {
				localGaps = append(localGaps, service.ChannelMonitorObservationGap{StartedAt: e.CompletedAt, EndedAt: e.CompletedAt.Add(time.Nanosecond), Reason: "terminal_conflict", LostEvents: 1})
			}
			continue
		}
		byID[e.RequestID] = e
		detail := e
		detail.Attempts = nil
		detailRaw, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		rows = append(rows, observationEventRow{RequestID: e.RequestID, SessionID: e.SessionID, CompletedAt: e.CompletedAt, Platform: e.Platform, GroupID: e.GroupID, Model: e.RequestedModel, Fingerprint: hex.EncodeToString(hash[:]), Facts: detailRaw})
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	insertedRows, err := tx.QueryContext(ctx, `WITH input AS (
 SELECT * FROM jsonb_to_recordset($1::jsonb) AS x(request_id UUID,session_id UUID,completed_at TIMESTAMPTZ,platform TEXT,group_id BIGINT,model TEXT,fingerprint TEXT,facts JSONB)
), inserted AS (
 INSERT INTO channel_monitor_observation_events(request_id,session_id,completed_at,platform,group_id,model,fingerprint,facts)
 SELECT request_id,session_id,completed_at,platform,group_id,model,fingerprint,facts FROM input
 ON CONFLICT(request_id) DO NOTHING RETURNING request_id
) SELECT i.request_id::text,TRUE AS inserted,FALSE AS conflict FROM inserted i
UNION ALL SELECT i.request_id::text,FALSE AS inserted,(e.fingerprint<>i.fingerprint) AS conflict
FROM input i JOIN channel_monitor_observation_events e USING(request_id)`, payload)
	if err != nil {
		return err
	}
	inserted := map[string]bool{}
	for insertedRows.Next() {
		var id string
		var isNew, conflict bool
		if err = insertedRows.Scan(&id, &isNew, &conflict); err != nil {
			_ = insertedRows.Close()
			return err
		}
		if isNew {
			inserted[id] = true
		}
		if conflict {
			e := byID[id]
			localGaps = append(localGaps, service.ChannelMonitorObservationGap{StartedAt: e.CompletedAt, EndedAt: e.CompletedAt.Add(time.Nanosecond), Reason: "terminal_conflict", LostEvents: 1})
		}
	}
	err = insertedRows.Err()
	_ = insertedRows.Close()
	if err != nil {
		return err
	}
	// Detail inserts and all retained tiers share the transaction; a replay cannot add counters twice.
	groups := map[string]*observationAggregateRow{}
	groupFacts := map[string]*service.ChannelMonitorObservationFact{}
	attemptRows := []observationAttemptRow{}
	for id := range inserted {
		e := byID[id]
		for _, a := range e.Attempts {
			raw, marshalErr := json.Marshal(a)
			if marshalErr != nil {
				return marshalErr
			}
			attemptRows = append(attemptRows, observationAttemptRow{RequestID: id, Sequence: a.Sequence, Facts: raw})
		}
		for _, tier := range observationRetention {
			fact := service.ChannelMonitorObservationFactFromEvent(e, time.Duration(tier.seconds)*time.Second)
			key := fmt.Sprintf("%d|%s|%s|%d|%s", tier.seconds, fact.BucketStart.Format(time.RFC3339), e.Platform, e.GroupID, e.RequestedModel)
			if groupFacts[key] == nil {
				groupFacts[key] = &service.ChannelMonitorObservationFact{}
				groups[key] = &observationAggregateRow{BucketSeconds: tier.seconds, BucketStart: fact.BucketStart, Platform: e.Platform, GroupID: e.GroupID, Model: e.RequestedModel}
			}
			groupFacts[key].Add(fact)
		}
	}
	if len(attemptRows) > 0 {
		raw, marshalErr := json.Marshal(attemptRows)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_observation_attempts(request_id,sequence,facts) SELECT request_id,sequence,facts FROM jsonb_to_recordset($1::jsonb) AS x(request_id UUID,sequence SMALLINT,facts JSONB) ON CONFLICT DO NOTHING`, raw); err != nil {
			return err
		}
	}
	aggregates := make([]observationAggregateRow, 0, len(groups))
	for key, row := range groups {
		raw, marshalErr := observationNumericFacts(*groupFacts[key])
		if marshalErr != nil {
			return marshalErr
		}
		row.Facts = raw
		aggregates = append(aggregates, *row)
	}
	// Deterministic lock order avoids deadlocks between concurrent instance batches.
	sort.Slice(aggregates, func(i, j int) bool {
		a, b := aggregates[i], aggregates[j]
		if a.BucketSeconds != b.BucketSeconds {
			return a.BucketSeconds < b.BucketSeconds
		}
		if !a.BucketStart.Equal(b.BucketStart) {
			return a.BucketStart.Before(b.BucketStart)
		}
		if a.Platform != b.Platform {
			return a.Platform < b.Platform
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		return a.Model < b.Model
	})
	if len(aggregates) > 0 {
		raw, marshalErr := json.Marshal(aggregates)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_observation_aggregates(bucket_seconds,bucket_start,platform,group_id,model,facts)
 SELECT bucket_seconds,bucket_start,platform,group_id,model,facts FROM jsonb_to_recordset($1::jsonb) AS x(bucket_seconds INTEGER,bucket_start TIMESTAMPTZ,platform TEXT,group_id BIGINT,model TEXT,facts JSONB)
 ORDER BY bucket_seconds,bucket_start,platform,group_id,model
 ON CONFLICT(bucket_seconds,bucket_start,platform,group_id,model) DO UPDATE SET facts=channel_monitor_observation_sum(channel_monitor_observation_aggregates.facts,EXCLUDED.facts),computed_at=NOW()`, raw); err != nil {
			return err
		}
	}
	if len(localGaps) > 0 {
		if err = observationWriteGaps(ctx, tx, events[0].SessionID, localGaps); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func observationNumericFacts(f service.ChannelMonitorObservationFact) (json.RawMessage, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "bucket_start")
	delete(m, "platform")
	delete(m, "group_id")
	delete(m, "model")
	return json.Marshal(m)
}

func (r *channelMonitorObservationRepository) RecordGaps(ctx context.Context, session string, gaps []service.ChannelMonitorObservationGap) error {
	return observationWriteGaps(ctx, r.db, session, gaps)
}
func observationWriteGaps(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, session string, gaps []service.ChannelMonitorObservationGap) error {
	if _, err := uuid.Parse(session); err != nil {
		return err
	}
	for _, g := range gaps {
		switch g.Reason {
		case "queue_loss", "write_failed", "capacity", "terminal_conflict", "heartbeat_failed", "unsupported_protocol", "shutdown_loss":
		default:
			return errors.New("invalid observation gap reason")
		}
		if g.StartedAt.IsZero() || g.EndedAt.Before(g.StartedAt) || g.LostEvents < 0 {
			return errors.New("invalid observation gap")
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO channel_monitor_observation_gaps(session_id,started_at,ended_at,reason,lost_events) VALUES($1,$2,$3,$4,$5) ON CONFLICT(session_id,started_at,reason) DO UPDATE SET ended_at=GREATEST(channel_monitor_observation_gaps.ended_at,EXCLUDED.ended_at),lost_events=GREATEST(channel_monitor_observation_gaps.lost_events,EXCLUDED.lost_events)`, session, g.StartedAt, g.EndedAt, g.Reason, g.LostEvents); err != nil {
			return err
		}
	}
	return nil
}

func (r *channelMonitorObservationRepository) Heartbeat(ctx context.Context, s service.ChannelMonitorObservationSession) error {
	if _, err := uuid.Parse(s.ID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO channel_monitor_observation_sessions(id,started_at,heartbeat_at,ended_at,in_flight,dropped_events) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET heartbeat_at=EXCLUDED.heartbeat_at,ended_at=EXCLUDED.ended_at,in_flight=EXCLUDED.in_flight,dropped_events=EXCLUDED.dropped_events`, s.ID, s.StartedAt, s.HeartbeatAt, s.EndedAt, s.InFlight, s.DroppedEvents)
	return err
}

func (r *channelMonitorObservationRepository) Maintain(ctx context.Context, now time.Time) error {
	now = now.UTC()
	previous := r.maintenanceAt.Load()
	if now.Unix()-previous < 60 {
		return nil
	}
	if !r.maintenanceAt.CompareAndSwap(previous, now.Unix()) {
		return nil
	}
	// Bounded deletes avoid long blocking cleanup transactions on the gateway pool.
	queries := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM channel_monitor_observation_events WHERE request_id IN (SELECT request_id FROM channel_monitor_observation_events WHERE completed_at<$1 AND aggregated_at IS NOT NULL ORDER BY completed_at LIMIT 20000)`, []any{now.Add(-72 * time.Hour)}},
		{`DELETE FROM channel_monitor_observation_gaps WHERE ended_at<$1`, []any{now.Add(-90 * 24 * time.Hour)}},
		{`DELETE FROM channel_monitor_observation_sessions WHERE COALESCE(ended_at,heartbeat_at)<$1`, []any{now.Add(-90 * 24 * time.Hour)}},
	}
	for _, tier := range observationRetention {
		queries = append(queries, struct {
			q    string
			args []any
		}{`DELETE FROM channel_monitor_observation_aggregates WHERE ctid IN (SELECT ctid FROM channel_monitor_observation_aggregates WHERE bucket_seconds=$1 AND bucket_start<$2 LIMIT 20000)`, []any{tier.seconds, now.Add(-tier.retention).Truncate(time.Duration(tier.seconds) * time.Second)}})
	}
	for _, q := range queries {
		if _, err := r.db.ExecContext(ctx, q.q, q.args...); err != nil {
			r.maintenanceAt.Store(previous)
			return err
		}
	}
	var bytes int64
	if err := r.db.QueryRowContext(ctx, `SELECT pg_total_relation_size('channel_monitor_observation_events')+pg_total_relation_size('channel_monitor_observation_attempts')+pg_total_relation_size('channel_monitor_observation_aggregates')`).Scan(&bytes); err != nil {
		return err
	}
	r.capacity.Store(bytes >= 8*1024*1024*1024)
	return nil
}

func (r *channelMonitorObservationRepository) Query(ctx context.Context, filter service.ChannelMonitorV2Filter) (*service.ChannelMonitorObservationSnapshot, error) {
	if filter.Start.IsZero() || !filter.End.After(filter.Start) || filter.End.Sub(filter.Start) > 90*24*time.Hour || filter.Bucket < time.Minute {
		return nil, errors.New("invalid observation range")
	}
	now := time.Now().UTC()
	snapshot := &service.ChannelMonitorObservationSnapshot{Facts: []service.ChannelMonitorObservationFact{}}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = observationLoadCoverage(ctx, tx, filter, now, &snapshot.Coverage); err != nil {
		return nil, err
	}
	if filter.RestrictGroups && len(filter.AllowedGroupIDs) == 0 {
		return snapshot, tx.Commit()
	}
	tier := observationQueryTier(filter, now)
	sourceBucket := time.Duration(tier) * time.Second
	startFull := filter.Start.UTC().Truncate(sourceBucket)
	if startFull.Before(filter.Start) {
		startFull = startFull.Add(sourceBucket)
	}
	endFull := filter.End.UTC().Truncate(sourceBucket)
	if endFull.Before(startFull) {
		endFull = startFull
	}
	args := []any{tier, startFull, endFull}
	where := observationWhere(filter, &args)
	rows, err := tx.QueryContext(ctx, `SELECT bucket_start,platform,group_id,model,facts FROM channel_monitor_observation_aggregates WHERE bucket_seconds=$1 AND bucket_start>=$2 AND bucket_start<$3`+where, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f service.ChannelMonitorObservationFact
		var raw []byte
		if err = rows.Scan(&f.BucketStart, &f.Platform, &f.GroupID, &f.Model, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		bucket, platform, groupID, model := f.BucketStart, f.Platform, f.GroupID, f.Model
		if err = json.Unmarshal(raw, &f); err != nil {
			_ = rows.Close()
			return nil, err
		}
		f.BucketStart = bucket.UTC().Truncate(filter.Bucket)
		f.Platform = platform
		f.GroupID = groupID
		f.Model = model
		snapshot.Facts = append(snapshot.Facts, f)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	// Only boundary fragments read detail; sealed middle buckets never rebuild from expired events.
	intervals := [][2]time.Time{}
	if filter.Start.Before(startFull) {
		end := startFull
		if end.After(filter.End) {
			end = filter.End
		}
		intervals = append(intervals, [2]time.Time{filter.Start, end})
	}
	if endFull.Before(filter.End) && !endFull.Before(startFull) {
		begin := endFull
		if begin.Before(filter.Start) {
			begin = filter.Start
		}
		intervals = append(intervals, [2]time.Time{begin, filter.End})
	}
	for _, interval := range intervals {
		if !interval[1].After(interval[0]) {
			continue
		}
		if interval[0].Before(now.Add(-72 * time.Hour)) {
			snapshot.Coverage.State = "partial"
			continue
		}
		args = []any{interval[0], interval[1]}
		where = observationWhere(filter, &args)
		rows, err = tx.QueryContext(ctx, `SELECT facts FROM channel_monitor_observation_events WHERE completed_at>=$1 AND completed_at<$2`+where, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				_ = rows.Close()
				return nil, err
			}
			var e service.ChannelMonitorEvent
			if err = json.Unmarshal(raw, &e); err != nil {
				_ = rows.Close()
				return nil, err
			}
			snapshot.Facts = append(snapshot.Facts, service.ChannelMonitorObservationFactFromEvent(e, filter.Bucket))
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func observationQueryTier(f service.ChannelMonitorV2Filter, now time.Time) int64 {
	for _, tier := range observationRetention {
		if !f.Start.Before(now.Add(-tier.retention)) {
			return tier.seconds
		}
	}
	return 86400
}
func observationWhere(f service.ChannelMonitorV2Filter, args *[]any) string {
	where := ""
	add := func(column string, value any) {
		*args = append(*args, value)
		where += fmt.Sprintf(" AND %s=ANY($%d)", column, len(*args))
	}
	if len(f.Platforms) > 0 {
		add("platform", pq.Array(f.Platforms))
	}
	if len(f.GroupIDs) > 0 {
		add("group_id", pq.Array(f.GroupIDs))
	}
	if f.RestrictGroups {
		add("group_id", pq.Array(f.AllowedGroupIDs))
	}
	if len(f.Models) > 0 {
		add("model", pq.Array(f.Models))
	}
	return where
}

func observationLoadCoverage(ctx context.Context, tx *sql.Tx, f service.ChannelMonitorV2Filter, now time.Time, c *service.ChannelMonitorObservationCoverage) error {
	c.State = "unavailable"
	c.UnsupportedProtocols = []string{"websocket", "async_task"}
	var started, through sql.NullTime
	var stale int64
	err := tx.QueryRowContext(ctx, `SELECT MIN(started_at),MAX(heartbeat_at),COALESCE(SUM(CASE WHEN ended_at IS NULL AND heartbeat_at<$3 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN ended_at IS NULL AND heartbeat_at>=$3 THEN in_flight ELSE 0 END),0) FROM channel_monitor_observation_sessions WHERE started_at<$2 AND COALESCE(ended_at,$2)>=$1`, f.Start, f.End, now.Add(-45*time.Second)).Scan(&started, &through, &stale, &c.InFlight)
	if err != nil {
		return err
	}
	if started.Valid {
		value := started.Time.UTC()
		c.SourceStartedAt = &value
	}
	if through.Valid {
		c.DataThrough = through.Time.UTC()
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(lost_events),0) FROM channel_monitor_observation_gaps WHERE started_at<$2 AND ended_at>$1`, f.Start, f.End).Scan(&c.GapCount, &c.LostEvents)
	if err != nil {
		return err
	}
	if !started.Valid {
		return nil
	}
	c.State = "complete"
	if started.Time.After(f.Start) || c.GapCount > 0 || stale > 0 {
		c.State = "partial"
	}
	if f.End.After(now.Add(-45*time.Second)) && (!through.Valid || through.Time.Before(now.Add(-45*time.Second))) {
		c.State = "stale"
	}
	return nil
}
