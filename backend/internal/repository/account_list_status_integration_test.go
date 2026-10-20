//go:build integration

package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// accountListStatusSpec describes one fixture account; mutate runs after the
// insert because mustCreateAccount cannot set every runtime column.
type accountListStatusSpec struct {
	key      string
	platform string
	accType  string
	grouped  bool
	mutate   func(u *dbent.AccountUpdateOne)
}

type accountListStatusFixture struct {
	ctx       context.Context
	client    *dbent.Client
	repo      *accountRepository
	groupRepo *groupRepository
	prefix    string
	groupID   int64
	accounts  map[string]*service.Account
}

func newAccountListStatusFixture(t *testing.T) *accountListStatusFixture {
	t.Helper()
	tx := testEntTx(t)
	client := tx.Client()
	return &accountListStatusFixture{
		ctx:       service.WithScope(context.Background(), service.AdminScope()),
		client:    client,
		repo:      newAccountRepositoryWithSQL(client, tx, nil),
		groupRepo: newGroupRepositoryWithSQL(client, tx),
		prefix:    "c14-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12] + "-",
		accounts:  map[string]*service.Account{},
	}
}

func (f *accountListStatusFixture) create(t *testing.T, specs []accountListStatusSpec) {
	t.Helper()
	if f.groupID == 0 {
		f.groupID = mustCreateGroup(t, f.client, &service.Group{Name: f.prefix + "group"}).ID
	}
	for _, spec := range specs {
		created := mustCreateAccount(t, f.client, &service.Account{
			Name: f.prefix + spec.key, Platform: spec.platform, Type: spec.accType,
		})
		if spec.mutate != nil {
			update := f.client.Account.UpdateOneID(created.ID)
			spec.mutate(update)
			require.NoError(t, update.Exec(context.Background()), spec.key)
		}
		if spec.grouped {
			mustBindAccountToGroup(t, f.client, created.ID, f.groupID, 1)
		}
		loaded, err := f.repo.GetByID(f.ctx, created.ID)
		if errors.Is(err, service.ErrAccountNotFound) {
			continue // soft deleted: nothing to classify
		}
		require.NoError(t, err)
		f.accounts[spec.key] = loaded
	}
}

// list returns the fixture keys in result order plus the reported total.
func (f *accountListStatusFixture) list(t *testing.T, ctx context.Context, status string, filter service.AccountListCountsFilter, sortBy, sortOrder string, pageSize int) ([]string, int64) {
	t.Helper()
	accounts, page, err := f.repo.ListWithFilters(ctx, pagination.PaginationParams{
		Page: 1, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder,
	}, filter.Platform, filter.AccountType, status, f.prefix, filter.GroupID, filter.PrivacyMode)
	require.NoError(t, err)
	keys := make([]string, 0, len(accounts))
	for _, account := range accounts {
		keys = append(keys, strings.TrimPrefix(account.Name, f.prefix))
	}
	return keys, page.Total
}

func (f *accountListStatusFixture) keysOf(t *testing.T, status string) []string {
	t.Helper()
	keys, total := f.list(t, f.ctx, status, service.AccountListCountsFilter{}, "id", "asc", 100)
	require.EqualValues(t, len(keys), total, status)
	sort.Strings(keys)
	return keys
}

