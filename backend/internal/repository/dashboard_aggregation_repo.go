package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type dashboardAggregationRepository struct {
	sql   sqlExecutor
	clock func() time.Time
}

const usageLogsCleanupBatchSize = 10000
const usageBillingDedupCleanupBatchSize = 10000
const dashboardRetentionHourLockLimit = 1024
const dashboardRetentionPrefixBatchLimit = 64

// NewDashboardAggregationRepository 创建仪表盘预聚合仓储。
func NewDashboardAggregationRepository(sqlDB *sql.DB) service.DashboardAggregationRepository {
	if sqlDB == nil {
		return nil
	}
	if !isPostgresDriver(sqlDB) {
		log.Printf("[DashboardAggregation] 检测到非 PostgreSQL 驱动，已自动禁用预聚合")
		return nil
	}
	return newDashboardAggregationRepositoryWithSQL(sqlDB)
}

func newDashboardAggregationRepositoryWithSQL(sqlq sqlExecutor) *dashboardAggregationRepository {
	return &dashboardAggregationRepository{sql: sqlq, clock: time.Now}
}

func (r *dashboardAggregationRepository) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Now()
}

func isPostgresDriver(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, ok := db.Driver().(*pq.Driver)
	return ok
}

func (r *dashboardAggregationRepository) AggregateRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := truncateToHour(startLocal)
	hourEnd := truncateToHour(endLocal)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.Add(24 * time.Hour)
	}

	if db, ok := r.sql.(*sql.DB); ok {
		// Publish one hour per transaction: a bulk mutation may encounter hours
		// in any order, so never hold multiple exclusive hour locks at once.
		for hour := hourStart; hour.Before(hourEnd); hour = hour.Add(time.Hour) {
			if err := r.aggregateHour(ctx, db, hour); err != nil {
				return err
			}
		}
		return nil
	}
	return r.aggregateRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

// The namespace and key calculation must match migration 246. Shared writer
// locks survive until commit, including a current-hour write spanning rollover.
const dashboardHourlyLockNamespace = 1937072738

func lockDashboardHour(ctx context.Context, tx *sql.Tx, hour time.Time) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::integer, $2::integer)`,
		dashboardHourlyLockNamespace, hour.Unix()/3600)
	return err
}

func (r *dashboardAggregationRepository) aggregateHour(ctx context.Context, db *sql.DB, hour time.Time, requireCoverage ...bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_verified_publish = 'true'`); err != nil {
		return err
	}
	// A source frontier is advanced in the same transaction as retention. Hold
	// its compatible lock before any hour lock so a publisher cannot observe a
	// partially pruned source. Normal ingest never takes this state-row lock.
	var retainedFrom time.Time
	if err := tx.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1 FOR SHARE`).Scan(&retainedFrom); err != nil {
		return err
	}
	if hour.Before(retainedFrom) {
		return nil // The independently retained summary is the remaining history.
	}

	// The transaction timestamp is also the computed_at publication timestamp.
	// Current/partial hours cannot be reused; no exclusive lock blocks ingest.
	var finalized bool
	if err := tx.QueryRowContext(ctx, `SELECT $1::timestamptz + INTERVAL '1 hour' <= CURRENT_TIMESTAMP`, hour).Scan(&finalized); err != nil {
		return err
	}
	if finalized {
		if err := lockDashboardHour(ctx, tx, hour); err != nil {
			return err
		}
	}
	if len(requireCoverage) > 0 && requireCoverage[0] {
		// Recheck after acquiring the source lock. Retention may have advanced
		// since selecting the repair batch; never erase an archived summary.
		var repairable bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_dashboard_hourly WHERE bucket_start=$1 AND (NOT source_verified OR NOT source_finalized OR computed_at=TIMESTAMPTZ 'epoch'))`, hour).Scan(&repairable); err != nil {
			return err
		}
		if !repairable {
			return nil
		}
	}
	if finalized {
		// The legacy database has no reliable record of past source retention.
		// Certify its finalized snapshots only when every additive counter and
		// active-user count agrees with raw. A mismatch remains untouched.
		canReplace, err := canReplaceDashboardHour(ctx, tx, hour)
		if err != nil {
			return err
		}
		if !canReplace {
			return nil
		}
	}
	txRepo := newDashboardAggregationRepositoryWithSQL(tx)
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_dashboard_hourly_users WHERE bucket_start=$1`, hour); err != nil {
		return err
	}
	if err := txRepo.insertHourlyActiveUsers(ctx, hour, hour.Add(time.Hour)); err != nil {
		return err
	}
	if err := txRepo.upsertHourlyAggregates(ctx, hour, hour.Add(time.Hour)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_dashboard_hourly (bucket_start,computed_at) VALUES ($1,NOW()) ON CONFLICT (bucket_start) DO NOTHING`, hour); err != nil {
		return err
	}
	if err := r.refreshDayInTx(ctx, tx, truncateToDay(hour)); err != nil {
		return err
	}
	return tx.Commit()
}

