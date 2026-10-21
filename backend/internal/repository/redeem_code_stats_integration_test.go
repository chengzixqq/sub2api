//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// redeemStatsFixture is one batch of codes covering every status the list shows, under a unique batch id and code
// prefix so the shared test database cannot leak into the counts.
type redeemStatsFixture struct {
	batchID uuid.UUID
	prefix  string
	emailA  string // redeemed the balance code worth 5
	emailB  string // redeemed the balance code worth 7 and the concurrency code
}

func (s *RedeemCodeRepoSuite) createRedeemStatsFixture() redeemStatsFixture {
	f := redeemStatsFixture{
		batchID: uuid.New(),
		prefix:  "ST" + uuid.NewString()[:8],
		emailA:  "stats-a-" + uuid.NewString() + "@example.com",
		emailB:  "stats-b-" + uuid.NewString() + "@example.com",
	}
	userA := s.createUser(f.emailA)
	userB := s.createUser(f.emailB)
	now := time.Now()
	future, past := now.Add(24*time.Hour), now.Add(-time.Hour)
	seq := 0
	add := func(codeType, status string, value float64, expiresAt *time.Time, usedBy *int64) {
		seq++
		code := &service.RedeemCode{
			Code: f.prefix + "-" + string(rune('a'+seq)), Type: codeType, Value: value, Status: status,
			ExpiresAt: expiresAt, UsedBy: usedBy, BatchID: &f.batchID,
		}
		if usedBy != nil {
			code.UsedAt = &now
		}
		s.Require().NoError(s.repo.Create(s.ctx, code))
	}
	add(service.RedeemTypeBalance, service.StatusUnused, 10, nil, nil)
	add(service.RedeemTypeBalance, service.StatusUnused, 10, &future, nil)
	add(service.RedeemTypeBalance, service.StatusUnused, 10, &past, nil) // lapsed: the list shows it as expired
	add(service.RedeemTypeBalance, service.StatusExpired, 10, nil, nil)
	add(service.RedeemTypeBalance, service.StatusUsed, 5, nil, &userA.ID)
	add(service.RedeemTypeBalance, service.StatusUsed, 7, nil, &userB.ID)
	add(service.RedeemTypeConcurrency, service.StatusUsed, 3, nil, &userB.ID)
	add(service.AdjustmentTypeAdminBalance, service.StatusUsed, 100, nil, &userA.ID) // not a distributed balance code
	add(service.RedeemTypeSubscription, service.StatusDisabled, 30, &past, nil)      // disabled regardless of expiry
	add(service.RedeemTypeInvitation, service.StatusUnused, 0, nil, nil)
	return f
}

func (s *RedeemCodeRepoSuite) redeemListTotal(filter service.RedeemCodeListFilter) int64 {
	_, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 1}, filter)
	s.Require().NoError(err)
	return page.Total
}

// requireStatsMatchList checks the acceptance rule: every status count equals the list total for that status, the
// four add up to the unfiltered total, and so do the per-type counts.
func (s *RedeemCodeRepoSuite) requireStatsMatchList(filter service.RedeemCodeListFilter) *service.RedeemCodeStats {
	stats, err := s.repo.StatsWithFilters(s.ctx, filter)
	s.Require().NoError(err)
	s.Require().NotNil(stats)
	for status, got := range map[string]int64{
		service.StatusUnused:   stats.Unused,
		service.StatusUsed:     stats.Used,
		service.StatusExpired:  stats.Expired,
		service.StatusDisabled: stats.Disabled,
	} {
		withStatus := filter
		withStatus.Status = status
		s.Require().Equal(s.redeemListTotal(withStatus), got, "status %s", status)
	}
	all := filter
	all.Status = ""
	s.Require().Equal(s.redeemListTotal(all), stats.Total)
	s.Require().Equal(stats.Total, stats.Unused+stats.Used+stats.Expired+stats.Disabled)
	var byType int64
	for _, n := range stats.ByType {
		byType += n
	}
	s.Require().Equal(stats.Total, byType)
	return stats
}

