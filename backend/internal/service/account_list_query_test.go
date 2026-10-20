//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// accountListRecordingRepo records the status the list query reaches the
// repository with; every other method keeps accountRepoStub's panic contract.
type accountListRecordingRepo struct {
	accountRepoStub
	listCalls  int
	lastStatus string
}

func (r *accountListRecordingRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, _, _, status, _ string, _ int64, _ string) ([]Account, *pagination.PaginationResult, error) {
	r.listCalls++
	r.lastStatus = status
	return []Account{}, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize}, nil
}

type accountListCountingRepo struct {
	accountRepoStub
	counts     *AccountListCounts
	lastFilter AccountListCountsFilter
	calls      int
}

func (r *accountListCountingRepo) CountAccountListBuckets(_ context.Context, filter AccountListCountsFilter) (*AccountListCounts, error) {
	r.calls++
	r.lastFilter = filter
	return r.counts, nil
}

func statusFilterValues(n int) string {
	values := make([]string, n)
	for i := range values {
		values[i] = "status-" + strconv.Itoa(i)
	}
	return strings.Join(values, ",")
}

func TestSplitAccountStatusFilter(t *testing.T) {
	require.Nil(t, SplitAccountStatusFilter(""))
	require.Equal(t, []string{"active"}, SplitAccountStatusFilter("active"))
	require.Equal(t, []string{"error", "rate_limited"}, SplitAccountStatusFilter(" error , rate_limited ,error"))
	// Only commas and blanks: keep the legacy raw match, which matches nothing,
	// instead of silently dropping the filter.
	require.Equal(t, []string{","}, SplitAccountStatusFilter(","))
	require.Equal(t, []string{" , "}, SplitAccountStatusFilter(" , "))
	// Case is kept: the raw status column match stays case sensitive.
	require.Equal(t, []string{"Active"}, SplitAccountStatusFilter("Active"))
	// A value without a comma is passed through untouched, exactly as before
	// multi-value support (whitespace included).
	require.Equal(t, []string{" active "}, SplitAccountStatusFilter(" active "))
	require.Equal(t, []string{"overloaded", "needs_attention"}, SplitAccountStatusFilter("overloaded,needs_attention,overloaded"))
}

func TestValidateAccountStatusFilter(t *testing.T) {
	require.NoError(t, ValidateAccountStatusFilter(""))
	require.NoError(t, ValidateAccountStatusFilter("active"))
	require.NoError(t, ValidateAccountStatusFilter(statusFilterValues(MaxAccountStatusFilterValues)))
	// Duplicates do not count towards the limit.
	require.NoError(t, ValidateAccountStatusFilter(statusFilterValues(MaxAccountStatusFilterValues)+",status-0,status-1"))

	err := ValidateAccountStatusFilter(statusFilterValues(MaxAccountStatusFilterValues + 1))
	require.Error(t, err)
	require.Equal(t, "INVALID_ACCOUNT_STATUS_FILTER", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "16", infraerrors.FromError(err).Metadata["max"])
}

func TestAdminServiceListAccounts_RejectsTooManyStatusValues(t *testing.T) {
	repo := &accountListRecordingRepo{}
	svc := &adminServiceImpl{accountRepo: repo}

	_, _, err := svc.ListAccounts(context.Background(), 1, 20, "", "", statusFilterValues(MaxAccountStatusFilterValues+1), "", 0, "", "name", "asc")
	require.Error(t, err)
	require.Equal(t, "INVALID_ACCOUNT_STATUS_FILTER", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Zero(t, repo.listCalls, "an oversized filter must not reach the repository")

	limit := statusFilterValues(MaxAccountStatusFilterValues)
	_, _, err = svc.ListAccounts(context.Background(), 1, 20, "", "", limit, "", 0, "", "urgency", "asc")
	require.NoError(t, err)
	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, limit, repo.lastStatus, "the raw status string is forwarded unchanged")
}

func TestAdminServiceCountAccountsForList(t *testing.T) {
	t.Run("repository without counts support returns nil", func(t *testing.T) {
		svc := &adminServiceImpl{accountRepo: &accountListRecordingRepo{}}
		counts, err := svc.CountAccountsForList(context.Background(), AccountListCountsFilter{Platform: PlatformOpenAI})
		require.NoError(t, err)
		require.Nil(t, counts)
	})

	t.Run("delegates the filter to the repository", func(t *testing.T) {
		want := &AccountListCounts{All: 9, NeedsAttention: 4, Error: 1, SchedulableNow: 5}
		repo := &accountListCountingRepo{counts: want}
		svc := &adminServiceImpl{accountRepo: repo}
		filter := AccountListCountsFilter{
			Platform: PlatformAnthropic, AccountType: AccountTypeOAuth, Search: "x",
			GroupID: AccountListGroupUngrouped, PrivacyMode: AccountPrivacyModeUnsetFilter,
		}
		counts, err := svc.CountAccountsForList(context.Background(), filter)
		require.NoError(t, err)
		require.Same(t, want, counts)
		require.Equal(t, 1, repo.calls)
		require.Equal(t, filter, repo.lastFilter)
	})
}
