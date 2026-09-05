package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotRoundTripKeepsGroupModelPricing(t *testing.T) {
	svc := &APIKeyService{}
	apiKey := &APIKey{
		ID:     1,
		UserID: 2,
		Key:    "test-key",
		Status: StatusActive,
		User:   &User{ID: 2, Status: StatusActive, Role: RoleUser},
		Group: &Group{
			ID:                        3,
			Status:                    StatusActive,
			LongContextPricingEnabled: true,
			ModelPricing: []ChannelModelPricing{{
				Models:      []string{"gpt-*"},
				BillingMode: BillingModeToken,
				InputPrice:  float64Ptr(0.000002),
				Intervals: []PricingInterval{{
					TierLabel:  "long",
					MinTokens:  200000,
					MaxTokens:  intPtrForAuthCacheTest(400000),
					InputPrice: float64Ptr(0.000004),
				}},
			}},
		},
	}

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.Equal(t, apiKeyAuthSnapshotVersion, snapshot.Version)
	require.NotNil(t, snapshot.Group)
	require.True(t, snapshot.Group.LongContextPricingEnabled)
	require.Equal(t, apiKey.Group.ModelPricing, snapshot.Group.ModelPricing)

	// Mutating the source after snapshot creation must not mutate cached pricing.
	apiKey.Group.ModelPricing[0].Models[0] = "mutated"
	*apiKey.Group.ModelPricing[0].InputPrice = 99
	*apiKey.Group.ModelPricing[0].Intervals[0].MaxTokens = 999
	*apiKey.Group.ModelPricing[0].Intervals[0].InputPrice = 99
	require.Equal(t, "gpt-*", snapshot.Group.ModelPricing[0].Models[0])
	require.Equal(t, 0.000002, *snapshot.Group.ModelPricing[0].InputPrice)
	require.Equal(t, 400000, *snapshot.Group.ModelPricing[0].Intervals[0].MaxTokens)
	require.Equal(t, 0.000004, *snapshot.Group.ModelPricing[0].Intervals[0].InputPrice)

	roundTrip := svc.snapshotToAPIKey(apiKey.Key, snapshot)
	require.NotNil(t, roundTrip.Group)
	require.True(t, roundTrip.Group.LongContextPricingEnabled)
	require.Equal(t, snapshot.Group.ModelPricing, roundTrip.Group.ModelPricing)

	// Restored objects must also own their nested slices.
	snapshot.Group.ModelPricing[0].Models[0] = "snapshot-mutated"
	*snapshot.Group.ModelPricing[0].InputPrice = 88
	*snapshot.Group.ModelPricing[0].Intervals[0].MaxTokens = 888
	*snapshot.Group.ModelPricing[0].Intervals[0].InputPrice = 88
	require.Equal(t, "gpt-*", roundTrip.Group.ModelPricing[0].Models[0])
	require.Equal(t, 0.000002, *roundTrip.Group.ModelPricing[0].InputPrice)
	require.Equal(t, 400000, *roundTrip.Group.ModelPricing[0].Intervals[0].MaxTokens)
	require.Equal(t, 0.000004, *roundTrip.Group.ModelPricing[0].Intervals[0].InputPrice)
}

func intPtrForAuthCacheTest(value int) *int { return &value }
