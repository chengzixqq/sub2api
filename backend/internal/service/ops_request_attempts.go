package service

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	// opsRequestAttemptsMaxIDBytes bounds the path parameter; stored ids are
	// at most 64 bytes plus a usage prefix such as "client:".
	opsRequestAttemptsMaxIDBytes = 256
	// opsRequestAttemptsAroundWindow narrows both lookups to around ± 1h.
	opsRequestAttemptsAroundWindow = time.Hour
	// opsRequestAttemptsMaxAmbiguousIDs caps metadata.client_request_ids.
	opsRequestAttemptsMaxAmbiguousIDs = 5

	opsUsageRequestIDClientPrefix = "client:"
	opsUsageRequestIDLocalPrefix  = "local:"
	// opsUsageRequestIDProbePrefix bills a coalesced probe leader; it wraps
	// the ops request_id when the client sent no X-Request-ID.
	opsUsageRequestIDProbePrefix = "probe:"

	opsRequestAttemptSourceUpstreamError = "upstream_error"
	opsRequestAttemptSourceUsage         = "usage"

	opsRequestAttemptOutcomeFailed  = "failed"
	opsRequestAttemptOutcomeSuccess = "success"
	// opsRequestAttemptOutcomePartial marks a billed attempt whose request
	// still ended in a client-visible failure (for example a broken stream).
	opsRequestAttemptOutcomePartial = "partial"
)

var (
	ErrOpsRequestNotFound  = infraerrors.NotFound("OPS_REQUEST_NOT_FOUND", "No ops error log or usage record matches this request id")
	ErrOpsRequestAmbiguous = infraerrors.Conflict("OPS_REQUEST_AMBIGUOUS", "Request id matches several requests; retry with one of metadata.client_request_ids")

	errOpsInvalidRequestID = infraerrors.BadRequest("INVALID_REQUEST_ID", "Invalid request_id")
	errOpsInvalidAround    = infraerrors.BadRequest("INVALID_AROUND", "Invalid around, use RFC3339")
)

// OpsRequestAttemptLookup selects ops_error_logs by request_id or
// client_request_id, and usage_logs by request_id. Times bound created_at to
// [StartTime, EndTime) when set.
type OpsRequestAttemptLookup struct {
	RequestIDs       []string
	ClientRequestIDs []string
	StartTime        *time.Time
	EndTime          *time.Time
}

type OpsRequestAttemptErrorRow struct {
	ID                 int64
	CreatedAt          time.Time
	RequestID          string
	ClientRequestID    string
	StatusCode         int
	ErrorPhase         string
	UpstreamErrorsJSON string
}

type OpsRequestAttemptUsageRow struct {
	ID                int64
	CreatedAt         time.Time
	RequestID         string
	AccountID         int64
	AccountName       string
	Platform          string
	DurationMs        *int64
	FirstTokenMs      *int64
	UpstreamRequestID string
}

// OpsRequestAttemptsRepository is optional so test doubles of OpsRepository
// need not implement it.
type OpsRequestAttemptsRepository interface {
	ListRequestAttemptErrorLogs(ctx context.Context, lookup OpsRequestAttemptLookup) ([]*OpsRequestAttemptErrorRow, error)
	ListRequestAttemptUsageLogs(ctx context.Context, lookup OpsRequestAttemptLookup) ([]*OpsRequestAttemptUsageRow, error)
	// ListRequestAttemptAccountNames includes soft-deleted accounts.
	ListRequestAttemptAccountNames(ctx context.Context, ids []int64) (map[int64]string, error)
}

