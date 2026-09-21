package service

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Account attempts are not request outcomes: a recovered retry belongs to both
// the failed account's diagnostics and the successful group's traffic result.
type ChannelMonitorAccountObservationFact struct {
	AccountID            int64     `json:"account_id"`
	Source               string    `json:"source"`
	Platform             string    `json:"platform"`
	GroupID              int64     `json:"group_id"`
	Model                string    `json:"model"`
	BucketStart          time.Time `json:"bucket_start"`
	AttemptCount         int64     `json:"attempt_count"`
	SuccessAttempts      int64     `json:"success_attempts"`
	ChannelErrorAttempts int64     `json:"channel_error_attempts"`
	ClientErrorAttempts  int64     `json:"client_error_attempts"`
	CancelledAttempts    int64     `json:"cancelled_attempts"`
	FailedAttempts       int64     `json:"failed_attempts"`
	UnclassifiedAttempts int64     `json:"unclassified_attempts"`
	UnknownAttempts      int64     `json:"unknown_attempts"`
	DurationSumMs        int64     `json:"duration_sum_ms"`
}

type ChannelMonitorObservationSample struct {
	CompletedAt   time.Time `json:"completed_at"`
	Model         string    `json:"model"`
	Outcome       string    `json:"outcome"`
	ErrorCategory string    `json:"error_category"`
	HTTPStatus    int       `json:"http_status"`
	AccountID     int64     `json:"account_id,omitempty"`
}

type ChannelMonitorAccountObservationReader interface {
	QueryAccounts(context.Context, ChannelMonitorV2Filter) ([]ChannelMonitorAccountObservationFact, error)
}

type ChannelMonitorObservationSampleReader interface {
	QuerySamples(context.Context, ChannelMonitorV2Filter) ([]ChannelMonitorObservationSample, error)
}

type ObservationAccountDetail struct {
	AccountID  int64              `json:"account_id"`
	Metrics    ObservationMetrics `json:"metrics"`
	LastSeenAt time.Time          `json:"last_seen_at"`
}

type ObservationAccountDetails struct {
	Items   []ObservationAccountDetail        `json:"items"`
	Samples []ChannelMonitorObservationSample `json:"samples"`
}

func (s *ChannelMonitorOverviewService) AccountDetails(ctx context.Context, f ChannelMonitorV2Filter) (*ObservationAccountDetails, error) {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	accounts, ok := s.repo.(ChannelMonitorAccountObservationReader)
	if !ok {
		return nil, errors.New("monitor account details unavailable")
	}
	facts, err := accounts.QueryAccounts(ctx, f)
	if err != nil {
		return nil, err
	}
	out := &ObservationAccountDetails{Items: []ObservationAccountDetail{}, Samples: []ChannelMonitorObservationSample{}}
	byID := map[int64]*ObservationAccountDetail{}
	for _, fact := range facts {
		if fact.Source != "traffic" || !observationHasID(f.AllowedGroupIDs, fact.GroupID) {
			continue
		}
		item := byID[fact.AccountID]
		if item == nil {
			item = &ObservationAccountDetail{AccountID: fact.AccountID}
			byID[fact.AccountID] = item
		}
		item.Metrics.AttemptCount += fact.AttemptCount
		item.Metrics.SuccessRequests += fact.SuccessAttempts
		item.Metrics.ChannelErrors += fact.ChannelErrorAttempts
		item.Metrics.ClientErrors += fact.ClientErrorAttempts
		item.Metrics.CancelledRequests += fact.CancelledAttempts
		unclassified := fact.UnclassifiedAttempts
		if unclassified == 0 {
			// Older compact rows only have failed_attempts, which combined several
			// outcomes. Preserve that evidence without mislabeling it as a channel
			// failure in the owner drilldown.
			unclassified = fact.FailedAttempts
		}
		item.Metrics.UnclassifiedAttempts += unclassified
		item.Metrics.UnknownRequests += fact.UnknownAttempts
		if fact.BucketStart.After(item.LastSeenAt) {
			item.LastSeenAt = fact.BucketStart
		}
	}
	for _, item := range byID {
		out.Items = append(out.Items, *item)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].AccountID < out.Items[j].AccountID })
	if samples, ok := s.repo.(ChannelMonitorObservationSampleReader); ok {
		out.Samples, err = samples.QuerySamples(ctx, f)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
