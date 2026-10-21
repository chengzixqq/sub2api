//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// announcementReadFixture tracks every row a test inserts through the real
// (committing) ent client so t.Cleanup can hard-delete them from the shared DB.
type announcementReadFixture struct {
	t        *testing.T
	client   *dbent.Client
	prefix   string
	userIDs  []int64
	groupIDs []int64
	annIDs   []int64
}

func newAnnouncementReadFixture(t *testing.T) *announcementReadFixture {
	t.Helper()
	f := &announcementReadFixture{
		t:      t,
		client: testEntClient(t),
		prefix: "annrs-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
	}
	t.Cleanup(func() {
		ctx := context.Background()
		users, groups, anns := pq.Array(f.userIDs), pq.Array(f.groupIDs), pq.Array(f.annIDs)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM announcement_reads WHERE announcement_id = ANY($1) OR user_id = ANY($2)", anns, users)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM announcements WHERE id = ANY($1)", anns)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM api_keys WHERE user_id = ANY($1)", users)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM user_subscriptions WHERE user_id = ANY($1)", users)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = ANY($1)", users)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = ANY($1)", groups)
	})
	return f
}

func (f *announcementReadFixture) group(name string) int64 {
	g := mustCreateGroup(f.t, f.client, &service.Group{Name: f.prefix + "-" + name})
	f.groupIDs = append(f.groupIDs, g.ID)
	return g.ID
}

func (f *announcementReadFixture) user(name string, balance float64) int64 {
	u := mustCreateUser(f.t, f.client, &service.User{
		Email:    f.prefix + "-" + name + "@example.com",
		Username: f.prefix + "-" + name,
		Balance:  balance,
	})
	f.userIDs = append(f.userIDs, u.ID)
	return u.ID
}

func (f *announcementReadFixture) subscription(userID, groupID int64, status string, expiresAt time.Time) int64 {
	s := mustCreateSubscription(f.t, f.client, &service.UserSubscription{
		UserID:    userID,
		GroupID:   groupID,
		Status:    status,
		ExpiresAt: expiresAt,
	})
	return s.ID
}

func (f *announcementReadFixture) announcement(targeting service.AnnouncementTargeting) int64 {
	a := &service.Announcement{
		Title:      f.prefix,
		Content:    "read stats",
		Status:     service.AnnouncementStatusActive,
		NotifyMode: service.AnnouncementNotifyModeSilent,
		Targeting:  targeting,
	}
	require.NoError(f.t, NewAnnouncementRepository(f.client).Create(context.Background(), a))
	f.annIDs = append(f.annIDs, a.ID)
	return a.ID
}

func (f *announcementReadFixture) exec(query string, args ...any) {
	_, err := integrationDB.ExecContext(context.Background(), query, args...)
	require.NoError(f.t, err)
}

func subscriptionTargeting(groupIDs ...int64) service.AnnouncementTargeting {
	return service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("in", groupIDs...))}}
}