func canReplaceDashboardHour(ctx context.Context, tx *sql.Tx, hour time.Time) (bool, error) {
	var verified bool
	err := tx.QueryRowContext(ctx, `SELECT source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified)
	if err == sql.ErrNoRows || (err == nil && verified) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var equal bool
	err = tx.QueryRowContext(ctx, `
        SELECT ROW(h.total_requests,h.input_tokens,h.output_tokens,h.cache_creation_tokens,
            h.cache_read_tokens,h.total_cost,h.actual_cost,h.account_cost,h.total_duration_ms,h.active_users)
            IS NOT DISTINCT FROM ROW(raw.requests,raw.input_tokens,raw.output_tokens,raw.cache_creation_tokens,
            raw.cache_read_tokens,raw.total_cost,raw.actual_cost,raw.account_cost,raw.duration,raw.active_users)
        FROM usage_dashboard_hourly h
        CROSS JOIN (
            SELECT COUNT(*) AS requests,COALESCE(SUM(input_tokens),0) AS input_tokens,
                COALESCE(SUM(output_tokens),0) AS output_tokens,
                COALESCE(SUM(cache_creation_tokens),0) AS cache_creation_tokens,
                COALESCE(SUM(cache_read_tokens),0) AS cache_read_tokens,
                COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded),0) AS total_cost,
                COALESCE(SUM(actual_cost),0) AS actual_cost,
                COALESCE(SUM(CASE WHEN provider_cost_recorded THEN COALESCE(account_stats_cost,total_cost)*COALESCE(account_rate_multiplier,1) ELSE 0 END),0) AS account_cost,
                COALESCE(SUM(COALESCE(duration_ms,0)),0) AS duration,
                COUNT(DISTINCT user_id) AS active_users
            FROM usage_logs WHERE created_at >= $1 AND created_at < $2
        ) raw WHERE h.bucket_start=$1`, hour, hour.Add(time.Hour)).Scan(&equal)
	return equal, err
}

func (r *dashboardAggregationRepository) refreshDayInTx(ctx context.Context, tx *sql.Tx, day time.Time) error {
	// Different hourly publishers may affect the same day. Acquire the day lock
	// after the single hour lock, then read anew. No publisher takes another hour
	// lock while holding this day lock; source and derived values commit together.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::integer,$2::integer)`, dashboardHourlyLockNamespace+1, day.Unix()/86400); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_dashboard_daily_users WHERE bucket_date=$1::date`, day); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_dashboard_daily_users (bucket_date,user_id)
        SELECT DISTINCT (bucket_start AT TIME ZONE $3)::date,user_id FROM usage_dashboard_hourly_users
        WHERE bucket_start >= $1 AND bucket_start < $2 ON CONFLICT DO NOTHING`, day, day.AddDate(0, 0, 1), timezone.Name()); err != nil {
		return err
	}
	if err := newDashboardAggregationRepositoryWithSQL(tx).upsertDailyAggregates(ctx, day, day.AddDate(0, 0, 1)); err != nil {
		return err
	}
	return nil
}

