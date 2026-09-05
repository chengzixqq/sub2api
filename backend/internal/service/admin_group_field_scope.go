package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 分组更新的字段级作用域过滤。
//
// 路由白名单只能判定「能否发起 PUT /groups/:id」，粒度到不了字段。
// 而 GroupOps 与 GroupBilling 是两张独立的权限档：只给了运营权的供应商
// 不该能改倍率和限额，只给了计费权的不该能改模型路由与降级链。
// 因此真正的隔离必须落在这里 —— 按档丢弃无权字段。
//
// 丢弃而非报错：分组编辑表单总是提交整个对象，一律报错会让
// 只想改个名字的供应商反复吃 400 且无从判断是哪个字段越权。

// billingGroupFields 是受 GroupBilling 约束的字段。
//
// 判定口径：直接决定「一次请求收多少钱」或「花到多少钱截断」的字段。
// 倍率、各档限额、图片/视频/搜索定价都在内。
func clearBillingFields(in *UpdateGroupInput) []string {
	var dropped []string
	drop := func(name string, clear func()) {
		clear()
		dropped = append(dropped, name)
	}

	if in.RateMultiplier != nil {
		drop("rate_multiplier", func() { in.RateMultiplier = nil })
	}
	if in.DailyLimitUSD != nil {
		drop("daily_limit_usd", func() { in.DailyLimitUSD = nil })
	}
	if in.WeeklyLimitUSD != nil {
		drop("weekly_limit_usd", func() { in.WeeklyLimitUSD = nil })
	}
	if in.MonthlyLimitUSD != nil {
		drop("monthly_limit_usd", func() { in.MonthlyLimitUSD = nil })
	}
	if in.LongContextPricingEnabled != nil {
		drop("long_context_pricing_enabled", func() { in.LongContextPricingEnabled = nil })
	}
	if in.ModelPricing != nil {
		drop("model_pricing", func() { in.ModelPricing = nil })
	}
	if in.ImageRateIndependent != nil {
		drop("image_rate_independent", func() { in.ImageRateIndependent = nil })
	}
	if in.ImageRateMultiplier != nil {
		drop("image_rate_multiplier", func() { in.ImageRateMultiplier = nil })
	}
	if in.BatchImageDiscountMultiplier != nil {
		drop("batch_image_discount_multiplier", func() { in.BatchImageDiscountMultiplier = nil })
	}
	if in.BatchImageHoldMultiplier != nil {
		drop("batch_image_hold_multiplier", func() { in.BatchImageHoldMultiplier = nil })
	}
	if in.VideoRateIndependent != nil {
		drop("video_rate_independent", func() { in.VideoRateIndependent = nil })
	}
	if in.VideoRateMultiplier != nil {
		drop("video_rate_multiplier", func() { in.VideoRateMultiplier = nil })
	}
	if in.PeakRateEnabled != nil {
		drop("peak_rate_enabled", func() { in.PeakRateEnabled = nil })
	}
	if in.PeakStart != nil {
		drop("peak_start", func() { in.PeakStart = nil })
	}
	if in.PeakEnd != nil {
		drop("peak_end", func() { in.PeakEnd = nil })
	}
	if in.PeakRateMultiplier != nil {
		drop("peak_rate_multiplier", func() { in.PeakRateMultiplier = nil })
	}
	if in.ImagePrice1K != nil {
		drop("image_price_1k", func() { in.ImagePrice1K = nil })
	}
	if in.ImagePrice2K != nil {
		drop("image_price_2k", func() { in.ImagePrice2K = nil })
	}
	if in.ImagePrice4K != nil {
		drop("image_price_4k", func() { in.ImagePrice4K = nil })
	}
	if in.VideoPrice480P != nil {
		drop("video_price_480p", func() { in.VideoPrice480P = nil })
	}
	if in.VideoPrice720P != nil {
		drop("video_price_720p", func() { in.VideoPrice720P = nil })
	}
	if in.VideoPrice1080P != nil {
		drop("video_price_1080p", func() { in.VideoPrice1080P = nil })
	}
	if in.VideoModelPrices != nil {
		drop("video_model_prices", func() { in.VideoModelPrices = nil })
	}
	if in.WebSearchPricePerCall != nil {
		drop("web_search_price_per_call", func() { in.WebSearchPricePerCall = nil })
	}
	if in.SearchPricePer1k != nil {
		drop("search_price_per_1k", func() { in.SearchPricePer1k = nil })
	}
	if in.AudioRealtimePricePerMin != nil {
		drop("audio_realtime_price_per_min", func() { in.AudioRealtimePricePerMin = nil })
	}
	if in.AudioTTSPricePerMillionChars != nil {
		drop("audio_tts_price_per_million_chars", func() { in.AudioTTSPricePerMillionChars = nil })
	}
	if in.AudioSTTPricePerHour != nil {
		drop("audio_stt_price_per_hour", func() { in.AudioSTTPricePerHour = nil })
	}
	if in.ProfitControlEnabled != nil {
		drop("profit_control_enabled", func() { in.ProfitControlEnabled = nil })
	}
	if in.ProfitMinMargin != nil {
		drop("profit_min_margin", func() { in.ProfitMinMargin = nil })
	}
	if in.ProfitSafetyBuffer != nil {
		drop("profit_safety_buffer", func() { in.ProfitSafetyBuffer = nil })
	}
	return dropped
}

