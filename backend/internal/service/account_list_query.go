package service

import (
	"context"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Account list status filter values. active, inactive and error keep matching
// the status column (active additionally excludes rate-limited, temporarily
// unschedulable and paused accounts, as before); the rest are derived from the
// runtime windows. The derived values added for the mobile list follow the D02
// schedule-state order (AccountScheduleState).
const (
	AccountListStatusInactive          = "inactive"
	AccountListStatusRateLimited       = "rate_limited"
	AccountListStatusTempUnschedulable = "temp_unschedulable"
	AccountListStatusUnschedulable     = "unschedulable"
	// AccountListStatusOverloaded is an active account inside its overload window.
	AccountListStatusOverloaded = "overloaded"
	// AccountListStatusNeedsAttention is an error account, or an active account
	// inside an overload, rate-limit or temporary-unschedulable window.
	AccountListStatusNeedsAttention = "needs_attention"
	// AccountListStatusSchedulableNow matches the group list's available count
	// (D02 available): active, schedulable, not auto-paused and no window active.
	AccountListStatusSchedulableNow = "schedulable_now"

	// AccountListSortUrgency orders error accounts first, then timed states by
	// the time they recover, paused, other non-active and finally healthy ones.
	AccountListSortUrgency = "urgency"

	// MaxAccountStatusFilterValues caps the distinct comma separated status values.
	MaxAccountStatusFilterValues = 16
)

// ErrInvalidAccountStatusFilter rejects a status filter with too many values.
var ErrInvalidAccountStatusFilter = infraerrors.BadRequest(
	"INVALID_ACCOUNT_STATUS_FILTER", "too many account status filter values",
).WithMetadata(map[string]string{"max": strconv.Itoa(MaxAccountStatusFilterValues)})

// SplitAccountStatusFilter parses the status query parameter. A value without
// a comma is returned untouched so single-value requests match exactly as
// before; comma separated values are trimmed, emptied ones dropped and
// duplicates removed in first-seen order. Case is preserved. A value made of
// commas and blanks only keeps the legacy raw match (it matches nothing).
func SplitAccountStatusFilter(raw string) []string {
	if raw == "" {
		return nil
	}
	if !strings.Contains(raw, ",") {
		return []string{raw}
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return []string{raw}
	}
	return values
}

// ValidateAccountStatusFilter rejects filters with more distinct values than
// MaxAccountStatusFilterValues.
func ValidateAccountStatusFilter(raw string) error {
	if len(SplitAccountStatusFilter(raw)) > MaxAccountStatusFilterValues {
		return ErrInvalidAccountStatusFilter
	}
	return nil
}

// AccountListCounts holds, for the list filters other than status, the total
// each status filter value would return. All is the total without status.
type AccountListCounts struct {
	All               int64 `json:"all"`
	NeedsAttention    int64 `json:"needs_attention"`
	Error             int64 `json:"error"`
	Overloaded        int64 `json:"overloaded"`
	RateLimited       int64 `json:"rate_limited"`
	TempUnschedulable int64 `json:"temp_unschedulable"`
	Unschedulable     int64 `json:"unschedulable"`
	Inactive          int64 `json:"inactive"`
	SchedulableNow    int64 `json:"schedulable_now"`
}

// AccountListCountsFilter is the account list filter set without status.
type AccountListCountsFilter struct {
	Platform    string
	AccountType string
	Search      string
	GroupID     int64
	PrivacyMode string
}

// AccountListCountsRepository is implemented by account repositories that can
// count the list buckets in one query; others make include_counts a no-op.
type AccountListCountsRepository interface {
	CountAccountListBuckets(ctx context.Context, filter AccountListCountsFilter) (*AccountListCounts, error)
}

// CountAccountsForList counts the account list buckets for the given filters.
// It returns nil, nil when the repository cannot count.
func (s *adminServiceImpl) CountAccountsForList(ctx context.Context, filter AccountListCountsFilter) (*AccountListCounts, error) {
	if filter.GroupID > 0 {
		if err := s.ValidateAccountGroupBindings(ctx, []int64{filter.GroupID}); err != nil {
			return nil, err
		}
	}
	repo, ok := s.accountRepo.(AccountListCountsRepository)
	if !ok {
		return nil, nil
	}
	return repo.CountAccountListBuckets(ctx, filter)
}
