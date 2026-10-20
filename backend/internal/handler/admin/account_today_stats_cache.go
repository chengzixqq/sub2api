package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var accountTodayStatsBatchCache = newSnapshotCache(30 * time.Second)

func buildAccountTodayStatsBatchCacheKey(accountIDs []int64) string {
	if len(accountIDs) == 0 {
		return "accounts_today_stats_empty"
	}
	var b strings.Builder
	b.Grow(len(accountIDs) * 6)
	_, _ = b.WriteString("accounts_today_stats:")
	writeTodayStatsCacheKeyIDs(&b, accountIDs)
	return b.String()
}

// buildAccountTodayStatsBatchCacheKeyForGroup 是带 group_id 时的缓存键。
// groupID <= 0（不按分组）时与 buildAccountTodayStatsBatchCacheKey 逐字相同；
// 否则形如 accounts_today_stats:g7:1,2,3，不同分组之间、分组与不分组之间不会串用。
func buildAccountTodayStatsBatchCacheKeyForGroup(accountIDs []int64, groupID int64) string {
	if groupID <= 0 {
		return buildAccountTodayStatsBatchCacheKey(accountIDs)
	}
	var b strings.Builder
	b.Grow(len(accountIDs)*6 + 32)
	_, _ = b.WriteString("accounts_today_stats:g")
	_, _ = b.WriteString(strconv.FormatInt(groupID, 10))
	_ = b.WriteByte(':')
	writeTodayStatsCacheKeyIDs(&b, accountIDs)
	return b.String()
}

func writeTodayStatsCacheKeyIDs(b *strings.Builder, accountIDs []int64) {
	for i, id := range accountIDs {
		if i > 0 {
			_ = b.WriteByte(',')
		}
		_, _ = b.WriteString(strconv.FormatInt(id, 10))
	}
}

// todayStatsBatchPayload 组装 today-stats/batch 的响应 data。
// failed_account_ids 必须是非 nil 切片，确保序列化为 [] 而不是 null。
func todayStatsBatchPayload(stats map[int64]*service.WindowStats, failedIDs []int64, degraded bool) gin.H {
	if failedIDs == nil {
		failedIDs = []int64{}
	}
	return gin.H{
		"stats":              stats,
		"failed_account_ids": failedIDs,
		"degraded":           degraded,
	}
}

// emptyTodayStatsBatchPayload 是没有可查询账号时的响应 data，字段与正常响应一致。
func emptyTodayStatsBatchPayload() gin.H {
	return gin.H{
		"stats":              map[string]any{},
		"failed_account_ids": []int64{},
		"degraded":           false,
	}
}
