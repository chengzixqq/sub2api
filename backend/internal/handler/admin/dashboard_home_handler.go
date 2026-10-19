package admin

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// dashboardHomeCache absorbs the home screen's periodic refresh. Degraded
// aggregates (a section in error or timeout) are not stored, so the next
// refresh retries them.
var dashboardHomeCache = newSnapshotCache(15 * time.Second)

type dashboardHomeBuilder interface {
	Build(ctx context.Context, q service.DashboardHomeQuery) *service.DashboardHome
}

type dashboardHomePayload struct {
	Home *service.DashboardHome
	ETag string
}

// dashboardHomeUncached carries a degraded aggregate out of the cache loader:
// the cache stores only successful loads and hands the same error to every
// waiter.
type dashboardHomeUncached struct {
	payload *dashboardHomePayload
}

func (e *dashboardHomeUncached) Error() string { return "dashboard home degraded; not cached" }

// SetDashboardHomeService attaches the home aggregate builder.
func (h *DashboardHandler) SetDashboardHomeService(svc *service.DashboardHomeService) {
	if h != nil && svc != nil {
		h.home = svc
	}
}

// GetHome returns the mobile home aggregate. Every section reports ok /
// disabled / error / timeout on its own, so the response is 200 even with ops
// monitoring off or a slow source. Not open to vendor workspaces: every
// section is station-wide.
// GET /api/v1/admin/dashboard/home?timezone=<IANA>&include=stats,rates,hourly,ops,alerts,availability,channels
func (h *DashboardHandler) GetHome(c *gin.Context) {
	loc, err := service.ParseDashboardHomeTimezone(c.Query("timezone"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	include, err := service.ParseDashboardHomeInclude(c.Query("include"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if h.home == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	ctx := queryContext(c)
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	query := service.DashboardHomeQuery{Location: loc, Include: include, ViewerID: subject.UserID, Refresh: usagequery.OptionsFrom(ctx).ForceRefresh}
	key := scopedUsageCacheKey(ctx, mustMarshalDashboardCacheKey(struct {
		Timezone string
		Include  []string
	}{loc.String(), include.Names()}))

	payload, hit, err := h.loadDashboardHome(ctx, key, query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if writeConditionalETag(c, payload.ETag) {
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, payload.Home)
}

func (h *DashboardHandler) loadDashboardHome(ctx context.Context, key string, query service.DashboardHomeQuery) (*dashboardHomePayload, bool, error) {
	entry, hit, err := dashboardHomeCache.GetOrLoadContext(ctx, key, func(work context.Context) (any, error) {
		home := h.home.Build(work, query)
		payload := &dashboardHomePayload{Home: home, ETag: usagequery.CacheETag(home.ETagView())}
		if home.Degraded() {
			return nil, &dashboardHomeUncached{payload: payload}
		}
		return payload, nil
	})
	var uncached *dashboardHomeUncached
	if errors.As(err, &uncached) {
		return uncached.payload, false, nil
	}
	if err != nil {
		return nil, hit, err
	}
	payload, err := snapshotPayloadAs[*dashboardHomePayload](entry.Payload)
	return payload, hit, err
}
