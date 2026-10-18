package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountScheduleState(t *testing.T) {
	now := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name string
		row  AccountStatusRow
		want string
	}{
		{"inactive wins", AccountStatusRow{Status: StatusDisabled, Schedulable: true, OverloadUntil: &future}, AccountStateInactive},
		{"plain inactive", AccountStatusRow{Status: "inactive", Schedulable: true}, AccountStateInactive},
		{"error", AccountStatusRow{Status: StatusError, RateLimitResetAt: &future}, AccountStateError},
		{"overloaded before rate limited", AccountStatusRow{Status: StatusActive, Schedulable: true, OverloadUntil: &future, RateLimitResetAt: &future}, AccountStateOverloaded},
		{"rate limited even when paused", AccountStatusRow{Status: StatusActive, Schedulable: false, RateLimitResetAt: &future}, AccountStateRateLimited},
		{"temp unschedulable", AccountStatusRow{Status: StatusActive, Schedulable: true, TempUnschedulableUntil: &future}, AccountStateTempUnschedulable},
		{"paused", AccountStatusRow{Status: StatusActive, Schedulable: false}, AccountStatePaused},
		{"expired with auto pause", AccountStatusRow{Status: StatusActive, Schedulable: true, ExpiresAt: &past, AutoPauseOnExpired: true}, AccountStatePaused},
		{"expired without auto pause stays available", AccountStatusRow{Status: StatusActive, Schedulable: true, ExpiresAt: &past}, AccountStateAvailable},
		{"elapsed windows are available", AccountStatusRow{Status: StatusActive, Schedulable: true, RateLimitResetAt: &past, OverloadUntil: &now}, AccountStateAvailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, AccountScheduleState(&tc.row, now))
		})
	}
}

type statusSummaryRepoFake struct {
	accounts []AccountStatusRow
	groups   []AccountStatusGroupRow
}

func (f *statusSummaryRepoFake) ListAccountsForStatusSummary(context.Context, string, *int64) ([]AccountStatusRow, error) {
	return f.accounts, nil
}

func (f *statusSummaryRepoFake) ListGroupsForStatusSummary(_ context.Context, _ string, groupID *int64, _ []int64) ([]AccountStatusGroupRow, error) {
	if groupID == nil {
		return f.groups, nil
	}
	for _, g := range f.groups {
		if g.ID == *groupID {
			return []AccountStatusGroupRow{g}, nil
		}
	}
	return nil, nil
}

type statusSummaryRulesFake struct{ rules []*OpsAlertRule }

func (f *statusSummaryRulesFake) ListAlertRules(context.Context) ([]*OpsAlertRule, error) {
	return f.rules, nil
}

type statusSummarySettingFake struct{ value string }

func (f *statusSummarySettingFake) GetValue(context.Context, string) (string, error) {
	if f.value == "" {
		return "", ErrSettingNotFound
	}
	return f.value, nil
}

