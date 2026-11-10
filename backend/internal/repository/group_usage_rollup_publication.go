package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// This advisory lock coordinates publishers only. Ingest never requests it.
const groupUsageRollupPublisherLock int64 = 1937072740
const groupUsageRollupBatchDays = 7
const groupUsageRollupPublishTimeout = 2 * time.Second

type groupUsageRollupPlan struct {
	closedBefore         string
	previousRetainedFrom time.Time
	stateTimezone        string
	sourceRevision       int64
	scanBefore           time.Time

	todayStart               time.Time
	targetTimezone           string
	start, end, retainedFrom time.Time
}

type groupUsageRollupCost struct {
	groupID    int64
	actualCost string // Keep PostgreSQL numeric precision through publication.
}

// SyncGroupUsageRollups commits one natural day at a time. Historical scans run
// without the global group-state row lock; only registration/publication use it.
func (r *dashboardAggregationRepository) SyncGroupUsageRollups(ctx context.Context, todayStart time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	var conn *sql.Conn
	switch q := r.sql.(type) {
	case *sql.DB:
		var err error
		conn, err = q.Conn(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
	case *sql.Conn:
		conn = q
	default:
		// A caller-owned transaction could retain a global lock across scans.
		return fmt.Errorf("分组用量汇总需要独立连接，不能在外部事务内扫描")
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", groupUsageRollupPublisherLock).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), groupUsageRollupPublishTimeout)
		defer cancel()
		// If unlock fails, discard the connection instead of returning a session
		// advisory lock to the pool, where another job could re-enter it.
		if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", groupUsageRollupPublisherLock); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	todayStart = service.GroupUsageTodayStart(todayStart)
	for day := 0; day < groupUsageRollupBatchDays; day++ {
		plan, pending, err := prepareGroupUsageRollup(ctx, conn, todayStart)
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		costs, err := scanGroupUsageRollup(ctx, conn, &plan)
		if err != nil {
			return err
		}
		published, err := publishGroupUsageRollup(ctx, conn, plan, costs)
		if err != nil {
			return err
		}
		if !published {
			return nil
		} // A source mutation won; retry next cycle.
		if !plan.end.Before(todayStart) {
			return nil
		}
	}
	return nil
}

type groupUsageRollupTransactionBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// Every transaction taking the global group gate must share this budget,
// including retention and manual cleanup. Slow selection/deletion rolls back
// instead of retaining an ingest-blocking lock for its caller's long deadline.
func beginGroupUsageRollupTransaction(ctx context.Context, beginner groupUsageRollupTransactionBeginner) (*sql.Tx, context.Context, context.CancelFunc, error) {
	limited, cancel := context.WithTimeout(ctx, groupUsageRollupPublishTimeout)
	tx, err := beginner.BeginTx(limited, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	if _, err := tx.ExecContext(limited, "SET LOCAL lock_timeout = '250ms'; SET LOCAL statement_timeout = '1s'"); err != nil {
		_ = tx.Rollback()
		cancel()
		return nil, nil, nil, err
	}
	return tx, limited, cancel, nil
}

func prepareGroupUsageRollup(ctx context.Context, conn *sql.Conn, todayStart time.Time) (groupUsageRollupPlan, bool, error) {
	plan := groupUsageRollupPlan{todayStart: todayStart, targetTimezone: service.GroupUsageTimezoneName()}
	tx, limited, cancel, err := beginGroupUsageRollupTransaction(ctx, conn)
	if err != nil {
		return plan, false, err
	}
	defer cancel()
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(limited, `
        SELECT closed_before::text, retained_from, timezone_name
        FROM usage_group_rollup_state WHERE id = 1 FOR UPDATE
    `).Scan(&plan.closedBefore, &plan.previousRetainedFrom, &plan.stateTimezone); err != nil {
		return plan, false, fmt.Errorf("登记分组用量扫描水位: %w", err)
	}
	todayDate := service.GroupUsageDate(todayStart)
	if plan.stateTimezone == plan.targetTimezone {
		if plan.closedBefore > todayDate {
			return plan, false, fmt.Errorf("分组用量汇总水位位于未来: %s", plan.closedBefore)
		}
		if plan.closedBefore == todayDate {
			return plan, false, tx.Commit()
		}
	}
	// Register before MIN(created_at), not afterwards: an older late insert
	// must invalidate earliest-date discovery as well as the daily SUM.
	if err := tx.QueryRowContext(limited, `
        UPDATE usage_group_rollup_state
        SET scan_before = GREATEST(scan_before, $1), updated_at = NOW()
        WHERE id = 1 RETURNING source_revision, scan_before
    `, todayStart.UTC()).Scan(&plan.sourceRevision, &plan.scanBefore); err != nil {
		return plan, false, err
	}
	if err := tx.Commit(); err != nil {
		return plan, false, err
	}
	return plan, true, nil
}

func scanGroupUsageRollup(ctx context.Context, conn *sql.Conn, plan *groupUsageRollupPlan) ([]groupUsageRollupCost, error) {
	var earliest sql.NullTime
	if err := conn.QueryRowContext(ctx, "SELECT MIN(created_at) FROM usage_logs").Scan(&earliest); err != nil {
		return nil, err
	}
	plan.retainedFrom = plan.todayStart
	if earliest.Valid {
		plan.retainedFrom = earliest.Time.UTC()
	}
	retainedDate := service.GroupUsageDate(plan.retainedFrom)
	startDate := retainedDate
	if plan.stateTimezone == plan.targetTimezone && plan.closedBefore > retainedDate {
		startDate = plan.closedBefore
	}
	start, err := service.ParseGroupUsageDate(startDate)
	if err != nil {
		return nil, err
	}
	plan.start = start
	plan.end = start.In(timezone.Location()).AddDate(0, 0, 1)
	if !start.Before(plan.todayStart) {
		plan.start, plan.end = plan.todayStart, plan.todayStart
		return nil, nil
	}
	if plan.end.After(plan.todayStart) {
		plan.end = plan.todayStart
	}
	rows, err := conn.QueryContext(ctx, `
        SELECT group_id, COALESCE(SUM(actual_cost), 0)::text
        FROM usage_logs WHERE group_id IS NOT NULL
            AND created_at >= $1 AND created_at < $2
        GROUP BY group_id ORDER BY group_id
    `, plan.start.UTC(), plan.end.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var costs []groupUsageRollupCost
	for rows.Next() {
		var cost groupUsageRollupCost
		if err := rows.Scan(&cost.groupID, &cost.actualCost); err != nil {
			return nil, err
		}
		costs = append(costs, cost)
	}
	return costs, rows.Err()
}

func publishGroupUsageRollup(ctx context.Context, conn *sql.Conn, plan groupUsageRollupPlan, costs []groupUsageRollupCost) (bool, error) {
	tx, limited, cancel, err := beginGroupUsageRollupTransaction(ctx, conn)
	if err != nil {
		return false, err
	}
	defer cancel()
	defer func() { _ = tx.Rollback() }()
	var revision int64
	var closedBefore, stateTimezone string
	var retainedFrom, scanBefore time.Time
	if err := tx.QueryRowContext(limited, `
        SELECT source_revision, closed_before::text, retained_from, timezone_name, scan_before
        FROM usage_group_rollup_state WHERE id = 1 FOR UPDATE
    `).Scan(&revision, &closedBefore, &retainedFrom, &stateTimezone, &scanBefore); err != nil {
		return false, err
	}
	if revision != plan.sourceRevision || closedBefore != plan.closedBefore ||
		!retainedFrom.Equal(plan.previousRetainedFrom) || stateTimezone != plan.stateTimezone ||
		!scanBefore.Equal(plan.scanBefore) {
		return false, nil
	}
	if plan.stateTimezone != plan.targetTimezone {
		// Clear old timezone buckets in the SAME commit that switches the
		// timezone and publishes the first new day; readers never certify them.
		if _, err := tx.ExecContext(limited, "DELETE FROM usage_group_daily_rollups"); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.ExecContext(limited, `
            DELETE FROM usage_group_daily_rollups
            WHERE bucket_date < $1::date OR bucket_date >= $2::date OR bucket_date = $3::date
        `, service.GroupUsageDate(plan.retainedFrom), service.GroupUsageDate(plan.todayStart), service.GroupUsageDate(plan.start)); err != nil {
			return false, err
		}
	}
	if len(costs) > 0 {
		ids := make([]int64, 0, len(costs))
		amounts := make([]string, 0, len(costs))
		for _, cost := range costs {
			ids = append(ids, cost.groupID)
			amounts = append(amounts, cost.actualCost)
		}
		if _, err := tx.ExecContext(limited, `
            INSERT INTO usage_group_daily_rollups (bucket_date, group_id, actual_cost, computed_at)
            SELECT $1::date, x.group_id, x.actual_cost, NOW()
            FROM unnest($2::bigint[], $3::numeric[]) AS x(group_id, actual_cost)
        `, service.GroupUsageDate(plan.start), pq.Array(ids), pq.Array(amounts)); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(limited, `
        UPDATE usage_group_rollup_state SET closed_before = $1::date,
            retained_from = $2, timezone_name = $3, updated_at = NOW()
        WHERE id = 1
    `, service.GroupUsageDate(plan.end), plan.retainedFrom, plan.targetTimezone); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
