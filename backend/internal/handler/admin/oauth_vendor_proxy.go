package admin

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// scopedProxyLookup is the workspace-filtered proxy read (AdminService.GetProxy).
type scopedProxyLookup interface {
	GetProxy(ctx context.Context, id int64) (*service.Proxy, error)
}

// constrainVendorOAuthProxy checks a caller-supplied proxy_id before a Claude
// OAuth helper (generate-auth-url, exchange-code, cookie-auth and their
// setup-token variants) routes upstream traffic through it.
//
// The OAuth service resolves proxy_id without a workspace filter, so without
// this check a vendor could borrow another workspace's or the station's proxy.
// Station-owner calls are returned unchanged. A vendor needs AccountManage and
// may only name a proxy of its own workspace; any other id is reported as
// PROXY_NOT_FOUND, like every scoped proxy read. A missing or non-positive
// proxy_id means "no proxy" (the owner path ignores an id it cannot load) and
// needs no lookup. A missing scope counts as a vendor without permissions.
func constrainVendorOAuthProxy(ctx context.Context, proxies scopedProxyLookup, requested *int64) (*int64, error) {
	scope := service.ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() {
		return requested, nil
	}
	if err := scope.RequireAccountManage(); err != nil {
		return nil, err
	}
	if requested == nil || *requested <= 0 {
		return nil, nil
	}
	if proxies == nil {
		return nil, service.ErrServiceUnavailable
	}
	proxy, err := proxies.GetProxy(ctx, *requested)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		return nil, service.ErrProxyNotFound
	}
	return requested, nil
}

// requestProxy resolves the caller's proxy_id for a Claude OAuth helper. On
// rejection it has already written the error response and returns false.
func (h *OAuthHandler) requestProxy(c *gin.Context, requested *int64) (*int64, bool) {
	proxyID, err := constrainVendorOAuthProxy(c.Request.Context(), h.proxies, requested)
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, false
	}
	return proxyID, true
}
