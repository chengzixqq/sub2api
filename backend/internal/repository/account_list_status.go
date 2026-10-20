package repository

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

var _ service.AccountListCountsRepository = (*accountRepository)(nil)

// accountStatusFilterPredicate ORs the per-value predicates of a parsed status
// filter; a single value keeps its predicate unchanged.
func accountStatusFilterPredicate(values []string) dbpredicate.Account {
	if len(values) == 1 {
		return accountStatusPredicate(values[0])
	}
	predicates := make([]dbpredicate.Account, 0, len(values))
	for _, value := range values {
		predicates = append(predicates, accountStatusPredicate(value))
	}
	return dbaccount.Or(predicates...)
}

// accountStatusPredicate maps one status filter value to its predicate. The
// legacy derived values keep their original predicates verbatim; values not
// listed match the status column as before.
func accountStatusPredicate(value string) dbpredicate.Account {
	switch value {
	case service.StatusActive:
		return dbaccount.And(
			dbaccount.StatusEQ(value),
			dbaccount.SchedulableEQ(true),
			dbaccount.Or(
				dbaccount.RateLimitResetAtIsNil(),
				dbaccount.RateLimitResetAtLTE(time.Now()),
			),
			tempUnschedulableInactivePredicate(),
		)
	case service.AccountListStatusRateLimited:
		return dbaccount.And(
			dbaccount.StatusEQ(service.StatusActive),
			dbaccount.RateLimitResetAtGT(time.Now()),
			tempUnschedulableInactivePredicate(),
		)
	case service.AccountListStatusTempUnschedulable:
		return dbaccount.And(
			dbaccount.StatusEQ(service.StatusActive),
			dbpredicate.Account(func(s *entsql.Selector) {
				col := s.C(dbaccount.FieldTempUnschedulableUntil)
				s.Where(entsql.And(
					entsql.Not(entsql.IsNull(col)),
					entsql.GT(col, entsql.Expr("NOW()")),
				))
			}),
		)
	case service.AccountListStatusUnschedulable:
		return dbaccount.And(
			dbaccount.StatusEQ(service.StatusActive),
			dbaccount.SchedulableEQ(false),
			dbaccount.Or(
				dbaccount.RateLimitResetAtIsNil(),
				dbaccount.RateLimitResetAtLTE(time.Now()),
			),
			tempUnschedulableInactivePredicate(),
		)
	case service.AccountListStatusOverloaded, service.AccountListStatusNeedsAttention, service.AccountListStatusSchedulableNow:
		return dbpredicate.Account(func(s *entsql.Selector) {
			s.Where(entsql.ExprP(accountStateConditionSQL(value, s.C)))
		})
	default:
		return dbaccount.StatusEQ(value)
	}
}

func tempUnschedulableInactivePredicate() dbpredicate.Account {
	return dbpredicate.Account(func(s *entsql.Selector) {
		col := s.C(dbaccount.FieldTempUnschedulableUntil)
		s.Where(entsql.Or(
			entsql.IsNull(col),
			entsql.LTE(col, entsql.Expr("NOW()")),
		))
	})
}

type accountStateColumns struct {
	status, schedulable, rateLimitResetAt, overloadUntil, tempUnschedulableUntil, expiresAt, autoPauseOnExpired string
}

func newAccountStateColumns(col func(string) string) accountStateColumns {
	return accountStateColumns{
		status:                 col(dbaccount.FieldStatus),
		schedulable:            col(dbaccount.FieldSchedulable),
		rateLimitResetAt:       col(dbaccount.FieldRateLimitResetAt),
		overloadUntil:          col(dbaccount.FieldOverloadUntil),
		tempUnschedulableUntil: col(dbaccount.FieldTempUnschedulableUntil),
		expiresAt:              col(dbaccount.FieldExpiresAt),
		autoPauseOnExpired:     col(dbaccount.FieldAutoPauseOnExpired),
	}
}

