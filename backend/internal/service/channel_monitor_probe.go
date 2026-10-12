package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

const (
	ChannelMonitorProbeTargetDailyLimit = 96
	ChannelMonitorProbeGlobalDailyLimit = 1000
	ChannelMonitorProbeTimeout          = 20 * time.Second
	channelMonitorProbeIdle             = 15 * time.Minute
)

// ChannelMonitorProbePolicy is captured once per operation so scheduling, the
// transactional reservation and execution use the same limits.
type ChannelMonitorProbePolicy struct {
	IntervalSeconds   int
	TimeoutSeconds    int
	TargetDailyLimit  int
	GlobalDailyLimit  int
	FailureThreshold  int
	QuotaStaleSeconds int
}

func (c ChannelMonitorObservationConfig) ProbePolicy() ChannelMonitorProbePolicy {
	return ChannelMonitorProbePolicy{c.ProbeIntervalSeconds, c.ProbeTimeoutSeconds, c.ProbeTargetDailyLimit, c.ProbeGlobalDailyLimit, c.ProbeFailureThreshold, c.QuotaStaleSeconds}
}

func (p ChannelMonitorProbePolicy) Validate() error {
	if p.IntervalSeconds < 60 || p.IntervalSeconds > 3600 || p.TimeoutSeconds < 5 || p.TimeoutSeconds > 60 || p.TargetDailyLimit < 1 || p.TargetDailyLimit > 1440 || p.GlobalDailyLimit < 1 || p.GlobalDailyLimit > 100000 {
		return errors.New("invalid probe policy: interval 60..3600s, timeout 5..60s, target budget 1..1440, global budget 1..100000")
	}
	if p.FailureThreshold < 1 || p.FailureThreshold > 10 || p.QuotaStaleSeconds < 300 || p.QuotaStaleSeconds > 3600 {
		return errors.New("invalid status policy: probe failure threshold 1..10, quota stale 300..3600s")
	}
	return nil
}

func (p ChannelMonitorProbePolicy) HistoryWindow() time.Duration {
	return time.Duration(p.IntervalSeconds*p.FailureThreshold)*time.Second + 5*time.Minute
}

func (p ChannelMonitorProbePolicy) EvidenceWindow() time.Duration {
	return time.Duration(p.IntervalSeconds)*time.Second + 5*time.Minute
}

var (
	ErrChannelMonitorProbeNotFound = errors.New("monitor probe target not found")
	ErrChannelMonitorProbeConflict = errors.New("monitor probe configuration or idempotency conflict")
	ErrChannelMonitorProbeBudget   = errors.New("monitor probe daily budget exhausted")
	ErrChannelMonitorProbeBusy     = errors.New("monitor probe target is already running")
	ErrChannelMonitorProbeDisabled = errors.New("monitor probe target disabled")
	ErrChannelMonitorProbeInvalid  = errors.New("invalid monitor probe target")
	monitorProbeModelPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)
)

type ChannelMonitorProbeTarget struct {
	ID          int64      `json:"id"`
	GroupID     int64      `json:"group_id"`
	Model       string     `json:"model"`
	Protocol    string     `json:"protocol"`
	Enabled     bool       `json:"enabled"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastProbeAt *time.Time `json:"last_probe_at,omitempty"`
}

func (t ChannelMonitorProbeTarget) Validate() error {
	if t.GroupID <= 0 || !monitorProbeModelPattern.MatchString(t.Model) || strings.Contains(t.Model, "..") {
		return ErrChannelMonitorProbeInvalid
	}
	for _, part := range []string{"image", "video", "audio", "embedding", "tts", "whisper"} {
		if strings.Contains(strings.ToLower(t.Model), part) {
			return ErrChannelMonitorProbeInvalid
		}
	}
	switch t.Protocol {
	case "openai_chat", "openai_responses", "anthropic", "gemini":
		return nil
	}
	return ErrChannelMonitorProbeInvalid
}

type ChannelMonitorProbeRun struct {
	ID              string     `json:"id"`
	TargetID        int64      `json:"target_id"`
	GroupID         int64      `json:"group_id"`
	Model           string     `json:"model"`
	Protocol        string     `json:"protocol"`
	Status          string     `json:"status"`
	Success         *bool      `json:"success,omitempty"`
	AccountID       *int64     `json:"account_id,omitempty"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	LatencyMS       int64      `json:"latency_ms"`
	ErrorClass      string     `json:"error_class,omitempty"`
	InputTokens     int64      `json:"input_tokens"`
	OutputTokens    int64      `json:"output_tokens"`
	UpstreamCostUSD *float64   `json:"upstream_cost_usd,omitempty"`
}

