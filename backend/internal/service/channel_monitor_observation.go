package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
)

const ChannelMonitorObservationMaxAttempts = 16

var ErrChannelMonitorObservationCapacity = errors.New("channel monitor observation capacity reached")

// Events contain terminal protocol facts only. No request/response payload or credentials belong here.
type ChannelMonitorEvent struct {
	RequestID           string                  `json:"request_id"`
	SessionID           string                  `json:"session_id"`
	StartedAt           time.Time               `json:"started_at"`
	CompletedAt         time.Time               `json:"completed_at"`
	Platform            string                  `json:"platform"`
	GroupID             int64                   `json:"group_id"`
	UserID              int64                   `json:"user_id"`
	APIKeyID            int64                   `json:"api_key_id"`
	RequestedModel      string                  `json:"requested_model"`
	ResponseModel       string                  `json:"response_model"`
	Protocol            string                  `json:"protocol"`
	Outcome             string                  `json:"outcome"`
	ErrorCategory       string                  `json:"error_category"`
	HTTPStatus          int                     `json:"http_status"`
	Stream              bool                    `json:"stream"`
	OutputSeen          bool                    `json:"output_seen"`
	TerminalSeen        bool                    `json:"terminal_seen"`
	PhaseMs             map[string]int64        `json:"phase_ms,omitempty"`
	FirstOutputMs       *int64                  `json:"first_output_ms,omitempty"`
	DurationMs          int64                   `json:"duration_ms"`
	InputTokens         int64                   `json:"input_tokens"`
	OutputTokens        int64                   `json:"output_tokens"`
	CacheCreationTokens int64                   `json:"cache_creation_tokens"`
	CacheReadTokens     int64                   `json:"cache_read_tokens"`
	Attempts            []ChannelMonitorAttempt `json:"attempts,omitempty"`
	AttemptsTruncated   bool                    `json:"attempts_truncated"`
}

type ChannelMonitorAttempt struct {
	Sequence      int              `json:"sequence"`
	AccountID     int64            `json:"account_id"`
	StartedAt     time.Time        `json:"started_at"`
	CompletedAt   time.Time        `json:"completed_at"`
	Outcome       string           `json:"outcome"`
	ErrorCategory string           `json:"error_category"`
	HTTPStatus    int              `json:"http_status"`
	DurationMs    int64            `json:"duration_ms"`
	PhaseMs       map[string]int64 `json:"phase_ms,omitempty"`
}

type ChannelMonitorEventSink interface {
	Submit(ChannelMonitorEvent) bool
}

type ChannelMonitorObservationConfig struct {
	Version                int                                 `json:"version"`
	Enabled                bool                                `json:"enabled"`
	Mode                   string                              `json:"mode"`
	DetailRetentionHours   int                                 `json:"detail_retention_hours"`
	RefreshIntervalSeconds int                                 `json:"refresh_interval_seconds"`
	MinimumSample          int64                               `json:"minimum_sample"`
	HealthyReliability     float64                             `json:"healthy_reliability"`
	WarningReliability     float64                             `json:"warning_reliability"`
	WarningTTFTMs          int64                               `json:"warning_ttft_ms"`
	CriticalTTFTMs         int64                               `json:"critical_ttft_ms"`
	Overrides              []ChannelMonitorObservationOverride `json:"overrides"`
}

type ChannelMonitorObservationOverride struct {
	Platform       string `json:"platform,omitempty"`
	GroupID        int64  `json:"group_id,omitempty"`
	Model          string `json:"model,omitempty"`
	WarningTTFTMs  int64  `json:"warning_ttft_ms"`
	CriticalTTFTMs int64  `json:"critical_ttft_ms"`
}

func DefaultChannelMonitorObservationConfig() ChannelMonitorObservationConfig {
	return ChannelMonitorObservationConfig{Version: 1, Enabled: true, Mode: "shadow", DetailRetentionHours: 72,
		RefreshIntervalSeconds: 60, MinimumSample: 50, HealthyReliability: .99, WarningReliability: .95,
		WarningTTFTMs: 3000, CriticalTTFTMs: 10000, Overrides: []ChannelMonitorObservationOverride{}}
}

var observationIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/@+\-]{0,191}$`)
var observationLabel = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

func validObservationIdentifier(s string) bool {
	if s == "" {
		return true
	}
	return observationIdentifier.MatchString(s) && !regexp.MustCompile(`://|@`).MatchString(s)
}

