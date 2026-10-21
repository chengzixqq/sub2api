package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Account schedule states, one per account, checked in this order.
const (
	AccountStateInactive          = "inactive"
	AccountStateError             = "error"
	AccountStateOverloaded        = "overloaded"
	AccountStateRateLimited       = "rate_limited"
	AccountStateTempUnschedulable = "temp_unschedulable"
	AccountStatePaused            = "paused"
	AccountStateAvailable         = "available"

	// SettingKeyGroupShortageMinRatio is the site-wide share of available accounts
	// below which a group without an availability alert rule counts as short.
	SettingKeyGroupShortageMinRatio     = "group_shortage_min_ratio"
	GroupShortageMinRatioDefault        = 0.5
	accountStatusShortageSourceRule     = "alert_rule"
	accountStatusShortageSourceDefault  = "default"
	accountStatusGroupAvailableRatio    = "group_available_ratio"
	accountStatusGroupAvailableAccounts = "group_available_accounts"
)

// AccountStatusRow is the slice of an account the status summary needs.
type AccountStatusRow struct {
	ID                      int64
	Name                    string
	Platform                string
	Status                  string
	ErrorMessage            string
	Schedulable             bool
	RateLimitResetAt        *time.Time
	OverloadUntil           *time.Time
	TempUnschedulableUntil  *time.Time
	TempUnschedulableReason string
	ExpiresAt               *time.Time
	AutoPauseOnExpired      bool
	ModelRateLimits         map[string]any
	GroupIDs                []int64
}

// AccountStatusGroupRow names a group in the summary.
type AccountStatusGroupRow struct {
	ID       int64
	Name     string
	Platform string
	Status   string
}

// AccountScheduleState classifies an account. Available uses the same predicates
// as the group list's active count (groupAccountAvailableSQL). Timed windows are
// checked before the schedulable switch and expiry, so the group list's
// temporarily-limited count (schedulable and unexpired only) is a subset of the
// three timed states here; the rest of them are idle in the group list.
func AccountScheduleState(a *AccountStatusRow, now time.Time) string {
	switch {
	case a.Status == StatusError:
		return AccountStateError
	case a.Status != StatusActive:
		return AccountStateInactive
	case a.OverloadUntil != nil && a.OverloadUntil.After(now):
		return AccountStateOverloaded
	case a.RateLimitResetAt != nil && a.RateLimitResetAt.After(now):
		return AccountStateRateLimited
	case a.TempUnschedulableUntil != nil && a.TempUnschedulableUntil.After(now):
		return AccountStateTempUnschedulable
	case !a.Schedulable:
		return AccountStatePaused
	case a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now):
		return AccountStatePaused
	default:
		return AccountStateAvailable
	}
}

func accountStateIsTimed(state string) bool {
	return state == AccountStateOverloaded || state == AccountStateRateLimited || state == AccountStateTempUnschedulable
}

// AccountStateCounts buckets accounts by schedule state. Stopped counts timed
// states whose schedulable switch is also off; those accounts are counted in
// their timed bucket as well.
type AccountStateCounts struct {
	Total             int64 `json:"total"`
	Available         int64 `json:"available"`
	Error             int64 `json:"error"`
	Overloaded        int64 `json:"overloaded"`
	RateLimited       int64 `json:"rate_limited"`
	TempUnschedulable int64 `json:"temp_unschedulable"`
	Paused            int64 `json:"paused"`
	Inactive          int64 `json:"inactive"`
	Stopped           int64 `json:"stopped"`
}

func (c *AccountStateCounts) add(state string, stopped bool) {
	c.Total++
	switch state {
	case AccountStateAvailable:
		c.Available++
	case AccountStateError:
		c.Error++
	case AccountStateOverloaded:
		c.Overloaded++
	case AccountStateRateLimited:
		c.RateLimited++
	case AccountStateTempUnschedulable:
		c.TempUnschedulable++
	case AccountStatePaused:
		c.Paused++
	case AccountStateInactive:
		c.Inactive++
	}
	if stopped {
		c.Stopped++
	}
}

