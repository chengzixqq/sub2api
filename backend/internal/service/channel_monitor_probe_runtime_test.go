//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type monitorProbeGroupStub struct {
	GroupRepository
	group *Group
}

func (s monitorProbeGroupStub) GetByID(context.Context, int64) (*Group, error) { return s.group, nil }

type monitorProbeRepoStub struct {
	ChannelMonitorProbeRepository
	target     ChannelMonitorProbeTarget
	completed  *ChannelMonitorProbeRun
	saved      bool
	quotaReads int
}

func (s *monitorProbeRepoStub) QuotaAccounts(context.Context) ([]int64, error) {
	s.quotaReads++
	return nil, nil
}

func TestChannelMonitorProbeService_QuotaIndependentOfProbeAndCollection(t *testing.T) {
	repo := &monitorProbeRepoStub{}
	svc := NewChannelMonitorProbeService(repo, nil, nil, &ChannelMonitorQuotaFetcher{})
	svc.SetEnabledReader(func(context.Context) bool { return false })
	svc.SetQuotaEnabledReader(func(context.Context) bool { return true })
	svc.runQuotaCycle(context.Background())
	require.Equal(t, 1, repo.quotaReads)
	svc.SetQuotaEnabledReader(func(context.Context) bool { return false })
	svc.runQuotaCycle(context.Background())
	require.Equal(t, 1, repo.quotaReads)
}

func (s *monitorProbeRepoStub) GetTarget(context.Context, int64) (*ChannelMonitorProbeTarget, error) {
	return &s.target, nil
}
func (s *monitorProbeRepoStub) ListTargets(context.Context) ([]ChannelMonitorProbeTarget, error) {
	return nil, nil
}
func (s *monitorProbeRepoStub) SaveTarget(context.Context, *ChannelMonitorProbeTarget) error {
	s.saved = true
	return nil
}
func (s *monitorProbeRepoStub) Reserve(context.Context, int64, string, bool, time.Time) (*ChannelMonitorProbeRun, bool, error) {
	return &ChannelMonitorProbeRun{TargetID: s.target.ID, GroupID: s.target.GroupID, Model: s.target.Model, Protocol: s.target.Protocol}, true, nil
}
func (s *monitorProbeRepoStub) Complete(_ context.Context, run *ChannelMonitorProbeRun) error {
	s.completed = run
	return nil
}

func TestChannelMonitorProbeService_UnsupportedDoesNotBecomeHealthFailure(t *testing.T) {
	repo := &monitorProbeRepoStub{target: ChannelMonitorProbeTarget{ID: 1, GroupID: 3, Model: "gpt-test", Protocol: "openai_chat", Enabled: true}}
	svc := NewChannelMonitorProbeService(repo, monitorProbeGroupStub{group: &Group{ID: 3, Status: StatusActive}}, nil, nil)
	svc.SetEnabledReader(func(context.Context) bool { return true })
	svc.SetTargetValidator(func(context.Context, *Group, string) error { return nil })
	run, err := svc.Probe(WithScope(context.Background(), AdminScope()), 1, "test-operation-123456")
	require.NoError(t, err)
	require.Nil(t, run.Success)
	require.Equal(t, "executor_unavailable", run.ErrorClass)
	require.Nil(t, repo.completed.Success)
}
func TestChannelMonitorProbeService_TargetMustMatchAllowlistAndCatalogue(t *testing.T) {
	repo := &monitorProbeRepoStub{}
	group := &Group{ID: 3, Status: StatusActive, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"gpt-allowed"}}}
	svc := NewChannelMonitorProbeService(repo, monitorProbeGroupStub{group: group}, nil, nil)
	called := false
	svc.SetTargetValidator(func(context.Context, *Group, string) error { called = true; return ErrChannelMonitorProbeInvalid })
	ctx := WithScope(context.Background(), AdminScope())
	target := &ChannelMonitorProbeTarget{GroupID: 3, Model: "gpt-denied", Protocol: "openai_chat"}
	require.ErrorIs(t, svc.SaveTarget(ctx, target), ErrChannelMonitorProbeInvalid)
	require.False(t, called)
	require.False(t, repo.saved)
	target.Model = "gpt-allowed"
	require.ErrorIs(t, svc.SaveTarget(ctx, target), ErrChannelMonitorProbeInvalid)
	require.True(t, called)
	require.False(t, repo.saved)
	svc.SetTargetValidator(func(context.Context, *Group, string) error { return nil })
	require.NoError(t, svc.SaveTarget(ctx, target))
	require.True(t, repo.saved)
}
func TestChannelMonitorProbeService_DisableAvailableWhenCatalogueUnavailable(t *testing.T) {
	repo := &monitorProbeRepoStub{target: ChannelMonitorProbeTarget{ID: 1, GroupID: 3, Model: "gpt-test", Protocol: "openai_chat", Enabled: true, Version: 2}}
	svc := NewChannelMonitorProbeService(repo, nil, nil, nil)
	target := repo.target
	target.Enabled = false
	require.NoError(t, svc.SaveTarget(WithScope(context.Background(), AdminScope()), &target))
	require.True(t, repo.saved)
}