func ValidateChannelMonitorObservationConfig(cfg ChannelMonitorObservationConfig) error {
	if cfg.Mode != "shadow" && cfg.Mode != "live" {
		return errors.New("invalid observation mode")
	}
	if cfg.DetailRetentionHours != 72 || cfg.RefreshIntervalSeconds != 60 {
		return errors.New("observation retention and refresh must be 72 hours and 60 seconds")
	}
	if cfg.MinimumSample < 1 || cfg.MinimumSample > 1000000 || !(cfg.WarningReliability > 0 && cfg.WarningReliability < cfg.HealthyReliability && cfg.HealthyReliability <= 1) {
		return errors.New("invalid observation reliability thresholds")
	}
	if cfg.WarningTTFTMs <= 0 || cfg.CriticalTTFTMs <= cfg.WarningTTFTMs || cfg.CriticalTTFTMs > 86400000 {
		return errors.New("invalid observation latency thresholds")
	}
	if len(cfg.Overrides) > 100 {
		return errors.New("too many observation overrides")
	}
	seen := map[string]bool{}
	for _, o := range cfg.Overrides {
		if (o.Platform == "" && o.GroupID == 0 && o.Model == "") || o.GroupID < 0 || !validObservationIdentifier(o.Model) || (o.Platform != "" && !observationLabel.MatchString(o.Platform)) || o.WarningTTFTMs <= 0 || o.CriticalTTFTMs <= o.WarningTTFTMs || o.CriticalTTFTMs > 86400000 {
			return errors.New("invalid observation override")
		}
		key := fmt.Sprintf("%s\x00%d\x00%s", o.Platform, o.GroupID, o.Model)
		if seen[key] {
			return errors.New("duplicate observation override selector")
		}
		seen[key] = true
	}
	return nil
}

func (c ChannelMonitorObservationConfig) LatencyThresholds(platform string, groupID int64, model string) (int64, int64) {
	warning, critical, best := c.WarningTTFTMs, c.CriticalTTFTMs, 0
	for _, o := range c.Overrides {
		if (o.Platform != "" && o.Platform != platform) || (o.GroupID != 0 && o.GroupID != groupID) || (o.Model != "" && o.Model != model) {
			continue
		}
		score := 0
		if o.Platform != "" {
			score += 9
		}
		if o.GroupID != 0 {
			score += 10
		}
		if o.Model != "" {
			score += 12
		}
		if score > best {
			best = score
			warning = o.WarningTTFTMs
			critical = o.CriticalTTFTMs
		}
	}
	return warning, critical
}

func NormalizeChannelMonitorEvent(e ChannelMonitorEvent, now time.Time) (ChannelMonitorEvent, error) {
	if _, err := uuid.Parse(e.RequestID); err != nil {
		return e, errors.New("invalid observation UUID")
	}
	if e.StartedAt.IsZero() || e.CompletedAt.IsZero() || e.CompletedAt.Before(e.StartedAt) || e.CompletedAt.After(now.Add(time.Minute)) || e.CompletedAt.Before(now.Add(-72*time.Hour)) {
		return e, errors.New("invalid observation time")
	}
	if e.GroupID < 0 || e.UserID < 0 || e.APIKeyID < 0 || !observationLabel.MatchString(e.Platform) || !observationLabel.MatchString(e.Protocol) || !validObservationIdentifier(e.RequestedModel) || !validObservationIdentifier(e.ResponseModel) {
		return e, errors.New("invalid observation dimension")
	}
	if !observationOutcome(e.Outcome) {
		return e, errors.New("invalid observation outcome")
	}
	if e.ErrorCategory != "" && !observationLabel.MatchString(e.ErrorCategory) {
		return e, errors.New("invalid observation category")
	}
	if e.DurationMs < 0 || e.DurationMs > 86400000 || e.InputTokens < 0 || e.OutputTokens < 0 || e.CacheCreationTokens < 0 || e.CacheReadTokens < 0 || e.HTTPStatus < 0 || e.HTTPStatus > 599 {
		return e, errors.New("invalid observation numeric fact")
	}
	if e.FirstOutputMs != nil {
		if *e.FirstOutputMs < 0 || *e.FirstOutputMs > 86400000 {
			return e, errors.New("invalid first output")
		}
		n := *e.FirstOutputMs
		e.FirstOutputMs = &n
	}
	e.StartedAt = e.StartedAt.UTC()
	e.CompletedAt = e.CompletedAt.UTC()
	e.PhaseMs = observationPhases(e.PhaseMs)
	if len(e.Attempts) > ChannelMonitorObservationMaxAttempts {
		e.Attempts = e.Attempts[:ChannelMonitorObservationMaxAttempts]
		e.AttemptsTruncated = true
	}
	e.Attempts = append([]ChannelMonitorAttempt(nil), e.Attempts...)
	for i := range e.Attempts {
		a := &e.Attempts[i]
		a.Sequence = i + 1
		a.PhaseMs = observationPhases(a.PhaseMs)
		if !observationOutcome(a.Outcome) {
			a.Outcome = "unknown"
		}
		if !observationLabel.MatchString(a.ErrorCategory) {
			a.ErrorCategory = ""
		}
		if a.AccountID < 0 || a.DurationMs < 0 || a.DurationMs > 86400000 || a.HTTPStatus < 0 || a.HTTPStatus > 599 {
			return e, errors.New("invalid observation attempt")
		}
	}
	return e, nil
}