// clearOpsFields 清除受 GroupOps 约束的运营字段。
//
// 判定口径：影响「请求怎么走、走到哪」的调度与路由配置。
// 也包含 Name/Description/Platform/Status 这类身份与开关字段 ——
// 只有计费权的供应商不该能改分组名或停用整个分组。
func clearOpsFields(in *UpdateGroupInput) []string {
	var dropped []string
	drop := func(name string, clear func()) {
		clear()
		dropped = append(dropped, name)
	}

	if in.Name != "" {
		drop("name", func() { in.Name = "" })
	}
	if in.Description != nil {
		drop("description", func() { in.Description = nil })
	}
	if in.Platform != "" {
		drop("platform", func() { in.Platform = "" })
	}
	if in.Status != "" {
		drop("status", func() { in.Status = "" })
	}
	if in.IsExclusive != nil {
		drop("is_exclusive", func() { in.IsExclusive = nil })
	}
	if in.AllowImageGeneration != nil {
		drop("allow_image_generation", func() { in.AllowImageGeneration = nil })
	}
	if in.AllowBatchImageGeneration != nil {
		drop("allow_batch_image_generation", func() { in.AllowBatchImageGeneration = nil })
	}
	if in.ClaudeCodeOnly != nil {
		drop("claude_code_only", func() { in.ClaudeCodeOnly = nil })
	}
	if in.FallbackGroupID != nil {
		drop("fallback_group_id", func() { in.FallbackGroupID = nil })
	}
	if in.FallbackGroupIDOnInvalidRequest != nil {
		drop("fallback_group_id_on_invalid_request", func() { in.FallbackGroupIDOnInvalidRequest = nil })
	}
	if in.ModelRouting != nil {
		drop("model_routing", func() { in.ModelRouting = nil })
	}
	if in.ModelRoutingEnabled != nil {
		drop("model_routing_enabled", func() { in.ModelRoutingEnabled = nil })
	}
	if in.MCPXMLInject != nil {
		drop("mcp_xml_inject", func() { in.MCPXMLInject = nil })
	}
	if in.SupportedModelScopes != nil {
		drop("supported_model_scopes", func() { in.SupportedModelScopes = nil })
	}
	if in.AllowMessagesDispatch != nil {
		drop("allow_messages_dispatch", func() { in.AllowMessagesDispatch = nil })
	}
	if in.AllowLive != nil {
		drop("allow_live", func() { in.AllowLive = nil })
	}
	if in.DefaultMappedModel != nil {
		drop("default_mapped_model", func() { in.DefaultMappedModel = nil })
	}
	if in.RequireOAuthOnly != nil {
		drop("require_oauth_only", func() { in.RequireOAuthOnly = nil })
	}
	if in.RequirePrivacySet != nil {
		drop("require_privacy_set", func() { in.RequirePrivacySet = nil })
	}
	if in.MessagesDispatchModelConfig != nil {
		drop("messages_dispatch_model_config", func() { in.MessagesDispatchModelConfig = nil })
	}
	if in.ModelsListConfig != nil {
		drop("models_list_config", func() { in.ModelsListConfig = nil })
	}
	if in.RPMLimit != nil {
		drop("rpm_limit", func() { in.RPMLimit = nil })
	}
	if in.MaxReasoningEffort != nil {
		drop("max_reasoning_effort", func() { in.MaxReasoningEffort = nil })
	}
	if in.ReasoningEffortMappings != nil {
		drop("reasoning_effort_mappings", func() { in.ReasoningEffortMappings = nil })
	}
	return dropped
}

