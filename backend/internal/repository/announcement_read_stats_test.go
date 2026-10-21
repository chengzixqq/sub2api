package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func annSubCond(op string, groupIDs ...int64) service.AnnouncementCondition {
	return service.AnnouncementCondition{Type: service.AnnouncementConditionTypeSubscription, Operator: op, GroupIDs: groupIDs}
}

func annBalanceCond(op string, value float64) service.AnnouncementCondition {
	return service.AnnouncementCondition{Type: service.AnnouncementConditionTypeBalance, Operator: op, Value: value}
}

func annGroup(conds ...service.AnnouncementCondition) service.AnnouncementConditionGroup {
	return service.AnnouncementConditionGroup{AllOf: conds}
}

func TestAnnouncementTargetingSQLShapes(t *testing.T) {
	subExists := func(n string) string {
		return "EXISTS (SELECT 1 FROM user_subscriptions s WHERE s.user_id = u.id AND s.status = 'active' AND s.expires_at > NOW() AND s.deleted_at IS NULL AND s.group_id = ANY(" + n + "))"
	}
	balance := func(op, n string) string {
		return "u.balance::double precision " + op + " " + n + "::double precision"
	}

	cases := []struct {
		name      string
		targeting service.AnnouncementTargeting
		want      string
		wantArgs  []any
	}{
		{name: "empty any_of shows to everyone", targeting: service.AnnouncementTargeting{}, want: "TRUE"},
		{name: "empty all_of group never matches", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{{}}}, want: "(FALSE)"},
		{name: "balance gt", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("gt", 5))}}, want: "((" + balance(">", "$2") + "))", wantArgs: []any{float64(5)}},
		{name: "balance gte", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("gte", 5))}}, want: "((" + balance(">=", "$2") + "))", wantArgs: []any{float64(5)}},
		{name: "balance lt", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("lt", 0.1))}}, want: "((" + balance("<", "$2") + "))", wantArgs: []any{0.1}},
		{name: "balance lte", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("lte", -1))}}, want: "((" + balance("<=", "$2") + "))", wantArgs: []any{float64(-1)}},
		{name: "balance eq", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("eq", 10.5))}}, want: "((" + balance("=", "$2") + "))", wantArgs: []any{10.5}},
		{name: "balance unknown operator", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annBalanceCond("in", 1))}}, want: "((FALSE))"},
		{name: "subscription in", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("in", 7, 9))}}, want: "((" + subExists("$2") + "))", wantArgs: []any{pq.Array([]int64{7, 9})}},
		{name: "subscription other operator", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("gt", 7))}}, want: "((FALSE))"},
		{name: "subscription empty group ids", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(annSubCond("in"))}}, want: "((FALSE))"},
		{name: "unknown condition type", targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{annGroup(service.AnnouncementCondition{Type: "vip", Operator: "in"})}}, want: "((FALSE))"},
		{
			name: "or of and groups numbers placeholders in order",
			targeting: service.AnnouncementTargeting{AnyOf: []service.AnnouncementConditionGroup{
				annGroup(annSubCond("in", 7), annBalanceCond("gte", 1)),
				{},
				annGroup(annBalanceCond("lt", 0)),
			}},
			want:     "((" + subExists("$2") + " AND " + balance(">=", "$3") + ") OR FALSE OR (" + balance("<", "$4") + "))",
			wantArgs: []any{pq.Array([]int64{7}), float64(1), float64(0)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := &announcementSQLArgs{}
			require.Equal(t, "$1", args.add(int64(31)), "announcement_id occupies $1")
			got := announcementTargetingSQL(tc.targeting, args)
			require.Equal(t, tc.want, got)
			require.Equal(t, append([]any{int64(31)}, tc.wantArgs...), args.args)
		})
	}
}

func TestAnnouncementReadStatusOrderSQL(t *testing.T) {
	const lastUsed = "(SELECT MAX(l.created_at) FROM usage_logs l WHERE l.user_id = u.id)"
	cases := []struct {
		sortBy, order, want string
	}{
		{"email", "asc", "u.email ASC, u.id ASC"},
		{"email", "desc", "u.email DESC, u.id DESC"},
		{" Email ", "bogus", "u.email DESC, u.id DESC"},
		{"username", "ASC", "u.username ASC, u.id ASC"},
		{"role", "asc", "u.role ASC, u.id ASC"},
		{"balance", "desc", "u.balance DESC, u.id DESC"},
		{"concurrency", "asc", "u.concurrency ASC, u.id ASC"},
		{"status", "asc", "u.status ASC, u.id ASC"},
		{"created_at", "desc", "u.created_at DESC, u.id DESC"},
		{"last_active_at", "asc", "u.last_active_at ASC NULLS LAST, u.id ASC"},
		{"last_active_at", "desc", "u.last_active_at DESC NULLS LAST, u.id DESC"},
		{"last_used_at", "asc", lastUsed + " ASC NULLS FIRST, u.id ASC"},
		{"last_used_at", "desc", lastUsed + " DESC NULLS LAST, u.id DESC"},
		{"read_at", "asc", "ar.read_at ASC NULLS LAST, u.id ASC"},
		{"read_at", "desc", "ar.read_at DESC NULLS LAST, u.id DESC"},
		{"read_at", "", "ar.read_at DESC NULLS LAST, u.id DESC"},
		{"", "asc", "u.id ASC"},
		{"id", "desc", "u.id DESC"},
		{"email; DROP TABLE users", "asc", "u.id ASC"},
	}
	for _, tc := range cases {
		got := announcementReadStatusOrderSQL(pagination.PaginationParams{SortBy: tc.sortBy, SortOrder: tc.order})
		require.Equal(t, tc.want, got, "sort_by=%q sort_order=%q", tc.sortBy, tc.order)
	}
}
