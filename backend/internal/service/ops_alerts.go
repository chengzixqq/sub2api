package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *OpsService) ListAlertRules(ctx context.Context) ([]*OpsAlertRule, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return []*OpsAlertRule{}, nil
	}
	return s.opsRepo.ListAlertRules(ctx)
}

func (s *OpsService) CreateAlertRule(ctx context.Context, rule *OpsAlertRule) (*OpsAlertRule, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if rule == nil {
		return nil, infraerrors.BadRequest("INVALID_RULE", "invalid rule")
	}

	created, err := s.opsRepo.CreateAlertRule(ctx, rule)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *OpsService) UpdateAlertRule(ctx context.Context, rule *OpsAlertRule) (*OpsAlertRule, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if rule == nil || rule.ID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE", "invalid rule")
	}

	updated, err := s.opsRepo.UpdateAlertRule(ctx, rule)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.NotFound("OPS_ALERT_RULE_NOT_FOUND", "alert rule not found")
		}
		return nil, err
	}
	return updated, nil
}

func (s *OpsService) DeleteAlertRule(ctx context.Context, id int64) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if id <= 0 {
		return infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	if err := s.opsRepo.DeleteAlertRule(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return infraerrors.NotFound("OPS_ALERT_RULE_NOT_FOUND", "alert rule not found")
		}
		return err
	}
	return nil
}

func (s *OpsService) ListAlertEvents(ctx context.Context, filter *OpsAlertEventFilter) ([]*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return []*OpsAlertEvent{}, nil
	}
	return s.opsRepo.ListAlertEvents(ctx, filter)
}

func (s *OpsService) GetAlertEventByID(ctx context.Context, eventID int64) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if eventID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_EVENT_ID", "invalid event id")
	}
	ev, err := s.opsRepo.GetAlertEventByID(ctx, eventID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.NotFound("OPS_ALERT_EVENT_NOT_FOUND", "alert event not found")
		}
		return nil, err
	}
	if ev == nil {
		return nil, infraerrors.NotFound("OPS_ALERT_EVENT_NOT_FOUND", "alert event not found")
	}
	return ev, nil
}

func (s *OpsService) GetActiveAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if ruleID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	return s.opsRepo.GetActiveAlertEvent(ctx, ruleID)
}

func (s *OpsService) CreateAlertSilence(ctx context.Context, input *OpsAlertSilence) (*OpsAlertSilence, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if input == nil {
		return nil, infraerrors.BadRequest("INVALID_SILENCE", "invalid silence")
	}
	if input.RuleID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	// An empty platform means the rule's scope has no platform (group-only, server and global rules).
	input.Platform = strings.TrimSpace(input.Platform)
	if input.Until.IsZero() {
		return nil, infraerrors.BadRequest("INVALID_UNTIL", "invalid until")
	}

	created, err := s.opsRepo.CreateAlertSilence(ctx, input)
	if err != nil {
		return nil, err
	}
	return created, nil
}

const (
	opsAlertSilenceListDefault = 100
	opsAlertSilenceListMax     = 500
	// opsAlertSilenceMaxDuration bounds how far a silence can be moved by PUT; the App offers 1h / 24h / 7d.
	opsAlertSilenceMaxDuration = 30 * 24 * time.Hour
)

var errOpsAlertSilenceNotFound = infraerrors.NotFound("OPS_ALERT_SILENCE_NOT_FOUND", "alert silence not found")

func (s *OpsService) ListAlertSilences(ctx context.Context, filter *OpsAlertSilenceFilter) ([]*OpsAlertSilence, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return []*OpsAlertSilence{}, nil
	}
	f := OpsAlertSilenceFilter{}
	if filter != nil {
		f = *filter
	}
	if f.RuleID != nil && *f.RuleID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	switch {
	case f.Limit <= 0:
		f.Limit = opsAlertSilenceListDefault
	case f.Limit > opsAlertSilenceListMax:
		f.Limit = opsAlertSilenceListMax
	}
	if f.Now.IsZero() {
		f.Now = time.Now().UTC()
	}
	return s.opsRepo.ListAlertSilences(ctx, &f)
}

// UpdateAlertSilenceUntil moves a silence's end (shorten or extend). Other silences of the same scope are capped at the
// new time, so “silenced until” always reads from the one row the App edits.
func (s *OpsService) UpdateAlertSilenceUntil(ctx context.Context, id int64, until time.Time, reason *string) (*OpsAlertSilence, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if id <= 0 {
		return nil, infraerrors.BadRequest("INVALID_SILENCE_ID", "invalid silence id")
	}
	now := time.Now().UTC()
	if !until.After(now) || until.Sub(now) > opsAlertSilenceMaxDuration {
		return nil, infraerrors.BadRequest("INVALID_UNTIL", "until must be in the future and within 30 days")
	}
	if reason != nil {
		trimmed := strings.TrimSpace(*reason)
		reason = &trimmed
	}
	updated, err := s.opsRepo.UpdateAlertSilenceUntil(ctx, id, until.UTC(), reason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errOpsAlertSilenceNotFound
		}
		return nil, err
	}
	return updated, nil
}

// EndAlertSilence revokes a silence: every active silence in its scope ends now, so the next evaluation can fire.
func (s *OpsService) EndAlertSilence(ctx context.Context, id int64) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if id <= 0 {
		return infraerrors.BadRequest("INVALID_SILENCE_ID", "invalid silence id")
	}
	if err := s.opsRepo.EndAlertSilence(ctx, id, time.Now().UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errOpsAlertSilenceNotFound
		}
		return err
	}
	return nil
}

func (s *OpsService) IsAlertSilenced(ctx context.Context, ruleID int64, platform string, groupID *int64, region *string, now time.Time) (bool, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return false, err
	}
	if s.opsRepo == nil {
		return false, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if ruleID <= 0 {
		return false, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	return s.opsRepo.IsAlertSilenced(ctx, ruleID, strings.TrimSpace(platform), groupID, region, now)
}

func (s *OpsService) GetLatestAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if ruleID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	return s.opsRepo.GetLatestAlertEvent(ctx, ruleID)
}

func (s *OpsService) CreateAlertEvent(ctx context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if event == nil {
		return nil, infraerrors.BadRequest("INVALID_EVENT", "invalid event")
	}

	created, err := s.opsRepo.CreateAlertEvent(ctx, event)
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateAlertEventStatus resolves an event. resolvedBy (the acting admin) is recorded only for manual_resolved.
func (s *OpsService) UpdateAlertEventStatus(ctx context.Context, eventID int64, status string, resolvedAt *time.Time, resolvedBy *int64) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if eventID <= 0 {
		return infraerrors.BadRequest("INVALID_EVENT_ID", "invalid event id")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return infraerrors.BadRequest("INVALID_STATUS", "invalid status")
	}
	if status != OpsAlertStatusResolved && status != OpsAlertStatusManualResolved {
		return infraerrors.BadRequest("INVALID_STATUS", "invalid status")
	}
	if status != OpsAlertStatusManualResolved || (resolvedBy != nil && *resolvedBy <= 0) {
		resolvedBy = nil
	}
	return s.opsRepo.UpdateAlertEventStatus(ctx, eventID, status, resolvedAt, resolvedBy)
}

func (s *OpsService) UpdateAlertEventEmailSent(ctx context.Context, eventID int64, emailSent bool) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if eventID <= 0 {
		return infraerrors.BadRequest("INVALID_EVENT_ID", "invalid event id")
	}
	return s.opsRepo.UpdateAlertEventEmailSent(ctx, eventID, emailSent)
}
