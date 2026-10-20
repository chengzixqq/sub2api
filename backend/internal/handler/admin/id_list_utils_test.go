//go:build unit

package admin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeInt64IDList(t *testing.T) {
	tests := []struct {
		name string
		in   []int64
		want []int64
	}{
		{"nil input", nil, nil},
		{"empty input", []int64{}, nil},
		{"single element", []int64{5}, []int64{5}},
		{"already sorted unique", []int64{1, 2, 3}, []int64{1, 2, 3}},
		{"duplicates removed", []int64{3, 1, 3, 2, 1}, []int64{1, 2, 3}},
		{"zero filtered", []int64{0, 1, 2}, []int64{1, 2}},
		{"negative filtered", []int64{-5, -1, 3}, []int64{3}},
		{"all invalid", []int64{0, -1, -2}, []int64{}},
		{"sorted output", []int64{9, 3, 7, 1}, []int64{1, 3, 7, 9}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeInt64IDList(tc.in)
			if tc.want == nil {
				require.Nil(t, got)
			} else {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestBuildAccountTodayStatsBatchCacheKey(t *testing.T) {
	tests := []struct {
		name string
		ids  []int64
		want string
	}{
		{"empty", nil, "accounts_today_stats_empty"},
		{"single", []int64{42}, "accounts_today_stats:42"},
		{"multiple", []int64{1, 2, 3}, "accounts_today_stats:1,2,3"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAccountTodayStatsBatchCacheKey(tc.ids)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuildAccountTodayStatsBatchCacheKeyForGroup(t *testing.T) {
	ids := []int64{1, 2, 3}
	ungrouped := buildAccountTodayStatsBatchCacheKey(ids)
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	nextDay := day.AddDate(0, 0, 1)

	// 不按分组时与原来逐字一致，日期不参与。
	require.Equal(t, "accounts_today_stats:1,2,3", buildAccountTodayStatsBatchCacheKeyForGroup(ids, 0, day))
	require.Equal(t, ungrouped, buildAccountTodayStatsBatchCacheKeyForGroup(ids, -5, nextDay))
	require.Equal(t, "accounts_today_stats:g7:2026-10-20:1,2,3", buildAccountTodayStatsBatchCacheKeyForGroup(ids, 7, day))

	g7 := buildAccountTodayStatsBatchCacheKeyForGroup(ids, 7, day)
	g9 := buildAccountTodayStatsBatchCacheKeyForGroup(ids, 9, day)
	require.NotEqual(t, g7, g9)
	require.NotEqual(t, ungrouped, g7)
	require.NotEqual(t, ungrouped, g9)
	// 跨零点换天后分组键必须变化。
	require.NotEqual(t, g7, buildAccountTodayStatsBatchCacheKeyForGroup(ids, 7, nextDay))
	// 分组 1 + 账号 2 与不分组的账号 "1:2" 之类的拼接不能撞键。
	require.NotEqual(t, buildAccountTodayStatsBatchCacheKeyForGroup([]int64{2}, 1, day), buildAccountTodayStatsBatchCacheKey([]int64{12}))
}
