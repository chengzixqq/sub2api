package service

import (
	"fmt"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const reasonOpsTimeRangeTooLarge = "OPS_TIME_RANGE_TOO_LARGE"

// opsOpenAITokenStatsMaxDays caps custom token-stats windows; 31 days covers
// "last month" and "last 30 days + today".
const opsOpenAITokenStatsMaxDays = 31

// opsOpenAITokenStatsMaxWindow adds one hour of DST slack so a 31-day month
// that gains an hour (e.g. October in Europe/Berlin, 745h) is still accepted.
const opsOpenAITokenStatsMaxWindow = opsOpenAITokenStatsMaxDays*24*time.Hour + time.Hour

var ErrOpsTokenStatsRangeTooLarge = newOpsTimeRangeTooLargeError(opsOpenAITokenStatsMaxDays)

// newOpsTimeRangeTooLargeError builds the stable OPS_TIME_RANGE_TOO_LARGE
// error for day-granular limits; metadata.max_days carries the limit.
func newOpsTimeRangeTooLargeError(maxDays int) *infraerrors.ApplicationError {
	return infraerrors.BadRequest(reasonOpsTimeRangeTooLarge, fmt.Sprintf("invalid time range: max window is %d days", maxDays)).
		WithMetadata(map[string]string{"max_days": strconv.Itoa(maxDays)})
}
