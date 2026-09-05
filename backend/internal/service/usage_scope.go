package service

import "context"

// usageAccountScopeKey 是账号白名单在 context 中的键。
type usageAccountScopeKey struct{}

// UsageAccountScope 约束用量查询可见的账号集合。
//
// 为什么走 context 而非查询参数：用量统计的谓词构造散落在十余个函数里，
// 其中八个接收展开的位置参数（userID, apiKeyID, accountID, groupID ...），
// 拿不到统一的 filters 结构体。逐个加参数会让本已臃肿的签名继续膨胀，
// 且漏改一处就是静默越权。
//
// 放在 service 包而非 repository：middleware 需要写入该值，而 middleware
// 只依赖 service 及以下，不依赖 repository —— 放在 repository 会造成
// 分层倒置。repository 已依赖 service，读取侧不受影响。
type UsageAccountScope struct {
	AccountIDs []int64
}

// WithUsageAccountScope 把账号白名单写入 context。
//
// 仅在管理端 vendor 请求链路调用。站长请求不写入，
// 查询点因此取不到白名单而保持全量可见。
func WithUsageAccountScope(ctx context.Context, accountIDs []int64) context.Context {
	if accountIDs == nil {
		accountIDs = []int64{}
	}
	return context.WithValue(ctx, usageAccountScopeKey{}, UsageAccountScope{AccountIDs: accountIDs})
}

// UsageAccountScopeFrom 读取账号白名单。
//
// 第二个返回值区分「不受限」（站长，false）与「受限但集合为空」
// （vendor 名下无账号，true + 空切片）。后者必须查不到任何数据 ——
// 因此调用方须以该标志判断，不可用 len(ids) == 0 代替。
func UsageAccountScopeFrom(ctx context.Context) ([]int64, bool) {
	if ctx == nil {
		return nil, false
	}
	scope, ok := ctx.Value(usageAccountScopeKey{}).(UsageAccountScope)
	if !ok {
		return nil, false
	}
	return scope.AccountIDs, true
}