func accountListStateSpecs() []accountListStatusSpec {
	now := time.Now()
	future := func(d time.Duration) time.Time { return now.Add(d) }
	return []accountListStatusSpec{
		{key: "available", platform: service.PlatformOpenAI, grouped: true},
		{key: "overloaded", accType: service.AccountTypeAPIKey, grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetOverloadUntil(future(10 * time.Minute))
		}},
		{key: "rate_limited", platform: service.PlatformOpenAI, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetRateLimitResetAt(future(10 * time.Minute))
		}},
		{key: "temp", grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetTempUnschedulableUntil(future(10 * time.Minute))
		}},
		{key: "paused", platform: service.PlatformOpenAI, grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetSchedulable(false)
		}},
		{key: "overloaded_paused", grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetOverloadUntil(future(10 * time.Minute)).SetSchedulable(false)
		}},
		{key: "expired_auto_pause", grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetExpiresAt(future(-time.Hour)).SetAutoPauseOnExpired(true)
		}},
		{key: "expired_no_auto_pause", accType: service.AccountTypeAPIKey, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetExpiresAt(future(-time.Hour)).SetAutoPauseOnExpired(false)
		}},
		{key: "error", platform: service.PlatformOpenAI, grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetStatus(service.StatusError)
		}},
		{key: "error_overloaded", accType: service.AccountTypeAPIKey, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetStatus(service.StatusError).SetOverloadUntil(future(10 * time.Minute))
		}},
		{key: "inactive", accType: service.AccountTypeAPIKey, grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetStatus("inactive")
		}},
		{key: "disabled", mutate: func(u *dbent.AccountUpdateOne) {
			u.SetStatus(service.StatusDisabled)
		}},
		{key: "stale_windows", platform: service.PlatformOpenAI, grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			past := future(-10 * time.Minute)
			u.SetOverloadUntil(past).SetRateLimitResetAt(past).SetTempUnschedulableUntil(past)
		}},
		// Soft-deleted accounts must stay out of every list, total and count.
		{key: "deleted_error", grouped: true, mutate: func(u *dbent.AccountUpdateOne) {
			u.SetStatus(service.StatusError).SetDeletedAt(future(-time.Minute))
		}},
	}
}

func TestAccountListStatus_OverloadedAndMultiValueUnion(t *testing.T) {
	f := newAccountListStatusFixture(t)
	f.create(t, accountListStateSpecs())

	require.Equal(t, []string{"overloaded", "overloaded_paused"}, f.keysOf(t, service.AccountListStatusOverloaded),
		"overloaded only matches active accounts inside the overload window")

	union := f.keysOf(t, "error,rate_limited")
	require.Equal(t, []string{"error", "error_overloaded", "rate_limited"}, union)
	_, total := f.list(t, f.ctx, "error, rate_limited", service.AccountListCountsFilter{}, "", "", 2)
	require.EqualValues(t, 3, total, "total counts the whole union, not the page")

	require.Equal(t, f.keysOf(t, service.StatusError), f.keysOf(t, "error,error"))
	require.NotContains(t, f.keysOf(t, service.StatusError), "deleted_error")
	require.NotContains(t, f.accounts, "deleted_error")
	require.Equal(t, []string{"error", "error_overloaded"}, f.keysOf(t, "error,unknown-status"))
	require.Equal(t, []string{"disabled", "error", "error_overloaded", "inactive"}, f.keysOf(t, "error,inactive,disabled"))

	// Q18: active keeps its legacy meaning and still includes overloaded accounts.
	require.Contains(t, f.keysOf(t, service.StatusActive), "overloaded")
	// The legacy unschedulable value still counts overloaded accounts whose
	// switch is off (D02 calls them overloaded); only new values follow D02.
	require.Equal(t, []string{"overloaded_paused", "paused"}, f.keysOf(t, "unschedulable"))
	require.Equal(t, []string{"rate_limited"}, f.keysOf(t, "rate_limited"))
	require.Equal(t, []string{"temp"}, f.keysOf(t, "temp_unschedulable"))

	// Unknown values keep the raw column match.
	require.Empty(t, f.keysOf(t, "Active"))
	require.Empty(t, f.keysOf(t, ","), "a comma-only filter still matches nothing, as before")
}