// OpsRequestAttempt is one upstream try of a request. Failed tries come from
// upstream_errors events; the billed try comes from usage_logs, whose start
// is estimated as created_at − duration_ms (no earlier than the end of the
// failed try before it) and whose upstream status is 200.
type OpsRequestAttempt struct {
	Index              int        `json:"index"`
	Source             string     `json:"source"`
	Outcome            string     `json:"outcome"`
	Platform           string     `json:"platform"`
	AccountID          *int64     `json:"account_id"`
	AccountName        string     `json:"account_name"`
	ProxyID            *int64     `json:"proxy_id"`
	ProxyName          string     `json:"proxy_name"`
	UpstreamStatusCode *int       `json:"upstream_status_code"`
	UpstreamRequestID  string     `json:"upstream_request_id"`
	Kind               string     `json:"kind"`
	Stage              string     `json:"stage"`
	Message            string     `json:"message"`
	StartedAt          *time.Time `json:"started_at"`
	EndedAt            *time.Time `json:"ended_at"`
	DurationMs         *int64     `json:"duration_ms"`
	FirstTokenMs       *int64     `json:"first_token_ms"`
	ErrorLogID         *int64     `json:"error_log_id"`
	UsageID            *int64     `json:"usage_id"`
}

// OpsRequestAttempts is the merged retry chain of GET ops/requests/:request_id/attempts.
type OpsRequestAttempts struct {
	RequestID              string               `json:"request_id"`
	ClientRequestID        string               `json:"client_request_id"`
	ClientStatusCode       *int                 `json:"client_status_code"`
	Attempts               []*OpsRequestAttempt `json:"attempts"`
	ErrorLogIDs            []int64              `json:"error_log_ids"`
	DroppedEarlierAttempts int                  `json:"dropped_earlier_attempts"`
}

// ParseOpsRequestAttemptsAround accepts an RFC3339 instant; empty means no
// time bound.
func ParseOpsRequestAttemptsAround(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errOpsInvalidAround
	}
	t = t.UTC()
	return &t, nil
}

// GetRequestAttempts merges the failed upstream attempts recorded in
// ops_error_logs with the billed attempt in usage_logs. requestID may be the
// ops request_id, the server client_request_id, or a usage request_id
// ("client:<id>" / "local:<id>" / "probe:<id>").
func (s *OpsService) GetRequestAttempts(ctx context.Context, requestID string, around *time.Time) (*OpsRequestAttempts, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	requestID = strings.TrimSpace(requestID)
	if !validOpsRequestAttemptID(requestID) {
		return nil, errOpsInvalidRequestID
	}
	repo, ok := s.opsRepo.(OpsRequestAttemptsRepository)
	if s.opsRepo == nil || !ok {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}

	start, end := opsRequestAttemptsWindow(around)
	errLookup := opsRequestAttemptErrorLookup(requestID)
	errLookup.StartTime, errLookup.EndTime = start, end
	errRows, err := repo.ListRequestAttemptErrorLogs(ctx, errLookup)
	if err != nil {
		return nil, err
	}
	clientIDs := opsRequestAttemptClientIDs(errRows)
	if len(clientIDs) > 1 {
		return nil, newOpsRequestAmbiguousError(errRows)
	}
	clientRequestID := ""
	if len(clientIDs) == 1 {
		clientRequestID = clientIDs[0]
	} else if rest, ok := strings.CutPrefix(requestID, opsUsageRequestIDClientPrefix); ok {
		clientRequestID = strings.TrimSpace(rest)
	}

	usageRows, err := repo.ListRequestAttemptUsageLogs(ctx, OpsRequestAttemptLookup{
		RequestIDs: opsRequestAttemptUsageIDs(requestID, clientRequestID, errRows),
		StartTime:  start,
		EndTime:    end,
	})
	if err != nil {
		return nil, err
	}
	if len(errRows) == 0 && len(usageRows) == 0 {
		return nil, ErrOpsRequestNotFound
	}

	result := buildOpsRequestAttempts(errRows, usageRows)
	result.RequestID = opsRequestAttemptCanonicalID(requestID, errRows)
	result.ClientRequestID = clientRequestID
	if result.ClientRequestID == "" {
		result.ClientRequestID = opsRequestAttemptClientIDFromUsage(usageRows)
	}
	if missing := opsRequestAttemptNamelessAccounts(result.Attempts); len(missing) > 0 {
		names, err := repo.ListRequestAttemptAccountNames(ctx, missing)
		if err != nil {
			// Names are cosmetic: keep the chain and leave them blank.
			log.Printf("[Ops] GetRequestAttempts account names failed: %v", err)
		} else {
			applyOpsRequestAttemptAccountNames(result.Attempts, names)
		}
	}
	return result, nil
}

