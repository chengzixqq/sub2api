package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestGroupMapperAccountBucketsAlwaysPresentForAdmin 锁定管理端分组的两个新计数键恒在。
//
// App 按键是否存在判断服务端是否已支持四桶计数：带 omitempty 的话，
// 值为 0 时键会消失，老服务端与“确实没有错误账号”就分不清了。
// 用户侧分组不该看到任何账号计数。
func TestGroupMapperAccountBucketsAlwaysPresentForAdmin(t *testing.T) {
	zero := &service.Group{ID: 1, Name: "empty", Platform: service.PlatformAnthropic, Status: service.StatusActive}
	got := marshalToMap(t, GroupFromServiceAdmin(zero))
	for _, key := range []string{"error_account_count", "idle_account_count"} {
		value, ok := got[key]
		if !ok {
			t.Fatalf("计数为 0 时 %s 也必须出现", key)
		}
		if value != float64(0) {
			t.Fatalf("%s 应为 0，实得 %v", key, value)
		}
	}

	filled := &service.Group{
		ID: 2, Name: "filled", Platform: service.PlatformAnthropic, Status: service.StatusActive,
		AccountCount: 7, ActiveAccountCount: 1, RateLimitedAccountCount: 1, ErrorAccountCount: 1, IdleAccountCount: 4,
	}
	got = marshalToMap(t, GroupFromServiceAdmin(filled))
	if got["error_account_count"] != float64(1) || got["idle_account_count"] != float64(4) {
		t.Fatalf("计数应原样输出 1 / 4，实得 %v / %v", got["error_account_count"], got["idle_account_count"])
	}

	user := marshalToMap(t, GroupFromService(filled))
	for _, key := range []string{"error_account_count", "idle_account_count"} {
		if _, exposed := user[key]; exposed {
			t.Fatalf("用户侧分组不应包含 %s", key)
		}
	}
}