func TestAccountListStatus_DerivedStatusesMatchD02ScheduleState(t *testing.T) {
	f := newAccountListStatusFixture(t)
	f.create(t, accountListStateSpecs())
	now := time.Now()

	members := map[string]map[string]bool{}
	for _, status := range []string{service.AccountListStatusNeedsAttention, service.AccountListStatusOverloaded, service.AccountListStatusSchedulableNow} {
		members[status] = map[string]bool{}
		for _, key := range f.keysOf(t, status) {
			members[status][key] = true
		}
	}

	for key, account := range f.accounts {
		state := service.AccountScheduleState(&service.AccountStatusRow{
			Status: account.Status, Schedulable: account.Schedulable,
			RateLimitResetAt: account.RateLimitResetAt, OverloadUntil: account.OverloadUntil,
			TempUnschedulableUntil: account.TempUnschedulableUntil,
			ExpiresAt:              account.ExpiresAt, AutoPauseOnExpired: account.AutoPauseOnExpired,
		}, now)
		needsAttention := state == service.AccountStateError || state == service.AccountStateOverloaded ||
			state == service.AccountStateRateLimited || state == service.AccountStateTempUnschedulable
		require.Equal(t, needsAttention, members[service.AccountListStatusNeedsAttention][key], "%s (%s) needs_attention", key, state)
		require.Equal(t, state == service.AccountStateOverloaded, members[service.AccountListStatusOverloaded][key], "%s (%s) overloaded", key, state)
		require.Equal(t, state == service.AccountStateAvailable, members[service.AccountListStatusSchedulableNow][key], "%s (%s) schedulable_now", key, state)
	}
	require.Equal(t, []string{"available", "expired_no_auto_pause", "stale_windows"}, f.keysOf(t, service.AccountListStatusSchedulableNow))
}

func TestAccountListStatus_SchedulableNowMatchesGroupActiveCount(t *testing.T) {
	f := newAccountListStatusFixture(t)
	f.create(t, accountListStateSpecs())

	_, total := f.list(t, f.ctx, service.AccountListStatusSchedulableNow, service.AccountListCountsFilter{GroupID: f.groupID}, "", "", 100)
	group, err := f.groupRepo.GetByID(f.ctx, f.groupID)
	require.NoError(t, err)
	require.EqualValues(t, 2, total, "available and stale_windows are the schedulable group members")
	require.Equal(t, group.ActiveAccountCount, total)
}

