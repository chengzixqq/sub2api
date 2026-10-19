package admin

import (
	"errors"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/gin-gonic/gin"
)

const (
	userBreakdownDefaultLimit = 50
	userBreakdownMaxLimit     = 200
)

// invalidDashboardParam builds the 400 message for a malformed dashboard query
// parameter, e.g. "Invalid group_id" or "Invalid stream, use true or false".
// The message is shown to clients verbatim, so it follows the capitalised
// "Invalid <param>" wording used across the admin API.
func invalidDashboardParam(name, hint string) error {
	if hint == "" {
		return errors.New("Invalid " + name)
	}
	return errors.New("Invalid " + name + ", " + hint)
}

// parseDashboardIDFilter reads an integer ID filter. A blank value (after
// trimming) means the filter is not set; anything else that is not an integer
// is rejected with a message naming the parameter.
func parseDashboardIDFilter(c *gin.Context, name string) (int64, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, invalidDashboardParam(name, "")
	}
	return id, nil
}

// parseDashboardBillingTypeFilter reads billing_type, which must fit in int8.
func parseDashboardBillingTypeFilter(c *gin.Context) (*int8, error) {
	raw := strings.TrimSpace(c.Query("billing_type"))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 8)
	if err != nil {
		return nil, invalidDashboardParam("billing_type", "")
	}
	billingType := int8(value)
	return &billingType, nil
}

// parseUserBreakdownLimit returns the default when limit is blank and rejects
// anything outside 1-200 instead of silently falling back.
func parseUserBreakdownLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return userBreakdownDefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > userBreakdownMaxLimit {
		return 0, invalidDashboardParam("limit", "use 1-200")
	}
	return limit, nil
}

// parseUserRankingSort reads users-ranking sort_by. Blank means the default
// actual_cost order; anything else unsupported is a 400 with reason
// INVALID_SORT_BY. The result is normalised so equivalent requests share a
// cache entry.
func parseUserRankingSort(c *gin.Context) (string, error) {
	sortBy := strings.TrimSpace(c.Query("sort_by"))
	if sortBy == "" {
		return usagestats.UserRankingSortActualCost, nil
	}
	if !usagestats.IsValidUserRankingSort(sortBy) {
		return "", infraerrors.BadRequest("INVALID_SORT_BY", "Invalid sort_by, use actual_cost/requests/tokens")
	}
	return sortBy, nil
}

// parseUserBreakdownQuery validates every user-breakdown parameter once and
// builds the repository dimension from the shared dashboard filters, so a value
// that passes validation is never dropped by a second, stricter parse.
func parseUserBreakdownQuery(c *gin.Context) (usagestats.UserBreakdownDimension, int, error) {
	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		return usagestats.UserBreakdownDimension{}, 0, err
	}
	// request_type takes precedence over stream (the shared parser ignores
	// stream then), but a malformed stream is still a client error here.
	if filters.RequestType != nil {
		if _, err := parseOptionalBoolDashboardFilter(c, "stream"); err != nil {
			return usagestats.UserBreakdownDimension{}, 0, err
		}
	}
	limit, err := parseUserBreakdownLimit(c.Query("limit"))
	if err != nil {
		return usagestats.UserBreakdownDimension{}, 0, err
	}

	dim := usagestats.UserBreakdownDimension{
		RequestedModel:        c.Query("requested_model"),
		GroupID:               filters.GroupID,
		Model:                 c.Query("model"),
		ModelType:             filters.modelSource(),
		Endpoint:              c.Query("endpoint"),
		EndpointType:          c.DefaultQuery("endpoint_type", "inbound"),
		UserID:                filters.UserID,
		APIKeyID:              filters.APIKeyID,
		AccountID:             filters.AccountID,
		RequestType:           filters.RequestType,
		Stream:                filters.Stream,
		NativeCompactionV2:    filters.NativeCompactionV2,
		BillingType:           filters.BillingType,
		BillingMode:           filters.BillingMode,
		RequestID:             filters.RequestID,
		UpstreamModelMismatch: filters.UpstreamModelMismatch,
		// sort_by 由 repo 层 allowlist 校验;非法值静默回退默认排序(actual_cost)。
		SortBy: strings.TrimSpace(c.Query("sort_by")),
	}
	return dim, limit, nil
}
