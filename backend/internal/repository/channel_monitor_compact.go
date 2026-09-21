package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const compactObservationDedupRetention = 25 * time.Hour

type compactObservationRepository struct {
	*channelMonitorObservationRepository
}

func (r *compactObservationRepository) GetConfig(ctx context.Context) (*service.ChannelMonitorObservationConfig, error) {
	var raw []byte
	var version int
	err := r.db.QueryRowContext(ctx, `SELECT version, config FROM channel_monitor_compact_config WHERE id=TRUE`).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		cfg := service.DefaultChannelMonitorObservationConfig()
		return &cfg, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := service.DefaultChannelMonitorObservationConfig()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	cfg.Version = version
	return &cfg, nil
}

func (r *compactObservationRepository) UpdateConfig(ctx context.Context, cfg service.ChannelMonitorObservationConfig, expected int) (*service.ChannelMonitorObservationConfig, error) {
	if err := service.ValidateChannelMonitorObservationConfig(cfg); err != nil {
		return nil, err
	}
	cfg.Version = expected + 1
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	err = r.db.QueryRowContext(ctx, `UPDATE channel_monitor_compact_config SET config=$1::jsonb,version=version+1,updated_at=NOW() WHERE id=TRUE AND version=$2 RETURNING version`, raw, expected).Scan(&cfg.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrChannelMonitorV2ConfigConflict
	}
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

type compactObservationRow struct {
	BucketStart time.Time       `json:"bucket_start"`
	Source      string          `json:"source"`
	Platform    string          `json:"platform"`
	Protocol    string          `json:"protocol"`
	GroupID     int64           `json:"group_id"`
	Model       string          `json:"model"`
	AccountID   int64           `json:"account_id"`
	Facts       json.RawMessage `json:"facts"`
	Seconds     int64           `json:"-"`
}

type compactObservationAttemptFact struct {
	AttemptCount         int64 `json:"attempt_count"`
	SuccessAttempts      int64 `json:"success_attempts"`
	ChannelErrorAttempts int64 `json:"channel_error_attempts"`
	ClientErrorAttempts  int64 `json:"client_error_attempts"`
	CancelledAttempts    int64 `json:"cancelled_attempts"`
	FailedAttempts       int64 `json:"failed_attempts"`
	UnknownAttempts      int64 `json:"unknown_attempts"`
	DurationSumMs        int64 `json:"duration_sum_ms"`
}

func compactObservationRows(events []service.ChannelMonitorEvent) ([]compactObservationRow, []compactObservationRow, error) {
	type key struct {
		time     time.Time
		seconds  int64
		source   string
		platform string
		protocol string
		group    int64
		model    string
		account  int64
	}
	facts := map[key]*service.ChannelMonitorObservationFact{}
	attempts := map[key]*compactObservationAttemptFact{}
	for _, e := range events {
		source := e.Source
		if source == "" {
			source = "traffic"
		}
		if source != "traffic" && source != "probe" {
			return nil, nil, errors.New("invalid observation source")
		}
		for _, seconds := range []int64{60, 3600} {
			k := key{time: e.CompletedAt.UTC().Truncate(time.Duration(seconds) * time.Second), seconds: seconds, source: source, platform: e.Platform, protocol: e.Protocol, group: e.GroupID, model: e.RequestedModel}
			if facts[k] == nil {
				facts[k] = &service.ChannelMonitorObservationFact{}
			}
			facts[k].Add(service.ChannelMonitorObservationFactFromEvent(e, time.Duration(seconds)*time.Second))
			for _, attempt := range e.Attempts {
				if attempt.AccountID <= 0 {
					continue
				}
				aKey := k
				aKey.account = attempt.AccountID
				if attempts[aKey] == nil {
					attempts[aKey] = &compactObservationAttemptFact{}
				}
				a := attempts[aKey]
				a.AttemptCount++
				a.DurationSumMs += attempt.DurationMs
				switch attempt.Outcome {
				case "success":
					a.SuccessAttempts++
				case "channel_error":
					a.ChannelErrorAttempts++
				case "client_error":
					a.ClientErrorAttempts++
				case "cancelled":
					a.CancelledAttempts++
				default:
					a.UnknownAttempts++
				}
			}
		}
	}
	makeRow := func(k key, raw json.RawMessage) compactObservationRow {
		return compactObservationRow{BucketStart: k.time, Seconds: k.seconds, Source: k.source, Platform: k.platform, Protocol: k.protocol, GroupID: k.group, Model: k.model, AccountID: k.account, Facts: raw}
	}
	rows := make([]compactObservationRow, 0, len(facts))
	for k, f := range facts {
		raw, err := observationNumericFacts(*f)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, makeRow(k, raw))
	}
	accountRows := make([]compactObservationRow, 0, len(attempts))
	for k, f := range attempts {
		raw, err := json.Marshal(f)
		if err != nil {
			return nil, nil, err
		}
		accountRows = append(accountRows, makeRow(k, raw))
	}
	return rows, accountRows, nil
}

