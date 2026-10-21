package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type readStatsUserRepoStub struct {
	UserRepository
	users   []User
	calls   int
	params  pagination.PaginationParams
	filters UserListFilters
}

func (s *readStatsUserRepoStub) ListWithFilters(_ context.Context, params pagination.PaginationParams, filters UserListFilters) ([]User, *pagination.PaginationResult, error) {
	s.calls++
	s.params = params
	s.filters = filters
	return s.users, &pagination.PaginationResult{Total: int64(len(s.users)), Page: params.Page, PageSize: params.Limit(), Pages: 1}, nil
}

type readStatsUserSubRepoStub struct {
	UserSubscriptionRepository
	t *testing.T
}

func (s *readStatsUserSubRepoStub) ListActiveByUserID(context.Context, int64) ([]UserSubscription, error) {
	s.t.Fatalf("read status must not query subscriptions per user")
	return nil, nil
}

type readStatsReadRepoStub struct {
	AnnouncementReadRepository
	readMap        map[int64]time.Time
	counts         map[int64]AnnouncementReadCounts
	summarized     []int64
	summarizeTgt   []AnnouncementTargeting
	listCalls      int
	listID         int64
	listTargeting  AnnouncementTargeting
	listParams     pagination.PaginationParams
	listFilter     AnnouncementReadStatusFilter
	listRows       []AnnouncementUserReadStatus
	readMapQueried bool
}

func (s *readStatsReadRepoStub) GetReadMapByUsers(context.Context, int64, []int64) (map[int64]time.Time, error) {
	s.readMapQueried = true
	return s.readMap, nil
}

func (s *readStatsReadRepoStub) SummarizeReads(_ context.Context, id int64, targeting AnnouncementTargeting) (AnnouncementReadCounts, error) {
	s.summarized = append(s.summarized, id)
	s.summarizeTgt = append(s.summarizeTgt, targeting)
	return s.counts[id], nil
}

func (s *readStatsReadRepoStub) ListUserReadStatus(_ context.Context, id int64, targeting AnnouncementTargeting, params pagination.PaginationParams, filter AnnouncementReadStatusFilter) ([]AnnouncementUserReadStatus, *pagination.PaginationResult, error) {
	s.listCalls++
	s.listID = id
	s.listTargeting = targeting
	s.listParams = params
	s.listFilter = filter
	return s.listRows, &pagination.PaginationResult{Total: int64(len(s.listRows)), Page: params.Page, PageSize: params.Limit(), Pages: 1}, nil
}

type readStatsAnnRepoStub struct {
	AnnouncementRepository
	items map[int64]*Announcement
	gets  int
}

func (s *readStatsAnnRepoStub) GetByID(_ context.Context, id int64) (*Announcement, error) {
	s.gets++
	if a, ok := s.items[id]; ok {
		return a, nil
	}
	return nil, ErrAnnouncementNotFound
}

func readStatsGroupTargeting(groupID int64) AnnouncementTargeting {
	return AnnouncementTargeting{AnyOf: []AnnouncementConditionGroup{{AllOf: []AnnouncementCondition{{
		Type: AnnouncementConditionTypeSubscription, Operator: AnnouncementOperatorIn, GroupIDs: []int64{groupID},
	}}}}}
}

