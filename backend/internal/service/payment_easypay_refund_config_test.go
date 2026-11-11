//go:build unit

package service

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestEasyPayConfig_RefundAliasPersistsCanonicallyAndCanBeEdited(t *testing.T) {
	ctx := context.Background()
	svc := &PaymentConfigService{entClient: newPaymentConfigServiceTestClient(t)}
	input := map[string]string{"refundSuccessCode": "0", "pkey": "preserved-secret"}
	inst, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeEasyPay, Name: "draft", Config: input,
	})
	require.NoError(t, err)
	require.Equal(t, "0", input["refundSuccessCode"])
	require.NotContains(t, input, "v1RefundSuccessCode")
	stored, err := svc.decryptConfig(inst.Config)
	require.NoError(t, err)
	require.Equal(t, "0", stored["v1RefundSuccessCode"])
	require.NotContains(t, stored, "refundSuccessCode")

	// Seed an official-only persisted record, as could be imported before this upgrade.
	_, err = svc.entClient.PaymentProviderInstance.UpdateOneID(inst.ID).
		SetConfig(`{"refundSuccessCode":"0","pkey":"preserved-secret"}`).Save(ctx)
	require.NoError(t, err)
	for _, patch := range []map[string]string{
		{"v1RefundSuccessCode": "1", "pkey": ""},
		{"refundSuccessCode": "0"},
		{"pid": "new-merchant"},
		{"v1RefundSuccessCode": ""},
	} {
		before := maps.Clone(patch)
		inst, err = svc.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Config: patch})
		require.NoError(t, err)
		require.Equal(t, before, patch)
		stored, err = svc.decryptConfig(inst.Config)
		require.NoError(t, err)
		require.NotContains(t, stored, "refundSuccessCode")
		require.Equal(t, "preserved-secret", stored["pkey"])
		if value, ok := patch["v1RefundSuccessCode"]; ok {
			require.Equal(t, value, stored["v1RefundSuccessCode"])
		} else {
			require.Equal(t, "0", stored["v1RefundSuccessCode"])
		}
	}
}

func TestEasyPayConfig_RejectsExplicitRefundAliasConflictEvenForDrafts(t *testing.T) {
	ctx := context.Background()
	svc := &PaymentConfigService{entClient: newPaymentConfigServiceTestClient(t)}
	conflict := map[string]string{"v1RefundSuccessCode": "1", "refundSuccessCode": "0"}
	_, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeEasyPay, Name: "invalid", Config: conflict})
	require.ErrorContains(t, err, "conflict")
	inst, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeEasyPay, Name: "draft", Config: map[string]string{"v1RefundSuccessCode": "0"}})
	require.NoError(t, err)
	_, err = svc.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Config: conflict})
	require.ErrorContains(t, err, "conflict")
	unchanged, err := svc.entClient.PaymentProviderInstance.Get(ctx, inst.ID)
	require.NoError(t, err)
	require.JSONEq(t, inst.Config, unchanged.Config)
	var stored map[string]string
	require.NoError(t, json.Unmarshal([]byte(unchanged.Config), &stored))
	require.Equal(t, "0", stored["v1RefundSuccessCode"])
}
