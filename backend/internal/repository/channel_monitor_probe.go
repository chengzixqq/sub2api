package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type channelMonitorProbeRepository struct{ db *sql.DB }

func NewChannelMonitorProbeRepository(db *sql.DB) service.ChannelMonitorProbeRepository {
	return &channelMonitorProbeRepository{db: db}
}

const probeTargetColumns = `id,group_id,model,protocol,enabled,version,created_at,updated_at,last_probe_at`

func scanProbeTarget(row interface{ Scan(...any) error }) (*service.ChannelMonitorProbeTarget, error) {
	var t service.ChannelMonitorProbeTarget
	err := row.Scan(&t.ID, &t.GroupID, &t.Model, &t.Protocol, &t.Enabled, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.LastProbeAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrChannelMonitorProbeNotFound
	}
	return &t, err
}
func (r *channelMonitorProbeRepository) ListTargets(ctx context.Context) ([]service.ChannelMonitorProbeTarget, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+probeTargetColumns+` FROM channel_monitor_probe_targets ORDER BY id LIMIT 1001`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []service.ChannelMonitorProbeTarget{}
	for rows.Next() {
		t, e := scanProbeTarget(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, *t)
	}
	return items, rows.Err()
}
func (r *channelMonitorProbeRepository) GetTarget(ctx context.Context, id int64) (*service.ChannelMonitorProbeTarget, error) {
	return scanProbeTarget(r.db.QueryRowContext(ctx, `SELECT `+probeTargetColumns+` FROM channel_monitor_probe_targets WHERE id=$1`, id))
}
func (r *channelMonitorProbeRepository) SaveTarget(ctx context.Context, t *service.ChannelMonitorProbeTarget) error {
	if t.ID == 0 {
		err := r.db.QueryRowContext(ctx, `INSERT INTO channel_monitor_probe_targets(group_id,model,protocol,enabled) VALUES($1,$2,$3,$4) ON CONFLICT(group_id,model,protocol) DO NOTHING RETURNING id,version,created_at,updated_at`, t.GroupID, t.Model, t.Protocol, t.Enabled).Scan(&t.ID, &t.Version, &t.CreatedAt, &t.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrChannelMonitorProbeConflict
		}
		return err
	}
	err := r.db.QueryRowContext(ctx, `UPDATE channel_monitor_probe_targets SET group_id=$2,model=$3,protocol=$4,enabled=$5,version=version+1,updated_at=NOW() WHERE id=$1 AND version=$6 AND (lease_until IS NULL OR lease_until<=NOW()) RETURNING version,created_at,updated_at`, t.ID, t.GroupID, t.Model, t.Protocol, t.Enabled, t.Version).Scan(&t.Version, &t.CreatedAt, &t.UpdatedAt)
	var sqlState interface{ SQLState() string }
	if errors.Is(err, sql.ErrNoRows) || (errors.As(err, &sqlState) && sqlState.SQLState() == "23505") {
		return service.ErrChannelMonitorProbeConflict
	}
	return err
}