func TestAnnouncementListUserReadStatus_NoPerUserSubscriptionQuery(t *testing.T) {
	now := time.Now()
	readAt := now.Add(-time.Hour)
	users := []User{
		{ID: 1, Email: "a@x", Username: "a", Balance: 1.5, Subscriptions: []UserSubscription{{GroupID: 7, Status: SubscriptionStatusActive, ExpiresAt: now.Add(time.Hour)}}},
		{ID: 2, Email: "b@x", Username: "b", Balance: 2, Subscriptions: []UserSubscription{{GroupID: 7, Status: SubscriptionStatusActive, ExpiresAt: now.Add(-time.Minute)}}},
		{ID: 3, Email: "c@x", Username: "c", Balance: 3},
	}
	userRepo := &readStatsUserRepoStub{users: users}
	readRepo := &readStatsReadRepoStub{readMap: map[int64]time.Time{2: readAt}}
	annRepo := &readStatsAnnRepoStub{items: map[int64]*Announcement{31: {ID: 31, Targeting: readStatsGroupTargeting(7)}}}
	svc := NewAnnouncementService(annRepo, readRepo, userRepo, &readStatsUserSubRepoStub{t: t})

	params := pagination.PaginationParams{Page: 1, PageSize: 200, SortBy: "email", SortOrder: "asc"}
	out, page, err := svc.ListUserReadStatus(context.Background(), 31, params, "  x  ")
	require.NoError(t, err)
	require.Equal(t, int64(3), page.Total)
	require.Equal(t, "x", userRepo.filters.Search)
	require.NotNil(t, userRepo.filters.IncludeSubscriptions)
	require.True(t, *userRepo.filters.IncludeSubscriptions)
	require.Equal(t, params, userRepo.params)

	require.Len(t, out, 3)
	require.Equal(t, []bool{true, false, false}, []bool{out[0].Eligible, out[1].Eligible, out[2].Eligible}, "expired subscriptions do not count")
	require.Nil(t, out[0].ReadAt)
	require.NotNil(t, out[1].ReadAt)
	require.True(t, out[1].ReadAt.Equal(readAt))
	require.Equal(t, AnnouncementUserReadStatus{UserID: 3, Email: "c@x", Username: "c", Balance: 3}, out[2])
}

func TestAnnouncementListUserReadStatusFiltered_Dispatch(t *testing.T) {
	targeting := readStatsGroupTargeting(9)
	newSvc := func() (*AnnouncementService, *readStatsUserRepoStub, *readStatsReadRepoStub, *readStatsAnnRepoStub) {
		userRepo := &readStatsUserRepoStub{}
		readRepo := &readStatsReadRepoStub{listRows: []AnnouncementUserReadStatus{{UserID: 5, Eligible: true}}}
		annRepo := &readStatsAnnRepoStub{items: map[int64]*Announcement{31: {ID: 31, Targeting: targeting}}}
		return NewAnnouncementService(annRepo, readRepo, userRepo, &readStatsUserSubRepoStub{t: t}), userRepo, readRepo, annRepo
	}
	params := pagination.PaginationParams{Page: 2, PageSize: 50, SortBy: "email", SortOrder: "asc"}

	t.Run("no new parameters keeps the legacy path", func(t *testing.T) {
		svc, userRepo, readRepo, _ := newSvc()
		_, _, err := svc.ListUserReadStatusFiltered(context.Background(), 31, params, AnnouncementReadStatusFilter{Search: "bob"})
		require.NoError(t, err)
		require.Equal(t, 1, userRepo.calls)
		require.Equal(t, "bob", userRepo.filters.Search)
		require.Zero(t, readRepo.listCalls)
	})

	yes, no := true, false
	for name, tc := range map[string]struct {
		params pagination.PaginationParams
		filter AnnouncementReadStatusFilter
		want   AnnouncementReadStatusFilter
	}{
		"read_state":     {params, AnnouncementReadStatusFilter{Search: "bob", ReadState: " Unread "}, AnnouncementReadStatusFilter{Search: "bob", ReadState: AnnouncementReadStateUnread}},
		"eligible false": {params, AnnouncementReadStatusFilter{Eligible: &no}, AnnouncementReadStatusFilter{Eligible: &no}},
		"eligible true":  {params, AnnouncementReadStatusFilter{Eligible: &yes, ReadState: "read"}, AnnouncementReadStatusFilter{Eligible: &yes, ReadState: AnnouncementReadStateRead}},
		"sort_by read_at": {
			pagination.PaginationParams{Page: 1, PageSize: 20, SortBy: " READ_AT ", SortOrder: "desc"},
			AnnouncementReadStatusFilter{},
			AnnouncementReadStatusFilter{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, userRepo, readRepo, _ := newSvc()
			out, page, err := svc.ListUserReadStatusFiltered(context.Background(), 31, tc.params, tc.filter)
			require.NoError(t, err)
			require.Zero(t, userRepo.calls, "filtered requests never page the whole user table in Go")
			require.Equal(t, 1, readRepo.listCalls)
			require.Equal(t, int64(31), readRepo.listID)
			require.Equal(t, targeting, readRepo.listTargeting)
			require.Equal(t, tc.params, readRepo.listParams)
			require.Equal(t, tc.want, readRepo.listFilter)
			require.Equal(t, readRepo.listRows, out)
			require.Equal(t, int64(1), page.Total)
		})
	}

	t.Run("invalid read_state is rejected before any repository access", func(t *testing.T) {
		svc, userRepo, readRepo, annRepo := newSvc()
		_, _, err := svc.ListUserReadStatusFiltered(context.Background(), 31, params, AnnouncementReadStatusFilter{ReadState: "foo"})
		require.ErrorIs(t, err, ErrAnnouncementInvalidReadStatusFilter)
		require.Equal(t, "INVALID_READ_STATUS_FILTER", infraerrors.Reason(err))
		require.Zero(t, userRepo.calls)
		require.Zero(t, readRepo.listCalls)
		require.Zero(t, annRepo.gets)
	})

	t.Run("missing announcement on the filtered path is 404", func(t *testing.T) {
		svc, _, readRepo, _ := newSvc()
		_, _, err := svc.ListUserReadStatusFiltered(context.Background(), 404, params, AnnouncementReadStatusFilter{ReadState: "read"})
		require.ErrorIs(t, err, ErrAnnouncementNotFound)
		require.Zero(t, readRepo.listCalls)
	})
}

