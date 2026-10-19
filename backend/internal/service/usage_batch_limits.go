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

// reasonTooManyIDs tells clients to split the batch and retry; metadata.max
// carries the limit and metadata.field names the offending request field.
const reasonTooManyIDs = "TOO_MANY_IDS"

var (
	ErrTooManyUsageUserIDs   = tooManyBatchUsageIDs("user_ids", "user IDs")
	ErrTooManyUsageAPIKeyIDs = tooManyBatchUsageIDs("api_key_ids", "API key IDs")
)

func tooManyBatchUsageIDs(field, noun string) *infraerrors.ApplicationError {
	return infraerrors.BadRequest(reasonTooManyIDs, fmt.Sprintf("Too many %s (maximum %d allowed)", noun, MaxBatchUsageIDs)).
		WithMetadata(map[string]string{"field": field, "max": strconv.Itoa(MaxBatchUsageIDs)})
}
