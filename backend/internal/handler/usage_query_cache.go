package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var userUsageCache = usagequery.NewCache(30 * time.Second)

func userQueryContext(c *gin.Context) context.Context {
	user, _ := middleware.GetAuthSubjectFromContext(c)
	role, _ := middleware.GetUserRoleFromContext(c)
	force, _ := strconv.ParseBool(c.Query("nocache"))
	refresh, _ := strconv.ParseBool(c.Query("force_refresh"))
	force = force || refresh
	return usagequery.WithOptions(c.Request.Context(), usagequery.Options{Identity: fmt.Sprintf("%s:%d", role, user.UserID), Timezone: c.Query("timezone"), ForceRefresh: force})
}

func loadUserUsageQuery[T any](ctx context.Context, kind string, filters usagestats.UsageLogFilters, load func(context.Context) (T, error)) (T, error) {
	scope, hasScope := service.ScopeFromContext(ctx)
	accountIDs, restricted := service.UsageAccountScopeFrom(ctx)
	options := usagequery.OptionsFrom(ctx)
	key, _ := json.Marshal(struct {
		Kind                 string
		Filters              usagestats.UsageLogFilters
		Identity, Timezone   string
		Scope                service.Scope
		HasScope, Restricted bool
		AccountIDs           []int64
	}{kind, filters, options.Identity, options.Timezone, scope, hasScope, restricted, accountIDs})
	entry, _, err := userUsageCache.GetOrLoad(ctx, string(key), options.ForceRefresh, func(work context.Context) (any, error) { return load(work) })
	var zero T
	if err != nil {
		return zero, err
	}
	value, ok := entry.Payload.(T)
	if !ok {
		return zero, fmt.Errorf("unexpected usage cache payload")
	}
	return value, nil
}
