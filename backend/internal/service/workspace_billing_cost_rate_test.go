package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// 结算倍率改挂账号自身的 rate_multiplier 后，计费路径上的工作区逻辑
// 被整体拿掉了：没有授权查询、没有缓存、没有「取不到授权怎么办」的回退。
//
// 本文件因此只锁两件事：取值就是账号那一列，以及站长直管账号的结果逐字未变。
// 原先那批 BillingCostRate 用例连同 grant 级倍率一起删除 ——
// 它们锁定的机制已不存在，留着只会让人以为热路径还在查授权。

// TestResolveAccountCostRateUsesAccountMultiplier 锁定取值就是账号倍率。
//
// 这是结算口径的全部：供应商在账号管理里改 rate_multiplier，改的即是结算价。
// 站长设的区间只在写入时校验，计费读到的永远是已落库的值。
func TestResolveAccountCostRateUsesAccountMultiplier(t *testing.T) {
	account := &Account{WorkspaceID: 7, RateMultiplier: ptrFloat64(0.6)}

	if got := resolveAccountCostRate(account); got != 0.6 {
		t.Errorf("resolveAccountCostRate = %v, 期望账号倍率 0.6", got)
	}
}

// TestResolveAccountCostRateOwnerAccountUnaffected 锁定站长直管账号口径不变。
//
// 这是最关键的回归锚点：引入工作区不得改变站长账号的计费结果。
//
// 两个取值都要锁。1 是站长直管工作区（迁移 192 的列默认值，存量行与
// 站长新建行都落这里）；0 只可能是迁移前的残留行 —— 计费不看归属，
// 两者都必须原样返回账号自己的倍率，不得因归属可疑而兜底成 1.0。
func TestResolveAccountCostRateOwnerAccountUnaffected(t *testing.T) {
	for _, workspaceID := range []int64{domain.DefaultWorkspaceID, 0} {
		account := &Account{WorkspaceID: workspaceID, RateMultiplier: ptrFloat64(1.8)}

		if got := resolveAccountCostRate(account); got != 1.8 {
			t.Errorf("workspace_id=%d: resolveAccountCostRate = %v, 必须保持 1.8",
				workspaceID, got)
		}
	}
}

// TestResolveAccountCostRateNilSafety 锁定账号缺失时按 1.0 结算。
//
// 网关有多条构造路径，账号可能为空；此时必须落到 1.0，
// 而不是 panic 或按 0 把这笔账记成免费。
func TestResolveAccountCostRateNilSafety(t *testing.T) {
	if got := resolveAccountCostRate(nil); got != 1.0 {
		t.Errorf("账号为空时应返回 1.0，得到 %v", got)
	}
	if got := resolveAccountCostRate(&Account{WorkspaceID: 7}); got != 1.0 {
		t.Errorf("倍率未配置时应返回 1.0，得到 %v", got)
	}
}

// TestResolveAccountCostRateZeroIsFree 锁定 0 是合法值而非「未配置」。
//
// 站长可能就是要某个账号不计成本（自有额度、赠送池）。若把 0 当缺失
// 处理成 1.0，这类账号会凭空产生成本，且越用越多。
func TestResolveAccountCostRateZeroIsFree(t *testing.T) {
	account := &Account{WorkspaceID: 7, RateMultiplier: ptrFloat64(0)}

	if got := resolveAccountCostRate(account); got != 0 {
		t.Errorf("倍率 0 应按 0 结算，得到 %v", got)
	}
}

// TestResolveAccountCostRateNegativeIsRejected 锁定负倍率按 1.0 兜底。
//
// 负倍率会让用量产生负成本、把结算金额倒扣回去。写入侧已有区间校验，
// 这条守的是绕过写入侧的脏数据（手改库、旧缓存）。
func TestResolveAccountCostRateNegativeIsRejected(t *testing.T) {
	account := &Account{WorkspaceID: 7, RateMultiplier: ptrFloat64(-0.5)}

	if got := resolveAccountCostRate(account); got != 1.0 {
		t.Errorf("负倍率应按 1.0 兜底，得到 %v", got)
	}
}