func (r *compactObservationRepository) StoreBatch(ctx context.Context, events []service.ChannelMonitorEvent) error {
	if len(events) == 0 {
		return nil
	}
	if len(events) > 512 {
		return errors.New("observation batch exceeds limit")
	}
	now := time.Now().UTC()
	type dedupRow struct {
		RequestID   string `json:"request_id"`
		Fingerprint string `json:"fingerprint"`
	}
	input := make([]dedupRow, 0, len(events))
	byID := make(map[string]service.ChannelMonitorEvent, len(events))
	var gaps []service.ChannelMonitorObservationGap
	for _, original := range events {
		e, err := service.NormalizeChannelMonitorEvent(original, now)
		if err != nil {
			return err
		}
		if e.CompletedAt.Before(now.Add(-24 * time.Hour)) {
			return errors.New("observation replay exceeds 24 hours")
		}
		if _, err := uuid.Parse(e.SessionID); err != nil {
			return errors.New("invalid observation session UUID")
		}
		requestID, _ := uuid.Parse(e.RequestID)
		e.RequestID = requestID.String()
		if e.Source == "" {
			e.Source = "traffic"
		}
		raw, err := compactObservationFingerprint(e)
		if err != nil {
			return err
		}
		if previous, ok := byID[e.RequestID]; ok {
			previousHash, _ := compactObservationFingerprint(previous)
			if raw != previousHash {
				gaps = append(gaps, compactObservationConflict(e))
			}
			continue
		}
		byID[e.RequestID] = e
		input = append(input, dedupRow{RequestID: e.RequestID, Fingerprint: raw})
	}
	sort.Slice(input, func(i, j int) bool { return input[i].RequestID < input[j].RequestID })
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `INSERT INTO channel_monitor_compact_dedup(request_id,fingerprint)
 SELECT request_id,decode(fingerprint,'hex') FROM jsonb_to_recordset($1::jsonb) AS x(request_id UUID,fingerprint TEXT)
 ORDER BY request_id ON CONFLICT(request_id) DO NOTHING RETURNING request_id::text`, payload)
	if err != nil {
		return err
	}
	inserted := make([]service.ChannelMonitorEvent, 0, len(input))
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		inserted = append(inserted, byID[id])
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	// A separate statement observes a concurrent transaction that won an UUID conflict.
	rows, err = tx.QueryContext(ctx, `SELECT x.request_id::text FROM jsonb_to_recordset($1::jsonb) AS x(request_id UUID,fingerprint TEXT) JOIN channel_monitor_compact_dedup d USING(request_id) WHERE d.fingerprint<>decode(x.fingerprint,'hex')`, payload)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		gaps = append(gaps, compactObservationConflict(byID[id]))
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	metrics, accounts, err := compactObservationRows(inserted)
	if err != nil {
		return err
	}
	if err = compactObservationWriteRows(ctx, tx, metrics, false); err != nil {
		return err
	}
	if err = compactObservationWriteRows(ctx, tx, accounts, true); err != nil {
		return err
	}
	if err = compactObservationWriteGaps(ctx, tx, events[0].SessionID, gaps); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Samples are diagnostic, and cannot make a committed aggregate look failed.
	if len(inserted) > 0 {
		if err := r.storeSamples(ctx, inserted); err != nil {
			slog.Warn("channel_monitor: compact samples not stored", "error", err)
		}
	}
	return nil
}

