package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.QuickTopupRepository = (*adminUserAdjustmentRepository)(nil)

// QuickTopup reads the recently adjusted users and the low-balance users.
func (r *adminUserAdjustmentRepository) QuickTopup(ctx context.Context, q service.QuickTopupQuery) (*service.QuickTopupResult, error) {
	recent, err := r.quickTopupRecent(ctx, q)
	if err != nil {
		return nil, err
	}
	low, err := r.quickTopupLowBalance(ctx, q)
	if err != nil {
		return nil, err
	}
	return &service.QuickTopupResult{Recent: recent, LowBalance: low}, nil
}

func (r *adminUserAdjustmentRepository) quickTopupRecent(ctx context.Context, q service.QuickTopupQuery) (out []service.QuickTopupRecentUser, err error) {
	out = []service.QuickTopupRecentUser{}
	if q.RecentLimit == 0 {
		return out, nil
	}
	// Site grants (signup / first-bind default balance, E10) are ledger entries without an operator; they are not
	// admin top-ups and would crowd the strip with every new user.
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (user_id) user_id, created_at, delta, operation
			FROM admin_user_adjustments
			WHERE kind = $1 AND user_id IS NOT NULL AND created_at >= NOW() - make_interval(days => $2)
			  AND NOT (source = $5 AND operator_user_id IS NULL)
			ORDER BY user_id, created_at DESC, id DESC
		)
		SELECT u.id, u.email, u.username, u.status, u.balance, l.created_at, l.delta::text, l.operation
		FROM latest l
		JOIN users u ON u.id = l.user_id
		WHERE u.deleted_at IS NULL AND u.status = $3
		ORDER BY l.created_at DESC, u.id DESC
		LIMIT $4`,
		service.AdjustmentKindBalance, q.RecentDays, service.StatusActive, q.RecentLimit, service.AdjustmentSourceDefaultBalance)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			out = nil
		}
	}()
	for rows.Next() {
		var item service.QuickTopupRecentUser
		if err = rows.Scan(&item.UserID, &item.Email, &item.Username, &item.Status, &item.Balance,
			&item.LastAdjustedAt, &item.LastDelta, &item.LastOperation); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// quickTopupLowBalance applies the same threshold rules as balance
// notifications: a user threshold wins over the global one (which only counts
// while global reminders are on); percentage thresholds are taken of
// total_recharged.
func (r *adminUserAdjustmentRepository) quickTopupLowBalance(ctx context.Context, q service.QuickTopupQuery) (out []service.QuickTopupLowBalanceUser, err error) {
	out = []service.QuickTopupLowBalanceUser{}
	if q.LowLimit == 0 {
		return out, nil
	}
	globalThreshold := sql.NullFloat64{Float64: q.GlobalThreshold, Valid: q.GlobalEnabled && q.GlobalThreshold > 0}
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx, `
		WITH resolved AS (
			SELECT u.id, u.email, u.username, u.status, u.balance,
				CASE WHEN u.balance_notify_threshold IS NOT NULL THEN 'user' ELSE 'global' END AS source,
				u.balance_notify_threshold_type AS threshold_type,
				CASE
					WHEN COALESCE(u.balance_notify_threshold, $1) IS NULL THEN NULL
					WHEN u.balance_notify_threshold_type = 'percentage' AND u.total_recharged > 0
						THEN u.total_recharged * COALESCE(u.balance_notify_threshold, $1) / 100
					ELSE COALESCE(u.balance_notify_threshold, $1)
				END AS threshold
			FROM users u
			WHERE u.deleted_at IS NULL AND u.status = $2
		)
		SELECT id, email, username, status, balance, threshold, source, threshold_type
		FROM resolved
		WHERE threshold > 0 AND balance < threshold
		ORDER BY balance / threshold ASC, id ASC
		LIMIT $3`,
		globalThreshold, service.StatusActive, q.LowLimit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			out = nil
		}
	}()
	for rows.Next() {
		var item service.QuickTopupLowBalanceUser
		if err = rows.Scan(&item.UserID, &item.Email, &item.Username, &item.Status, &item.Balance,
			&item.EffectiveThreshold, &item.ThresholdSource, &item.ThresholdType); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