// ChannelMonitorProbeEvidence carries configured enabled targets with recent
// terminal runs. A target is the unit of health evidence, so one protocol
// cannot overwrite a different protocol's result.
type ChannelMonitorProbeEvidence struct {
	MaxAge           time.Duration
	HistoryAge       time.Duration
	FailureThreshold int
	Targets          []ChannelMonitorProbeTarget
	Runs             []ChannelMonitorProbeRun
}

type ChannelMonitorProbeTargetBudget struct {
	TargetID int64 `json:"target_id"`
	Limit    int   `json:"limit"`
	Used     int   `json:"used"`
}
type ChannelMonitorProbeBudget struct {
	UTCDay      string                            `json:"utc_day"`
	GlobalLimit int                               `json:"global_limit"`
	GlobalUsed  int                               `json:"global_used"`
	Targets     []ChannelMonitorProbeTargetBudget `json:"targets"`
}
type ChannelMonitorProbeActivity struct {
	CollectionHealthy bool
	ObservedSince     time.Time
	LastTrafficAt     *time.Time
}
type ChannelMonitorProbeActivityReader interface {
	ProbeActivity(context.Context, int64, string, string) (ChannelMonitorProbeActivity, error)
}
type ChannelMonitorQuotaSummary struct {
	State     string     `json:"state"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}
type ChannelMonitorProbeRepository interface {
	ListTargets(context.Context) ([]ChannelMonitorProbeTarget, error)
	GetTarget(context.Context, int64) (*ChannelMonitorProbeTarget, error)
	SaveTarget(context.Context, *ChannelMonitorProbeTarget) error
	Reserve(context.Context, int64, string, bool, time.Time, ChannelMonitorProbePolicy) (*ChannelMonitorProbeRun, bool, error)
	Complete(context.Context, *ChannelMonitorProbeRun) error
	Budget(context.Context, time.Time, ChannelMonitorProbePolicy) (*ChannelMonitorProbeBudget, error)
	RecentRuns(context.Context, []int64, time.Time) ([]ChannelMonitorProbeRun, error)
	QuotaAccounts(context.Context) ([]int64, error)
	ClaimQuota(context.Context, int64, time.Time) (bool, error)
	StoreQuota(context.Context, int64, *domain.MonitorQuotaSnapshot) error
	ReadQuotas(context.Context, []int64) (map[int64]*domain.MonitorQuotaSnapshot, error)
	QuotaSummaries(context.Context, []int64, time.Time, time.Duration) (map[int64]ChannelMonitorQuotaSummary, error)
}

func channelMonitorProbeDue(now time.Time, a ChannelMonitorProbeActivity, lastProbe *time.Time) bool {
	return channelMonitorProbeDueWithInterval(now, a, lastProbe, channelMonitorProbeIdle)
}

func channelMonitorProbeDueWithInterval(now time.Time, a ChannelMonitorProbeActivity, lastProbe *time.Time, interval time.Duration) bool {
	if !a.CollectionHealthy || a.ObservedSince.IsZero() || now.Sub(a.ObservedSince) < interval {
		return false
	}
	if a.LastTrafficAt != nil && now.Sub(*a.LastTrafficAt) < interval {
		return false
	}
	return lastProbe == nil || now.Sub(*lastProbe) >= interval
}
func validMonitorProbeKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
