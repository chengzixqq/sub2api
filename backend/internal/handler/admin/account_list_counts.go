package admin

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// accountListPageWithCounts is the account list page plus include_counts'
// counts; the page fields serialize exactly as response.PaginatedData.
type accountListPageWithCounts struct {
	response.PaginatedData
	Counts *service.AccountListCounts `json:"counts"`
}

// accountListCounts returns nil unless include_counts was requested and the
// account repository can count.
func (h *AccountHandler) accountListCounts(ctx context.Context, includeCounts bool, filter service.AccountListCountsFilter) (*service.AccountListCounts, error) {
	if !includeCounts {
		return nil, nil
	}
	return h.adminService.CountAccountsForList(ctx, filter)
}

// accountListPayload keeps the legacy paginated body when counts is nil.
func accountListPayload(items any, total int64, page, pageSize int, counts *service.AccountListCounts) any {
	payload := response.PaginatedPayload(items, total, page, pageSize)
	if counts == nil {
		return payload
	}
	return accountListPageWithCounts{PaginatedData: payload, Counts: counts}
}
