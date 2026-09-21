package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type ChannelMonitorProbeService struct {
	repo           ChannelMonitorProbeRepository
	groups         GroupRepository
	executor       *ChannelMonitorProbeExecutor
	quota          *ChannelMonitorQuotaFetcher
	activity       ChannelMonitorProbeActivityReader
	enabled        func(context.Context) bool
	quotaEnabled   func(context.Context) bool
	validateTarget func(context.Context, *Group, string) error
	once           sync.Once
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

func NewChannelMonitorProbeService(repo ChannelMonitorProbeRepository, groups GroupRepository, executor *ChannelMonitorProbeExecutor, quota *ChannelMonitorQuotaFetcher) *ChannelMonitorProbeService {
	return &ChannelMonitorProbeService{repo: repo, groups: groups, executor: executor, quota: quota}
}
func (s *ChannelMonitorProbeService) SetActivityReader(reader ChannelMonitorProbeActivityReader) {
	s.activity = reader
}
func (s *ChannelMonitorProbeService) SetEnabledReader(reader func(context.Context) bool) {
	s.enabled = reader
}
func (s *ChannelMonitorProbeService) SetQuotaEnabledReader(reader func(context.Context) bool) {
	s.quotaEnabled = reader
}
func (s *ChannelMonitorProbeService) SetTargetValidator(validator func(context.Context, *Group, string) error) {
	s.validateTarget = validator
}
func (s *ChannelMonitorProbeService) ListTargets(ctx context.Context) ([]ChannelMonitorProbeTarget, error) {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	return s.repo.ListTargets(ctx)
}
func (s *ChannelMonitorProbeService) SaveTarget(ctx context.Context, t *ChannelMonitorProbeTarget) error {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if t.ID != 0 && t.Version < 1 {
		return ErrChannelMonitorProbeConflict
	}
	if t.ID != 0 && !t.Enabled {
		previous, err := s.repo.GetTarget(ctx, t.ID)
		if err != nil {
			return err
		}
		if previous.GroupID == t.GroupID && previous.Model == t.Model && previous.Protocol == t.Protocol {
			return s.repo.SaveTarget(ctx, t)
		}
	}
	if s.groups == nil {
		return ErrChannelMonitorProbeInvalid
	}
	g, err := s.groups.GetByID(ctx, t.GroupID)
	if err != nil {
		return err
	}
	if g == nil || g.Status != StatusActive || g.Platform == PlatformComposite {
		return ErrChannelMonitorProbeInvalid
	}
	if !g.ModelAllowlist.Allows(t.Model) {
		return ErrChannelMonitorProbeInvalid
	}
	if s.validateTarget == nil {
		return ErrChannelMonitorProbeInvalid
	}
	if err := s.validateTarget(ctx, g, t.Model); err != nil {
		return err
	}
	if t.ID == 0 {
		items, err := s.repo.ListTargets(ctx)
		if err != nil {
			return err
		}
		if len(items) >= 1000 {
			return ErrChannelMonitorProbeInvalid
		}
	}
	return s.repo.SaveTarget(ctx, t)
}
func (s *ChannelMonitorProbeService) Budget(ctx context.Context) (*ChannelMonitorProbeBudget, error) {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	return s.repo.Budget(ctx, time.Now().UTC())
}
func (s *ChannelMonitorProbeService) Probe(ctx context.Context, id int64, key string) (*ChannelMonitorProbeRun, error) {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	if !validMonitorProbeKey(key) {
		return nil, ErrChannelMonitorProbeInvalid
	}
	if s.enabled == nil || !s.enabled(ctx) {
		return nil, ErrChannelMonitorProbeDisabled
	}
	return s.run(ctx, id, key, true)
}
func (s *ChannelMonitorProbeService) run(ctx context.Context, id int64, key string, manual bool) (*ChannelMonitorProbeRun, error) {
	target, err := s.repo.GetTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.groups == nil || s.validateTarget == nil {
		return nil, ErrChannelMonitorProbeInvalid
	}
	g, err := s.groups.GetByID(ctx, target.GroupID)
	if err != nil {
		return nil, err
	}
	if g == nil || !g.IsActive() || !g.ModelAllowlist.Allows(target.Model) {
		return nil, ErrChannelMonitorProbeInvalid
	}
	if err := s.validateTarget(ctx, g, target.Model); err != nil {
		return nil, err
	}
	run, reserved, err := s.repo.Reserve(ctx, id, key, manual, time.Now().UTC())
	if err != nil || !reserved {
		return run, err
	}
	// A disconnected management client cannot cancel persistence of an already
	// dispatched generation. The execution itself remains bounded to 20 seconds.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ChannelMonitorProbeTimeout)
	defer cancel()
	if run.GroupID != target.GroupID || run.Model != target.Model || run.Protocol != target.Protocol {
		now := time.Now().UTC()
		run.CompletedAt = &now
		run.Status = "error"
		run.ErrorClass = "target_changed"
	} else {
		s.executor.Execute(runCtx, run)
	}
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer writeCancel()
	if err := s.repo.Complete(writeCtx, run); err != nil {
		return nil, err
	}
	return run, nil
}
func (s *ChannelMonitorProbeService) Start() {
	s.once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.wg.Add(2)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.runCycle(ctx)
				}
			}
		}()
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.runQuotaCycle(ctx)
				}
			}
		}()
	})
}
func (s *ChannelMonitorProbeService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *ChannelMonitorProbeService) runQuotaCycle(ctx context.Context) {
	if s.quotaEnabled != nil && s.quotaEnabled(ctx) {
		s.pollQuotas(ctx)
	}
}
func (s *ChannelMonitorProbeService) runCycle(ctx context.Context) {
	if s.enabled == nil || !s.enabled(ctx) {
		return
	}
	if s.activity != nil {
		targets, err := s.repo.ListTargets(ctx)
		if err != nil {
			slog.Warn("channel_monitor_probe: targets unavailable", "error", err)
		} else {
			sort.SliceStable(targets, func(i, j int) bool {
				if targets[i].LastProbeAt == nil {
					return targets[j].LastProbeAt != nil
				}
				return targets[j].LastProbeAt != nil && targets[i].LastProbeAt.Before(*targets[j].LastProbeAt)
			})
			var pending sync.WaitGroup
			slots := make(chan struct{}, 4)
			dispatched := 0
			for _, target := range targets {
				if ctx.Err() != nil {
					break
				}
				if !target.Enabled {
					continue
				}
				now := time.Now().UTC()
				a, err := s.activity.ProbeActivity(ctx, target.GroupID, target.Model, target.Protocol)
				if err != nil || !channelMonitorProbeDue(now, a, target.LastProbeAt) {
					continue
				}
				if dispatched >= 8 {
					break
				}
				dispatched++
				slots <- struct{}{}
				pending.Add(1)
				go func(target ChannelMonitorProbeTarget) {
					defer pending.Done()
					defer func() { <-slots }()
					key := fmt.Sprintf("auto:%d:%d", target.ID, now.Unix()/900)
					if _, err := s.run(ctx, target.ID, key, false); err != nil {
						slog.Warn("channel_monitor_probe: run skipped", "target_id", target.ID, "error", err)
					}
				}(target)
			}
			pending.Wait()
		}
	}
	if maintenance, ok := s.repo.(interface {
		MaintainProbe(context.Context, time.Time) error
	}); ok {
		if err := maintenance.MaintainProbe(ctx, time.Now().UTC()); err != nil {
			slog.Warn("channel_monitor_probe: retention failed", "error", err)
		}
	}
}
func (s *ChannelMonitorProbeService) pollQuotas(ctx context.Context) {
	if s.quota == nil {
		return
	}
	ids, err := s.repo.QuotaAccounts(ctx)
	if err != nil {
		slog.Warn("channel_monitor_probe: quota accounts unavailable", "error", err)
		return
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			defer func() { <-slots }()
			claimed, err := s.repo.ClaimQuota(ctx, id, time.Now().UTC())
			if err != nil || !claimed {
				return
			}
			snapshot := s.quota.FetchReadOnly(ctx, id)
			if err := s.repo.StoreQuota(ctx, id, snapshot); err != nil {
				slog.Warn("channel_monitor_probe: quota snapshot write failed", "account_id", id, "error", err)
			}
		}(id)
	}
	wg.Wait()
}

