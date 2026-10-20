package service

import (
	"fmt"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// MaxBatchUsageIDs caps how many IDs a single batch "today usage" request may
// aggregate over usage_logs. It matches the largest list page the Web tables
// can request (page_size <= 1000), so a page's IDs always fit in one call.
const MaxBatchUsageIDs = 1000

// MaxAccountRuntimeBatchIDs caps POST /admin/accounts/runtime/batch after de-duplication.
// Each account costs Redis reads and, for Anthropic OAuth accounts with a
// window cost limit, one usage_logs query; clients poll only visible rows.
const MaxAccountRuntimeBatchIDs = 200

// reasonTooManyIDs tells clients to split the batch and retry; metadata.max
// carries the limit and metadata.field names the offending request field.
const reasonTooManyIDs = "TOO_MANY_IDS"

var (
	ErrTooManyUsageUserIDs      = tooManyBatchIDs("user_ids", "user IDs", MaxBatchUsageIDs)
	ErrTooManyUsageAPIKeyIDs    = tooManyBatchIDs("api_key_ids", "API key IDs", MaxBatchUsageIDs)
	ErrTooManyRuntimeAccountIDs = tooManyBatchIDs("account_ids", "account IDs", MaxAccountRuntimeBatchIDs)
)

func tooManyBatchIDs(field, noun string, limit int) *infraerrors.ApplicationError {
	return infraerrors.BadRequest(reasonTooManyIDs, fmt.Sprintf("Too many %s (maximum %d allowed)", noun, limit)).
		WithMetadata(map[string]string{"field": field, "max": strconv.Itoa(limit)})
}
