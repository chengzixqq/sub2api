package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func queryContext(c *gin.Context) context.Context {
	user, _ := middleware.GetAuthSubjectFromContext(c)
	role, _ := middleware.GetUserRoleFromContext(c)
	return usagequery.WithOptions(c.Request.Context(), usagequery.Options{Identity: fmt.Sprintf("%s:%d", role, user.UserID), Timezone: strings.TrimSpace(c.Query("timezone")), ForceRefresh: parseBoolQueryWithDefault(c.Query("nocache"), false) || parseBoolQueryWithDefault(c.Query("force_refresh"), false)})
}

func scopedUsageCacheKey(ctx context.Context, key string) string {
	ids, restricted := service.UsageAccountScopeFrom(ctx)
	ids = append([]int64(nil), ids...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	scope, hasScope := service.ScopeFromContext(ctx)
	options := usagequery.OptionsFrom(ctx)
	return mustMarshalDashboardCacheKey(struct {
		Query      string
		Identity   string
		Timezone   string
		Restricted bool
		AccountIDs []int64
		Scope      service.Scope
		HasScope   bool
	}{key, options.Identity, options.Timezone, restricted, ids, scope, hasScope})
}