func observationOutcome(s string) bool {
	switch s {
	case "success", "channel_error", "client_error", "cancelled", "unknown":
		return true
	}
	return false
}
func observationPhases(m map[string]int64) map[string]int64 {
	allowed := map[string]bool{"queue": true, "user_queue": true, "account_queue": true, "selection": true, "dns": true, "connect": true, "tls": true, "upstream": true, "upstream_first_byte": true, "downstream_write": true, "total": true, "user_slot": true, "account_slot": true, "transport": true}
	out := map[string]int64{}
	for k, v := range m {
		if allowed[k] && v >= 0 && v <= 86400000 {
			out[k] = v
		}
	}
	return out
}

type ChannelMonitorObservationFact struct {
	BucketStart            time.Time        `json:"bucket_start"`
	Platform               string           `json:"platform"`
	GroupID                int64            `json:"group_id"`
	Model                  string           `json:"model"`
	SuccessRequests        int64            `json:"success_requests"`
	ChannelErrors          int64            `json:"channel_errors"`
	ClientErrors           int64            `json:"client_errors"`
	CancelledRequests      int64            `json:"cancelled_requests"`
	UnknownRequests        int64            `json:"unknown_requests"`
	AttemptCount           int64            `json:"attempt_count"`
	RetryRecoveredRequests int64            `json:"retry_recovered_requests"`
	InputTokens            int64            `json:"input_tokens"`
	OutputTokens           int64            `json:"output_tokens"`
	CacheCreationTokens    int64            `json:"cache_creation_tokens"`
	CacheReadTokens        int64            `json:"cache_read_tokens"`
	TTFTSumMs              int64            `json:"ttft_sum_ms"`
	TTFTCount              int64            `json:"ttft_count"`
	DurationSumMs          int64            `json:"duration_sum_ms"`
	DurationCount          int64            `json:"duration_count"`
	TTFTHistogram          map[int64]int64  `json:"ttft_histogram"`
	DurationHistogram      map[int64]int64  `json:"duration_histogram"`
	PhaseSumMs             map[string]int64 `json:"phase_sum_ms"`
	PhaseCounts            map[string]int64 `json:"phase_counts"`
	ErrorCategories        map[string]int64 `json:"error_categories"`
}

func ChannelMonitorObservationFactFromEvent(e ChannelMonitorEvent, bucket time.Duration) ChannelMonitorObservationFact {
	f := ChannelMonitorObservationFact{BucketStart: e.CompletedAt.UTC().Truncate(bucket), Platform: e.Platform, GroupID: e.GroupID, Model: e.RequestedModel,
		AttemptCount: int64(len(e.Attempts)), InputTokens: e.InputTokens, OutputTokens: e.OutputTokens, CacheCreationTokens: e.CacheCreationTokens, CacheReadTokens: e.CacheReadTokens,
		TTFTHistogram: map[int64]int64{}, DurationHistogram: map[int64]int64{}, PhaseSumMs: map[string]int64{}, PhaseCounts: map[string]int64{}, ErrorCategories: map[string]int64{}}
	switch e.Outcome {
	case "success":
		f.SuccessRequests = 1
	case "channel_error":
		f.ChannelErrors = 1
	case "client_error":
		f.ClientErrors = 1
	case "cancelled":
		f.CancelledRequests = 1
	default:
		f.UnknownRequests = 1
	}
	if e.Outcome == "success" && len(e.Attempts) > 1 {
		for _, a := range e.Attempts[:len(e.Attempts)-1] {
			if a.Outcome != "success" {
				f.RetryRecoveredRequests = 1
				break
			}
		}
	}
	if e.FirstOutputMs != nil && e.Outcome == "success" {
		f.TTFTCount = 1
		f.TTFTSumMs = *e.FirstOutputMs
		f.TTFTHistogram[ChannelMonitorObservationHistogramBound(*e.FirstOutputMs)] = 1
	}
	if e.Outcome == "success" {
		f.DurationCount = 1
		f.DurationSumMs = e.DurationMs
		f.DurationHistogram[ChannelMonitorObservationHistogramBound(e.DurationMs)] = 1
	}
	for k, v := range e.PhaseMs {
		f.PhaseSumMs[k] = v
		f.PhaseCounts[k] = 1
	}
	if e.ErrorCategory != "" {
		f.ErrorCategories[e.ErrorCategory] = 1
	}
	return f
}