type AccountStatusPlatformSummary struct {
	Platform string `json:"platform"`
	AccountStateCounts
}

type AccountStatusShortage struct {
	IsShort    bool     `json:"is_short"`
	Source     string   `json:"source"`
	RuleID     *int64   `json:"rule_id,omitempty"`
	MetricType string   `json:"metric_type,omitempty"`
	Operator   string   `json:"operator,omitempty"`
	Threshold  *float64 `json:"threshold,omitempty"`
	Current    *float64 `json:"current,omitempty"`
}

type AccountStatusGroupSummary struct {
	GroupID     int64  `json:"group_id"`
	GroupName   string `json:"group_name"`
	Platform    string `json:"platform"`
	GroupStatus string `json:"group_status"`
	AccountStateCounts
	Shortage AccountStatusShortage `json:"shortage"`
}

type AccountStatusModelRateLimit struct {
	Model   string    `json:"model"`
	ResetAt time.Time `json:"reset_at"`
}

type AccountStatusItem struct {
	ID                      int64                         `json:"id"`
	Name                    string                        `json:"name"`
	Platform                string                        `json:"platform"`
	GroupIDs                []int64                       `json:"group_ids"`
	State                   string                        `json:"state"`
	Stopped                 bool                          `json:"stopped"`
	Status                  string                        `json:"status"`
	Schedulable             bool                          `json:"schedulable"`
	ErrorMessage            string                        `json:"error_message"`
	RateLimitResetAt        *time.Time                    `json:"rate_limit_reset_at"`
	OverloadUntil           *time.Time                    `json:"overload_until"`
	TempUnschedulableUntil  *time.Time                    `json:"temp_unschedulable_until"`
	TempUnschedulableReason string                        `json:"temp_unschedulable_reason"`
	ExpiresAt               *time.Time                    `json:"expires_at"`
	ModelRateLimits         []AccountStatusModelRateLimit `json:"model_rate_limits"`
}

type AccountStatusSummary struct {
	CollectedAt time.Time                      `json:"collected_at"`
	Totals      AccountStateCounts             `json:"totals"`
	Platforms   []AccountStatusPlatformSummary `json:"platforms"`
	Groups      []AccountStatusGroupSummary    `json:"groups"`
	Accounts    []AccountStatusItem            `json:"accounts,omitempty"`
}

type AccountStatusSummaryQuery struct {
	Platform        string
	GroupID         *int64
	IncludeAccounts bool
	// DefaultMinRatio overrides the group_shortage_min_ratio site setting.
	DefaultMinRatio *float64
}

type AccountStatusSummaryRepository interface {
	ListAccountsForStatusSummary(ctx context.Context, platform string, groupID *int64) ([]AccountStatusRow, error)
	// ListGroupsForStatusSummary returns the active groups matching the filters
	// plus any group named in extraIDs (inactive groups that still hold accounts).
	ListGroupsForStatusSummary(ctx context.Context, platform string, groupID *int64, extraIDs []int64) ([]AccountStatusGroupRow, error)
}

type accountStatusAlertRuleReader interface {
	ListAlertRules(ctx context.Context) ([]*OpsAlertRule, error)
}

type accountStatusSettingReader interface {
	GetValue(ctx context.Context, key string) (string, error)
}

// AccountStatusSummaryService builds the schedule-state summary used by the
// mobile overview. It reads the account table directly and does not depend on
// ops monitoring being enabled.
type AccountStatusSummaryService struct {
	repo     AccountStatusSummaryRepository
	rules    accountStatusAlertRuleReader
	settings accountStatusSettingReader
	now      func() time.Time
}

func NewAccountStatusSummaryService(repo AccountStatusSummaryRepository, rules accountStatusAlertRuleReader, settings accountStatusSettingReader) *AccountStatusSummaryService {
	return &AccountStatusSummaryService{repo: repo, rules: rules, settings: settings, now: time.Now}
}

