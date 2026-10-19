package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// accountTestStatusFailed is the status of a failed scheduled / background test.
const accountTestStatusFailed = "failed"

// accountTestStatusErrorKinds maps upstream statuses to error kinds for test
// failures whose message has no recognized account error prefix.
var accountTestStatusErrorKinds = map[int]string{
	401: AccountErrorKindAuth,
	402: AccountErrorKindQuota,
	403: AccountErrorKindForbidden,
	429: AccountErrorKindRateLimit,
}

// AccountTestErrorKind classifies a failed account test result with the C02
// account error kinds plus rate_limit; it returns nil unless status is failed.
// A recognized message prefix wins, then the upstream status (parsed from the
// message when nil, for classification only), otherwise other.
func AccountTestErrorKind(status string, upstreamStatus *int, message string) *string {
	if status != accountTestStatusFailed {
		return nil
	}
	message = strings.TrimSpace(message)
	kind := classifyAccountError(message)
	if kind == AccountErrorKindOther {
		code := upstreamStatus
		if code == nil {
			code = accountTestUpstreamStatus(0, message)
		}
		if code != nil {
			if mapped, ok := accountTestStatusErrorKinds[*code]; ok {
				kind = mapped
			}
		}
	}
	return &kind
}

// accountTestRecordedUpstreamStatus returns the upstream status recorded on the
// test trace, falling back to the status in errMsg (e.g. when the probe failed
// without emitting an error event).
func accountTestRecordedUpstreamStatus(c *gin.Context, errMsg string) *int {
	if snapshot, ok := AccountTestTraceFrom(c); ok && snapshot.UpstreamStatus != nil {
		return snapshot.UpstreamStatus
	}
	if errMsg == "" {
		return nil
	}
	return accountTestUpstreamStatus(0, errMsg)
}
