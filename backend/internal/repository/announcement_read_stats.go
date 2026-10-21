package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// announcementSQLArgs numbers positional parameters for hand-built SQL.
type announcementSQLArgs struct {
	args []any
}

func (a *announcementSQLArgs) add(v any) string {
	a.args = append(a.args, v)
	return "$" + strconv.Itoa(len(a.args))
}

// announcementTargetingSQL translates targeting into a boolean SQL expression
// over users u with exactly the semantics of domain AnnouncementTargeting.Matches:
// no groups match everyone, an empty AND group matches nobody, unknown types or
// operators and empty group_ids are FALSE. Balances are compared as float8 so
// the result agrees bit-for-bit with Go's float64 comparison; an active
// subscription is one ListActiveByUserID would return.
func announcementTargetingSQL(t service.AnnouncementTargeting, args *announcementSQLArgs) string {
	if len(t.AnyOf) == 0 {
		return "TRUE"
	}
	groups := make([]string, 0, len(t.AnyOf))
	for _, group := range t.AnyOf {
		if len(group.AllOf) == 0 {
			groups = append(groups, "FALSE")
			continue
		}
		conds := make([]string, 0, len(group.AllOf))
		for _, cond := range group.AllOf {
			conds = append(conds, announcementConditionSQL(cond, args))
		}
		groups = append(groups, "("+strings.Join(conds, " AND ")+")")
	}
	return "(" + strings.Join(groups, " OR ") + ")"
}

var announcementBalanceSQLOperators = map[string]string{
	service.AnnouncementOperatorGT:  ">",
	service.AnnouncementOperatorGTE: ">=",
	service.AnnouncementOperatorLT:  "<",
	service.AnnouncementOperatorLTE: "<=",
	service.AnnouncementOperatorEQ:  "=",
}

func announcementConditionSQL(c service.AnnouncementCondition, args *announcementSQLArgs) string {
	switch c.Type {
	case service.AnnouncementConditionTypeSubscription:
		if c.Operator != service.AnnouncementOperatorIn || len(c.GroupIDs) == 0 {
			return "FALSE"
		}
		groupIDs := append([]int64(nil), c.GroupIDs...)
		return "EXISTS (SELECT 1 FROM user_subscriptions s WHERE s.user_id = u.id AND s.status = 'active'" +
			" AND s.expires_at > NOW() AND s.deleted_at IS NULL AND s.group_id = ANY(" + args.add(pq.Array(groupIDs)) + "))"
	case service.AnnouncementConditionTypeBalance:
		op, ok := announcementBalanceSQLOperators[c.Operator]
		if !ok {
			return "FALSE"
		}
		return "u.balance::double precision " + op + " " + args.add(c.Value) + "::double precision"
	default:
		return "FALSE"
	}
}

// announcementReadStatusOrderSQL mirrors userListOrder's whitelist and adds
// read_at (unread last). Unknown keys fall back to the user id.
func announcementReadStatusOrderSQL(params pagination.PaginationParams) string {
	dir := "DESC"
	if params.NormalizedSortOrder(pagination.SortOrderDesc) == pagination.SortOrderAsc {
		dir = "ASC"
	}
	tie := ", u.id " + dir
	switch strings.ToLower(strings.TrimSpace(params.SortBy)) {
	case "email", "username", "role", "balance", "concurrency", "status", "created_at":
		return "u." + strings.ToLower(strings.TrimSpace(params.SortBy)) + " " + dir + tie
	case "last_active_at":
		return "u.last_active_at " + dir + " NULLS LAST" + tie
	case "last_used_at":
		nulls := " NULLS LAST"
		if dir == "ASC" {
			nulls = " NULLS FIRST"
		}
		return "(SELECT MAX(l.created_at) FROM usage_logs l WHERE l.user_id = u.id) " + dir + nulls + tie
	case "read_at":
		return "ar.read_at " + dir + " NULLS LAST" + tie
	default:
		return "u.id " + dir
	}
}

const announcementReadFrom = " FROM users u LEFT JOIN announcement_reads ar ON ar.announcement_id = %s AND ar.user_id = u.id"

func (r *announcementReadRepository) querier() (sqlExecutor, error) {
	if r.sql == nil {
		return nil, fmt.Errorf("announcement read repository: sql executor is not configured")
	}
	return r.sql, nil
}

