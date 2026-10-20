package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"golang.org/x/sync/errgroup"
)

// todayStatsFallbackConcurrency 批量 SQL 失败后逐个查询的并发上限。
const todayStatsFallbackConcurrency = 8

// ErrAccountStatsUnavailable 表示请求的账号今日统计全部查询失败。
var ErrAccountStatsUnavailable = infraerrors.ServiceUnavailable(
	"ACCOUNT_STATS_UNAVAILABLE",
	"account today stats are temporarily unavailable",
)

// ErrInvalidGroupFilter 表示 group_id 参数不是正整数。
var ErrInvalidGroupFilter = infraerrors.BadRequest(
	"INVALID_GROUP_FILTER",
	"group_id must be a positive integer",
)

// accountWindowStatsGroupBatchReader 是按分组批量读取账号窗口统计的可选仓储能力。
// 不并入 UsageLogRepository，避免为一个新方法改动所有测试桩。
type accountWindowStatsGroupBatchReader interface {
	GetAccountWindowStatsBatchByGroup(ctx context.Context, accountIDs []int64, groupID int64, startTime time.Time) (map[int64]*usagestats.AccountStats, error)
}

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

// GetTodayStatsBatchByGroup 批量获取账号今日在指定分组下的统计，只计 usage_logs.group_id
// （请求时 Key 所属分组）等于 groupID 的用量，与 groups/usage-summary 同口径；
// “今天”的起点与不分组路径相同（timezone.Today()，等于站长分组汇总的 GroupUsageTodayStart）。
//
// 返回值与 GetTodayStatsBatchDetailed 同形，但这条路径不做逐个账号回退：
//   - 成功：每个有效账号 ID 都有一项（今天在该分组没有请求的补 0），failed 为空，degraded=false。
//   - groupID 不是正数：ErrInvalidGroupFilter。
//   - 分组 SQL 失败或仓储不支持分组读取：stats 为 nil，failed 为全部账号，
//     err 为 ErrAccountStatsUnavailable。宁可失败也不返回未按分组过滤或补 0 的数字。
func (s *AccountUsageService) GetTodayStatsBatchByGroup(ctx context.Context, accountIDs []int64, groupID int64) (map[int64]*WindowStats, []int64, bool, error) {
	if groupID <= 0 {
		return nil, []int64{}, false, ErrInvalidGroupFilter
	}
	uniqueIDs := uniquePositiveAccountIDs(accountIDs)
	if len(uniqueIDs) == 0 {
		return map[int64]*WindowStats{}, []int64{}, false, nil
	}

	groupReader, ok := s.usageLogRepo.(accountWindowStatsGroupBatchReader)
	if !ok {
		slog.Error("account_today_stats_group_reader_unsupported",
			"accounts", len(uniqueIDs),
			"group_id", groupID)
		return nil, uniqueIDs, false, ErrAccountStatsUnavailable
	}

	statsByAccount, err := groupReader.GetAccountWindowStatsBatchByGroup(ctx, uniqueIDs, groupID, timezone.Today())
	if err != nil {
		slog.Warn("account_today_stats_group_query_failed",
			"accounts", len(uniqueIDs),
			"group_id", groupID,
			"error", err)
		return nil, uniqueIDs, false, ErrAccountStatsUnavailable
	}

	result := make(map[int64]*WindowStats, len(uniqueIDs))
	for _, accountID := range uniqueIDs {
		result[accountID] = windowStatsFromAccountStats(statsByAccount[accountID])
	}
	return result, []int64{}, false, nil
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
