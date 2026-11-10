package repository

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBillingDetailsJSONValue_NullAndPartialAndInvalid(t *testing.T) {
	v, err := (billingDetailsJSONValue{}).Value()
	require.NoError(t, err)
	require.Nil(t, v)
	details := &service.BillingDetails{Version: 1, Status: "partial_unpriced", Components: []service.BillingComponent{{Role: "advisor", Model: "unknown", InputTokens: 17}}}
	v, err = (billingDetailsJSONValue{details: details}).Value()
	require.NoError(t, err)
	var decoded service.BillingDetails
	require.NoError(t, json.Unmarshal([]byte(v.(string)), &decoded))
	require.Equal(t, *details, decoded)
	invalid := math.Inf(1)
	details.Components[0].TotalCost = &invalid
	_, err = (billingDetailsJSONValue{details: details}).Value()
	require.Error(t, err)
}
