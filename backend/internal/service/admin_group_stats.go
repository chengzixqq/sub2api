package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"golang.org/x/sync/errgroup"
)

// GroupStats are the live counts behind GET /admin/groups/:id/stats.
type GroupStats struct {
	TotalAPIKeys        int64     `json:"total_api_keys"`
	ActiveAPIKeys       int64     `json:"active_api_keys"`
	TotalRequests       int64     `json:"total_requests"`
	TotalCost           float64   `json:"total_cost"`
	SubscriptionsTotal  int64     `json:"subscriptions_total"`
	SubscriptionsActive int64     `json:"subscriptions_active"`
	GeneratedAt         time.Time `json:"generated_at"`
}

type groupAPIKeyCounter interface {
	CountActiveByGroupID(ctx context.Context, groupID int64) (int64, error)
}

type groupSubscriptionCounter interface {
	CountByGroupID(ctx context.Context, groupID int64) (int64, error)
	CountActiveByGroupID(ctx context.Context, groupID int64) (int64, error)
}

// GetGroupStats counts a group's keys (excluding deleted ones; active ones
// separately) and subscriptions (all; active = status active and not
// expired). Key and subscription counts are end-user data, so vendors are
// refused. The handler fills the all-time cost from the group usage rollup.
func (s *adminServiceImpl) GetGroupStats(ctx context.Context, groupID int64) (*GroupStats, error) {
	if ScopeFromContextOrDeny(ctx).IsVendor() {
		return nil, domain.ErrWorkspaceScopeViolation
	}
	if _, err := s.groupRepo.GetByIDLite(ctx, groupID); err != nil {
		return nil, err
	}
	out := &GroupStats{GeneratedAt: time.Now().UTC()}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		out.TotalAPIKeys, err = s.apiKeyRepo.CountByGroupID(gctx, groupID)
		return err
	})
	if counter, ok := s.apiKeyRepo.(groupAPIKeyCounter); ok {
		g.Go(func() (err error) {
			out.ActiveAPIKeys, err = counter.CountActiveByGroupID(gctx, groupID)
			return err
		})
	}
	if counter, ok := s.userSubRepo.(groupSubscriptionCounter); ok {
		g.Go(func() (err error) {
			out.SubscriptionsTotal, err = counter.CountByGroupID(gctx, groupID)
			return err
		})
		g.Go(func() (err error) {
			out.SubscriptionsActive, err = counter.CountActiveByGroupID(gctx, groupID)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