// clearOwnerOnlyFields 清除站长专属字段，两张权限档都不放行。
//
// SubscriptionType 决定分组走订阅制还是标准计费，是商业模式层面的开关。
// CopyAccountsFromGroupIDs 会先清空本分组绑定再复制源分组的账号 ——
// 源分组不受授权约束，供应商可借此把别家账号搬进自己分组，
// 既拿到别人的号，又让对方的号进了自己的结算口径。
func clearOwnerOnlyFields(in *UpdateGroupInput) []string {
	var dropped []string
	if in.SubscriptionType != "" {
		in.SubscriptionType = ""
		dropped = append(dropped, "subscription_type")
	}
	if in.CopyAccountsFromGroupIDs != nil {
		in.CopyAccountsFromGroupIDs = nil
		dropped = append(dropped, "copy_accounts_from_group_ids")
	}
	return dropped
}

// applyGroupFieldScope 按权限档裁剪分组更新入参，返回被丢弃的字段名。
//
// admin 直接原样返回：工作区机制的硬约束是站长行为逐字不变。
// billingLocked 为 true 时无视 GroupBilling 档，一律清除计费字段。
func applyGroupFieldScope(ctx context.Context, in *UpdateGroupInput, billingLocked bool) []string {
	scope := ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() {
		return nil
	}

	var dropped []string
	dropped = append(dropped, clearOwnerOnlyFields(in)...)
	if billingLocked || !scope.Perms.GroupBilling {
		dropped = append(dropped, clearBillingFields(in)...)
	}
	if !scope.Perms.GroupOps {
		dropped = append(dropped, clearOpsFields(in)...)
	}

	if len(dropped) > 0 {
		// 记审计：被静默丢弃的越权字段必须留痕，否则供应商反复尝试改倍率
		// 在日志里将完全不可见。
		logger.LegacyPrintf("service.admin",
			"audit: vendor group update fields dropped workspace_id=%d billing_locked=%v dropped=%v",
			scope.WorkspaceID, billingLocked, dropped)
	}
	return dropped
}

// applyGroupUpdateScope 是 UpdateGroup 的作用域收窄入口。
//
// 共享分组的计费锁定放在这里而非权限档里：是否共享随授权变动，
// 手工开关必然与实际状态漂移，只能每次更新时实时判定。
func (s *adminServiceImpl) applyGroupUpdateScope(ctx context.Context, groupID int64, in *UpdateGroupInput) error {
	scope := ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() {
		return nil
	}

	billingLocked := false
	if scope.Perms.GroupBilling {
		if s.workspaceScopeCache == nil {
			// 与 grantedGroupIDs 同一取舍：装配疏漏时拒绝，不静默放行。
			// 这里是准入判定，失败只能收紧。
			return domain.ErrWorkspaceScopeViolation
		}
		shared, err := s.workspaceScopeCache.IsGroupShared(ctx, groupID)
		if err != nil {
			return err
		}
		billingLocked = shared
	}

	applyGroupFieldScope(ctx, in, billingLocked)
	return nil
}