func (r *announcementReadRepository) SummarizeReads(
	ctx context.Context,
	announcementID int64,
	targeting service.AnnouncementTargeting,
) (service.AnnouncementReadCounts, error) {
	q, err := r.querier()
	if err != nil {
		return service.AnnouncementReadCounts{}, err
	}
	args := &announcementSQLArgs{}
	from := fmt.Sprintf(announcementReadFrom, args.add(announcementID))
	elig := announcementTargetingSQL(targeting, args)
	query := "SELECT COUNT(*), COUNT(*) FILTER (WHERE " + elig + "), COUNT(ar.user_id), COUNT(ar.user_id) FILTER (WHERE " + elig + ")" +
		from + " WHERE u.deleted_at IS NULL"

	var out service.AnnouncementReadCounts
	if err := scanSingleRow(ctx, q, query, args.args, &out.TotalUsers, &out.EligibleCount, &out.ReadCount, &out.EligibleReadCount); err != nil {
		return service.AnnouncementReadCounts{}, err
	}
	return out, nil
}

// announcementReadStatusQuery builds FROM ... WHERE ... for the read-status
// list. The eligibility expression is only emitted (and its parameters only
// bound) when the statement references it.
func announcementReadStatusQuery(
	announcementID int64,
	targeting service.AnnouncementTargeting,
	filter service.AnnouncementReadStatusFilter,
	selectEligible bool,
) (fromWhere, elig string, args *announcementSQLArgs) {
	args = &announcementSQLArgs{}
	from := fmt.Sprintf(announcementReadFrom, args.add(announcementID))
	conds := []string{"u.deleted_at IS NULL"}
	if search := strings.TrimSpace(filter.Search); search != "" {
		p := args.add("%" + escapeLike(search) + "%")
		conds = append(conds, "(u.email ILIKE "+p+" OR u.username ILIKE "+p+" OR u.notes ILIKE "+p+
			" OR EXISTS (SELECT 1 FROM api_keys k WHERE k.user_id = u.id AND k.deleted_at IS NULL AND k.key ILIKE "+p+"))")
	}
	switch filter.ReadState {
	case service.AnnouncementReadStateRead:
		conds = append(conds, "ar.user_id IS NOT NULL")
	case service.AnnouncementReadStateUnread:
		conds = append(conds, "ar.user_id IS NULL")
	}
	if selectEligible || filter.Eligible != nil {
		elig = announcementTargetingSQL(targeting, args)
	}
	if filter.Eligible != nil {
		if *filter.Eligible {
			conds = append(conds, "("+elig+")")
		} else {
			conds = append(conds, "NOT ("+elig+")")
		}
	}
	return from + " WHERE " + strings.Join(conds, " AND "), elig, args
}

func (r *announcementReadRepository) ListUserReadStatus(
	ctx context.Context,
	announcementID int64,
	targeting service.AnnouncementTargeting,
	params pagination.PaginationParams,
	filter service.AnnouncementReadStatusFilter,
) ([]service.AnnouncementUserReadStatus, *pagination.PaginationResult, error) {
	q, err := r.querier()
	if err != nil {
		return nil, nil, err
	}

	countFromWhere, _, countArgs := announcementReadStatusQuery(announcementID, targeting, filter, false)
	var total int64
	if err := scanSingleRow(ctx, q, "SELECT COUNT(*)"+countFromWhere, countArgs.args, &total); err != nil {
		return nil, nil, err
	}

	out := make([]service.AnnouncementUserReadStatus, 0, params.Limit())
	if total == 0 {
		return out, paginationResultFromTotal(0, params), nil
	}

	fromWhere, elig, args := announcementReadStatusQuery(announcementID, targeting, filter, true)
	query := "SELECT u.id, u.email, u.username, u.balance, ar.read_at, (" + elig + ")" + fromWhere +
		" ORDER BY " + announcementReadStatusOrderSQL(params) +
		" LIMIT " + args.add(params.Limit()) + " OFFSET " + args.add(params.Offset())
	rows, err := q.QueryContext(ctx, query, args.args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var item service.AnnouncementUserReadStatus
		var readAt sql.NullTime
		if err := rows.Scan(&item.UserID, &item.Email, &item.Username, &item.Balance, &readAt, &item.Eligible); err != nil {
			return nil, nil, err
		}
		if readAt.Valid {
			t := readAt.Time
			item.ReadAt = &t
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return out, paginationResultFromTotal(total, params), nil
}
