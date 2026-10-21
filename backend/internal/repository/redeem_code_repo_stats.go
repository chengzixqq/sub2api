package repository

import (
	"context"
	"math"
	"slices"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.RedeemCodeStatsRepository = (*redeemCodeRepository)(nil)

// redeemCodeStatsRow is one (type, status) group: its code count and face value sum.
type redeemCodeStatsRow struct {
	Type   string  `json:"type"`
	Status string  `json:"status"`
	Count  int64   `json:"count"`
	Sum    float64 `json:"sum"`
}

// StatsWithFilters counts the codes ListWithFilters would return for filter (filter.Status ignored), with the same
// predicates. One GROUP BY type, status gives the totals; a second COUNT moves the lapsed unused codes (expires_at
// passed) into expired, as the list does. The two statements are not one snapshot: a concurrent write can shift a
// code between buckets, but the move is clamped so the buckets still add up to Total.
func (r *redeemCodeRepository) StatsWithFilters(ctx context.Context, filter service.RedeemCodeListFilter) (*service.RedeemCodeStats, error) {
	now := time.Now()
	preds := redeemCodeFilterPredicates(filter)

	var rows []redeemCodeStatsRow
	err := r.client.RedeemCode.Query().
		Where(preds...).
		GroupBy(redeemcode.FieldType, redeemcode.FieldStatus).
		Aggregate(dbent.As(dbent.Count(), "count"), dbent.As(dbent.Sum(redeemcode.FieldValue), "sum")).
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}

	stats := &service.RedeemCodeStats{ByType: make(map[string]int64, len(rows))}
	var distributed float64
	for _, row := range rows {
		stats.Total += row.Count
		stats.ByType[row.Type] += row.Count
		switch row.Status {
		case service.StatusUnused:
			stats.Unused += row.Count
		case service.StatusUsed:
			stats.Used += row.Count
			if row.Type == service.RedeemTypeBalance {
				distributed += row.Sum
			}
		case service.StatusExpired:
			stats.Expired += row.Count
		case service.StatusDisabled:
			stats.Disabled += row.Count
		}
	}

	if stats.Unused > 0 {
		lapsed, err := r.client.RedeemCode.Query().
			Where(append(slices.Clip(preds), redeemCodeLapsed(now))...).
			Count(ctx)
		if err != nil {
			return nil, err
		}
		moved := min(int64(lapsed), stats.Unused)
		stats.Unused -= moved
		stats.Expired += moved
	}
	stats.ValueDistributed = math.Round(distributed*1e8) / 1e8
	return stats, nil
}