func (r *channelMonitorProbeRepository) QuotaSummaries(ctx context.Context, groupIDs []int64, now time.Time) (map[int64]service.ChannelMonitorQuotaSummary, error) {
	result := map[int64]service.ChannelMonitorQuotaSummary{}
	if len(groupIDs) == 0 {
		return result, nil
	}
	data, err := json.Marshal(groupIDs)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT ag.group_id,BOOL_OR(q.snapshot IS NULL),BOOL_OR(q.snapshot IS NOT NULL AND NOT COALESCE((q.snapshot->>'success')::boolean,FALSE)),BOOL_OR(q.fetched_at<$2),MIN(q.fetched_at) FROM account_groups ag JOIN accounts a ON a.id=ag.account_id LEFT JOIN channel_monitor_probe_quotas q ON q.account_id=ag.account_id WHERE a.deleted_at IS NULL AND a.status='active' AND ag.group_id IN (SELECT value::bigint FROM jsonb_array_elements_text($1::jsonb)) GROUP BY ag.group_id`, data, now.Add(-10*time.Minute))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var missing bool
		var failed, stale sql.NullBool
		var updated *time.Time
		if err = rows.Scan(&id, &missing, &failed, &stale, &updated); err != nil {
			return nil, err
		}
		state := "available"
		if missing {
			state = "unknown"
		} else if stale.Bool {
			state = "stale"
		} else if failed.Bool {
			state = "unavailable"
		}
		result[id] = service.ChannelMonitorQuotaSummary{State: state, UpdatedAt: updated}
	}
	return result, rows.Err()
}

func (r *channelMonitorProbeRepository) Reserve(ctx context.Context, id int64, key string, manual bool, now time.Time) (*service.ChannelMonitorProbeRun, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Global budget lock is always first: cross-target requests cannot overspend or deadlock.
	day := now.UTC().Format("2006-01-02")
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_probe_budgets(utc_day,target_id) VALUES($1,0),($1,$2) ON CONFLICT DO NOTHING`, day, id); err != nil {
		return nil, false, err
	}
	var globalUsed int
	if err = tx.QueryRowContext(ctx, `SELECT used FROM channel_monitor_probe_budgets WHERE utc_day=$1 AND target_id=0 FOR UPDATE`, day).Scan(&globalUsed); err != nil {
		return nil, false, err
	}
	keyHash := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(keyHash[:])
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT result FROM channel_monitor_probe_runs WHERE target_id=$1 AND idempotency_key=$2`, id, hash).Scan(&previous)
	if err == nil {
		var run service.ChannelMonitorProbeRun
		if err = json.Unmarshal(previous, &run); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	t, err := scanProbeTarget(tx.QueryRowContext(ctx, `SELECT `+probeTargetColumns+` FROM channel_monitor_probe_targets WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, false, err
	}
	if !t.Enabled {
		return nil, false, service.ErrChannelMonitorProbeDisabled
	}
	var lease *time.Time
	if err = tx.QueryRowContext(ctx, `SELECT lease_until FROM channel_monitor_probe_targets WHERE id=$1`, id).Scan(&lease); err != nil {
		return nil, false, err
	}
	if lease != nil && lease.After(now) {
		return nil, false, service.ErrChannelMonitorProbeBusy
	}
	if !manual && t.LastProbeAt != nil && now.Sub(*t.LastProbeAt) < 15*time.Minute {
		return nil, false, service.ErrChannelMonitorProbeBusy
	}
	var used int
	if err = tx.QueryRowContext(ctx, `SELECT used FROM channel_monitor_probe_budgets WHERE utc_day=$1 AND target_id=$2 FOR UPDATE`, day, id).Scan(&used); err != nil {
		return nil, false, err
	}
	if globalUsed >= service.ChannelMonitorProbeGlobalDailyLimit || used >= service.ChannelMonitorProbeTargetDailyLimit {
		return nil, false, service.ErrChannelMonitorProbeBudget
	}
	runHash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", id, hash)))
	run := &service.ChannelMonitorProbeRun{ID: hex.EncodeToString(runHash[:]), TargetID: id, GroupID: t.GroupID, Model: t.Model, Protocol: t.Protocol, Status: "pending", StartedAt: now.UTC()}
	data, err := json.Marshal(run)
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE channel_monitor_probe_budgets SET used=used+1 WHERE utc_day=$1 AND target_id IN (0,$2)`, day, id); err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_probe_runs(id,target_id,idempotency_key,manual,started_at,result) VALUES($1,$2,$3,$4,$5,$6)`, run.ID, id, hash, manual, now, data); err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE channel_monitor_probe_targets SET lease_until=$2,last_probe_at=$3 WHERE id=$1`, id, now.Add(30*time.Second), now); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return run, true, nil
}
func (r *channelMonitorProbeRepository) Complete(ctx context.Context, run *service.ChannelMonitorProbeRun) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE channel_monitor_probe_runs SET result=$2,completed_at=$3 WHERE id=$1 AND completed_at IS NULL`, run.ID, data, run.CompletedAt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE channel_monitor_probe_targets SET lease_until=NULL WHERE id=$1 AND last_probe_at=$2`, run.TargetID, run.StartedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (r *channelMonitorProbeRepository) Budget(ctx context.Context, now time.Time) (*service.ChannelMonitorProbeBudget, error) {
	b := &service.ChannelMonitorProbeBudget{UTCDay: now.UTC().Format("2006-01-02"), GlobalLimit: service.ChannelMonitorProbeGlobalDailyLimit, Targets: []service.ChannelMonitorProbeTargetBudget{}}
	rows, err := r.db.QueryContext(ctx, `SELECT target_id,used FROM channel_monitor_probe_budgets WHERE utc_day=$1 ORDER BY target_id`, b.UTCDay)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var used int
		if err = rows.Scan(&id, &used); err != nil {
			return nil, err
		}
		if id == 0 {
			b.GlobalUsed = used
		} else {
			b.Targets = append(b.Targets, service.ChannelMonitorProbeTargetBudget{TargetID: id, Limit: service.ChannelMonitorProbeTargetDailyLimit, Used: used})
		}
	}
	return b, rows.Err()
}
func (r *channelMonitorProbeRepository) RecentRuns(ctx context.Context, groupIDs []int64, since time.Time) ([]service.ChannelMonitorProbeRun, error) {
	result := []service.ChannelMonitorProbeRun{}
	if len(groupIDs) == 0 {
		return result, nil
	}
	groups, err := json.Marshal(groupIDs)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT r.result FROM channel_monitor_probe_runs r JOIN channel_monitor_probe_targets t ON t.id=r.target_id WHERE r.started_at>=$1 AND t.enabled AND (r.result->>'group_id')::bigint IN (SELECT value::bigint FROM jsonb_array_elements_text($2::jsonb)) ORDER BY r.started_at DESC LIMIT 10000`, since, groups)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var data []byte
		var item service.ChannelMonitorProbeRun
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (r *channelMonitorProbeRepository) QuotaAccounts(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT ag.account_id FROM account_groups ag JOIN groups g ON g.id=ag.group_id JOIN accounts a ON a.id=ag.account_id WHERE g.status='active' AND g.deleted_at IS NULL AND a.deleted_at IS NULL AND a.status='active' ORDER BY ag.account_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *channelMonitorProbeRepository) ClaimQuota(ctx context.Context, id int64, now time.Time) (bool, error) {
	var got int64
	err := r.db.QueryRowContext(ctx, `INSERT INTO channel_monitor_probe_quotas(account_id,next_poll_at) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET next_poll_at=EXCLUDED.next_poll_at WHERE channel_monitor_probe_quotas.next_poll_at<=$3 RETURNING account_id`, id, now.Add(5*time.Minute), now).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
func (r *channelMonitorProbeRepository) StoreQuota(ctx context.Context, id int64, snapshot *domain.MonitorQuotaSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE channel_monitor_probe_quotas SET snapshot=$2,fetched_at=$3 WHERE account_id=$1`, id, data, snapshot.FetchedAt)
	return err
}
func (r *channelMonitorProbeRepository) ReadQuotas(ctx context.Context, ids []int64) (map[int64]*domain.MonitorQuotaSnapshot, error) {
	result := map[int64]*domain.MonitorQuotaSnapshot{}
	if len(ids) == 0 {
		return result, nil
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT account_id,snapshot FROM channel_monitor_probe_quotas WHERE snapshot IS NOT NULL AND account_id IN (SELECT value::bigint FROM jsonb_array_elements_text($1::jsonb))`, data)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var raw []byte
		var snapshot domain.MonitorQuotaSnapshot
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return nil, err
		}
		result[id] = &snapshot
	}
	return result, rows.Err()
}

func (r *channelMonitorProbeRepository) MaintainProbe(ctx context.Context, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE channel_monitor_probe_runs SET completed_at=$1,result=(result-'success')||jsonb_build_object('status','error','error_class','execution_interrupted','completed_at',$1::timestamptz) WHERE id IN (SELECT id FROM channel_monitor_probe_runs WHERE completed_at IS NULL AND started_at<$2 LIMIT 1000)`, now, now.Add(-time.Minute)); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM channel_monitor_probe_runs WHERE id IN (SELECT id FROM channel_monitor_probe_runs WHERE started_at<$1 LIMIT 5000)`, now.Add(-30*24*time.Hour)); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM channel_monitor_probe_budgets WHERE utc_day<$1`, now.UTC().Add(-30*24*time.Hour).Format("2006-01-02"))
	return err
}
