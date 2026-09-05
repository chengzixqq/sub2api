package service

import (
	"context"
	"slices"
	"testing"
)

// vendorFieldCtx 构造只开指定权限档的 vendor 作用域。
//
// 字段级隔离的全部意义在于「档与档之间互不透支」，
// 因此这里刻意只开一档，其余保持关闭。
func vendorFieldCtx(perms WorkspacePermissions) context.Context {
	return WithScope(context.Background(), VendorScope(7, perms))
}

// TestGroupFieldScopeBillingOnlyCannotTouchOps 锁定「只有计费权改不动运营字段」。
//
// 路由白名单对 PUT /groups/:id 的判定是 GroupOps || GroupBilling，
// 若字段级隔离缺失，任一档都等于整组配置全开 —— 只给了调价权的供应商
// 能顺手改分组名、换平台，甚至把整个分组停用。
func TestGroupFieldScopeBillingOnlyCannotTouchOps(t *testing.T) {
	desc := "改名企图"
	in := &UpdateGroupInput{
		Name:        "被改的名字",
		Description: &desc,
		Platform:    "openai",
		Status:      "inactive",
	}

	applyGroupFieldScope(vendorFieldCtx(WorkspacePermissions{GroupBilling: true}), in, false)

	if in.Name != "" || in.Description != nil || in.Platform != "" || in.Status != "" {
		t.Fatalf("运营字段应被清空，实得 name=%q desc=%v platform=%q status=%q",
			in.Name, in.Description, in.Platform, in.Status)
	}
}

// TestGroupFieldScopeOpsOnlyCannotTouchBilling 锁定「只有运营权改不动计费字段」。
//
// 倍率与限额直接决定一次请求收多少钱。供应商拿到运营权后若能改倍率，
// 等于可以单方面抬价，站长的结算口径就失守了。
func TestGroupFieldScopeOpsOnlyCannotTouchBilling(t *testing.T) {
	rate := 9.9
	daily := 100.0
	peak := true
	longContext := false
	modelPricing := []ChannelModelPricing{{Models: []string{"gpt-*"}}}
	in := &UpdateGroupInput{
		RateMultiplier:            &rate,
		DailyLimitUSD:             &daily,
		PeakRateEnabled:           &peak,
		LongContextPricingEnabled: &longContext,
		ModelPricing:              &modelPricing,
	}

	dropped := applyGroupFieldScope(vendorFieldCtx(WorkspacePermissions{GroupOps: true}), in, false)

	if in.RateMultiplier != nil || in.DailyLimitUSD != nil || in.PeakRateEnabled != nil ||
		in.LongContextPricingEnabled != nil || in.ModelPricing != nil {
		t.Fatalf("计费字段应被清空，实得 rate=%v daily=%v peak=%v long_context=%v model_pricing=%v",
			in.RateMultiplier, in.DailyLimitUSD, in.PeakRateEnabled,
			in.LongContextPricingEnabled, in.ModelPricing)
	}
	// 被丢弃的字段必须留痕，否则供应商反复试探改价在日志里完全不可见。
	if len(dropped) != 5 {
		t.Errorf("应报告 5 个被丢弃字段，实得 %v", dropped)
	}
}

// TestGroupFieldScopeBillingLockedOverridesPerm 锁定共享分组的计费锁定优先于权限档。
//
// 分组同时授权给 A、B 两家时，A 改倍率会直接改变 B 的结算金额。
// 因此共享状态一旦成立，GroupBilling 档必须失效 —— 且这个判定只能实时做，
// 手工开关必然与授权变动漂移。
func TestGroupFieldScopeBillingLockedOverridesPerm(t *testing.T) {
	rate := 0.01
	in := &UpdateGroupInput{RateMultiplier: &rate}

	applyGroupFieldScope(vendorFieldCtx(WorkspacePermissions{GroupBilling: true}), in, true)

	if in.RateMultiplier != nil {
		t.Fatal("共享分组即便持有 GroupBilling 也不得改倍率")
	}
}