func compactObservationFingerprint(e service.ChannelMonitorEvent) (string, error) {
	// Collector sessions may change during a legitimate replay.
	e.SessionID = ""
	raw, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func compactObservationConflict(e service.ChannelMonitorEvent) service.ChannelMonitorObservationGap {
	return service.ChannelMonitorObservationGap{StartedAt: e.CompletedAt, EndedAt: e.CompletedAt.Add(time.Microsecond), Reason: "terminal_conflict", LostEvents: 1}
}

func compactObservationWriteRows(ctx context.Context, tx *sql.Tx, rows []compactObservationRow, attempts bool) error {
	for _, seconds := range []int64{60, 3600} {
		selected := make([]compactObservationRow, 0, len(rows))
		for _, row := range rows {
			if row.Seconds == seconds {
				selected = append(selected, row)
			}
		}
		if len(selected) == 0 {
			continue
		}
		table := "channel_monitor_compact_"
		if attempts {
			table += "attempt_"
		}
		if seconds == 60 {
			table += "minute"
		} else {
			table += "hour"
		}
		columns := "bucket_start,source,platform,group_id,model,protocol"
		if attempts {
			columns += ",account_id"
		}
		payload, err := json.Marshal(selected)
		if err != nil {
			return err
		}
		query := fmt.Sprintf(`INSERT INTO %s(%s,facts) SELECT %s,facts FROM jsonb_to_recordset($1::jsonb) AS x(bucket_start TIMESTAMPTZ,source TEXT,platform TEXT,group_id BIGINT,model TEXT,protocol TEXT,account_id BIGINT,facts JSONB) ORDER BY %s ON CONFLICT(%s) DO UPDATE SET facts=channel_monitor_observation_sum(%s.facts,EXCLUDED.facts),computed_at=NOW()`, table, columns, columns, columns, columns, table)
		if _, err = tx.ExecContext(ctx, query, payload); err != nil {
			return err
		}
	}
	return nil
}

type compactObservationSampleRow struct {
	RequestID     string    `json:"request_id"`
	CompletedAt   time.Time `json:"completed_at"`
	Source        string    `json:"source"`
	GroupID       int64     `json:"group_id"`
	Platform      string    `json:"platform"`
	Model         string    `json:"model"`
	Protocol      string    `json:"protocol"`
	Outcome       string    `json:"outcome"`
	ErrorCategory string    `json:"error_category,omitempty"`
	HTTPStatus    int       `json:"http_status"`
	DurationMs    int64     `json:"duration_ms"`
}

func compactObservationSample(e service.ChannelMonitorEvent) (compactObservationSampleRow, bool) {
	if e.Outcome == "success" {
		h := sha256.Sum256([]byte(e.RequestID))
		if (uint16(h[0])<<8|uint16(h[1]))%100 != 0 {
			return compactObservationSampleRow{}, false
		}
	}
	source := e.Source
	if source == "" {
		source = "traffic"
	}
	return compactObservationSampleRow{RequestID: e.RequestID, CompletedAt: e.CompletedAt, Source: source, GroupID: e.GroupID, Platform: e.Platform, Model: e.RequestedModel, Protocol: e.Protocol, Outcome: e.Outcome, ErrorCategory: e.ErrorCategory, HTTPStatus: e.HTTPStatus, DurationMs: e.DurationMs}, true
}

func (r *compactObservationRepository) storeSamples(ctx context.Context, events []service.ChannelMonitorEvent) error {
	type sampleGroup struct {
		day  string
		kind string
	}
	groups := map[sampleGroup][]compactObservationSampleRow{}
	for _, e := range events {
		if sample, ok := compactObservationSample(e); ok {
			kind := "failure"
			if e.Outcome == "success" {
				kind = "success"
			}
			key := sampleGroup{e.CompletedAt.UTC().Format("2006-01-02"), kind}
			groups[key] = append(groups[key], sample)
		}
	}
	keys := make([]sampleGroup, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].day != keys[j].day {
			return keys[i].day < keys[j].day
		}
		return keys[i].kind < keys[j].kind
	})
	if len(keys) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, k := range keys {
		limit := 8000
		if k.kind == "success" {
			limit = 2000
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_compact_sample_budgets(day,kind) VALUES($1,$2) ON CONFLICT DO NOTHING`, k.day, k.kind); err != nil {
			return err
		}
		var used int
		if err = tx.QueryRowContext(ctx, `SELECT used FROM channel_monitor_compact_sample_budgets WHERE day=$1 AND kind=$2 FOR UPDATE`, k.day, k.kind).Scan(&used); err != nil {
			return err
		}
		count := min(len(groups[k]), max(0, limit-used))
		if count == 0 {
			continue
		}
		for _, sample := range groups[k][:count] {
			raw, marshalErr := json.Marshal(sample)
			if marshalErr != nil {
				return marshalErr
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_compact_samples(request_id,completed_at,source,group_id,kind,facts) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT DO NOTHING`, sample.RequestID, sample.CompletedAt, sample.Source, sample.GroupID, k.kind, raw); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE channel_monitor_compact_sample_budgets SET used=used+$3 WHERE day=$1 AND kind=$2`, k.day, k.kind, count); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *compactObservationRepository) RecordGaps(ctx context.Context, session string, gaps []service.ChannelMonitorObservationGap) error {
	return compactObservationWriteGaps(ctx, r.db, session, gaps)
}

func compactObservationWriteGaps(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, session string, gaps []service.ChannelMonitorObservationGap) error {
	if len(gaps) == 0 {
		return nil
	}
	if _, err := uuid.Parse(session); err != nil {
		return err
	}
	for _, gap := range gaps {
		switch gap.Reason {
		case "queue_loss", "write_failed", "capacity", "terminal_conflict", "heartbeat_failed", "unsupported_protocol", "shutdown_loss":
		default:
			return errors.New("invalid observation gap reason")
		}
		if gap.StartedAt.IsZero() || gap.EndedAt.Before(gap.StartedAt) || gap.LostEvents < 0 {
			return errors.New("invalid observation gap")
		}
		if gap.EndedAt.Sub(gap.StartedAt) < time.Microsecond {
			gap.EndedAt = gap.StartedAt.Add(time.Microsecond)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO channel_monitor_compact_gaps(session_id,started_at,ended_at,reason,lost_events) VALUES($1,$2,$3,$4,$5) ON CONFLICT(session_id,started_at,reason) DO UPDATE SET ended_at=GREATEST(channel_monitor_compact_gaps.ended_at,EXCLUDED.ended_at),lost_events=GREATEST(channel_monitor_compact_gaps.lost_events,EXCLUDED.lost_events)`, session, gap.StartedAt, gap.EndedAt, gap.Reason, gap.LostEvents); err != nil {
			return err
		}
	}
	return nil
}