// NewAccountStatusSummaryServiceForWire adapts the concrete repositories.
func NewAccountStatusSummaryServiceForWire(accountRepo AccountRepository, opsRepo OpsRepository, settingRepo SettingRepository) *AccountStatusSummaryService {
	repo, _ := accountRepo.(AccountStatusSummaryRepository)
	var rules accountStatusAlertRuleReader
	if opsRepo != nil {
		rules = opsRepo
	}
	var settings accountStatusSettingReader
	if settingRepo != nil {
		settings = settingRepo
	}
	return NewAccountStatusSummaryService(repo, rules, settings)
}

var errAccountStatusSummaryUnavailable = errors.New("account status summary is not supported by the account repository")

func (s *AccountStatusSummaryService) Summarize(ctx context.Context, q AccountStatusSummaryQuery) (*AccountStatusSummary, error) {
	if s == nil || s.repo == nil {
		return nil, errAccountStatusSummaryUnavailable
	}
	now := s.now()
	rows, err := s.repo.ListAccountsForStatusSummary(ctx, q.Platform, q.GroupID)
	if err != nil {
		return nil, err
	}

	out := &AccountStatusSummary{CollectedAt: now.UTC(), Platforms: []AccountStatusPlatformSummary{}, Groups: []AccountStatusGroupSummary{}}
	platformCounts := map[string]*AccountStateCounts{}
	groupCounts := map[int64]*AccountStateCounts{}
	if q.IncludeAccounts {
		out.Accounts = make([]AccountStatusItem, 0, len(rows))
	}
	for i := range rows {
		row := &rows[i]
		state := AccountScheduleState(row, now)
		stopped := accountStateIsTimed(state) && !row.Schedulable
		out.Totals.add(state, stopped)
		if platformCounts[row.Platform] == nil {
			platformCounts[row.Platform] = &AccountStateCounts{}
		}
		platformCounts[row.Platform].add(state, stopped)
		for _, gid := range row.GroupIDs {
			if q.GroupID != nil && gid != *q.GroupID {
				continue
			}
			if groupCounts[gid] == nil {
				groupCounts[gid] = &AccountStateCounts{}
			}
			groupCounts[gid].add(state, stopped)
		}
		if q.IncludeAccounts {
			out.Accounts = append(out.Accounts, accountStatusItem(row, state, stopped, now))
		}
	}

	platforms := make([]string, 0, len(platformCounts))
	for p := range platformCounts {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	for _, p := range platforms {
		out.Platforms = append(out.Platforms, AccountStatusPlatformSummary{Platform: p, AccountStateCounts: *platformCounts[p]})
	}

	groups, err := s.listGroups(ctx, q, groupCounts)
	if err != nil {
		return nil, err
	}
	shortage := s.shortageJudge(ctx, q)
	for _, g := range groups {
		counts := AccountStateCounts{}
		if c := groupCounts[g.ID]; c != nil {
			counts = *c
		}
		out.Groups = append(out.Groups, AccountStatusGroupSummary{
			GroupID: g.ID, GroupName: g.Name, Platform: g.Platform, GroupStatus: g.Status,
			AccountStateCounts: counts,
			Shortage:           shortage(g.ID, counts),
		})
	}
	return out, nil
}

func (s *AccountStatusSummaryService) listGroups(ctx context.Context, q AccountStatusSummaryQuery, counts map[int64]*AccountStateCounts) ([]AccountStatusGroupRow, error) {
	extra := make([]int64, 0, len(counts))
	for id := range counts {
		extra = append(extra, id)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	groups, err := s.repo.ListGroupsForStatusSummary(ctx, q.Platform, q.GroupID, extra)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups, nil
}

// shortageJudge returns the per-group shortage rule: an enabled
// group_available_ratio / group_available_accounts alert rule scoped to the
// group decides when present; otherwise the group is short when nothing is
// available or available < total × min ratio.
func (s *AccountStatusSummaryService) shortageJudge(ctx context.Context, q AccountStatusSummaryQuery) func(int64, AccountStateCounts) AccountStatusShortage {
	minRatio := s.defaultMinRatio(ctx, q.DefaultMinRatio)
	rulesByGroup := map[int64]*OpsAlertRule{}
	if s.rules != nil {
		// Alert rules are optional input: a read failure falls back to the default ratio.
		if rules, err := s.rules.ListAlertRules(ctx); err == nil {
			for _, rule := range rules {
				if rule == nil || !rule.Enabled {
					continue
				}
				metric := strings.TrimSpace(rule.MetricType)
				if metric != accountStatusGroupAvailableRatio && metric != accountStatusGroupAvailableAccounts {
					continue
				}
				_, groupID, _ := parseOpsAlertRuleScope(rule.Filters)
				if groupID == nil {
					continue
				}
				if existing, ok := rulesByGroup[*groupID]; !ok || rule.ID < existing.ID {
					rulesByGroup[*groupID] = rule
				}
			}
		}
	}
	return func(groupID int64, c AccountStateCounts) AccountStatusShortage {
		if rule, ok := rulesByGroup[groupID]; ok {
			current := float64(c.Available)
			if rule.MetricType == accountStatusGroupAvailableRatio {
				current = 0
				if c.Total > 0 {
					current = float64(c.Available) / float64(c.Total) * 100
				}
			}
			id, threshold := rule.ID, rule.Threshold
			return AccountStatusShortage{
				IsShort: compareMetric(current, rule.Operator, rule.Threshold), Source: accountStatusShortageSourceRule,
				RuleID: &id, MetricType: rule.MetricType, Operator: rule.Operator, Threshold: &threshold, Current: &current,
			}
		}
		ratio := minRatio
		var current float64
		if c.Total > 0 {
			current = float64(c.Available) / float64(c.Total)
		}
		return AccountStatusShortage{
			IsShort:   c.Available == 0 || float64(c.Available) < float64(c.Total)*ratio,
			Source:    accountStatusShortageSourceDefault,
			Threshold: &ratio,
			Current:   &current,
		}
	}
}

func (s *AccountStatusSummaryService) defaultMinRatio(ctx context.Context, override *float64) float64 {
	if override != nil {
		return *override
	}
	if s.settings == nil {
		return GroupShortageMinRatioDefault
	}
	raw, err := s.settings.GetValue(ctx, SettingKeyGroupShortageMinRatio)
	if err != nil {
		return GroupShortageMinRatioDefault
	}
	ratio, err := ParseGroupShortageMinRatio(raw)
	if err != nil {
		return GroupShortageMinRatioDefault
	}
	return ratio
}

// ParseGroupShortageMinRatio validates a ratio in [0, 1].
func ParseGroupShortageMinRatio(raw string) (float64, error) {
	ratio, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return 0, errors.New("group shortage min ratio must be a number between 0 and 1")
	}
	return ratio, nil
}

func accountStatusItem(row *AccountStatusRow, state string, stopped bool, now time.Time) AccountStatusItem {
	groupIDs := row.GroupIDs
	if groupIDs == nil {
		groupIDs = []int64{}
	}
	return AccountStatusItem{
		ID: row.ID, Name: row.Name, Platform: row.Platform, GroupIDs: groupIDs,
		State: state, Stopped: stopped, Status: row.Status, Schedulable: row.Schedulable,
		ErrorMessage: row.ErrorMessage, RateLimitResetAt: row.RateLimitResetAt, OverloadUntil: row.OverloadUntil,
		TempUnschedulableUntil: row.TempUnschedulableUntil, TempUnschedulableReason: row.TempUnschedulableReason,
		ExpiresAt: row.ExpiresAt, ModelRateLimits: activeModelRateLimits(row.ModelRateLimits, now),
	}
}

// activeModelRateLimits lists model scopes whose rate limit has not reset yet.
func activeModelRateLimits(raw map[string]any, now time.Time) []AccountStatusModelRateLimit {
	out := []AccountStatusModelRateLimit{}
	for model, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		resetRaw, _ := entry["rate_limit_reset_at"].(string)
		resetAt, err := time.Parse(time.RFC3339, resetRaw)
		if err != nil || !resetAt.After(now) {
			continue
		}
		out = append(out, AccountStatusModelRateLimit{Model: model, ResetAt: resetAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
