//go:build integration

package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

func (s *UsageLogRepoSuite) TestUserDashboardStats_SingleScanPreservesBillingAndPlatformSemantics() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "dashboard-performance@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-dashboard-performance", Name: "key"})
	openai := mustCreateGroup(s.T(), s.client, &service.Group{Name: "dashboard-openai", Platform: "openai"})
	composite := mustCreateGroup(s.T(), s.client, &service.Group{Name: "dashboard-composite", Platform: "composite"})
	anthropic := mustCreateAccount(s.T(), s.client, &service.Account{Name: "dashboard-anthropic", Platform: "anthropic"})
	gemini := mustCreateAccount(s.T(), s.client, &service.Account{Name: "dashboard-gemini", Platform: "gemini"})
	unknown := mustCreateAccount(s.T(), s.client, &service.Account{Name: "dashboard-unknown"})
	// Ent rejects empty new platform values; use SQL to model a historical row
	// whose platform is unknown, which the dashboard must keep in grand totals.
	_, err := s.repo.sql.ExecContext(s.ctx, "UPDATE accounts SET platform = '' WHERE id = $1", unknown.ID)
	s.Require().NoError(err)
	today := timezone.Today()
	failure, estimated := "failed_upstream", "estimated"
	d100, d300, d500, d700 := 100, 300, 500, 700
	fixtures := []struct {
		account    *service.Account
		group      *int64
		at         time.Time
		provenance *string
		recorded   bool
		actual     float64
		duration   *int
	}{
		{anthropic, &openai.ID, today.Add(-time.Hour), nil, true, 1, &d100},
		{anthropic, &openai.ID, today, &failure, true, 1.5, &d300},
		{anthropic, &openai.ID, today, nil, false, 0, nil},
		{anthropic, &composite.ID, today, &estimated, true, 2, &d500},
		{unknown, nil, today, nil, true, 2.5, nil},
		{gemini, nil, today, nil, false, 3, &d700},
	}
	for i, fixture := range fixtures {
		n := i + 1
		log := &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: fixture.account.ID, GroupID: fixture.group,
			RequestID: uuid.NewString(), Model: "dashboard-performance", CreatedAt: fixture.at,
			InputTokens: n * 10, OutputTokens: n*10 + 10, CacheCreationTokens: n, CacheReadTokens: n + 1,
			TotalCost: float64(n + 1), ActualCost: fixture.actual, DurationMs: fixture.duration,
			BillingProvenance: fixture.provenance, ProviderCostRecorded: fixture.recorded,
		}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err, fmt.Sprintf("fixture %d", i))
		// Create defaults ordinary rows to provider-recorded. Explicitly model
		// persisted follower/import rows whose user charge has no provider cost.
		if !fixture.recorded {
			_, err = s.repo.sql.ExecContext(s.ctx, "UPDATE usage_logs SET provider_cost_recorded = false WHERE id = $1", log.ID)
			s.Require().NoError(err)
		}
	}
	stats, err := s.repo.GetUserDashboardStats(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), stats.TotalAPIKeys)
	s.Require().Equal(int64(1), stats.ActiveAPIKeys)
	s.Require().Equal(int64(6), stats.TotalRequests)
	s.Require().Equal(int64(5), stats.TodayRequests)
	s.Require().Equal(int64(528), stats.TotalTokens)
	s.Require().Equal(int64(495), stats.TodayTokens)
	s.Require().Equal(float64(16), stats.TotalCost)
	s.Require().Equal(float64(14), stats.TodayCost)
	s.Require().Equal(float64(10), stats.TotalActualCost)
	s.Require().Equal(float64(9), stats.TodayActualCost)
	s.Require().Equal(float64(400), stats.AverageDurationMs)
	s.Require().Equal([]PlatformDashboardStats{
		{Platform: "gemini", TotalRequests: 1, TotalTokens: 143, TotalActualCost: 3, TodayRequests: 1, TodayTokens: 143, TodayActualCost: 3},
		{Platform: "openai", TotalRequests: 1, TotalTokens: 165, TotalActualCost: 2.5, TodayRequests: 0, TodayTokens: 132, TodayActualCost: 1.5},
		{Platform: "anthropic", TotalRequests: 1, TotalTokens: 99, TotalActualCost: 2, TodayRequests: 1, TodayTokens: 99, TodayActualCost: 2},
	}, stats.ByPlatform)
}

func (s *UsageLogRepoSuite) TestUserDashboardStats_EmptyHistory() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "dashboard-empty@test.com"})
	stats, err := s.repo.GetUserDashboardStats(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Zero(stats.TotalRequests)
	s.Require().Zero(stats.TodayRequests)
	s.Require().Zero(stats.TotalActualCost)
	s.Require().Zero(stats.AverageDurationMs)
	s.Require().Nil(stats.ByPlatform)
}

// Keep the file's integration build tag discoverable by ordinary test tooling.
func TestUserDashboardPerformanceIntegrationSmoke(_ *testing.T) {}
