package middleware

import "testing"

// TestVendorUsageScopedRoutesAreAllowed 锁定：需要注入账号白名单的路由
// 必须同时出现在放行白名单里。
//
// 两份清单靠人工同步，登记在 vendorUsageScopedRoutes 但漏在
// vendorAllowedRoutes 的条目会被 403 挡掉（可见的失败），反向遗漏则是
// 静默越权。本测试盯住前者，TestVendorUsageRoutesRequireScope 盯住后者。
func TestVendorUsageScopedRoutesAreAllowed(t *testing.T) {
	for _, prefix := range vendorUsageScopedRoutes {
		found := false
		for _, route := range vendorAllowedRoutes {
			if route.prefix == prefix {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("路由 %q 登记在 vendorUsageScopedRoutes，但不在 vendorAllowedRoutes 中，"+
				"vendor 请求会被 403 挡掉", prefix)
		}
	}
}

// TestVendorUsageRoutesRequireScope 锁定：放行的用量类路由都注入了账号白名单。
//
// 这是隔离的关键不变量。usage 与 dashboard 前缀下的端点都查 usage_logs，
// 一旦放行却未登记到 vendorUsageScopedRoutes，该端点会在无白名单的情况下
// 执行查询 —— 返回全站数据，无任何报错。
//
// 新增 vendor 可见的用量端点时，本测试会失败，提示补登记。
func TestVendorUsageRoutesRequireScope(t *testing.T) {
	usageLikePrefixes := []string{
		"/api/v1/admin/usage",
		"/api/v1/admin/dashboard",
	}

	for _, route := range vendorAllowedRoutes {
		if !isUsageLike(route.prefix, usageLikePrefixes) {
			continue
		}
		if !containsString(vendorUsageScopedRoutes, route.prefix) {
			t.Errorf("路由 %q 对 vendor 放行且会查询用量日志，但未登记到 "+
				"vendorUsageScopedRoutes —— 该端点将返回全站数据（越权）", route.prefix)
		}
	}
}

// TestVendorDashboardRoutesAreExact 锁定看板路由必须精确匹配。
//
// /api/v1/admin/dashboard 下混有可收窄（trend/models/groups）与不可收窄
// （users-ranking、user-breakdown、stats 等按用户维度聚合）的端点。
// 任何一条改回前缀匹配都会连带放出后者。
func TestVendorDashboardRoutesAreExact(t *testing.T) {
	const dashboardPrefix = "/api/v1/admin/dashboard"

	for _, route := range vendorAllowedRoutes {
		if !matchesPrefix(route.prefix, dashboardPrefix) {
			continue
		}
		if route.prefix == dashboardPrefix {
			t.Errorf("看板前缀 %q 被整体放行，会连带放出 users-ranking / "+
				"user-breakdown 等无法按账号收窄的端点", route.prefix)
			continue
		}
		if !route.exact {
			t.Errorf("看板路由 %q 未设 exact，其子路径会被连带放行", route.prefix)
		}
	}
}

func isUsageLike(prefix string, usageLikePrefixes []string) bool {
	for _, candidate := range usageLikePrefixes {
		if matchesPrefix(prefix, candidate) {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