// RepairInvalidatedBuckets bounds historical rebuilding per scheduled cycle.
// A failed/interrupted rebuild leaves epoch markers committed and reads use raw.
func (r *dashboardAggregationRepository) RepairInvalidatedBuckets(ctx context.Context, limit int) error {
	if limit <= 0 {
		return nil
	}
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return nil
	}
	type repairHour struct {
		hour     time.Time
		verified bool
	}
	load := func() ([]repairHour, error) {
		rows, err := db.QueryContext(ctx, `SELECT h.bucket_start,h.source_verified FROM usage_dashboard_hourly h JOIN usage_dashboard_source_state s ON s.id=1 WHERE (NOT h.source_verified OR NOT h.source_finalized OR h.computed_at=TIMESTAMPTZ 'epoch') AND h.bucket_start >= s.raw_retained_from AND h.bucket_start + INTERVAL '1 hour' <= CURRENT_TIMESTAMP AND (h.source_verified OR h.bucket_start<s.repair_before) ORDER BY h.source_verified DESC,h.bucket_start DESC LIMIT $1`, limit)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var hours []repairHour
		for rows.Next() {
			var h repairHour
			if err := rows.Scan(&h.hour, &h.verified); err != nil {
				return nil, err
			}
			hours = append(hours, h)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		return hours, nil
	}
	hours, err := load()
	if err != nil {
		return err
	}
	if len(hours) == 0 {
		// Unverifiable legacy snapshots must not occupy the same bounded batch
		// forever. Revisit them only after the older candidates were considered.
		if _, err := db.ExecContext(ctx, `UPDATE usage_dashboard_source_state SET repair_before=TIMESTAMPTZ 'infinity' WHERE id=1 AND repair_before<>TIMESTAMPTZ 'infinity'`); err != nil {
			return err
		}
		hours, err = load()
		if err != nil {
			return err
		}
	}
	var legacyCursor time.Time
	for _, h := range hours {
		if err := r.aggregateHour(ctx, db, h.hour, true); err != nil {
			return err
		}
		if !h.verified {
			legacyCursor = h.hour
		}
	}
	if !legacyCursor.IsZero() {
		_, err = db.ExecContext(ctx, `UPDATE usage_dashboard_source_state SET repair_before=$1 WHERE id=1`, legacyCursor)
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) aggregateRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// 以桶边界聚合，允许覆盖 end 所在桶的剩余区间。
	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) RecomputeRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := truncateToHour(startLocal)
	hourEnd := truncateToHour(endLocal)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.Add(24 * time.Hour)
	}

	if db, ok := r.sql.(*sql.DB); ok {
		for hour := hourStart; hour.Before(hourEnd); hour = hour.Add(time.Hour) {
			if err := r.aggregateHour(ctx, db, hour); err != nil {
				return err
			}
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := lockGroupUsageRollupState(ctx, tx); err != nil {
			return err
		}
		if err := invalidateGroupUsageRollupsAt(ctx, tx, start); err != nil {
			return err
		}
		if err := newDashboardAggregationRepositoryWithSQL(tx).syncGroupUsageRollupsInTx(ctx, service.GroupUsageTodayStart(r.now())); err != nil {
			return err
		}
		return tx.Commit()
	}
	return r.recomputeRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

func (r *dashboardAggregationRepository) recomputeRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// 先清空范围内桶，再重建（避免仅增量插入导致活跃用户等指标无法回退）。
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}

	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	var ts time.Time
	query := "SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1"
	if err := scanSingleRow(ctx, r.sql, query, nil, &ts); err != nil {
		if err == sql.ErrNoRows {
			return time.Unix(0, 0).UTC(), nil
		}
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

func (r *dashboardAggregationRepository) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	query := `
		INSERT INTO usage_dashboard_aggregation_watermark (id, last_aggregated_at, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id)
		DO UPDATE SET last_aggregated_at = EXCLUDED.last_aggregated_at, updated_at = EXCLUDED.updated_at
	`
	_, err := r.sql.ExecContext(ctx, query, aggregatedAt.UTC())
	return err
}