func sqlWindowOn(col string) string  { return "(" + col + " IS NOT NULL AND " + col + " > NOW())" }
func sqlWindowOff(col string) string { return "(" + col + " IS NULL OR " + col + " <= NOW())" }
func sqlStatusIs(col, status string) string {
	return col + " = '" + status + "'"
}

func (c accountStateColumns) active() string { return sqlStatusIs(c.status, service.StatusActive) }

// timedOn is true while any overload, rate-limit or temp-unschedulable window runs.
func (c accountStateColumns) timedOn() string {
	return "(" + sqlWindowOn(c.overloadUntil) + " OR " + sqlWindowOn(c.rateLimitResetAt) + " OR " + sqlWindowOn(c.tempUnschedulableUntil) + ")"
}

// accountStateConditionSQL returns a parameterless SQL condition for a status
// bucket, built only from column names, NOW() and constant literals so it can
// sit inside COUNT(*) FILTER (...). rate_limited, temp_unschedulable and
// unschedulable restate the legacy list predicates; the D02 aligned buckets
// follow AccountScheduleState and schedulable_now equals groupAccountAvailableSQL.
func accountStateConditionSQL(state string, col func(string) string) string {
	c := newAccountStateColumns(col)
	switch state {
	case service.StatusError:
		return "(" + sqlStatusIs(c.status, service.StatusError) + ")"
	case service.AccountListStatusInactive:
		return "(" + sqlStatusIs(c.status, service.AccountListStatusInactive) + ")"
	case service.AccountListStatusOverloaded:
		return "(" + c.active() + " AND " + sqlWindowOn(c.overloadUntil) + ")"
	case service.AccountListStatusRateLimited:
		return "(" + c.active() + " AND " + sqlWindowOn(c.rateLimitResetAt) + " AND " + sqlWindowOff(c.tempUnschedulableUntil) + ")"
	case service.AccountListStatusTempUnschedulable:
		return "(" + c.active() + " AND " + sqlWindowOn(c.tempUnschedulableUntil) + ")"
	case service.AccountListStatusUnschedulable:
		return "(" + c.active() + " AND " + c.schedulable + " = FALSE AND " + sqlWindowOff(c.rateLimitResetAt) + " AND " + sqlWindowOff(c.tempUnschedulableUntil) + ")"
	case service.AccountListStatusNeedsAttention:
		return "(" + sqlStatusIs(c.status, service.StatusError) + " OR (" + c.active() + " AND " + c.timedOn() + "))"
	case service.AccountListStatusSchedulableNow:
		return "(" + c.active() + " AND " + c.schedulable + " = TRUE" +
			" AND (" + c.expiresAt + " IS NULL OR " + c.expiresAt + " > NOW() OR " + c.autoPauseOnExpired + " = FALSE)" +
			" AND " + sqlWindowOff(c.rateLimitResetAt) + " AND " + sqlWindowOff(c.overloadUntil) + " AND " + sqlWindowOff(c.tempUnschedulableUntil) + ")"
	default:
		return "FALSE"
	}
}

// accountUrgencySortExpressions ranks accounts in D02 order: error 0, other
// non-active 3, timed windows 1, paused (switch off or auto-paused after
// expiry) 2, healthy 4. recovery is the latest end of the running windows and
// is only set for rank 1; Postgres GREATEST ignores NULLs.
func accountUrgencySortExpressions(col func(string) string) (rank, recovery string) {
	c := newAccountStateColumns(col)
	paused := "(" + c.schedulable + " = FALSE OR (" + c.autoPauseOnExpired + " = TRUE AND " + c.expiresAt + " IS NOT NULL AND " + c.expiresAt + " <= NOW()))"
	rank = "CASE WHEN " + sqlStatusIs(c.status, service.StatusError) + " THEN 0" +
		" WHEN " + c.status + " <> '" + service.StatusActive + "' THEN 3" +
		" WHEN " + c.timedOn() + " THEN 1" +
		" WHEN " + paused + " THEN 2" +
		" ELSE 4 END"
	recovery = "CASE WHEN " + c.active() + " AND " + c.timedOn() +
		" THEN GREATEST(" + c.overloadUntil + ", " + c.rateLimitResetAt + ", " + c.tempUnschedulableUntil + ") END"
	return rank, recovery
}