func TestAnnouncementGetReadSummary(t *testing.T) {
	targeting := readStatsGroupTargeting(3)
	counts := AnnouncementReadCounts{TotalUsers: 1036, EligibleCount: 900, ReadCount: 412, EligibleReadCount: 410}
	readRepo := &readStatsReadRepoStub{counts: map[int64]AnnouncementReadCounts{31: counts}}
	annRepo := &readStatsAnnRepoStub{items: map[int64]*Announcement{31: {ID: 31, Status: AnnouncementStatusDraft, Targeting: targeting}}}
	svc := NewAnnouncementService(annRepo, readRepo, nil, nil)

	_, err := svc.GetReadSummary(context.Background(), 404)
	require.ErrorIs(t, err, ErrAnnouncementNotFound)
	require.Empty(t, readRepo.summarized)

	before := time.Now()
	got, err := svc.GetReadSummary(context.Background(), 31)
	require.NoError(t, err)
	require.Equal(t, int64(31), got.AnnouncementID)
	require.Equal(t, counts, got.AnnouncementReadCounts)
	require.Equal(t, time.UTC, got.ComputedAt.Location())
	require.False(t, got.ComputedAt.Before(before.Truncate(time.Second)))
	require.Equal(t, []AnnouncementTargeting{targeting}, readRepo.summarizeTgt, "drafts are summarized with their own targeting")
}

func TestAnnouncementReadStatsFor(t *testing.T) {
	readRepo := &readStatsReadRepoStub{counts: map[int64]AnnouncementReadCounts{
		1: {EligibleCount: 3, ReadCount: 2, EligibleReadCount: 1},
		2: {EligibleCount: 9, ReadCount: 9, EligibleReadCount: 9},
	}}
	svc := NewAnnouncementService(&readStatsAnnRepoStub{}, readRepo, nil, nil)

	empty, err := svc.ReadStatsFor(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Empty(t, readRepo.summarized)

	got, err := svc.ReadStatsFor(context.Background(), []Announcement{{ID: 1}, {ID: 2, Targeting: readStatsGroupTargeting(4)}})
	require.NoError(t, err)
	require.Equal(t, readRepo.counts, got)
	require.Equal(t, []int64{1, 2}, readRepo.summarized)
	require.Equal(t, readStatsGroupTargeting(4), readRepo.summarizeTgt[1])
}