func ChannelMonitorObservationHistogramBound(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	bound := int64(1)
	for bound < ms && bound < 86400000 {
		step := bound / 20
		if step < 1 {
			step = 1
		}
		bound += step
	}
	return bound
}

func (f *ChannelMonitorObservationFact) Add(o ChannelMonitorObservationFact) {
	f.SuccessRequests += o.SuccessRequests
	f.ChannelErrors += o.ChannelErrors
	f.ClientErrors += o.ClientErrors
	f.CancelledRequests += o.CancelledRequests
	f.UnknownRequests += o.UnknownRequests
	f.AttemptCount += o.AttemptCount
	f.RetryRecoveredRequests += o.RetryRecoveredRequests
	f.InputTokens += o.InputTokens
	f.OutputTokens += o.OutputTokens
	f.CacheCreationTokens += o.CacheCreationTokens
	f.CacheReadTokens += o.CacheReadTokens
	f.TTFTSumMs += o.TTFTSumMs
	f.TTFTCount += o.TTFTCount
	f.DurationSumMs += o.DurationSumMs
	f.DurationCount += o.DurationCount
	if f.TTFTHistogram == nil {
		f.TTFTHistogram = map[int64]int64{}
	}
	for k, v := range o.TTFTHistogram {
		f.TTFTHistogram[k] += v
	}
	if f.DurationHistogram == nil {
		f.DurationHistogram = map[int64]int64{}
	}
	for k, v := range o.DurationHistogram {
		f.DurationHistogram[k] += v
	}
	if f.PhaseSumMs == nil {
		f.PhaseSumMs = map[string]int64{}
	}
	for k, v := range o.PhaseSumMs {
		f.PhaseSumMs[k] += v
	}
	if f.PhaseCounts == nil {
		f.PhaseCounts = map[string]int64{}
	}
	for k, v := range o.PhaseCounts {
		f.PhaseCounts[k] += v
	}
	if f.ErrorCategories == nil {
		f.ErrorCategories = map[string]int64{}
	}
	for k, v := range o.ErrorCategories {
		f.ErrorCategories[k] += v
	}
}

func ChannelMonitorObservationLatency(sum, count int64, hist map[int64]int64) ChannelMonitorV2Latency {
	l := ChannelMonitorV2Latency{SampleCount: count}
	if count <= 0 {
		return l
	}
	avg := float64(sum) / float64(count)
	l.AvgMs = &avg
	keys := make([]int64, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	quantile := func(p float64) *int64 {
		target := int64(float64(count)*p + .999999)
		var n int64
		for _, k := range keys {
			n += hist[k]
			if n >= target {
				v := k
				return &v
			}
		}
		return nil
	}
	l.P50Ms = quantile(.5)
	l.P90Ms = quantile(.9)
	l.P95Ms = quantile(.95)
	return l
}

type ChannelMonitorObservationGap struct {
	StartedAt  time.Time
	EndedAt    time.Time
	Reason     string
	LostEvents int64
}
type ChannelMonitorObservationSession struct {
	ID            string
	StartedAt     time.Time
	HeartbeatAt   time.Time
	EndedAt       *time.Time
	InFlight      int64
	DroppedEvents int64
}
type ChannelMonitorObservationCoverage struct {
	State                string     `json:"state"`
	SourceStartedAt      *time.Time `json:"source_started_at,omitempty"`
	DataThrough          time.Time  `json:"data_through"`
	GapCount             int64      `json:"gap_count"`
	LostEvents           int64      `json:"lost_events"`
	InFlight             int64      `json:"in_flight"`
	UnsupportedProtocols []string   `json:"unsupported_protocols"`
}
type ChannelMonitorObservationSnapshot struct {
	Facts    []ChannelMonitorObservationFact
	Coverage ChannelMonitorObservationCoverage
}

type ChannelMonitorObservationRepository interface {
	GetConfig(context.Context) (*ChannelMonitorObservationConfig, error)
	UpdateConfig(context.Context, ChannelMonitorObservationConfig, int) (*ChannelMonitorObservationConfig, error)
	StoreBatch(context.Context, []ChannelMonitorEvent) error
	RecordGaps(context.Context, string, []ChannelMonitorObservationGap) error
	Heartbeat(context.Context, ChannelMonitorObservationSession) error
	Maintain(context.Context, time.Time) error
	Query(context.Context, ChannelMonitorV2Filter) (*ChannelMonitorObservationSnapshot, error)
}