func (r *dashboardAggregationRepository) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	hourlyCutoffUTC := hourlyCutoff.UTC()
	dailyCutoffUTC := dailyCutoff.UTC()
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil {
		return err
	}
	if isPartitioned {
		if err := r.dropUsageLogsPartitions(ctx, cutoff); err != nil {
			if errors.Is(err, errDashboardRetentionCapacity) {
				// An old certified gap can exceed the DROP lock budget. Reuse the
				// bounded row/gap prefix below; retry the month in a later cycle.
			} else if errors.Is(err, errDashboardRetentionDeferred) {
				return nil // Retry after the bounded repair/writer finishes next cycle.
			} else {
				return err
			}
		}
	}
	// Remove the expired part of the boundary partition as well, so a retention
	// window measured in days does not silently round up to a whole month.
	if err := r.cleanupUsageLogsBatches(ctx, cutoff); err != nil {
		return err
	}
	return r.SyncGroupUsageRollups(ctx, service.GroupUsageTodayStart(r.now()))
}

func (r *dashboardAggregationRepository) cleanupUsageLogsBatches(ctx context.Context, cutoff time.Time) error {
	db, transactional := r.sql.(*sql.DB)
	boundedPrefixes := 0
	for {
		if transactional {
			affected, boundedProgress, err := cleanupUsageLogsBatchWithRollupInvalidationProgress(ctx, db, cutoff)
			if err != nil {
				return err
			}
			if boundedProgress {
				boundedPrefixes++
				if boundedPrefixes >= dashboardRetentionPrefixBatchLimit {
					return nil
				}
				continue // Locks from the committed prefix have already released.
			}
			if affected < usageLogsCleanupBatchSize {
				return nil
			}
			continue
		}

		res, err := r.sql.ExecContext(ctx, `
			WITH victims AS (
				SELECT tableoid, ctid
				FROM usage_logs
				WHERE created_at < $1
				ORDER BY created_at ASC, id ASC
				LIMIT $2
			)
			DELETE FROM usage_logs
			WHERE (tableoid, ctid) IN (SELECT tableoid, ctid FROM victims)
		`, cutoff.UTC(), usageLogsCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected < usageLogsCleanupBatchSize {
			return nil
		}
	}
}

func cleanupUsageLogsBatchWithRollupInvalidation(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	affected, _, err := cleanupUsageLogsBatchWithRollupInvalidationProgress(ctx, db, cutoff)
	return affected, err
}

func cleanupUsageLogsBatchWithRollupInvalidationProgress(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	rollback := func(err error) (int64, bool, error) {
		_ = tx.Rollback()
		return 0, false, err
	}

	if _, err := tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_preserve_aggregates = 'true'`); err != nil {
		return rollback(err)
	}

	if err := lockGroupUsageRollupState(ctx, tx); err != nil {
		return rollback(err)
	}
	var retainedFrom time.Time
	// A publisher holds source SHARE before waiting for a writer's hour. Never
	// wait for it while holding group state: that same writer can request group
	// state in its next statement, completing a group -> source -> hour cycle.
	if err := tx.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1 FOR UPDATE NOWAIT`).Scan(&retainedFrom); err != nil {
		if isDashboardRetentionLockUnavailable(err) {
			return rollback(nil)
		}
		return rollback(err)
	}
	// Lock tuples before acquiring exclusive hour locks: UPDATE/DELETE acquires
	// its tuple before its AFTER trigger requests the shared hour lock. NOWAIT
	// also prevents crossing a row whose invalidation has not run yet; skipping
	// such a row could archive a clean-looking but already changing snapshot.
	rows, err := tx.QueryContext(ctx, `
		SELECT tableoid::bigint, ctid::text, created_at FROM usage_logs
		WHERE created_at < $1 ORDER BY created_at ASC, id ASC LIMIT $2
		FOR UPDATE NOWAIT
	`, cutoff.UTC(), usageLogsCleanupBatchSize)
	if err != nil {
		if isDashboardRetentionLockUnavailable(err) {
			return rollback(nil)
		}
		return rollback(err)
	}
	var tableOIDs []int64
	var ctids []string
	var victimHours []time.Time
	var victimTimestamps []time.Time
	var earliestDeletedAt, latestDeletedAt time.Time
	for rows.Next() {
		var tableOID int64
		var ctid string
		var deletedAt time.Time
		if err := rows.Scan(&tableOID, &ctid, &deletedAt); err != nil {
			_ = rows.Close()
			return rollback(err)
		}
		tableOIDs = append(tableOIDs, tableOID)
		ctids = append(ctids, ctid)
		victimHours = append(victimHours, truncateToHour(deletedAt.In(timezone.Location())))
		victimTimestamps = append(victimTimestamps, deletedAt)
		if earliestDeletedAt.IsZero() || deletedAt.Before(earliestDeletedAt) {
			earliestDeletedAt = deletedAt
		}
		if latestDeletedAt.IsZero() || deletedAt.After(latestDeletedAt) {
			latestDeletedAt = deletedAt
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		if isDashboardRetentionLockUnavailable(err) {
			return rollback(nil)
		}
		return rollback(err)
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	// PostgreSQL timestamps have microsecond precision. Advance only through
	// rows actually removed by this batch, never the entire requested cutoff.
	// A partially removed hour becomes archived as a whole, with a fresh closed
	// snapshot; later raw mutations retain the existing archive semantics.
	nextRetainedFrom := cutoff
	if len(ctids) > 0 && latestDeletedAt.Add(time.Microsecond).Before(cutoff) {
		nextRetainedFrom = latestDeletedAt.Add(time.Microsecond)
	}
	prefixEnd, err := dashboardRetentionPrefix(ctx, tx, retainedFrom, nextRetainedFrom, victimHours)
	if err != nil {
		return rollback(err)
	}
	boundedPrefix := prefixEnd.Before(nextRetainedFrom)
	if len(ctids) == 0 && !boundedPrefix {
		return rollback(nil) // An ordinary empty request keeps its existing no-op.
	}
	if boundedPrefix {
		kept := 0
		for i, at := range victimTimestamps {
			if at.Before(prefixEnd) {
				tableOIDs[kept], ctids[kept] = tableOIDs[i], ctids[i]
				victimHours[kept], victimTimestamps[kept] = victimHours[i], at
				kept++
			}
		}
		tableOIDs, ctids = tableOIDs[:kept], ctids[:kept]
		victimHours, victimTimestamps = victimHours[:kept], victimTimestamps[:kept]
		if kept == 0 {
			// The ordered, locked victim selection proves this prefix has no
			// committed raw rows. Advance only after its complete hour guard.
			nextRetainedFrom = prefixEnd
		} else {
			earliestDeletedAt = victimTimestamps[0]
			nextRetainedFrom = victimTimestamps[kept-1].Add(time.Microsecond)
		}
	}
	safe, err := lockDashboardRetentionHours(ctx, tx, retainedFrom, nextRetainedFrom, victimHours)
	if err != nil {
		return rollback(err)
	}
	if !safe {
		return rollback(nil)
	}
	if err := advanceDashboardRawRetention(ctx, tx, nextRetainedFrom); err != nil {
		return rollback(err)
	}
	if len(ctids) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return 0, true, nil // Explicit progress; zero affected is not a full batch.
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE (tableoid,ctid) IN
		(SELECT * FROM unnest($1::oid[],$2::tid[]))`, pq.Array(tableOIDs), pq.Array(ctids))
	if err != nil {
		return rollback(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return rollback(err)
	}
	if err := invalidateGroupUsageRollupsAt(ctx, tx, earliestDeletedAt); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return affected, boundedPrefix, nil
}

var errDashboardRetentionDeferred = errors.New("dashboard retention deferred until source publication is safe")
var errDashboardRetentionCapacity = errors.New("dashboard retention requires a bounded historical prefix")

func isDashboardRetentionLockUnavailable(err error) bool {
	var pgErr *pq.Error
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// Choose a prefix before acquiring advisory locks. Include both raw victims and
// certified empty hours; truncating just the lock list would let retention cross
// an unlocked dirty snapshot. Publishers cannot add certified rows while this
// transaction holds source state, so reading LIMIT+1 is sufficient to bound it.
func dashboardRetentionPrefix(ctx context.Context, tx *sql.Tx, retainedFrom, nextRetainedFrom time.Time, victimHours []time.Time) (time.Time, error) {
	hours := make(map[int64]time.Time, len(victimHours)+dashboardRetentionHourLockLimit+1)
	for _, hour := range victimHours {
		hours[hour.Unix()/3600] = hour
	}
	rows, err := tx.QueryContext(ctx, `SELECT bucket_start FROM usage_dashboard_hourly
		WHERE source_verified AND bucket_start >= $1 AND bucket_start < $2
		ORDER BY bucket_start LIMIT $3`, retainedFrom, nextRetainedFrom, dashboardRetentionHourLockLimit+1)
	if err != nil {
		return time.Time{}, err
	}
	for rows.Next() {
		var hour time.Time
		if err := rows.Scan(&hour); err != nil {
			_ = rows.Close()
			return time.Time{}, err
		}
		hours[hour.Unix()/3600] = hour
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return time.Time{}, err
	}
	if err := rows.Close(); err != nil {
		return time.Time{}, err
	}
	if len(hours) <= dashboardRetentionHourLockLimit {
		return nextRetainedFrom, nil
	}
	keys := make([]int64, 0, len(hours))
	for key := range hours {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return hours[keys[dashboardRetentionHourLockLimit]], nil
}

// Call after group state, source state and raw tuple/relation locks. A writer
// can already hold shared hours in arbitrary order, so never wait for an hour
// lock while retaining the group/source locks: abandon this batch instead.
func lockDashboardRetentionHours(ctx context.Context, tx *sql.Tx, retainedFrom, nextRetainedFrom time.Time, victimHours []time.Time) (bool, error) {
	hours := make(map[int64]struct{}, len(victimHours))
	for _, hour := range victimHours {
		hours[hour.Unix()/3600] = struct{}{}
	}
	// Include certified hours without remaining raw rows. A manual deletion of
	// the last row leaves a dirty zero-hour that must be repaired before the
	// global frontier can cross it. Legacy mismatches keep their old policy.
	rows, err := tx.QueryContext(ctx, `SELECT bucket_start FROM usage_dashboard_hourly
		WHERE source_verified AND bucket_start >= $1 AND bucket_start < $2 ORDER BY bucket_start`, retainedFrom, nextRetainedFrom)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var hour time.Time
		if err := rows.Scan(&hour); err != nil {
			_ = rows.Close()
			return false, err
		}
		hours[hour.Unix()/3600] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	keys := make([]int64, 0, len(hours))
	for key := range hours {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if len(keys) > 0 {
		var locked bool
		if err := tx.QueryRowContext(ctx, `SELECT bool_and(pg_try_advisory_xact_lock($1::integer,k))
			FROM (SELECT unnest($2::integer[]) AS k ORDER BY k) ordered`, dashboardHourlyLockNamespace, pq.Array(keys)).Scan(&locked); err != nil {
			return false, err
		}
		if !locked {
			return false, nil
		}
	}
	// Recheck after locks. Any committed pre-retention change is now visible;
	// no writer or publisher can replace these certified values until commit.
	var unsafe bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_dashboard_hourly
		WHERE source_verified AND bucket_start >= $1 AND bucket_start < $2
		AND (NOT source_finalized OR computed_at=TIMESTAMPTZ 'epoch'))`, retainedFrom, nextRetainedFrom).Scan(&unsafe); err != nil {
		return false, err
	}
	return !unsafe, nil
}

func (r *dashboardAggregationRepository) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	for {
		res, err := r.sql.ExecContext(ctx, `
			WITH victims AS (
				SELECT ctid, request_id, api_key_id, request_fingerprint, created_at
				FROM usage_billing_dedup
				WHERE created_at < $1
				LIMIT $2
			), archived AS (
				INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
				SELECT request_id, api_key_id, request_fingerprint, created_at
				FROM victims
				ON CONFLICT (request_id, api_key_id) DO NOTHING
			)
			DELETE FROM usage_billing_dedup
			WHERE ctid IN (SELECT ctid FROM victims)
		`, cutoff.UTC(), usageBillingDedupCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected < usageBillingDedupCleanupBatchSize {
			return nil
		}
	}
}

func (r *dashboardAggregationRepository) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil || !isPartitioned {
		return err
	}
	monthStart := truncateToMonthUTC(now)
	prevMonth := monthStart.AddDate(0, -1, 0)
	nextMonth := monthStart.AddDate(0, 1, 0)

	for _, m := range []time.Time{prevMonth, monthStart, nextMonth} {
		if err := r.createUsageLogsPartition(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (r *dashboardAggregationRepository) insertHourlyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_hourly_users (bucket_start, user_id)
		SELECT DISTINCT
			date_trunc('hour', created_at, $3) AS bucket_start,
			user_id
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) insertDailyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_daily_users (bucket_date, user_id)
		SELECT DISTINCT
			(bucket_start AT TIME ZONE $3)::date AS bucket_date,
			user_id
		FROM usage_dashboard_hourly_users
		WHERE bucket_start >= $1 AND bucket_start < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertHourlyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at, $3) AS bucket_start,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost) FILTER (WHERE provider_cost_recorded), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(CASE WHEN provider_cost_recorded THEN COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1) ELSE 0 END), 0) AS account_cost,
				COALESCE(SUM(COALESCE(duration_ms, 0)), 0) AS total_duration_ms
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1
		),
		user_counts AS (
			SELECT bucket_start, COUNT(*) AS active_users
			FROM usage_dashboard_hourly_users
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY bucket_start
		)
		INSERT INTO usage_dashboard_hourly (
			bucket_start,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			hourly.bucket_start,
			hourly.total_requests,
			hourly.input_tokens,
			hourly.output_tokens,
			hourly.cache_creation_tokens,
			hourly.cache_read_tokens,
			hourly.total_cost,
			hourly.actual_cost,
			hourly.account_cost,
			hourly.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM hourly
		LEFT JOIN user_counts ON user_counts.bucket_start = hourly.bucket_start
		ON CONFLICT (bucket_start)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertDailyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $5)::date AS bucket_date,
				COALESCE(SUM(total_requests), 0) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(account_cost), 0) AS account_cost,
				COALESCE(SUM(total_duration_ms), 0) AS total_duration_ms
			FROM usage_dashboard_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY (bucket_start AT TIME ZONE $5)::date
		),
		user_counts AS (
			SELECT bucket_date, COUNT(*) AS active_users
			FROM usage_dashboard_daily_users
			WHERE bucket_date >= $3::date AND bucket_date < $4::date
			GROUP BY bucket_date
		)
		INSERT INTO usage_dashboard_daily (
			bucket_date,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			daily.bucket_date,
			daily.total_requests,
			daily.input_tokens,
			daily.output_tokens,
			daily.cache_creation_tokens,
			daily.cache_read_tokens,
			daily.total_cost,
			daily.actual_cost,
			daily.account_cost,
			daily.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM daily
		LEFT JOIN user_counts ON user_counts.bucket_date = daily.bucket_date
		ON CONFLICT (bucket_date)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) isUsageLogsPartitioned(ctx context.Context) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`
	var partitioned bool
	if err := scanSingleRow(ctx, r.sql, query, nil, &partitioned); err != nil {
		return false, err
	}
	return partitioned, nil
}

func (r *dashboardAggregationRepository) dropUsageLogsPartitions(ctx context.Context, cutoff time.Time) error {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_inherits
		JOIN pg_class c ON c.oid = pg_inherits.inhrelid
		JOIN pg_class p ON p.oid = pg_inherits.inhparent
		WHERE p.relname = 'usage_logs'
	`)
	if err != nil {
		return err
	}
	cutoffMonth := truncateToMonthUTC(cutoff)
	type usageLogsPartition struct {
		name  string
		month time.Time
	}
	partitions := make([]usageLogsPartition, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		if !strings.HasPrefix(name, "usage_logs_") {
			continue
		}
		suffix := strings.TrimPrefix(name, "usage_logs_")
		month, err := time.Parse("200601", suffix)
		if err != nil {
			continue
		}
		month = month.UTC()
		if month.Before(cutoffMonth) {
			partitions = append(partitions, usageLogsPartition{name: name, month: month})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	sort.Slice(partitions, func(i, j int) bool {
		return partitions[i].month.Before(partitions[j].month)
	})
	if db, ok := r.sql.(*sql.DB); ok {
		for _, partition := range partitions {
			if err := dropUsageLogsPartitionWithRollupInvalidation(ctx, db, partition.name, partition.month); err != nil {
				return err
			}
		}
		return nil
	}
	for _, partition := range partitions {
		if _, err := r.sql.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(partition.name))); err != nil {
			return err
		}
	}
	return nil
}

