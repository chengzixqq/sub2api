package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateGroupRequestLongContextPricingDefaultsEnabled(t *testing.T) {
	var omitted CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"group"}`), &omitted))
	require.Nil(t, omitted.LongContextPricingEnabled)
	require.True(t, omitted.LongContextPricingEnabled == nil || *omitted.LongContextPricingEnabled)

	var disabled CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"group","long_context_pricing_enabled":false}`), &disabled))
	require.NotNil(t, disabled.LongContextPricingEnabled)
	require.False(t, *disabled.LongContextPricingEnabled)
}