func (s *RedeemCodeRepoSuite) TestStatsWithFilters_MatchesListPerStatus() {
	f := s.createRedeemStatsFixture()

	stats := s.requireStatsMatchList(service.RedeemCodeListFilter{BatchID: &f.batchID})
	s.Require().Equal(int64(10), stats.Total)
	s.Require().Equal(int64(3), stats.Unused, "no expiry, future expiry and the invitation")
	s.Require().Equal(int64(4), stats.Used)
	s.Require().Equal(int64(2), stats.Expired, "status expired plus the lapsed unused code")
	s.Require().Equal(int64(1), stats.Disabled)
	s.Require().InDelta(12, stats.ValueDistributed, 1e-9, "used balance codes only, not admin_balance")
	s.Require().Equal(map[string]int64{
		service.RedeemTypeBalance:          6,
		service.RedeemTypeConcurrency:      1,
		service.RedeemTypeSubscription:     1,
		service.RedeemTypeInvitation:       1,
		service.AdjustmentTypeAdminBalance: 1,
	}, stats.ByType)
}

func (s *RedeemCodeRepoSuite) TestStatsWithFilters_SearchAndTypesMatchList() {
	f := s.createRedeemStatsFixture()

	stats := s.requireStatsMatchList(service.RedeemCodeListFilter{Search: f.prefix})
	s.Require().Equal(int64(10), stats.Total, "the code prefix matches the whole fixture")

	stats = s.requireStatsMatchList(service.RedeemCodeListFilter{Search: f.prefix + "-b"})
	s.Require().Equal(int64(1), stats.Total, "a code fragment matches one code")
	s.Require().Equal(int64(1), stats.Unused)

	stats = s.requireStatsMatchList(service.RedeemCodeListFilter{Search: f.emailA[:20]})
	s.Require().Equal(int64(2), stats.Total, "an email fragment matches the codes that user redeemed")
	s.Require().Equal(int64(2), stats.Used)
	s.Require().InDelta(5, stats.ValueDistributed, 1e-9)

	stats = s.requireStatsMatchList(service.RedeemCodeListFilter{Search: f.emailB})
	s.Require().Equal(int64(2), stats.Total)
	s.Require().InDelta(7, stats.ValueDistributed, 1e-9)

	stats = s.requireStatsMatchList(service.RedeemCodeListFilter{BatchID: &f.batchID, Types: []string{service.RedeemTypeBalance, service.RedeemTypeConcurrency}})
	s.Require().Equal(int64(7), stats.Total)
	s.Require().Equal(map[string]int64{service.RedeemTypeBalance: 6, service.RedeemTypeConcurrency: 1}, stats.ByType)

	stats = s.requireStatsMatchList(service.RedeemCodeListFilter{BatchID: &f.batchID, ExcludeTypes: []string{service.RedeemTypeBalance}})
	s.Require().Equal(int64(4), stats.Total)
	s.Require().Zero(stats.ValueDistributed)

	stats = s.requireStatsMatchList(service.RedeemCodeListFilter{BatchID: &f.batchID, Type: service.RedeemTypeBalance})
	s.Require().Equal(int64(6), stats.Total)
	s.Require().Equal(int64(2), stats.Unused)
	s.Require().Equal(int64(2), stats.Expired)
	s.Require().Equal(int64(2), stats.Used)
}

func (s *RedeemCodeRepoSuite) TestStatsWithFilters_IgnoresStatusFilter() {
	f := s.createRedeemStatsFixture()

	all, err := s.repo.StatsWithFilters(s.ctx, service.RedeemCodeListFilter{BatchID: &f.batchID})
	s.Require().NoError(err)
	withStatus, err := s.repo.StatsWithFilters(s.ctx, service.RedeemCodeListFilter{BatchID: &f.batchID, Status: service.StatusUsed})
	s.Require().NoError(err)
	s.Require().Equal(all, withStatus)
}

func (s *RedeemCodeRepoSuite) TestStatsWithFilters_EmptyResult() {
	batchID := uuid.New()

	stats, err := s.repo.StatsWithFilters(s.ctx, service.RedeemCodeListFilter{BatchID: &batchID})
	s.Require().NoError(err)
	s.Require().Zero(stats.Total)
	s.Require().Zero(stats.Unused + stats.Used + stats.Expired + stats.Disabled)
	s.Require().Zero(stats.ValueDistributed)
	s.Require().Empty(stats.ByType)
}
