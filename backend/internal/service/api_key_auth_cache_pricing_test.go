package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotGroupPricingRoundtrip(t *testing.T) {
	groupID := int64(50)
	inputPrice := 1e-6
	outputPrice := 2e-6
	apiKey := &APIKey{
		ID: 82, UserID: 40, GroupID: &groupID, Key: "sk-pricing-roundtrip", Status: StatusActive,
		User: &User{ID: 40, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "pricing-roundtrip", Platform: PlatformAnthropic, Status: StatusActive,
			LongContextPricingEnabled: true,
			ModelPricing: []ChannelModelPricing{{
				Models: []string{"claude-sonnet-*"}, BillingMode: BillingModeToken,
				InputPrice: &inputPrice, OutputPrice: &outputPrice,
			}},
		},
	}
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.LongContextPricingEnabled)
	require.Equal(t, apiKey.Group.ModelPricing, materialized.Group.ModelPricing)

	billing := &BillingService{fallbackPrices: map[string]*ModelPricing{
		"claude-sonnet-4": {InputPricePerToken: 3e-6, OutputPricePerToken: 15e-6},
	}}
	resolver := NewModelPricingResolver(nil, billing)
	resolved := resolver.Resolve(context.Background(), PricingInput{Model: "claude-sonnet-4", Group: materialized.Group})
	require.Equal(t, PricingSourceGroup, resolved.Source)
	require.True(t, resolved.longContextPricingEnabled)
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, outputPrice, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

func TestAPIKeyAuthSnapshotDoesNotAliasMutablePricingInputs(t *testing.T) {
	groupID := int64(7)
	limit := 3.5
	rpm := 12
	apiKey := &APIKey{
		ID: 1, UserID: 2, GroupID: &groupID, IPWhitelist: []string{"10.0.0.0/8"},
		User: &User{
			ID: 2, Status: StatusActive, AllowedGroups: []int64{7},
			BalanceNotifyThreshold: &limit, BalanceNotifyExtraEmails: []NotifyEmailEntry{{Email: "a@example.com"}},
			UserGroupRPMOverride: &rpm,
		},
		Group: &Group{
			ID: groupID, Status: StatusActive,
			ModelRouting:                map[string][]int64{"claude-*": {11, 12}},
			SupportedModelScopes:        []string{"claude"},
			MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{ExactModelMappings: map[string]string{"a": "b"}},
			ModelsListConfig:            GroupModelsListConfig{Models: []string{"m1"}},
			ReasoningEffortMappings:     []ReasoningEffortMapping{{From: "low", To: "minimal"}},
		},
	}
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	apiKey.IPWhitelist[0] = "changed"
	apiKey.User.AllowedGroups[0] = 99
	apiKey.User.BalanceNotifyExtraEmails[0].Email = "changed@example.com"
	apiKey.Group.ModelRouting["claude-*"][0] = 99
	apiKey.Group.SupportedModelScopes[0] = "changed"
	apiKey.Group.MessagesDispatchModelConfig.ExactModelMappings["a"] = "changed"
	apiKey.Group.ModelsListConfig.Models[0] = "changed"
	apiKey.Group.ReasoningEffortMappings[0].To = "changed"

	restored := svc.snapshotToAPIKey("k", snapshot)
	require.Equal(t, "10.0.0.0/8", restored.IPWhitelist[0])
	require.Equal(t, int64(7), restored.User.AllowedGroups[0])
	require.Equal(t, "a@example.com", restored.User.BalanceNotifyExtraEmails[0].Email)
	require.Equal(t, int64(11), restored.Group.ModelRouting["claude-*"][0])
	require.Equal(t, "claude", restored.Group.SupportedModelScopes[0])
	require.Equal(t, "b", restored.Group.MessagesDispatchModelConfig.ExactModelMappings["a"])
	require.Equal(t, "m1", restored.Group.ModelsListConfig.Models[0])
	require.Equal(t, "minimal", restored.Group.ReasoningEffortMappings[0].To)
}