// TestGroupFieldScopeOwnerOnlyFieldsAlwaysDropped 锁定站长专属字段两档都不放行。
//
// CopyAccountsFromGroupIDs 会先清空本分组绑定再复制源分组账号，
// 而源分组不受授权约束 —— 供应商可借此把别家账号搬进自己分组，
// 既拿到别人的号，又让对方的号进了自己的结算口径。
func TestGroupFieldScopeOwnerOnlyFieldsAlwaysDropped(t *testing.T) {
	all := WorkspacePermissions{
		AccountManage: true, GroupOps: true, GroupBilling: true,
		ProxyManage: true, MonitorView: true,
	}
	in := &UpdateGroupInput{
		SubscriptionType:         "subscription",
		CopyAccountsFromGroupIDs: []int64{99},
	}

	applyGroupFieldScope(vendorFieldCtx(all), in, false)

	if in.SubscriptionType != "" || in.CopyAccountsFromGroupIDs != nil {
		t.Fatalf("站长专属字段应被清空，实得 subType=%q copyFrom=%v",
			in.SubscriptionType, in.CopyAccountsFromGroupIDs)
	}
}

// TestGroupFieldScopeAdminUntouched 锁定站长入参逐字不变。
//
// 工作区机制的硬约束：admin 行为与引入之前完全一致。
// 一旦这里开始裁剪，站长自己就改不了分组了。
func TestGroupFieldScopeAdminUntouched(t *testing.T) {
	rate := 2.5
	in := &UpdateGroupInput{
		Name:             "站长改的名字",
		RateMultiplier:   &rate,
		SubscriptionType: "subscription",
	}

	dropped := applyGroupFieldScope(WithScope(context.Background(), AdminScope()), in, true)

	if len(dropped) != 0 {
		t.Errorf("站长不应有字段被丢弃，实得 %v", dropped)
	}
	if in.Name != "站长改的名字" || in.RateMultiplier == nil || in.SubscriptionType != "subscription" {
		t.Fatal("站长入参必须原样保留")
	}
}

func TestGroupFieldScopeOpsOnlyDropsAllV173BillingFields(t *testing.T) {
	value := 1.25
	enabled := true
	in := &UpdateGroupInput{
		VideoModelPrices:             map[string]map[string]float64{"grok-imagine-video": {"720p": value}},
		SearchPricePer1k:             &value,
		AudioRealtimePricePerMin:     &value,
		AudioTTSPricePerMillionChars: &value,
		AudioSTTPricePerHour:         &value,
		ProfitControlEnabled:         &enabled,
		ProfitMinMargin:              &value,
		ProfitSafetyBuffer:           &value,
	}

	dropped := applyGroupFieldScope(vendorFieldCtx(WorkspacePermissions{GroupOps: true}), in, false)

	if in.VideoModelPrices != nil || in.SearchPricePer1k != nil ||
		in.AudioRealtimePricePerMin != nil || in.AudioTTSPricePerMillionChars != nil ||
		in.AudioSTTPricePerHour != nil || in.ProfitControlEnabled != nil ||
		in.ProfitMinMargin != nil || in.ProfitSafetyBuffer != nil {
		t.Fatal("all v0.1.173 pricing and profit-control fields must require GroupBilling")
	}
	for _, field := range []string{
		"video_model_prices",
		"search_price_per_1k",
		"audio_realtime_price_per_min",
		"audio_tts_price_per_million_chars",
		"audio_stt_price_per_hour",
		"profit_control_enabled",
		"profit_min_margin",
		"profit_safety_buffer",
	} {
		if !slices.Contains(dropped, field) {
			t.Errorf("missing dropped-field audit entry %q: %v", field, dropped)
		}
	}
}

func TestApplyGroupLimitUpdatesPreservesDeniedOrOmittedValues(t *testing.T) {
	daily, weekly, monthly := 10.0, 20.0, 30.0
	group := &Group{
		DailyLimitUSD:   &daily,
		WeeklyLimitUSD:  &weekly,
		MonthlyLimitUSD: &monthly,
	}

	applyGroupLimitUpdates(group, &UpdateGroupInput{})

	if group.DailyLimitUSD == nil || *group.DailyLimitUSD != daily ||
		group.WeeklyLimitUSD == nil || *group.WeeklyLimitUSD != weekly ||
		group.MonthlyLimitUSD == nil || *group.MonthlyLimitUSD != monthly {
		t.Fatalf("omitted limit fields must preserve persisted values: %+v", group)
	}

	clear := 0.0
	applyGroupLimitUpdates(group, &UpdateGroupInput{DailyLimitUSD: &clear})
	if group.DailyLimitUSD == nil || *group.DailyLimitUSD != 0 {
		t.Fatalf("an explicit zero must remain an explicit zero limit: %v", group.DailyLimitUSD)
	}
	if group.WeeklyLimitUSD == nil || *group.WeeklyLimitUSD != weekly ||
		group.MonthlyLimitUSD == nil || *group.MonthlyLimitUSD != monthly {
		t.Fatal("updating one limit must not mutate the others")
	}
}
