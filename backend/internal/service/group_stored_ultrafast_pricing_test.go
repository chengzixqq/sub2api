package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 存量分组价卡只清空 ultrafast_multiplier：入参不被修改，其他字段与条目原值保留，
// 返回值与入参不共享可变数据；没有该值时原样返回。
func TestGroupStoredUltrafast_SanitizeKeepsInputAndOtherEntries(t *testing.T) {
	require.Nil(t, SanitizeStoredGroupModelPricing(nil))

	clean := []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-5"}, BillingMode: BillingModeToken, InputPrice: float64Ptr(0.000001)}}
	sameClean := SanitizeStoredGroupModelPricing(clean)
	require.Same(t, &clean[0], &sameClean[0], "clean cards are returned without copying")

	stored := []ChannelModelPricing{
		{Platform: PlatformOpenAI, Models: []string{"gpt-5"}, BillingMode: BillingModeToken, InputPrice: float64Ptr(0.000001)},
		{
			Platform:                   PlatformOpenAI,
			Models:                     []string{"gpt-5.5"},
			BillingMode:                BillingModeToken,
			InputPrice:                 float64Ptr(0.000002),
			FastMultiplier:             float64Ptr(1.5),
			UltrafastMultiplier:        float64Ptr(2.5),
			ReasoningEffortMultipliers: map[string]float64{"high": 1.2},
			Intervals:                  []PricingInterval{{MinTokens: 200000, InputPrice: float64Ptr(0.000004)}},
		},
	}
	want := cloneChannelModelPricing(stored)
	want[1].UltrafastMultiplier = nil

	got := SanitizeStoredGroupModelPricing(stored)
	require.Equal(t, want, got)
	require.NotNil(t, stored[1].UltrafastMultiplier)
	require.Equal(t, 2.5, *stored[1].UltrafastMultiplier, "the stored card itself is not modified")

	*got[0].InputPrice = 9
	*got[1].FastMultiplier = 9
	got[1].ReasoningEffortMultipliers["high"] = 9
	got[1].Models[0] = "mutated"
	*got[1].Intervals[0].InputPrice = 9
	require.Equal(t, 0.000001, *stored[0].InputPrice)
	require.Equal(t, 1.5, *stored[1].FastMultiplier)
	require.Equal(t, 1.2, stored[1].ReasoningEffortMultipliers["high"])
	require.Equal(t, "gpt-5.5", stored[1].Models[0])
	require.Equal(t, 0.000004, *stored[1].Intervals[0].InputPrice)
}

// 修复前写入缓存的鉴权快照（版本号相同，仍会被采用）里分组卡带 ultrafast_multiplier：
// 恢复后的分组卡不再带该值，分组计价与不带该值的同一张卡一致，缓存中的快照保持原样。
func TestGroupStoredUltrafast_StaleAuthSnapshotIsNotUsedForGroupPricing(t *testing.T) {
	const stale = 2.5
	groupInput := 0.000002
	groupOutput := 0.000008
	apiKey := &APIKey{
		ID: 91, UserID: 92, Key: "sk-stale-group-ultrafast", Status: StatusActive,
		User: &User{ID: 92, Status: StatusActive},
		Group: &Group{
			ID: 93, Name: "stale-snapshot", Platform: PlatformOpenAI, Status: StatusActive,
			ModelPricing: []ChannelModelPricing{
				{Platform: PlatformOpenAI, Models: []string{"gpt-5.4"}, BillingMode: BillingModeToken, InputPrice: &groupInput, OutputPrice: &groupOutput, UltrafastMultiplier: float64Ptr(stale)},
				{Platform: PlatformOpenAI, Models: []string{"gpt-5"}, BillingMode: BillingModeToken, InputPrice: &groupInput},
			},
		},
	}
	svc := &APIKeyService{}
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	require.Contains(t, string(payload), `"ultrafast_multiplier":2.5`, "fixture must be a stale snapshot")
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	want := cloneChannelModelPricing(cached.Snapshot.Group.ModelPricing)
	want[0].UltrafastMultiplier = nil
	require.Equal(t, want, materialized.Group.ModelPricing)
	require.NotNil(t, cached.Snapshot.Group.ModelPricing[0].UltrafastMultiplier, "the cached snapshot is shared and must stay unchanged")
	require.Equal(t, stale, *cached.Snapshot.Group.ModelPricing[0].UltrafastMultiplier)

	resolver := NewModelPricingResolver(nil, NewBillingService(&config.Config{}, nil))
	resolved := resolver.Resolve(context.Background(), PricingInput{Model: "gpt-5.4", Group: materialized.Group})
	require.Equal(t, PricingSourceGroup, resolved.Source, "the group card still takes precedence")
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, groupInput, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.Nil(t, resolved.BasePricing.UltrafastMultiplierOverride)
	effective := configuredServiceTierMultiplier(OpenAIFastTierUltrafast, resolved.BasePricing)
	require.NotEqual(t, stale, effective)

	baseline := resolver.Resolve(context.Background(), PricingInput{Model: "gpt-5.4", Group: &Group{ID: 93, Platform: PlatformOpenAI, ModelPricing: want}})
	require.Equal(t, configuredServiceTierMultiplier(OpenAIFastTierUltrafast, baseline.BasePricing), effective)
}
