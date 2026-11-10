//go:build unit

package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProbeCoalescer_AdvisorUsageSnapshotIsolatedForFollowers(t *testing.T) {
	p := NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingActive, Window: time.Minute, LeaderTimeout: time.Second, AttemptBudget: 2})
	leader := p.Begin(context.Background(), testProbeCandidate(), "leader")
	follower := p.Begin(context.Background(), testProbeCandidate(), "follower")
	usage := service.ClaudeUsage{InputTokens: 10, OutputTokens: 2, IterationUsage: &service.ClaudeIterationUsage{Present: true, Terminal: true, AdvisorSeen: true,
		Iterations: []service.ClaudeUsageIteration{{Type: "message", Usage: service.UsageTokens{InputTokens: 10, OutputTokens: 2}}, {Type: "advisor_message", Model: "advisor-model", Usage: service.UsageTokens{InputTokens: 30, OutputTokens: 4}}}}}
	leader.ObserveUsage(usage)
	usage.IterationUsage.Iterations[1].Usage.InputTokens = 999
	leader.MarkBillingReady(nil)
	leader.Finish(true, "leader", &service.Account{ID: 1})
	resolution, err := follower.Resolve(context.Background())
	require.NoError(t, err)
	require.True(t, resolution.Synthetic)
	require.NotNil(t, resolution.Candidate.UsageSnapshot)
	require.Equal(t, 30, resolution.Candidate.UsageSnapshot.IterationUsage.Iterations[1].Usage.InputTokens)
	resolution.Candidate.UsageSnapshot.IterationUsage.Iterations[1].Usage.InputTokens = 777
	next := p.Begin(context.Background(), testProbeCandidate(), "next")
	nextResolution, err := next.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, 30, nextResolution.Candidate.UsageSnapshot.IterationUsage.Iterations[1].Usage.InputTokens)
}