func dropUsageLogsPartitionWithRollupInvalidation(ctx context.Context, db *sql.DB, name string, monthStart time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}

	if err := lockGroupUsageRollupState(ctx, tx); err != nil {
		return rollback(err)
	}
	var retainedFrom time.Time
	if err := tx.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1 FOR UPDATE NOWAIT`).Scan(&retainedFrom); err != nil {
		if isDashboardRetentionLockUnavailable(err) {
			return rollback(errDashboardRetentionDeferred)
		}
		return rollback(err)
	}
	// DROP needs a parent relation lock as well as the leaf lock. Acquire both
	// without waiting: a writer may already hold a relation/tuple and be waiting
	// for our group lock before reaching its dashboard invalidation trigger.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("LOCK TABLE ONLY usage_logs, %s IN ACCESS EXCLUSIVE MODE NOWAIT", pq.QuoteIdentifier(name))); err != nil {
		if isDashboardRetentionLockUnavailable(err) {
			return rollback(errDashboardRetentionDeferred)
		}
		return rollback(err)
	}
	nextRetainedFrom := monthStart.AddDate(0, 1, 0)
	var victimHours []time.Time
	for hour := truncateToHour(monthStart.In(timezone.Location())); hour.Before(nextRetainedFrom); hour = hour.Add(time.Hour) {
		victimHours = append(victimHours, hour)
	}
	prefixEnd, err := dashboardRetentionPrefix(ctx, tx, retainedFrom, nextRetainedFrom, victimHours)
	if err != nil {
		return rollback(err)
	}
	if prefixEnd.Before(nextRetainedFrom) {
		return rollback(errDashboardRetentionCapacity)
	}
	safe, err := lockDashboardRetentionHours(ctx, tx, retainedFrom, nextRetainedFrom, victimHours)
	if err != nil {
		return rollback(err)
	}
	if !safe {
		return rollback(errDashboardRetentionDeferred)
	}
	if err := advanceDashboardRawRetention(ctx, tx, nextRetainedFrom); err != nil {
		return rollback(err)
	}
	if err := invalidateGroupUsageRollupsAt(ctx, tx, monthStart); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(name))); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func advanceDashboardRawRetention(ctx context.Context, tx *sql.Tx, cutoff time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE usage_dashboard_source_state SET raw_retained_from=GREATEST(raw_retained_from,$1) WHERE id=1`, cutoff.UTC())
	return err
}

func (r *dashboardAggregationRepository) createUsageLogsPartition(ctx context.Context, month time.Time) error {
	monthStart := truncateToMonthUTC(month)
	nextMonth := monthStart.AddDate(0, 1, 0)
	name := fmt.Sprintf("usage_logs_%s", monthStart.Format("200601"))
	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF usage_logs FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(name),
		pq.QuoteLiteral(monthStart.Format("2006-01-02")),
		pq.QuoteLiteral(nextMonth.Format("2006-01-02")),
	)
	_, err := r.sql.ExecContext(ctx, query)
	return err
}

func truncateToDay(t time.Time) time.Time {
	return timezone.StartOfDay(t)
}

func truncateToHour(t time.Time) time.Time {
	// time.Truncate uses absolute UTC hours, which do not match PostgreSQL's
	// local-hour date_trunc for offsets such as Asia/Kolkata (+05:30).
	return t.Add(-time.Duration(t.Minute())*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond()))
}

func truncateToMonthUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