func TestAccountListStatus_SortByUrgency(t *testing.T) {
	f := newAccountListStatusFixture(t)
	now := time.Now()
	f.create(t, []accountListStatusSpec{
		{key: "n-normal"},
		{key: "e-b", mutate: func(u *dbent.AccountUpdateOne) { u.SetStatus(service.StatusError) }},
		{key: "i-inactive", mutate: func(u *dbent.AccountUpdateOne) { u.SetStatus("inactive") }},
		{key: "t-rate", mutate: func(u *dbent.AccountUpdateOne) { u.SetRateLimitResetAt(now.Add(30 * time.Minute)) }},
		{key: "p-paused", mutate: func(u *dbent.AccountUpdateOne) { u.SetSchedulable(false) }},
		{key: "t-over-rate", mutate: func(u *dbent.AccountUpdateOne) {
			u.SetOverloadUntil(now.Add(5 * time.Minute)).SetRateLimitResetAt(now.Add(60 * time.Minute))
		}},
		{key: "e-a", mutate: func(u *dbent.AccountUpdateOne) {
			u.SetStatus(service.StatusError).SetSchedulable(false)
		}},
		{key: "n-stale", mutate: func(u *dbent.AccountUpdateOne) { u.SetOverloadUntil(now.Add(-time.Minute)) }},
		{key: "p-expired", mutate: func(u *dbent.AccountUpdateOne) {
			u.SetExpiresAt(now.Add(-time.Hour)).SetAutoPauseOnExpired(true)
		}},
		{key: "t-temp", mutate: func(u *dbent.AccountUpdateOne) { u.SetTempUnschedulableUntil(now.Add(5 * time.Minute)) }},
	})
	want := []string{"e-a", "e-b", "t-temp", "t-rate", "t-over-rate", "p-expired", "p-paused", "i-inactive", "n-normal", "n-stale"}

	asc, total := f.list(t, f.ctx, "", service.AccountListCountsFilter{}, service.AccountListSortUrgency, "asc", 100)
	require.EqualValues(t, len(want), total)
	require.Equal(t, want, asc)

	firstPage, _ := f.list(t, f.ctx, "", service.AccountListCountsFilter{}, service.AccountListSortUrgency, "", 2)
	require.Equal(t, []string{"e-a", "e-b"}, firstPage, "the first page holds the most urgent accounts")

	desc, _ := f.list(t, f.ctx, "", service.AccountListCountsFilter{}, " URGENCY ", "desc", 100)
	reversed := append([]string(nil), want...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	require.Equal(t, reversed, desc)

	attention, _ := f.list(t, f.ctx, service.AccountListStatusNeedsAttention, service.AccountListCountsFilter{}, service.AccountListSortUrgency, "asc", 100)
	require.Equal(t, []string{"e-a", "e-b", "t-temp", "t-rate", "t-over-rate"}, attention)
}

func accountListCountsByKey(counts *service.AccountListCounts) map[string]int64 {
	return map[string]int64{
		service.AccountListStatusNeedsAttention:    counts.NeedsAttention,
		service.StatusError:                        counts.Error,
		service.AccountListStatusOverloaded:        counts.Overloaded,
		service.AccountListStatusRateLimited:       counts.RateLimited,
		service.AccountListStatusTempUnschedulable: counts.TempUnschedulable,
		service.AccountListStatusUnschedulable:     counts.Unschedulable,
		service.AccountListStatusInactive:          counts.Inactive,
		service.AccountListStatusSchedulableNow:    counts.SchedulableNow,
	}
}

func TestCountAccountListBuckets_MatchesFilterTotals(t *testing.T) {
	f := newAccountListStatusFixture(t)
	f.create(t, accountListStateSpecs())

	filters := []service.AccountListCountsFilter{
		{Search: f.prefix},
		{Search: f.prefix, Platform: service.PlatformOpenAI},
		{Search: f.prefix, AccountType: service.AccountTypeAPIKey},
		{Search: f.prefix, GroupID: f.groupID},
		{Search: f.prefix, GroupID: service.AccountListGroupUngrouped},
	}
	for _, filter := range filters {
		counts, err := f.repo.CountAccountListBuckets(f.ctx, filter)
		require.NoError(t, err)
		require.NotNil(t, counts)

		_, all := f.list(t, f.ctx, "", filter, "", "", 1)
		require.Equal(t, all, counts.All, "%+v all", filter)
		for status, got := range accountListCountsByKey(counts) {
			_, total := f.list(t, f.ctx, status, filter, "", "", 1)
			require.Equal(t, total, got, "%+v %s", filter, status)
		}
	}

	counts, err := f.repo.CountAccountListBuckets(f.ctx, service.AccountListCountsFilter{Search: f.prefix})
	require.NoError(t, err)
	require.Equal(t, service.AccountListCounts{
		All: 13, NeedsAttention: 6, Error: 2, Overloaded: 2, RateLimited: 1, TempUnschedulable: 1,
		Unschedulable: 2, Inactive: 1, SchedulableNow: 3,
	}, *counts)
	// needs_attention - error is the App's "limited" pill and stays exact.
	require.EqualValues(t, 4, counts.NeedsAttention-counts.Error)
}

func TestCountAccountListBuckets_ScopedToWorkspace(t *testing.T) {
	f := newAccountListStatusFixture(t)
	workspace, err := f.client.Workspace.Create().SetName(f.prefix + "workspace").Save(context.Background())
	require.NoError(t, err)

	for _, workspaceID := range []int64{1, workspace.ID} {
		created := mustCreateAccount(t, f.client, &service.Account{Name: f.prefix + "err-" + uuid.NewString()[:8], WorkspaceID: workspaceID})
		require.NoError(t, f.client.Account.UpdateOneID(created.ID).SetStatus(service.StatusError).Exec(context.Background()))
	}

	filter := service.AccountListCountsFilter{Search: f.prefix}
	vendorCtx := service.WithScope(context.Background(), service.Scope{WorkspaceID: workspace.ID})
	vendorCounts, err := f.repo.CountAccountListBuckets(vendorCtx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 1, vendorCounts.All)
	require.EqualValues(t, 1, vendorCounts.Error)
	require.EqualValues(t, 1, vendorCounts.NeedsAttention)
	_, vendorTotal := f.list(t, vendorCtx, service.AccountListStatusNeedsAttention, service.AccountListCountsFilter{}, "", "", 10)
	require.EqualValues(t, 1, vendorTotal)

	adminCounts, err := f.repo.CountAccountListBuckets(f.ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 2, adminCounts.All)
	require.EqualValues(t, 2, adminCounts.Error)

	// A context without a scope is denied to workspace 0 and counts nothing.
	deniedCounts, err := f.repo.CountAccountListBuckets(context.Background(), filter)
	require.NoError(t, err)
	require.Zero(t, deniedCounts.All)
}
