package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"golang.org/x/sync/errgroup"
)

// todayStatsFallbackConcurrency 批量 SQL 失败后逐个查询的并发上限。
const todayStatsFallbackConcurrency = 8

// ErrAccountStatsUnavailable 表示请求的账号今日统计全部查询失败。
var ErrAccountStatsUnavailable = infraerrors.ServiceUnavailable(
	"ACCOUNT_STATS_UNAVAILABLE",
	"account today stats are temporarily unavailable",
)

// GetTodayStatsBatchDetailed 批量获取账号今日统计，并报告哪些账号查询失败。
//
// 返回值：
//   - stats：每个有效账号 ID 都有一项；查询失败的账号补全 0，便于旧客户端照常显示。
//   - failed：查询失败的账号 ID，按输入顺序去重，永远非 nil。
//   - degraded：批量 SQL 失败（或仓储不支持批量）、走了逐个查询的回退路径。
//   - err：请求的账号全部失败时为 ErrAccountStatsUnavailable，此时 stats 与 failed 仍然完整。
//
// 批量 SQL 成功时，结果里没有的账号表示今天没有请求，补 0 但不算失败。
func (s *AccountUsageService) GetTodayStatsBatchDetailed(ctx context.Context, accountIDs []int64) (map[int64]*WindowStats, []int64, bool, error) {
	uniqueIDs := uniquePositiveAccountIDs(accountIDs)
	result := make(map[int64]*WindowStats, len(uniqueIDs))
	failed := []int64{}
	if len(uniqueIDs) == 0 {
		return result, failed, false, nil
	}

	startTime := timezone.Today()
	if batchReader, ok := s.usageLogRepo.(accountWindowStatsBatchReader); ok {
		statsByAccount, err := batchReader.GetAccountWindowStatsBatch(ctx, uniqueIDs, startTime)
		if err == nil {
			for _, accountID := range uniqueIDs {
				result[accountID] = windowStatsFromAccountStats(statsByAccount[accountID])
			}
			return result, failed, false, nil
		}
		slog.Warn("account_today_stats_batch_query_failed",
			"accounts", len(uniqueIDs),
			"error", err)
	}

	firstErr := s.fillTodayStatsOneByOne(ctx, uniqueIDs, startTime, result)

	for _, accountID := range uniqueIDs {
		if _, ok := result[accountID]; ok {
			continue
		}
		result[accountID] = &WindowStats{}
		failed = append(failed, accountID)
	}
	if len(failed) == 0 {
		return result, failed, true, nil
	}

	slog.Warn("account_today_stats_partial_failure",
		"failed", len(failed),
		"total", len(uniqueIDs),
		"error", firstErr)
	if len(failed) == len(uniqueIDs) {
		return result, failed, true, ErrAccountStatsUnavailable
	}
	return result, failed, true, nil
}

// fillTodayStatsOneByOne 逐个查询账号今日统计，成功的写入 result，返回遇到的第一个错误。
// 单个账号失败不会中断其他账号的查询。
func (s *AccountUsageService) fillTodayStatsOneByOne(ctx context.Context, accountIDs []int64, startTime time.Time, result map[int64]*WindowStats) error {
	var (
		mu       sync.Mutex
		firstErr error
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(todayStatsFallbackConcurrency)

	for _, accountID := range accountIDs {
		id := accountID
		g.Go(func() error {
			stats, err := s.usageLogRepo.GetAccountWindowStats(gctx, id, startTime)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return nil
			}
			result[id] = windowStatsFromAccountStats(stats)
			return nil
		})
	}

	_ = g.Wait()
	return firstErr
}

// uniquePositiveAccountIDs 去掉非正数与重复 ID，保持首次出现的顺序。
func uniquePositiveAccountIDs(accountIDs []int64) []int64 {
	uniqueIDs := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		uniqueIDs = append(uniqueIDs, accountID)
	}
	return uniqueIDs
}