// Evidence is an internal read surface. Callers must supply their already-authorized group set.
func (s *ChannelMonitorProbeService) Evidence(ctx context.Context, groupIDs []int64, now time.Time) (ChannelMonitorProbeEvidence, error) {
	targets, err := s.repo.ListTargets(ctx)
	if err != nil {
		return ChannelMonitorProbeEvidence{}, err
	}
	allowed := make([]ChannelMonitorProbeTarget, 0, len(targets))
	for _, target := range targets {
		if target.Enabled && observationHasID(groupIDs, target.GroupID) {
			allowed = append(allowed, target)
		}
	}
	runs, err := s.repo.RecentRuns(ctx, groupIDs, now.Add(-20*time.Minute))
	if err != nil {
		return ChannelMonitorProbeEvidence{}, err
	}
	return ChannelMonitorProbeEvidence{Targets: allowed, Runs: runs}, nil
}
func (s *ChannelMonitorProbeService) QuotaSummaries(ctx context.Context, groupIDs []int64, now time.Time) (map[int64]ChannelMonitorQuotaSummary, error) {
	return s.repo.QuotaSummaries(ctx, groupIDs, now)
}
func (s *ChannelMonitorProbeService) ReadQuotas(ctx context.Context, ids []int64) (map[int64]*domain.MonitorQuotaSnapshot, error) {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	return s.repo.ReadQuotas(ctx, ids)
}