func TestAnnouncementTargetingSQLMatchesDomain(t *testing.T) {
	ctx := context.Background()
	f := newAnnouncementReadFixture(t)
	g1, g2 := f.group("g1"), f.group("g2")
	future, past := time.Now().Add(48*time.Hour), time.Now().Add(-time.Hour)

	uA := f.user("a", -1)
	f.subscription(uA, g1, service.SubscriptionStatusActive, future)
	uB := f.user("b", 0)
	f.subscription(uB, g1, service.SubscriptionStatusActive, past) // expired but not yet swept
	uC := f.user("c", 0.1)
	deletedSub := f.subscription(uC, g2, service.SubscriptionStatusActive, future)
	f.exec("UPDATE user_subscriptions SET deleted_at = NOW() WHERE id = $1", deletedSub)
	uD := f.user("d", 5)
	f.subscription(uD, g2, service.SubscriptionStatusSuspended, future)
	uE := f.user("e", 10)
	f.subscription(uE, g2, service.SubscriptionStatusActive, future)
	f.subscription(uE, g1, service.SubscriptionStatusActive, future)
	uF := f.user("f", 10.5)
	uG := f.user("g", 0.3)
	f.subscription(uG, g1, service.SubscriptionStatusActive, future)
	f.subscription(uG, g2, service.SubscriptionStatusActive, past)
	uH := f.user("h", 0.00000001)
	ids := []int64{uA, uB, uC, uD, uE, uF, uG, uH}

	subs := NewUserSubscriptionRepository(f.client)
	balances := make(map[int64]float64, len(ids))
	active := make(map[int64]map[int64]struct{}, len(ids))
	for _, id := range ids {
		u, err := f.client.User.Get(ctx, id)
		require.NoError(t, err)
		balances[id] = u.Balance
		list, err := subs.ListActiveByUserID(ctx, id)
		require.NoError(t, err)
		set := map[int64]struct{}{}
		for _, s := range list {
			set[s.GroupID] = struct{}{}
		}
		active[id] = set
	}

	targetings := map[string]service.AnnouncementTargeting{
		"empty":             {},
		"balance gt 5":      {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("gt", 5))}},
		"balance gte 5":     {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("gte", 5))}},
		"balance lt 0":      {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("lt", 0))}},
		"balance lte 0":     {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("lte", 0))}},
		"balance eq 10.5":   {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("eq", 10.5))}},
		"balance eq 0.3":    {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("eq", 0.3))}},
		"balance eq 1e-8":   {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("eq", 0.00000001))}},
		"balance gt 0.1":    {AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("gt", 0.1))}},
		"sub g1":            subscriptionTargeting(g1),
		"sub g2":            subscriptionTargeting(g2),
		"sub g1 or g2":      subscriptionTargeting(g1, g2),
		"sub g1 and gte 5":  {AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("in", g1), annBalanceCond("gte", 5))}},
		"sub g2 or lt 0":    {AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("in", g2)), annGroup(annBalanceCond("lt", 0))}},
		"sub bad operator":  {AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("gt", g1))}},
		"sub empty groups":  {AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("in"))}},
		"unknown type":      {AnyOf: []service.AnnouncementConditionGroup{annGroup(service.AnnouncementCondition{Type: "vip", Operator: "in"})}},
		"empty group or gt": {AnyOf: []service.AnnouncementConditionGroup{{}, annGroup(annBalanceCond("gt", 10))}},
	}

	for name, targeting := range targetings {
		t.Run(name, func(t *testing.T) {
			args := &announcementSQLArgs{}
			elig := announcementTargetingSQL(targeting, args)
			idsParam := args.add(pq.Array(ids))
			rows, err := integrationDB.QueryContext(ctx, "SELECT u.id, ("+elig+") FROM users u WHERE u.id = ANY("+idsParam+")", args.args...)
			require.NoError(t, err)
			defer func() { _ = rows.Close() }()
			seen := 0
			for rows.Next() {
				var id int64
				var got bool
				require.NoError(t, rows.Scan(&id, &got))
				want := targeting.Matches(balances[id], active[id])
				require.Equal(t, want, got, "user %d balance=%v active=%v", id, balances[id], active[id])
				seen++
			}
			require.NoError(t, rows.Err())
			require.Equal(t, len(ids), seen)
		})
	}
}

