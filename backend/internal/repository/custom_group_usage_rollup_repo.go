package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// groupUsageRollupSnapshot 是一次汇总水位的快照。
// closedBefore / retainedFrom 在水位无效时退化为 1970-01-01，
// 与此前 SQL 里 CASE WHEN valid 的语义一一对应。
type groupUsageRollupSnapshot struct {
	valid        bool
	closedBefore string    // date，历史日桶的右开边界
	retainedDate string    // retained_from 在服务端时区内的日期，历史日桶的左闭边界
	tailStart    time.Time // 尾段（还没进日桶的那部分 usage_logs）的起点
}

// readGroupUsageRollupSnapshot 读一次汇总水位并在 Go 侧判定有效性。
//
// 判定条件与原先 SQL 里的 state_values CTE 完全一致：
// 恰好一行、时区名与当前服务端配置相同、且水位不在未来。
func (r *usageLogRepository) readGroupUsageRollupSnapshot(ctx context.Context, timezoneName, todayDate string) (groupUsageRollupSnapshot, error) {
	epoch := time.Unix(0, 0).UTC()
	invalid := groupUsageRollupSnapshot{
		closedBefore: "1970-01-01",
		retainedDate: "1970-01-01",
		tailStart:    epoch,
	}

	var rowCount int
	var closedBefore sql.NullString
	var retainedFrom sql.NullTime
	var stateTimezone sql.NullString
	if err := scanSingleRow(ctx, r.sql, `
		SELECT
			COUNT(*),
			MAX(closed_before)::text,
			MAX(retained_from),
			MAX(timezone_name)
		FROM usage_group_rollup_state
		WHERE id = 1
	`, nil, &rowCount, &closedBefore, &retainedFrom, &stateTimezone); err != nil {
		return groupUsageRollupSnapshot{}, fmt.Errorf("读取分组用量汇总水位: %w", err)
	}
	if rowCount != 1 || !closedBefore.Valid || !retainedFrom.Valid ||
		!stateTimezone.Valid || stateTimezone.String != timezoneName ||
		closedBefore.String > todayDate {
		return invalid, nil
	}

	tailStart, err := service.ParseGroupUsageDate(closedBefore.String)
	if err != nil {
		// 水位值解析不出来，按无效处理：宁可多扫一次，也不要算错钱。
		return invalid, nil //nolint:nilerr // 与上面的 valid 判定同语义，均降级为全量重算
	}
	return groupUsageRollupSnapshot{
		valid:        true,
		closedBefore: closedBefore.String,
		retainedDate: service.GroupUsageDate(retainedFrom.Time),
		tailStart:    tailStart.UTC(),
	}, nil
}

func (r *usageLogRepository) getAllGroupUsageSummaryFromRollups(ctx context.Context, todayStart time.Time) ([]usagestats.GroupUsageSummary, error) {
	if beginner, ok := r.sql.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err := beginner.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		result, err := newUsageLogRepositoryWithSQL(nil, tx).getAllGroupUsageSummaryFromSnapshot(ctx, todayStart)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	return r.getAllGroupUsageSummaryFromSnapshot(ctx, todayStart)
}

func (r *usageLogRepository) getAllGroupUsageSummaryFromSnapshot(ctx context.Context, todayStart time.Time) (results []usagestats.GroupUsageSummary, err error) {
	todayStart = service.GroupUsageTodayStart(todayStart)
	yesterdayStart := service.GroupUsageYesterdayStart(todayStart)
	timezoneName := service.GroupUsageTimezoneName()
	todayDate := service.GroupUsageDate(todayStart)
	yesterdayDate := service.GroupUsageDate(yesterdayStart)

	state, err := r.readGroupUsageRollupSnapshot(ctx, timezoneName, todayDate)
	if err != nil {
		return nil, err
	}

	// 尾段起点必须以**查询参数**的形式给进来。
	//
	// 此前它是在同一条 SQL 里由 state CTE 算出、再 CROSS JOIN 给 tail 用的，
	// 于是 created_at 的下界对 planner 来说是个运行期才知道的值：既进不了
	// index cond，也估不准选择性，只能退化成 usage_logs 全表扫。
	// 生产实测（1721 万行 / PostgreSQL 17）：
	//   Seq Scan on usage_logs … rows=17212870, Rows Removed by Join Filter: 17178082
	//   Execution Time: 48720 ms
	// 换成参数之后走 idx_usage_logs_created_at：
	//   Index Scan … Index Cond: (created_at >= $7)
	//   Execution Time: 34 ms
	const query = `
		WITH historical AS (
			SELECT
				rollup.group_id,
				COALESCE(SUM(rollup.actual_cost), 0) AS actual_cost,
				COALESCE(SUM(rollup.actual_cost) FILTER (
					WHERE rollup.bucket_date = $3::date
				), 0) AS yesterday_cost
			FROM usage_group_daily_rollups rollup
			WHERE $4::boolean
				AND rollup.bucket_date >= $5::date
				AND rollup.bucket_date < $6::date
			GROUP BY rollup.group_id
		),
		tail AS (
			SELECT
				ul.group_id,
				COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
				COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $1), 0) AS today_cost,
				COALESCE(SUM(ul.actual_cost) FILTER (
					WHERE ul.created_at >= $2
						AND ul.created_at < $1
				), 0) AS yesterday_cost
			FROM usage_logs ul
			WHERE ul.created_at >= $7
			GROUP BY ul.group_id
		)
		SELECT
			g.id AS group_id,
			COALESCE(historical.actual_cost, 0) + COALESCE(tail.actual_cost, 0) AS total_cost,
			COALESCE(tail.today_cost, 0) AS today_cost,
			COALESCE(historical.yesterday_cost, 0) + COALESCE(tail.yesterday_cost, 0) AS yesterday_cost
		FROM groups g
		LEFT JOIN historical ON historical.group_id = g.id
		LEFT JOIN tail ON tail.group_id = g.id
		ORDER BY g.id
	`

	rows, err := r.sql.QueryContext(
		ctx,
		query,
		todayStart,
		yesterdayStart,
		yesterdayDate,
		state.valid,
		state.retainedDate,
		state.closedBefore,
		state.tailStart,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.GroupUsageSummary, 0)
	for rows.Next() {
		var row usagestats.GroupUsageSummary
		if err := rows.Scan(&row.GroupID, &row.TotalCost, &row.TodayCost, &row.YesterdayCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func lockGroupUsageRollupState(ctx context.Context, tx *sql.Tx) error {
	var id int16
	if err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM usage_group_rollup_state
		WHERE id = 1
		FOR UPDATE
	`).Scan(&id); err != nil {
		return fmt.Errorf("锁定分组用量汇总水位: %w", err)
	}
	return nil
}

func invalidateGroupUsageRollupsAt(ctx context.Context, tx *sql.Tx, affectedAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE usage_group_rollup_state
		SET closed_before = LEAST(
			closed_before,
			($1::timestamptz AT TIME ZONE timezone_name)::date
		),
			updated_at = NOW()
		WHERE id = 1
	`, affectedAt.UTC())
	return err
}