func TestAccountStatusSummaryService_Summarize(t *testing.T) {
	now := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Minute)
	repo := &statusSummaryRepoFake{
		accounts: []AccountStatusRow{
			{ID: 1, Name: "a1", Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, GroupIDs: []int64{3, 5}},
			{ID: 2, Name: "a2", Platform: PlatformAnthropic, Status: StatusActive, Schedulable: false, RateLimitResetAt: &future, GroupIDs: []int64{3},
				ModelRateLimits: map[string]any{"claude-opus-4": map[string]any{"rate_limit_reset_at": future.Format(time.RFC3339)}, "old": map[string]any{"rate_limit_reset_at": now.Add(-time.Minute).Format(time.RFC3339)}}},
			{ID: 3, Name: "a3", Platform: PlatformOpenAI, Status: StatusError, ErrorMessage: "boom", GroupIDs: []int64{5}},
			{ID: 4, Name: "a4", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true},
		},
		groups: []AccountStatusGroupRow{
			{ID: 3, Name: "高级组", Platform: PlatformAnthropic, Status: StatusActive},
			{ID: 5, Name: "混合", Platform: PlatformOpenAI, Status: StatusActive},
			{ID: 9, Name: "空组", Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	rules := &statusSummaryRulesFake{rules: []*OpsAlertRule{
		{ID: 35, Enabled: true, MetricType: "group_available_ratio", Operator: "<", Threshold: 60, Filters: map[string]any{"group_id": float64(3)}},
		{ID: 36, Enabled: false, MetricType: "group_available_accounts", Operator: "<", Threshold: 5, Filters: map[string]any{"group_id": float64(5)}},
	}}
	svc := NewAccountStatusSummaryService(repo, rules, &statusSummarySettingFake{})
	svc.now = func() time.Time { return now }

	got, err := svc.Summarize(context.Background(), AccountStatusSummaryQuery{IncludeAccounts: true})
	require.NoError(t, err)

	require.Equal(t, AccountStateCounts{Total: 4, Available: 2, Error: 1, RateLimited: 1, Stopped: 1}, got.Totals)
	require.Len(t, got.Platforms, 2)
	require.Equal(t, PlatformAnthropic, got.Platforms[0].Platform)
	require.Equal(t, int64(2), got.Platforms[0].Total)

	require.Len(t, got.Groups, 3)
	g3 := got.Groups[0]
	require.Equal(t, int64(3), g3.GroupID)
	require.Equal(t, int64(2), g3.Total)
	require.Equal(t, int64(1), g3.Available)
	require.True(t, g3.Shortage.IsShort)
	require.Equal(t, "alert_rule", g3.Shortage.Source)
	require.Equal(t, int64(35), *g3.Shortage.RuleID)
	require.InDelta(t, 50.0, *g3.Shortage.Current, 1e-9)

	g5 := got.Groups[1]
	require.Equal(t, int64(2), g5.Total, "an account in two groups counts in both")
	require.Equal(t, int64(1), g5.Available)
	require.Equal(t, "default", g5.Shortage.Source, "disabled rules fall back to the default ratio")
	require.False(t, g5.Shortage.IsShort, "1 of 2 is not below 0.5")
	require.InDelta(t, 0.5, *g5.Shortage.Threshold, 1e-9)

	g9 := got.Groups[2]
	require.Equal(t, int64(0), g9.Total)
	require.True(t, g9.Shortage.IsShort, "no available account is always a shortage")

	require.Len(t, got.Accounts, 4)
	a2 := got.Accounts[1]
	require.Equal(t, AccountStateRateLimited, a2.State)
	require.True(t, a2.Stopped)
	require.Len(t, a2.ModelRateLimits, 1)
	require.Equal(t, "claude-opus-4", a2.ModelRateLimits[0].Model)

	withoutAccounts, err := svc.Summarize(context.Background(), AccountStatusSummaryQuery{})
	require.NoError(t, err)
	require.Nil(t, withoutAccounts.Accounts)
}

func TestAccountStatusSummaryService_MinRatioSources(t *testing.T) {
	repo := &statusSummaryRepoFake{
		accounts: []AccountStatusRow{
			{ID: 1, Status: StatusActive, Schedulable: true, GroupIDs: []int64{3}},
			{ID: 2, Status: StatusActive, Schedulable: false, GroupIDs: []int64{3}},
			{ID: 3, Status: StatusActive, Schedulable: false, GroupIDs: []int64{3}},
		},
		groups: []AccountStatusGroupRow{{ID: 3, Name: "g", Status: StatusActive}},
	}
	// Site setting 0.3: 1/3 is not short.
	svc := NewAccountStatusSummaryService(repo, &statusSummaryRulesFake{}, &statusSummarySettingFake{value: "0.3"})
	got, err := svc.Summarize(context.Background(), AccountStatusSummaryQuery{})
	require.NoError(t, err)
	require.False(t, got.Groups[0].Shortage.IsShort)

	// Query override 0.5 wins: 1/3 is short.
	ratio := 0.5
	got, err = svc.Summarize(context.Background(), AccountStatusSummaryQuery{DefaultMinRatio: &ratio})
	require.NoError(t, err)
	require.True(t, got.Groups[0].Shortage.IsShort)

	// A malformed setting falls back to 0.5.
	svc = NewAccountStatusSummaryService(repo, &statusSummaryRulesFake{}, &statusSummarySettingFake{value: "abc"})
	got, err = svc.Summarize(context.Background(), AccountStatusSummaryQuery{})
	require.NoError(t, err)
	require.InDelta(t, 0.5, *got.Groups[0].Shortage.Threshold, 1e-9)
}
