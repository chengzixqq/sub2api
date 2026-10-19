package service

import "time"

// Ops alert rule/event models.
//
// NOTE: These are admin-facing DTOs and intentionally keep JSON naming aligned
// with the existing ops dashboard frontend (backup style).

const (
	OpsAlertStatusFiring         = "firing"
	OpsAlertStatusResolved       = "resolved"
	OpsAlertStatusManualResolved = "manual_resolved"
)

type OpsAlertRule struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`

	Enabled  bool   `json:"enabled"`
	Severity string `json:"severity"`

	MetricType string  `json:"metric_type"`
	Operator   string  `json:"operator"`
	Threshold  float64 `json:"threshold"`

	WindowMinutes    int `json:"window_minutes"`
	SustainedMinutes int `json:"sustained_minutes"`
	CooldownMinutes  int `json:"cooldown_minutes"`

	NotifyEmail bool `json:"notify_email"`

	Filters map[string]any `json:"filters,omitempty"`

	LastTriggeredAt *time.Time `json:"last_triggered_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type OpsAlertEvent struct {
	ID       int64  `json:"id"`
	RuleID   int64  `json:"rule_id"`
	Severity string `json:"severity"`
	Status   string `json:"status"`

	Title       string `json:"title"`
	Description string `json:"description"`

	MetricValue    *float64 `json:"metric_value,omitempty"`
	ThresholdValue *float64 `json:"threshold_value,omitempty"`

	Dimensions map[string]any `json:"dimensions,omitempty"`

	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	EmailSent bool      `json:"email_sent"`
	CreatedAt time.Time `json:"created_at"`

	// Silence is the active silence covering this event's scope (null when none). Filled by event list and detail reads.
	Silence *OpsAlertEventSilence `json:"silence"`

	// Rule is the rule as it was when the event fired; events from before snapshots existed fall back to the current
	// rule, and the value is null when neither is available. Written by the evaluator, filled by list and detail reads.
	Rule *OpsAlertEventRule `json:"rule"`
	// GroupName is the name of the group in dimensions.group_id (deleted groups included); null for unscoped events.
	GroupName *string `json:"group_name"`
	// ResolvedByUserID / ResolvedByUserName identify the admin who resolved the event by hand (manual_resolved only).
	ResolvedByUserID   *int64  `json:"resolved_by_user_id"`
	ResolvedByUserName *string `json:"resolved_by_user_name"`
}

// OpsAlertEventRule summarises the rule behind an alert event.
type OpsAlertEventRule struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	MetricType       string  `json:"metric_type"`
	Operator         string  `json:"operator"`
	Threshold        float64 `json:"threshold"`
	WindowMinutes    int     `json:"window_minutes"`
	SustainedMinutes int     `json:"sustained_minutes"`
	// Deleted reports that the rule no longer exists; the other fields then come from the snapshot.
	Deleted bool `json:"deleted"`
}

// OpsAlertEventCounts is the response of GET /admin/ops/alert-events/counts. Silenced firing events are counted, so
// Firing equals the number of events listed with status=firing; BySeverity always has P0–P3 and its values add up
// to Firing.
type OpsAlertEventCounts struct {
	Firing     int64            `json:"firing"`
	BySeverity map[string]int64 `json:"by_severity"`
}

// OpsAlertEventSilence is the latest-ending active silence whose scope matches an event.
type OpsAlertEventSilence struct {
	ID    int64     `json:"id"`
	Until time.Time `json:"until"`
}

type OpsAlertSilence struct {
	ID int64 `json:"id"`

	RuleID   int64   `json:"rule_id"`
	Platform string  `json:"platform"`
	GroupID  *int64  `json:"group_id,omitempty"`
	Region   *string `json:"region,omitempty"`

	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`

	CreatedBy *int64    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// CreatedByName is the creator's username (or email); filled by list queries only.
	CreatedByName string `json:"created_by_name,omitempty"`
}

// OpsAlertSilenceFilter selects silences for GET /admin/ops/alert-silences.
type OpsAlertSilenceFilter struct {
	RuleID *int64
	// ActiveOnly keeps silences whose until is after Now.
	ActiveOnly bool
	Limit      int
	Now        time.Time
}

type OpsAlertEventFilter struct {
	Limit int

	// Cursor pagination (descending by fired_at, then id).
	BeforeFiredAt *time.Time
	BeforeID      *int64

	// Optional filters.
	Status    string
	Severity  string
	EmailSent *bool

	StartTime *time.Time
	EndTime   *time.Time

	// Dimensions filters (best-effort).
	Platform string
	GroupID  *int64
}
