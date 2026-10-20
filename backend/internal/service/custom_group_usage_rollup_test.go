//go:build unit

package service

import (
	"testing"
	"time"

	appTimezone "github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func useGroupUsageTestTimezone(t *testing.T, name string) bool {
	t.Helper()
	return !runServiceTestInFreshProcess(t, serviceTestTimezoneEnv+"="+name)
}

func TestGroupUsageDateUsesConfiguredTimezoneBoundary(t *testing.T) {
	if !useGroupUsageTestTimezone(t, "America/New_York") {
		return
	}

	beforeMidnight := time.Date(2026, 3, 9, 3, 59, 59, 0, time.UTC)
	atMidnight := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)

	require.Equal(t, "2026-03-08", GroupUsageDate(beforeMidnight))
	require.Equal(t, "2026-03-09", GroupUsageDate(atMidnight))
	require.Equal(t, atMidnight, GroupUsageTodayStart(atMidnight))
}

func TestGroupUsageParseDateUsesConfiguredTimezone(t *testing.T) {
	if !useGroupUsageTestTimezone(t, "America/New_York") {
		return
	}

	parsed, err := ParseGroupUsageDate("2026-03-08")
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), parsed.UTC())
	require.Equal(t, "America/New_York", parsed.Location().String())
}

func TestGroupUsageYesterdayStartHandlesDST(t *testing.T) {
	if !useGroupUsageTestTimezone(t, "America/New_York") {
		return
	}

	todayStart := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
	yesterdayStart := GroupUsageYesterdayStart(todayStart)

	require.Equal(t, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), yesterdayStart)
	require.Equal(t, 23*time.Hour, todayStart.Sub(yesterdayStart))
	require.Equal(t, "America/New_York", GroupUsageTimezoneName())
}

// TestAccountTodayStatsStartMatchesGroupUsageTodayStart 锁定账号今日（today-stats 用
// timezone.Today()，即配置时区的 StartOfDay）与站长分组今日（GroupUsageTodayStart）
// 是同一时刻，含夏令时切换日的午夜前后；否则分组成员行之和与分组今日对不上。
func TestAccountTodayStatsStartMatchesGroupUsageTodayStart(t *testing.T) {
	if !useGroupUsageTestTimezone(t, "America/New_York") {
		return
	}

	moments := []time.Time{
		time.Date(2026, 3, 8, 4, 59, 59, 0, time.UTC),  // 3/7 23:59:59 EST
		time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC),    // 3/8 00:00 EST（当天 23 小时）
		time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),   // 切到 EDT 之后
		time.Date(2026, 3, 9, 3, 59, 59, 0, time.UTC),  // 3/8 23:59:59 EDT
		time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC),    // 3/9 00:00 EDT
		time.Date(2026, 11, 1, 3, 59, 59, 0, time.UTC), // 10/31 23:59:59 EDT
		time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),   // 11/1 00:00 EDT（当天 25 小时）
		time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC),   // 切回 EST 之后
		time.Date(2026, 11, 2, 4, 59, 59, 0, time.UTC), // 11/1 23:59:59 EST
		time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC),   // 11/2 00:00 EST
	}
	for _, at := range moments {
		require.True(t, appTimezone.StartOfDay(at).Equal(GroupUsageTodayStart(at)), "at=%s", at)
	}
	require.Equal(t, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), GroupUsageTodayStart(moments[2]))
	require.Equal(t, time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), GroupUsageTodayStart(moments[7]))
}