// validOpsRequestAttemptID mirrors what can be stored: ops and usage ids are
// valid UTF-8 without NUL, which Postgres would otherwise reject with a 500.
func validOpsRequestAttemptID(requestID string) bool {
	return requestID != "" && len(requestID) <= opsRequestAttemptsMaxIDBytes &&
		utf8.ValidString(requestID) && strings.IndexByte(requestID, 0) < 0
}

// newOpsRequestAmbiguousError lists the newest requests first, each with the
// created_at of its newest ops row, so the caller can pick the one it shows.
func newOpsRequestAmbiguousError(rows []*OpsRequestAttemptErrorRow) *infraerrors.ApplicationError {
	newest := make([]*OpsRequestAttemptErrorRow, 0, len(rows))
	for _, row := range rows {
		if row != nil && strings.TrimSpace(row.ClientRequestID) != "" {
			newest = append(newest, row)
		}
	}
	sort.SliceStable(newest, func(i, j int) bool { return opsRequestAttemptRowIsNewer(newest[i], newest[j]) })
	ids := make([]string, 0, opsRequestAttemptsMaxAmbiguousIDs)
	times := make([]string, 0, opsRequestAttemptsMaxAmbiguousIDs)
	seen := make(map[string]struct{}, len(newest))
	for _, row := range newest {
		if len(ids) == opsRequestAttemptsMaxAmbiguousIDs {
			break
		}
		id := strings.TrimSpace(row.ClientRequestID)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		times = append(times, row.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	return ErrOpsRequestAmbiguous.WithMetadata(map[string]string{
		"client_request_ids":      strings.Join(ids, ","),
		"client_request_id_times": strings.Join(times, ","),
	})
}

func opsRequestAttemptsWindow(around *time.Time) (*time.Time, *time.Time) {
	if around == nil {
		return nil, nil
	}
	start := around.UTC().Add(-opsRequestAttemptsAroundWindow)
	end := around.UTC().Add(opsRequestAttemptsAroundWindow)
	return &start, &end
}

// opsRequestAttemptErrorLookup matches the id against both correlation
// columns; usage prefixes are translated back to the ops columns.
func opsRequestAttemptErrorLookup(requestID string) OpsRequestAttemptLookup {
	if rest, ok := strings.CutPrefix(requestID, opsUsageRequestIDClientPrefix); ok {
		return OpsRequestAttemptLookup{RequestIDs: []string{requestID}, ClientRequestIDs: nonEmptyStrings(strings.TrimSpace(rest))}
	}
	for _, prefix := range []string{opsUsageRequestIDLocalPrefix, opsUsageRequestIDProbePrefix} {
		if rest, ok := strings.CutPrefix(requestID, prefix); ok {
			return OpsRequestAttemptLookup{RequestIDs: nonEmptyStrings(requestID, strings.TrimSpace(rest)), ClientRequestIDs: []string{}}
		}
	}
	return OpsRequestAttemptLookup{RequestIDs: []string{requestID}, ClientRequestIDs: []string{requestID}}
}

// opsRequestAttemptOpsIDUsageIDs lists the usage request_ids that wrap an ops
// request_id: "local:" (no client id) and "probe:" (coalesced probe leader).
func opsRequestAttemptOpsIDUsageIDs(opsRequestID string) []string {
	return []string{opsUsageRequestIDLocalPrefix + opsRequestID, opsUsageRequestIDProbePrefix + opsRequestID}
}

// opsRequestAttemptUsageIDs lists the usage_logs.request_id values the
// request may be billed under (see resolveUsageBillingRequestID).
func opsRequestAttemptUsageIDs(requestID, clientRequestID string, errRows []*OpsRequestAttemptErrorRow) []string {
	ids := []string{requestID}
	unprefixed := !strings.HasPrefix(requestID, opsUsageRequestIDClientPrefix) &&
		!strings.HasPrefix(requestID, opsUsageRequestIDLocalPrefix) &&
		!strings.HasPrefix(requestID, opsUsageRequestIDProbePrefix)
	switch {
	case clientRequestID != "":
		ids = append(ids, opsUsageRequestIDClientPrefix+clientRequestID)
	case unprefixed:
		// A bare id may itself be the server client_request_id.
		ids = append(ids, opsUsageRequestIDClientPrefix+requestID)
	}
	if unprefixed {
		ids = append(ids, opsRequestAttemptOpsIDUsageIDs(requestID)...)
	}
	for _, row := range errRows {
		if row != nil && strings.TrimSpace(row.RequestID) != "" {
			ids = append(ids, opsRequestAttemptOpsIDUsageIDs(strings.TrimSpace(row.RequestID))...)
		}
	}
	return nonEmptyStrings(ids...)
}

// opsRequestAttemptClientIDs returns the distinct client_request_ids in row
// order; more than one means a client-supplied X-Request-ID was reused.
func opsRequestAttemptClientIDs(rows []*OpsRequestAttemptErrorRow) []string {
	ids := make([]string, 0, 1)
	for _, row := range rows {
		if row != nil {
			ids = append(ids, strings.TrimSpace(row.ClientRequestID))
		}
	}
	return nonEmptyStrings(ids...)
}

func opsRequestAttemptCanonicalID(requestID string, rows []*OpsRequestAttemptErrorRow) string {
	for _, row := range rows {
		if row != nil && strings.TrimSpace(row.RequestID) != "" {
			return strings.TrimSpace(row.RequestID)
		}
	}
	return requestID
}

// opsRequestAttemptClientIDFromUsage recovers the client_request_id from a
// "client:<id>" usage request_id when no ops row carried it.
func opsRequestAttemptClientIDFromUsage(rows []*OpsRequestAttemptUsageRow) string {
	for _, row := range rows {
		if row == nil {
			continue
		}
		if rest, ok := strings.CutPrefix(row.RequestID, opsUsageRequestIDClientPrefix); ok && strings.TrimSpace(rest) != "" {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// nonEmptyStrings drops empty values and duplicates, keeping first-seen order.
func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// opsAttemptEventKey identifies an event copied into several rows of one
// request (a stream-failure row and a recovered row share the same context).
type opsAttemptEventKey struct {
	atUnixMs  int64
	accountID int64
	status    int
	kind      string
	stage     string
}

type opsTimedAttempt struct {
	attempt *OpsRequestAttempt
	endMs   int64
}

// buildOpsRequestAttempts orders failed events and billed usage by end time.
// An event repeated across rows is kept once; repeats inside one row are
// distinct attempts (same-millisecond retries).
func buildOpsRequestAttempts(errRows []*OpsRequestAttemptErrorRow, usageRows []*OpsRequestAttemptUsageRow) *OpsRequestAttempts {
	out := &OpsRequestAttempts{Attempts: []*OpsRequestAttempt{}, ErrorLogIDs: []int64{}}
	items := make([]opsTimedAttempt, 0, len(errRows)+len(usageRows))
	seen := make(map[opsAttemptEventKey]int)
	for _, row := range errRows {
		if row == nil {
			continue
		}
		out.ErrorLogIDs = append(out.ErrorLogIDs, row.ID)
		events, err := ParseOpsUpstreamErrors(row.UpstreamErrorsJSON)
		if err != nil {
			log.Printf("[Ops] request attempts: unreadable upstream_errors on ops_error_logs id=%d: %v", row.ID, err)
			continue
		}
		inRow := make(map[opsAttemptEventKey]int)
		for _, ev := range events {
			if ev == nil {
				continue
			}
			key := opsAttemptEventKey{ev.AtUnixMs, ev.AccountID, ev.UpstreamStatusCode, ev.Kind, ev.Stage}
			n := inRow[key]
			inRow[key] = n + 1
			if n < seen[key] {
				continue
			}
			if ev.DroppedEarlierAttempts > out.DroppedEarlierAttempts {
				out.DroppedEarlierAttempts = ev.DroppedEarlierAttempts
			}
			endMs := ev.AtUnixMs
			if endMs <= 0 {
				endMs = row.CreatedAt.UnixMilli()
			}
			items = append(items, opsTimedAttempt{attempt: opsRequestAttemptFromEvent(ev, row.ID), endMs: endMs})
		}
		for key, n := range inRow {
			if n > seen[key] {
				seen[key] = n
			}
		}
	}

	failureStatus, recoveredStatus := opsRequestAttemptRowStatuses(errRows)
	for _, row := range usageRows {
		if row == nil {
			continue
		}
		items = append(items, opsTimedAttempt{attempt: opsRequestAttemptFromUsage(row, failureStatus > 0), endMs: row.CreatedAt.UnixMilli()})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].endMs < items[j].endMs })
	clampOpsBilledAttemptStarts(items)
	for i, item := range items {
		item.attempt.Index = i + 1
		out.Attempts = append(out.Attempts, item.attempt)
	}

	switch {
	case failureStatus > 0:
		out.ClientStatusCode = &failureStatus
	case recoveredStatus > 0:
		out.ClientStatusCode = &recoveredStatus
	case len(usageRows) > 0:
		ok := http.StatusOK
		out.ClientStatusCode = &ok
	}
	return out
}

// clampOpsBilledAttemptStarts keeps a billed try from overlapping the failed
// tries sorted before it. usage duration_ms runs from the Forward entry, so a
// same-account retry inside Forward would otherwise start the billed try at
// or before the failed one; the clamped duration still includes the retry
// backoff. Partial tries keep the estimate because their own stream failure
// event ends inside them.
func clampOpsBilledAttemptStarts(items []opsTimedAttempt) {
	var prevEnd *time.Time
	for _, item := range items {
		a := item.attempt
		if a.Source != opsRequestAttemptSourceUsage {
			if a.EndedAt != nil && (prevEnd == nil || a.EndedAt.After(*prevEnd)) {
				prevEnd = a.EndedAt
			}
			continue
		}
		if a.Outcome != opsRequestAttemptOutcomeSuccess || prevEnd == nil || a.StartedAt == nil ||
			a.DurationMs == nil || a.EndedAt == nil || !prevEnd.After(*a.StartedAt) || prevEnd.After(*a.EndedAt) {
			continue
		}
		started := prevEnd.UTC()
		duration := a.EndedAt.Sub(started).Milliseconds()
		shift := *a.DurationMs - duration
		a.StartedAt = &started
		a.DurationMs = &duration
		if a.FirstTokenMs != nil {
			if ft := *a.FirstTokenMs - shift; ft >= 0 {
				a.FirstTokenMs = &ft
			} else {
				a.FirstTokenMs = nil
			}
		}
	}
}

// opsRequestAttemptRowIsNewer orders rows by created_at, then id; any row is
// newer than nil.
func opsRequestAttemptRowIsNewer(a, b *OpsRequestAttemptErrorRow) bool {
	if b == nil {
		return true
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// opsRequestAttemptRowStatuses returns the status of the newest failure row
// (>= 400) and of the newest recovered row (< 400).
func opsRequestAttemptRowStatuses(rows []*OpsRequestAttemptErrorRow) (failure, recovered int) {
	var failureRow, recoveredRow *OpsRequestAttemptErrorRow
	for _, row := range rows {
		switch {
		case row == nil || row.StatusCode <= 0:
		case row.StatusCode >= http.StatusBadRequest:
			if opsRequestAttemptRowIsNewer(row, failureRow) {
				failureRow = row
			}
		default:
			if opsRequestAttemptRowIsNewer(row, recoveredRow) {
				recoveredRow = row
			}
		}
	}
	if failureRow != nil {
		failure = failureRow.StatusCode
	}
	if recoveredRow != nil {
		recovered = recoveredRow.StatusCode
	}
	return failure, recovered
}

func opsRequestAttemptFromEvent(ev *OpsUpstreamErrorEvent, errorLogID int64) *OpsRequestAttempt {
	logID := errorLogID
	attempt := &OpsRequestAttempt{
		Source:            opsRequestAttemptSourceUpstreamError,
		Outcome:           opsRequestAttemptOutcomeFailed,
		Platform:          ev.Platform,
		AccountName:       ev.AccountName,
		ProxyName:         ev.ProxyName,
		UpstreamRequestID: ev.UpstreamRequestID,
		Kind:              ev.Kind,
		Stage:             ev.Stage,
		Message:           ev.Message,
		ErrorLogID:        &logID,
	}
	if ev.AccountID > 0 {
		id := ev.AccountID
		attempt.AccountID = &id
	}
	if ev.ProxyID != nil {
		id := *ev.ProxyID
		attempt.ProxyID = &id
	}
	if ev.UpstreamStatusCode > 0 {
		code := ev.UpstreamStatusCode
		attempt.UpstreamStatusCode = &code
	}
	if ev.AtUnixMs > 0 {
		ended := time.UnixMilli(ev.AtUnixMs).UTC()
		attempt.EndedAt = &ended
	}
	if started, duration := sanitizeOpsUpstreamAttemptTiming(ev.StartedAtUnixMs, ev.DurationMs); started > 0 {
		startedAt := time.UnixMilli(started).UTC()
		attempt.StartedAt = &startedAt
		if duration == nil && ev.AtUnixMs >= started {
			derived := ev.AtUnixMs - started
			duration = &derived
		}
		if duration != nil {
			d := *duration
			attempt.DurationMs = &d
		}
	}
	return attempt
}

func opsRequestAttemptFromUsage(row *OpsRequestAttemptUsageRow, requestFailed bool) *OpsRequestAttempt {
	usageID := row.ID
	status := http.StatusOK
	ended := row.CreatedAt.UTC()
	attempt := &OpsRequestAttempt{
		Source:             opsRequestAttemptSourceUsage,
		Outcome:            opsRequestAttemptOutcomeSuccess,
		Platform:           row.Platform,
		AccountName:        row.AccountName,
		UpstreamStatusCode: &status,
		UpstreamRequestID:  row.UpstreamRequestID,
		EndedAt:            &ended,
		UsageID:            &usageID,
	}
	if requestFailed {
		attempt.Outcome = opsRequestAttemptOutcomePartial
	}
	if row.AccountID > 0 {
		id := row.AccountID
		attempt.AccountID = &id
	}
	if row.DurationMs != nil && *row.DurationMs >= 0 {
		d := *row.DurationMs
		started := ended.Add(-time.Duration(d) * time.Millisecond)
		attempt.DurationMs = &d
		attempt.StartedAt = &started
	}
	if row.FirstTokenMs != nil && *row.FirstTokenMs >= 0 {
		ft := *row.FirstTokenMs
		attempt.FirstTokenMs = &ft
	}
	return attempt
}

// opsRequestAttemptNamelessAccounts lists accounts whose events carry no name
// (failover events never do).
func opsRequestAttemptNamelessAccounts(attempts []*OpsRequestAttempt) []int64 {
	ids := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, a := range attempts {
		if a == nil || a.AccountID == nil || a.AccountName != "" {
			continue
		}
		if _, dup := seen[*a.AccountID]; dup {
			continue
		}
		seen[*a.AccountID] = struct{}{}
		ids = append(ids, *a.AccountID)
	}
	return ids
}

func applyOpsRequestAttemptAccountNames(attempts []*OpsRequestAttempt, names map[int64]string) {
	if len(names) == 0 {
		return
	}
	for _, a := range attempts {
		if a != nil && a.AccountID != nil && a.AccountName == "" {
			a.AccountName = names[*a.AccountID]
		}
	}
}