func (r *compactObservationRepository) Heartbeat(ctx context.Context, s service.ChannelMonitorObservationSession) error {
	if _, err := uuid.Parse(s.ID); err != nil {
		return err
	}
	through := s.DataThrough
	if through == nil && s.LastWriteError == "" && s.PendingEvents == 0 {
		through = &s.HeartbeatAt
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO channel_monitor_compact_writer_sessions(id,started_at,heartbeat_at,data_through,last_ingested_at,last_write_error,ended_at,in_flight,dropped_events,pending_events)
 VALUES($1,$2,$3::timestamptz,$9,$4,$5::boolean,$6,$7,$8,$10)
 ON CONFLICT(id) DO UPDATE SET heartbeat_at=EXCLUDED.heartbeat_at,data_through=CASE WHEN EXCLUDED.last_write_error OR EXCLUDED.pending_events>0 THEN channel_monitor_compact_writer_sessions.data_through ELSE COALESCE(EXCLUDED.data_through,channel_monitor_compact_writer_sessions.data_through) END,last_ingested_at=COALESCE(EXCLUDED.last_ingested_at,channel_monitor_compact_writer_sessions.last_ingested_at),last_write_error=EXCLUDED.last_write_error,ended_at=EXCLUDED.ended_at,in_flight=EXCLUDED.in_flight,dropped_events=EXCLUDED.dropped_events,pending_events=EXCLUDED.pending_events`, s.ID, s.StartedAt, s.HeartbeatAt, s.LastIngestedAt, s.LastWriteError != "", s.EndedAt, s.InFlight, s.DroppedEvents, through, s.PendingEvents)
	return err
}

func (r *compactObservationRepository) Maintain(ctx context.Context, now time.Time) error {
	now = now.UTC()
	previous := r.maintenanceAt.Load()
	if now.Unix()-previous < 60 || !r.maintenanceAt.CompareAndSwap(previous, now.Unix()) {
		return nil
	}
	failed := true
	defer func() {
		if failed {
			r.maintenanceAt.Store(previous)
		}
	}()
	if _, err := r.db.ExecContext(ctx, `SELECT channel_monitor_compact_ensure_partitions($1)`, now); err != nil {
		return err
	}
	for _, item := range []struct {
		table, column string
		cutoff        time.Time
	}{
		{"channel_monitor_compact_dedup", "received_at", now.Add(-compactObservationDedupRetention)},
		{"channel_monitor_compact_gaps", "ended_at", now.Add(-30 * 24 * time.Hour)},
		{"channel_monitor_compact_writer_sessions", "heartbeat_at", now.Add(-30 * 24 * time.Hour)},
	} {
		q := fmt.Sprintf(`DELETE FROM %s WHERE ctid IN (SELECT ctid FROM %s WHERE %s<$1 ORDER BY %s LIMIT 20000)`, item.table, item.table, item.column, item.column)
		if _, err := r.db.ExecContext(ctx, q, item.cutoff); err != nil {
			return err
		}
	}
	// Partition-local ctids are not globally unique. Delete by the composite
	// primary key so trimming the boundary day cannot remove newer samples.
	if _, err := r.db.ExecContext(ctx, `DELETE FROM channel_monitor_compact_samples WHERE (request_id,completed_at) IN (SELECT request_id,completed_at FROM channel_monitor_compact_samples WHERE completed_at<$1 ORDER BY completed_at LIMIT 20000)`, now.Add(-24*time.Hour)); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM channel_monitor_compact_sample_budgets WHERE day<$1::date`, now.Add(-48*time.Hour).Format("2006-01-02")); err != nil {
		return err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT parent.relname,child.relname FROM pg_inherits JOIN pg_class parent ON parent.oid=inhparent JOIN pg_class child ON child.oid=inhrelid JOIN pg_namespace n ON n.oid=child.relnamespace WHERE n.nspname=current_schema() AND parent.relname IN ('channel_monitor_compact_minute','channel_monitor_compact_hour','channel_monitor_compact_attempt_minute','channel_monitor_compact_attempt_hour','channel_monitor_compact_samples')`)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var parent, child string
		if err = rows.Scan(&parent, &child); err != nil {
			_ = rows.Close()
			return err
		}
		prefix := parent + "_"
		if !strings.HasPrefix(child, prefix) {
			continue
		}
		day, parseErr := time.Parse("20060102", strings.TrimPrefix(child, prefix))
		if parseErr != nil {
			continue
		}
		retention := 48 * time.Hour
		if strings.HasSuffix(parent, "_hour") {
			retention = 30 * 24 * time.Hour
		}
		if parent == "channel_monitor_compact_samples" {
			retention = 24 * time.Hour
		}
		if !day.Add(24 * time.Hour).After(now.Add(-retention)) {
			drop = append(drop, child)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, table := range drop {
		if _, err = r.db.ExecContext(ctx, `DROP TABLE IF EXISTS "`+table+`"`); err != nil {
			return err
		}
	}
	failed = false
	return nil
}

type compactObservationRange struct {
	Table      string
	Start, End time.Time
}

func compactObservationQueryRanges(f service.ChannelMonitorV2Filter, now time.Time) ([]compactObservationRange, bool, error) {
	if f.Start.IsZero() || !f.End.After(f.Start) || f.End.Sub(f.Start) > 30*24*time.Hour || f.Bucket < time.Minute {
		return nil, false, errors.New("invalid compact observation range")
	}
	start, end := f.Start.UTC(), f.End.UTC()
	partial := false
	if start.Before(now.Add(-30 * 24 * time.Hour)) {
		start = now.Add(-30 * 24 * time.Hour)
		partial = true
	}
	if !start.Equal(start.Truncate(time.Minute)) {
		start = start.Truncate(time.Minute).Add(time.Minute)
		partial = true
	}
	if !end.Equal(end.Truncate(time.Minute)) {
		end = end.Truncate(time.Minute)
		partial = true
	}
	cutoff := now.Add(-48 * time.Hour).Truncate(time.Hour).Add(time.Hour)
	parts := []compactObservationRange{}
	if start.Before(cutoff) {
		hourEnd := end.Truncate(time.Hour)
		if hourEnd.After(cutoff) {
			hourEnd = cutoff
		}
		hourStart := start.Truncate(time.Hour)
		if hourStart.Before(start) {
			hourStart = hourStart.Add(time.Hour)
			partial = true
		}
		if hourEnd.After(hourStart) {
			parts = append(parts, compactObservationRange{Table: "channel_monitor_compact_hour", Start: hourStart, End: hourEnd})
		}
		if end.Before(cutoff) && end.After(hourEnd) {
			partial = true
		}
		start = cutoff
	}
	if end.After(start) {
		parts = append(parts, compactObservationRange{Table: "channel_monitor_compact_minute", Start: start, End: end})
	}
	return parts, partial, nil
}

func (r *compactObservationRepository) Query(ctx context.Context, f service.ChannelMonitorV2Filter) (*service.ChannelMonitorObservationSnapshot, error) {
	return r.QuerySource(ctx, f, "traffic")
}

func (r *compactObservationRepository) QuerySource(ctx context.Context, f service.ChannelMonitorV2Filter, source string) (*service.ChannelMonitorObservationSnapshot, error) {
	if source != "traffic" && source != "probe" {
		return nil, errors.New("invalid observation source")
	}
	now := time.Now().UTC()
	parts, partial, err := compactObservationQueryRanges(f, now)
	if err != nil {
		return nil, err
	}
	snapshot := &service.ChannelMonitorObservationSnapshot{Facts: []service.ChannelMonitorObservationFact{}}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = compactObservationCoverage(ctx, tx, f, now, &snapshot.Coverage); err != nil {
		return nil, err
	}
	if partial && snapshot.Coverage.State == "complete" {
		snapshot.Coverage.State = "partial"
	}
	if f.RestrictGroups && len(f.AllowedGroupIDs) == 0 {
		return snapshot, tx.Commit()
	}
	for _, part := range parts {
		args := []any{part.Start, part.End, source}
		where := observationWhere(f, &args)
		rows, queryErr := tx.QueryContext(ctx, `SELECT bucket_start,platform,group_id,model,protocol,facts FROM `+part.Table+` WHERE bucket_start>=$1 AND bucket_start<$2 AND source=$3`+where, args...)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var fact service.ChannelMonitorObservationFact
			var raw []byte
			if err = rows.Scan(&fact.BucketStart, &fact.Platform, &fact.GroupID, &fact.Model, &fact.Protocol, &raw); err != nil {
				_ = rows.Close()
				return nil, err
			}
			bucket, platform, group, model, protocol := fact.BucketStart, fact.Platform, fact.GroupID, fact.Model, fact.Protocol
			if err = json.Unmarshal(raw, &fact); err != nil {
				_ = rows.Close()
				return nil, err
			}
			fact.BucketStart = bucket.UTC().Truncate(f.Bucket)
			fact.Platform = platform
			fact.GroupID = group
			fact.Model = model
			fact.Protocol = protocol
			fact.Source = source
			snapshot.Facts = append(snapshot.Facts, fact)
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

func compactObservationCoverage(ctx context.Context, tx *sql.Tx, f service.ChannelMonitorV2Filter, now time.Time, c *service.ChannelMonitorObservationCoverage) error {
	c.State = "unavailable"
	c.CollectorState = "unavailable"
	c.UnsupportedProtocols = []string{"batch_images", "video"}
	c.GapReasons = []string{}
	var started, through, ingested sql.NullTime
	var stale, failed, live, unobserved int64
	err := tx.QueryRowContext(ctx, `WITH spans AS (
 SELECT started_at,COALESCE(ended_at,heartbeat_at) AS observed_until FROM channel_monitor_compact_writer_sessions WHERE started_at<$2 AND COALESCE(ended_at,heartbeat_at)>=$1
), ordered AS (
 SELECT started_at,MAX(observed_until) OVER(ORDER BY started_at,observed_until ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS previous_end FROM spans
)
 SELECT MIN(started_at),COALESCE(MIN(data_through) FILTER(WHERE ended_at IS NULL AND heartbeat_at>=$3),MAX(data_through)),MAX(last_ingested_at),COALESCE(SUM(CASE WHEN ended_at IS NULL AND heartbeat_at<$3 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN ended_at IS NULL AND heartbeat_at>=$3 AND last_write_error THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN ended_at IS NULL AND heartbeat_at>=$3 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN ended_at IS NULL AND heartbeat_at>=$3 THEN in_flight ELSE 0 END),0),(SELECT COUNT(*) FROM ordered WHERE previous_end IS NOT NULL AND started_at>previous_end),COALESCE(SUM(pending_events) FILTER(WHERE ended_at IS NULL AND heartbeat_at>=$3),0) FROM channel_monitor_compact_writer_sessions WHERE started_at<$2 AND COALESCE(ended_at,heartbeat_at+INTERVAL '45 seconds')>=$1`, f.Start, f.End, now.Add(-45*time.Second)).Scan(&started, &through, &ingested, &stale, &failed, &live, &c.InFlight, &unobserved, &c.PendingEvents)
	if err != nil {
		return err
	}
	if started.Valid {
		t := started.Time.UTC()
		c.SourceStartedAt = &t
	}
	if through.Valid {
		c.DataThrough = through.Time.UTC()
	}
	if ingested.Valid {
		t := ingested.Time.UTC()
		c.LastIngestedAt = &t
	}
	rows, err := tx.QueryContext(ctx, `SELECT reason,COUNT(*),COALESCE(SUM(lost_events),0) FROM channel_monitor_compact_gaps WHERE started_at<$2 AND ended_at>$1 GROUP BY reason ORDER BY reason`, f.Start, f.End)
	if err != nil {
		return err
	}
	for rows.Next() {
		var reason string
		var count, lost int64
		if err = rows.Scan(&reason, &count, &lost); err != nil {
			_ = rows.Close()
			return err
		}
		c.GapReasons = append(c.GapReasons, reason)
		c.GapCount += count
		c.LostEvents += lost
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if unobserved > 0 {
		c.GapCount += unobserved
		c.GapReasons = append(c.GapReasons, "collector_unobserved")
	}
	if !started.Valid {
		return nil
	}
	c.State = "complete"
	c.CollectorState = "healthy"
	if started.Time.After(f.Start) || c.GapCount > 0 || stale > 0 {
		c.State = "partial"
	}
	if failed > 0 {
		c.State = "partial"
		c.CollectorState = "write_failed"
	} else if c.PendingEvents > 0 {
		c.State = "partial"
		c.CollectorState = "backlogged"
		c.GapReasons = append(c.GapReasons, "collector_backlog")
	} else if live == 0 {
		c.CollectorState = "stale"
	}
	if f.End.After(now.Add(-2*time.Minute)) && (!through.Valid || through.Time.Before(now.Add(-45*time.Second))) {
		c.State = "stale"
	}
	return nil
}

func (r *compactObservationRepository) QueryAccounts(ctx context.Context, f service.ChannelMonitorV2Filter) ([]service.ChannelMonitorAccountObservationFact, error) {
	parts, _, err := compactObservationQueryRanges(f, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	out := []service.ChannelMonitorAccountObservationFact{}
	if f.RestrictGroups && len(f.AllowedGroupIDs) == 0 {
		return out, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, part := range parts {
		table := strings.Replace(part.Table, "compact_", "compact_attempt_", 1)
		args := []any{part.Start, part.End}
		where := observationWhere(f, &args)
		rows, queryErr := tx.QueryContext(ctx, `SELECT bucket_start,platform,group_id,model,account_id,facts FROM `+table+` WHERE bucket_start>=$1 AND bucket_start<$2 AND source='traffic'`+where, args...)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var fact service.ChannelMonitorAccountObservationFact
			var raw []byte
			if err = rows.Scan(&fact.BucketStart, &fact.Platform, &fact.GroupID, &fact.Model, &fact.AccountID, &raw); err != nil {
				_ = rows.Close()
				return nil, err
			}
			var counters compactObservationAttemptFact
			if err = json.Unmarshal(raw, &counters); err != nil {
				_ = rows.Close()
				return nil, err
			}
			fact.Source = "traffic"
			fact.BucketStart = fact.BucketStart.UTC().Truncate(f.Bucket)
			fact.AttemptCount = counters.AttemptCount
			fact.SuccessAttempts = counters.SuccessAttempts
			fact.FailedAttempts = counters.FailedAttempts
			fact.ChannelErrorAttempts = counters.ChannelErrorAttempts
			fact.ClientErrorAttempts = counters.ClientErrorAttempts
			fact.CancelledAttempts = counters.CancelledAttempts
			fact.UnclassifiedAttempts = counters.FailedAttempts
			fact.UnknownAttempts = counters.UnknownAttempts
			fact.DurationSumMs = counters.DurationSumMs
			out = append(out, fact)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func (r *compactObservationRepository) QuerySamples(ctx context.Context, f service.ChannelMonitorV2Filter) ([]service.ChannelMonitorObservationSample, error) {
	now := time.Now().UTC()
	if _, _, err := compactObservationQueryRanges(f, now); err != nil {
		return nil, err
	}
	out := []service.ChannelMonitorObservationSample{}
	if f.RestrictGroups && len(f.AllowedGroupIDs) == 0 {
		return out, nil
	}
	if cutoff := now.Add(-24 * time.Hour); f.Start.Before(cutoff) {
		f.Start = cutoff
	}
	if !f.End.After(f.Start) {
		return out, nil
	}
	args := []any{f.Start, f.End}
	where := observationWhere(f, &args)
	rows, err := r.db.QueryContext(ctx, `SELECT facts FROM (SELECT completed_at,source,group_id,facts,facts->>'platform' AS platform,facts->>'model' AS model FROM channel_monitor_compact_samples) s WHERE completed_at>=$1 AND completed_at<$2 AND source='traffic'`+where+` ORDER BY completed_at DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var sample compactObservationSampleRow
		if err = json.Unmarshal(raw, &sample); err != nil {
			return nil, err
		}
		out = append(out, service.ChannelMonitorObservationSample{CompletedAt: sample.CompletedAt, Model: sample.Model, Outcome: sample.Outcome, ErrorCategory: sample.ErrorCategory, HTTPStatus: sample.HTTPStatus})
	}
	return out, rows.Err()
}

func (r *compactObservationRepository) ProbeActivity(ctx context.Context, groupID int64, model, protocol string) (service.ChannelMonitorProbeActivity, error) {
	activity := service.ChannelMonitorProbeActivity{}
	if groupID <= 0 || model == "" || protocol == "" {
		return activity, errors.New("invalid probe activity target")
	}
	now := time.Now().UTC()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return activity, err
	}
	defer func() { _ = tx.Rollback() }()
	var started, latest sql.NullTime
	var failed, gaps int64
	err = tx.QueryRowContext(ctx, `SELECT MIN(started_at),COALESCE(SUM(CASE WHEN last_write_error OR pending_events>0 OR data_through IS NULL OR data_through<$1 THEN 1 ELSE 0 END),0) FROM channel_monitor_compact_writer_sessions WHERE ended_at IS NULL AND heartbeat_at>=$1`, now.Add(-45*time.Second)).Scan(&started, &failed)
	if err != nil {
		return activity, err
	}
	if started.Valid {
		activity.ObservedSince = started.Time.UTC()
	}
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM channel_monitor_compact_gaps WHERE ended_at>$1)+(SELECT COUNT(*) FROM channel_monitor_compact_writer_sessions WHERE ended_at IS NULL AND heartbeat_at>=$1 AND heartbeat_at<$2)`, now.Add(-15*time.Minute), now.Add(-45*time.Second)).Scan(&gaps)
	if err != nil {
		return activity, err
	}
	activity.CollectionHealthy = started.Valid && failed == 0 && gaps == 0
	err = tx.QueryRowContext(ctx, `SELECT MAX(bucket_start) FROM channel_monitor_compact_minute WHERE source='traffic' AND group_id=$1 AND model=$2 AND (protocol=$3 OR ($3='openai_responses' AND protocol='responses_websocket')) AND bucket_start>=$4 AND COALESCE((facts->>'success_requests')::bigint,0)+COALESCE((facts->>'channel_errors')::bigint,0)+COALESCE((facts->>'client_errors')::bigint,0)+COALESCE((facts->>'cancelled_requests')::bigint,0)>0`, groupID, model, protocol, now.Add(-48*time.Hour)).Scan(&latest)
	if err != nil {
		return activity, err
	}
	if latest.Valid {
		value := latest.Time.UTC().Add(time.Minute)
		activity.LastTrafficAt = &value
	}
	return activity, tx.Commit()
}
