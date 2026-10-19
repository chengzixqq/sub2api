//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// TestGroupStatsModelSourceMatchesUsageStats checks that dashboard/groups
// matches the model filter by model_source, the same way usage/stats does.
func (s *UsageLogRepoSuite) TestGroupStatsModelSourceMatchesUsageStats() {
	suffix := uuid.NewString()
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "group-model-source-" + suffix + "@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-group-model-source-" + suffix, Name: "k"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "group-model-source-" + suffix})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "group-model-source-" + suffix})
	groupID := group.ID
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	upstream := "claude-opus-4-20250514"
	for i, requested := range []string{"claude-opus-4", "opus-alias", "claude-sonnet-4"} {
		model := upstream
		if requested == "claude-sonnet-4" {
			model = "claude-sonnet-4-20250514"
		}
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, GroupID: &groupID, RequestID: uuid.NewString(),
			Model: requested, RequestedModel: requested, UpstreamModel: &model,
			TotalCost: 1, ActualCost: 1, CreatedAt: start.Add(time.Duration(i) * time.Hour),
		})
		s.Require().NoError(err)
	}

	requestsFor := func(model, source string) int64 {
		filters := usagestats.UsageLogFilters{GroupID: group.ID, Model: model, ModelFilterSource: source}
		groups, err := s.repo.GetGroupStatsWithUsageFilters(s.ctx, start, end, filters)
		s.Require().NoError(err)
		var requests int64
		for _, row := range groups {
			s.Require().Equal(group.ID, row.GroupID)
			requests += row.Requests
		}
		filters.StartTime, filters.EndTime = &start, &end
		stats, err := s.repo.GetStatsWithFilters(s.ctx, filters)
		s.Require().NoError(err)
		s.Require().Equal(stats.TotalRequests, requests, "dashboard/groups matches usage/stats for %s=%s", source, model)
		return requests
	}

	s.Require().Equal(int64(2), requestsFor(upstream, usagestats.ModelSourceUpstream))
	s.Require().Zero(requestsFor(upstream, usagestats.ModelSourceRequested), "the upstream id is not a requested model")
	s.Require().Equal(int64(1), requestsFor("opus-alias -> "+upstream, usagestats.ModelSourceMapping))
	s.Require().Equal(int64(1), requestsFor("claude-opus-4", usagestats.ModelSourceRequested))
	s.Require().Zero(requestsFor("claude-opus-4", usagestats.ModelSourceUpstream), "the requested alias is not an upstream model")
}