// accountUrgencyOrder sorts by urgency rank, then recovery time, name and id;
// desc reverses every key. Within a rank recovery is either always set (rank
// 1) or always NULL, so Postgres' default NULL placement keeps desc an exact
// reversal of asc.
func accountUrgencyOrder(sortOrder string) []func(*entsql.Selector) {
	direction := "ASC"
	tieOrder := entsql.Asc
	if sortOrder == pagination.SortOrderDesc {
		direction = "DESC"
		tieOrder = entsql.Desc
	}
	return []func(*entsql.Selector){func(s *entsql.Selector) {
		rank, recovery := accountUrgencySortExpressions(s.C)
		s.OrderExpr(entsql.Expr(rank + " " + direction))
		s.OrderExpr(entsql.Expr(recovery + " " + direction))
		s.OrderBy(tieOrder(s.C(dbaccount.FieldName)), tieOrder(s.C(dbaccount.FieldID)))
	}}
}

// accountListCountBuckets lists the counted status buckets and their aliases
// (aliases avoid reserved words such as ALL).
var accountListCountBuckets = []struct{ alias, state string }{
	{"c_needs_attention", service.AccountListStatusNeedsAttention},
	{"c_error", service.StatusError},
	{"c_overloaded", service.AccountListStatusOverloaded},
	{"c_rate_limited", service.AccountListStatusRateLimited},
	{"c_temp_unschedulable", service.AccountListStatusTempUnschedulable},
	{"c_unschedulable", service.AccountListStatusUnschedulable},
	{"c_inactive", service.AccountListStatusInactive},
	{"c_schedulable_now", service.AccountListStatusSchedulableNow},
}

type accountListCountsRow struct {
	All               int64 `sql:"c_all"`
	NeedsAttention    int64 `sql:"c_needs_attention"`
	Error             int64 `sql:"c_error"`
	Overloaded        int64 `sql:"c_overloaded"`
	RateLimited       int64 `sql:"c_rate_limited"`
	TempUnschedulable int64 `sql:"c_temp_unschedulable"`
	Unschedulable     int64 `sql:"c_unschedulable"`
	Inactive          int64 `sql:"c_inactive"`
	SchedulableNow    int64 `sql:"c_schedulable_now"`
}

// CountAccountListBuckets counts every status bucket in one query over the
// same filters (workspace scope and soft delete included) the list applies
// without a status filter.
func (r *accountRepository) CountAccountListBuckets(ctx context.Context, f service.AccountListCountsFilter) (*service.AccountListCounts, error) {
	aggregates := make([]dbent.AggregateFunc, 0, len(accountListCountBuckets)+1)
	aggregates = append(aggregates, func(*entsql.Selector) string { return "COUNT(*) AS c_all" })
	for _, bucket := range accountListCountBuckets {
		aggregates = append(aggregates, func(s *entsql.Selector) string {
			return "COUNT(*) FILTER (WHERE " + accountStateConditionSQL(bucket.state, s.C) + ") AS " + bucket.alias
		})
	}

	var rows []accountListCountsRow
	err := r.accountListFilteredQuery(ctx, f.Platform, f.AccountType, "", f.Search, f.GroupID, f.PrivacyMode).
		Aggregate(aggregates...).
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("count account list buckets: expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	return &service.AccountListCounts{
		All:               row.All,
		NeedsAttention:    row.NeedsAttention,
		Error:             row.Error,
		Overloaded:        row.Overloaded,
		RateLimited:       row.RateLimited,
		TempUnschedulable: row.TempUnschedulable,
		Unschedulable:     row.Unschedulable,
		Inactive:          row.Inactive,
		SchedulableNow:    row.SchedulableNow,
	}, nil
}