func TestAnnouncementReadRepository_SummarizeReads(t *testing.T) {
	ctx := context.Background()
	f := newAnnouncementReadFixture(t)
	repo := newAnnouncementReadRepositoryWithSQL(f.client, integrationDB)
	g := f.group("only")
	future := time.Now().Add(48 * time.Hour)
	readAt := time.Now().Add(-time.Minute)

	var eligible []int64
	for i := 0; i < 3; i++ {
		id := f.user(fmt.Sprintf("e%d", i), 1)
		f.subscription(id, g, service.SubscriptionStatusActive, future)
		eligible = append(eligible, id)
	}
	ineligibleRead := f.user("n-read", 1)
	f.user("n-unread", 1)
	deleted := f.user("deleted", 1)
	f.subscription(deleted, g, service.SubscriptionStatusActive, future)

	targeted := f.announcement(subscriptionTargeting(g))
	for _, id := range []int64{eligible[0], eligible[1], ineligibleRead, deleted} {
		require.NoError(t, repo.MarkRead(ctx, targeted, id, readAt))
	}
	f.exec("UPDATE users SET deleted_at = NOW() WHERE id = $1", deleted)

	counts, err := repo.SummarizeReads(ctx, targeted, subscriptionTargeting(g))
	require.NoError(t, err)
	require.Equal(t, int64(3), counts.EligibleCount, "only live users with an active subscription in the unique group")
	require.Equal(t, int64(3), counts.ReadCount, "reads by deleted users are not counted")
	require.Equal(t, int64(2), counts.EligibleReadCount)
	require.GreaterOrEqual(t, counts.TotalUsers, int64(5))

	everyone := f.announcement(service.AnnouncementTargeting{})
	for _, id := range []int64{eligible[2], ineligibleRead, deleted} {
		require.NoError(t, repo.MarkRead(ctx, everyone, id, readAt))
	}
	counts, err = repo.SummarizeReads(ctx, everyone, service.AnnouncementTargeting{})
	require.NoError(t, err)
	require.Equal(t, counts.TotalUsers, counts.EligibleCount, "empty targeting matches every live user")
	require.Equal(t, int64(2), counts.ReadCount)
	require.Equal(t, int64(2), counts.EligibleReadCount)

	var direct int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM announcement_reads ar JOIN users u ON u.id = ar.user_id AND u.deleted_at IS NULL WHERE ar.announcement_id = $1",
		targeted).Scan(&direct))
	require.Equal(t, int64(3), direct, "acceptance query from the spec agrees")
}

