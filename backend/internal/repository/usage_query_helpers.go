package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
)

func appendUsageExtraQueryFilters(query string, args []any, filters []UsageLogFilters, prefix string) (string, []any) {
	if len(filters) == 0 || strings.TrimSpace(filters[0].RequestID) == "" {
		return query, args
	}
	condition := fmt.Sprintf(" AND %srequest_id = $%d ", prefix, len(args)+1)
	// These callers append their GROUP BY only after building the complete row predicate.
	index := strings.Index(query, " GROUP BY ")
	if index < 0 {
		query += condition
	} else {
		query = query[:index] + condition + query[index:]
	}
	return query, append(args, strings.TrimSpace(filters[0].RequestID))
}

func usageQueryTimezone(ctx context.Context, query string, args []any) (string, []any) {
	zone := usagequery.OptionsFrom(ctx).Timezone
	if zone == "" {
		return query, args
	}
	for _, column := range []string{"created_at", "u.created_at", "bucket_start"} {
		old := "TO_CHAR(" + column + ","
		if strings.Contains(query, old) {
			query = strings.ReplaceAll(query, old, fmt.Sprintf("TO_CHAR(%s AT TIME ZONE $%d,", column, len(args)+1))
			return query, append(args, zone)
		}
	}
	return query, args
}

func usageRankingWhere(ctx context.Context, filters UsageLogFilters, args []any) (string, []any) {
	conditions := []string{"created_at >= $1", "created_at < $2"}
	for _, item := range []struct {
		name  string
		value int64
	}{{"user_id", filters.UserID}, {"api_key_id", filters.APIKeyID}, {"account_id", filters.AccountID}, {"group_id", filters.GroupID}} {
		if item.value > 0 {
			conditions = append(conditions, fmt.Sprintf("%s = $%d", item.name, len(args)+1))
			args = append(args, item.value)
		}
	}
	conditions, args = appendUsageAccountScopeCondition(ctx, "account_id", conditions, args)
	conditions, args = appendUsageLogModelWhereCondition(conditions, args, filters.Model, filters.ModelFilterSource)
	conditions, args = appendRequestTypeOrStreamWhereCondition(conditions, args, filters.RequestType, filters.Stream)
	conditions, args = appendNativeCompactionV2WhereCondition(conditions, args, filters.NativeCompactionV2, "")
	conditions, args = appendUsageLogBillingModeWhereCondition(conditions, args, filters.BillingMode)
	if filters.BillingType != nil {
		conditions = append(conditions, fmt.Sprintf("billing_type = $%d", len(args)+1))
		args = append(args, int16(*filters.BillingType))
	}
	if filters.UpstreamModelMismatch != nil {
		conditions = append(conditions, upstreamModelMismatchCondition("upstream_model_mismatch", *filters.UpstreamModelMismatch))
	}
	if filters.RequestID != "" {
		conditions = append(conditions, fmt.Sprintf("request_id = $%d", len(args)+1))
		args = append(args, filters.RequestID)
	}
	return buildWhere(conditions), args
}
