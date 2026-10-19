package service

import (
	"time"

	"github.com/gin-gonic/gin"
)

// AccountTestResult is the payload of the trailing test_result SSE event. The
// success field carries no omitempty: TestEvent.Success drops false, so clients
// read data.success.
type AccountTestResult struct {
	Success        bool                       `json:"success"`
	StartedAt      time.Time                  `json:"started_at"`
	FirstTokenMs   *int64                     `json:"first_token_ms"`
	TotalMs        int64                      `json:"total_ms"`
	UpstreamStatus *int                       `json:"upstream_status"`
	Usage          *AccountTestUsage          `json:"usage,omitempty"`
	Account        *AccountTestResultAccount  `json:"account"`
	Recovery       *AccountTestResultRecovery `json:"recovery"`
	Marked         *AccountTestResultMarked   `json:"marked"`
}

// AccountTestResultAccount is the account state read after the test and recovery.
type AccountTestResultAccount struct {
	Status                 string     `json:"status"`
	Schedulable            bool       `json:"schedulable"`
	RateLimitResetAt       *time.Time `json:"rate_limit_reset_at"`
	TempUnschedulableUntil *time.Time `json:"temp_unschedulable_until"`
	ErrorSummary           string     `json:"error_summary"`
	ErrorKind              string     `json:"error_kind,omitempty"`
}

// AccountTestResultRecovery reports what the post-success recovery cleared.
type AccountTestResultRecovery struct {
	ClearedError     bool `json:"cleared_error"`
	ClearedRateLimit bool `json:"cleared_rate_limit"`
}

// AccountTestResultMarked reports state the test wrote, from comparing the
// account before and after. Gateway traffic during the test can also change the
// account, so this is best effort.
type AccountTestResultMarked struct {
	Error             bool `json:"error"`
	RateLimited       bool `json:"rate_limited"`
	TempUnschedulable bool `json:"temp_unschedulable"`
}

// BuildAccountTestResult assembles the test_result payload from the trace on c
// and the account snapshots taken before the test and after recovery.
func BuildAccountTestResult(c *gin.Context, success bool, before, after *Account, recovery *SuccessfulTestRecoveryResult) AccountTestResult {
	result := AccountTestResult{Success: success}
	if snapshot, ok := AccountTestTraceFrom(c); ok {
		result.StartedAt = snapshot.StartedAt
		finishedAt := time.Now()
		if snapshot.FinishedAt != nil {
			finishedAt = *snapshot.FinishedAt
		}
		result.TotalMs = nonNegativeMs(finishedAt.Sub(snapshot.StartedAt))
		if snapshot.FirstOutputAt != nil {
			firstMs := nonNegativeMs(snapshot.FirstOutputAt.Sub(snapshot.StartedAt))
			result.FirstTokenMs = &firstMs
		}
		result.UpstreamStatus = snapshot.UpstreamStatus
		result.Usage = snapshot.Usage
	}
	if after != nil {
		view := &AccountTestResultAccount{
			Status:                 after.Status,
			Schedulable:            after.Schedulable,
			RateLimitResetAt:       after.RateLimitResetAt,
			TempUnschedulableUntil: after.TempUnschedulableUntil,
		}
		if desc := DescribeAccountError(after.ErrorMessage); desc.Kind != "" {
			view.ErrorSummary = desc.Summary
			view.ErrorKind = desc.Kind
		}
		result.Account = view
	}
	if success && recovery != nil {
		result.Recovery = &AccountTestResultRecovery{
			ClearedError:     recovery.ClearedError,
			ClearedRateLimit: recovery.ClearedRateLimit,
		}
	}
	if before != nil && after != nil {
		marked := computeAccountTestMarked(before, after)
		result.Marked = &marked
	}
	return result
}

// SendTestResult writes the trailing test_result SSE event.
func (s *AccountTestService) SendTestResult(c *gin.Context, result AccountTestResult) {
	s.sendEvent(c, TestEvent{Type: "test_result", Success: result.Success, Data: result})
}

func computeAccountTestMarked(before, after *Account) AccountTestResultMarked {
	var marked AccountTestResultMarked
	if after.Status == StatusError {
		marked.Error = before.Status != StatusError ||
			before.ErrorMessage != after.ErrorMessage ||
			!accountTestTimeEqual(before.ErrorAt, after.ErrorAt)
	}
	marked.RateLimited = accountTestTimeAdvanced(before.RateLimitResetAt, after.RateLimitResetAt) ||
		accountTestTimeAdvanced(before.RateLimitedAt, after.RateLimitedAt)
	if after.TempUnschedulableUntil != nil {
		marked.TempUnschedulable = accountTestTimeAdvanced(before.TempUnschedulableUntil, after.TempUnschedulableUntil) ||
			before.TempUnschedulableReason != after.TempUnschedulableReason
	}
	return marked
}

func accountTestTimeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// accountTestTimeAdvanced reports whether after is set and later than before.
func accountTestTimeAdvanced(before, after *time.Time) bool {
	if after == nil {
		return false
	}
	return before == nil || after.After(*before)
}

func nonNegativeMs(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}