func TestAnnouncementReadRepository_ListUserReadStatusFilters(t *testing.T) {
	ctx := context.Background()
	f := newAnnouncementReadFixture(t)
	repo := newAnnouncementReadRepositoryWithSQL(f.client, integrationDB)
	g := f.group("only")
	future := time.Now().Add(48 * time.Hour)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	e1, e2, e3 := f.user("e1", 1), f.user("e2", 2), f.user("e3", 3)
	for _, id := range []int64{e1, e2, e3} {
		f.subscription(id, g, service.SubscriptionStatusActive, future)
	}
	n1, n2 := f.user("n1", 4), f.user("n2", 5)
	deleted := f.user("deleted", 6)
	f.subscription(deleted, g, service.SubscriptionStatusActive, future)

	targeting := subscriptionTargeting(g)
	ann := f.announcement(targeting)
	require.NoError(t, repo.MarkRead(ctx, ann, e1, base))
	require.NoError(t, repo.MarkRead(ctx, ann, e2, base.Add(time.Minute)))
	require.NoError(t, repo.MarkRead(ctx, ann, n1, base.Add(2*time.Minute)))
	require.NoError(t, repo.MarkRead(ctx, ann, deleted, base.Add(3*time.Minute)))
	f.exec("UPDATE users SET deleted_at = NOW() WHERE id = $1", deleted)

	liveKey := "sk-h06-" + uuid.NewString()
	mustCreateApiKey(t, f.client, &service.APIKey{UserID: e1, Key: liveKey})
	deadKey := "sk-h06-" + uuid.NewString()
	dead := mustCreateApiKey(t, f.client, &service.APIKey{UserID: n2, Key: deadKey})
	f.exec("UPDATE api_keys SET deleted_at = NOW() WHERE id = $1", dead.ID)

	page := func(sortBy, order string, size int) pagination.PaginationParams {
		return pagination.PaginationParams{Page: 1, PageSize: size, SortBy: sortBy, SortOrder: order}
	}
	ids := func(rows []service.AnnouncementUserReadStatus) []int64 {
		out := make([]int64, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.UserID)
		}
		return out
	}
	yes, no := true, false

	rows, res, err := repo.ListUserReadStatus(ctx, ann, targeting, page("email", "asc", 50), service.AnnouncementReadStatusFilter{Search: f.prefix, ReadState: service.AnnouncementReadStateRead})
	require.NoError(t, err)
	require.Equal(t, int64(3), res.Total)
	require.Equal(t, []int64{e1, e2, n1}, ids(rows))
	for _, r := range rows {
		require.NotNil(t, r.ReadAt)
	}
	require.Equal(t, f.prefix+"-e1@example.com", rows[0].Email)
	require.Equal(t, f.prefix+"-e1", rows[0].Username)
	require.Equal(t, float64(1), rows[0].Balance)
	require.True(t, rows[0].ReadAt.Equal(base))

	rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page("email", "asc", 50), service.AnnouncementReadStatusFilter{Search: f.prefix, ReadState: service.AnnouncementReadStateUnread, Eligible: &yes})
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Total, "eligible_count - eligible_read_count")
	require.Equal(t, []int64{e3}, ids(rows))
	require.True(t, rows[0].Eligible)
	require.Nil(t, rows[0].ReadAt)

	rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page("email", "asc", 50), service.AnnouncementReadStatusFilter{Search: f.prefix, Eligible: &no})
	require.NoError(t, err)
	require.Equal(t, int64(2), res.Total)
	require.Equal(t, []int64{n1, n2}, ids(rows))
	for _, r := range rows {
		require.False(t, r.Eligible)
	}

	rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page("read_at", "desc", 50), service.AnnouncementReadStatusFilter{Search: f.prefix})
	require.NoError(t, err)
	require.Equal(t, int64(5), res.Total, "deleted users are excluded")
	require.Equal(t, []int64{n1, e2, e1, n2, e3}, ids(rows), "newest read first, unread last by id desc")
	wantEligible := map[int64]bool{e1: true, e2: true, e3: true, n1: false, n2: false}
	for _, r := range rows {
		require.Equal(t, wantEligible[r.UserID], r.Eligible, "user %d", r.UserID)
	}

	rows, _, err = repo.ListUserReadStatus(ctx, ann, targeting, page("read_at", "asc", 50), service.AnnouncementReadStatusFilter{Search: f.prefix})
	require.NoError(t, err)
	require.Equal(t, []int64{e1, e2, n1, e3, n2}, ids(rows), "oldest read first, unread still last by id asc")

	rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page("read_at", "asc", 2), service.AnnouncementReadStatusFilter{Search: f.prefix})
	require.NoError(t, err)
	require.Equal(t, int64(5), res.Total)
	require.Equal(t, 3, res.Pages)
	require.Equal(t, []int64{e1, e2}, ids(rows))
	rows, _, err = repo.ListUserReadStatus(ctx, ann, targeting, pagination.PaginationParams{Page: 3, PageSize: 2, SortBy: "read_at", SortOrder: "asc"}, service.AnnouncementReadStatusFilter{Search: f.prefix})
	require.NoError(t, err)
	require.Equal(t, []int64{n2}, ids(rows))

	rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page("email", "asc", 50), service.AnnouncementReadStatusFilter{Search: liveKey})
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Total)
	require.Equal(t, []int64{e1}, ids(rows))

	rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page("email", "asc", 50), service.AnnouncementReadStatusFilter{Search: deadKey})
	require.NoError(t, err)
	require.Zero(t, res.Total, "deleted API keys are not searchable")
	require.Empty(t, rows)

	rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page("email", "asc", 50), service.AnnouncementReadStatusFilter{Search: f.prefix + "%"})
	require.NoError(t, err)
	require.Zero(t, res.Total, "LIKE wildcards in search are literal")
	require.Empty(t, rows)

	for _, sortBy := range []string{"email", "username", "role", "balance", "concurrency", "status", "created_at", "last_active_at", "last_used_at", "read_at", "bogus"} {
		for _, order := range []string{"asc", "desc"} {
			rows, res, err = repo.ListUserReadStatus(ctx, ann, targeting, page(sortBy, order, 50), service.AnnouncementReadStatusFilter{Search: f.prefix, Eligible: &yes})
			require.NoError(t, err, "sort_by=%s sort_order=%s", sortBy, order)
			require.Equal(t, int64(3), res.Total)
			require.ElementsMatch(t, []int64{e1, e2, e3}, ids(rows))
		}
	}
}
