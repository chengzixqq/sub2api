//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestEasyPayConfig_ProtocolCannotChangeAfterHistoricalOrder(t *testing.T) {
	for _, status := range []string{OrderStatusCompleted, OrderStatusCancelled, OrderStatusExpired, OrderStatusRefunded, OrderStatusRefundPending} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "protocol-history")
			id, err := strconv.ParseInt(*order.ProviderInstanceID, 10, 64)
			require.NoError(t, err)
			_, err = client.PaymentProviderInstance.UpdateOneID(id).SetProviderKey(payment.TypeEasyPay).SetSupportedTypes(payment.TypeAlipay).SetEnabled(false).SetConfig(`{"pid":"merchant"}`).Save(ctx)
			require.NoError(t, err)
			_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(status).Save(ctx)
			require.NoError(t, err)
			svc := &PaymentConfigService{entClient: client}
			_, err = svc.UpdateProviderInstance(ctx, id, UpdateProviderInstanceRequest{Config: map[string]string{"apiVersion": "v2"}})
			require.Equal(t, "PROVIDER_PROTOCOL_LOCKED", infraerrors.Reason(err))
			inst, err := client.PaymentProviderInstance.Get(ctx, id)
			require.NoError(t, err)
			require.NotContains(t, inst.Config, "v2")
		})
	}
}

func TestEasyPayConfig_NewDisabledInstanceCanSelectProtocolButRejectUnknown(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}
	inst, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeEasyPay, Name: "draft", SupportedTypes: []string{payment.TypeAlipay}, Config: map[string]string{"apiVersion": "v2"}})
	require.NoError(t, err)
	_, err = svc.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Config: map[string]string{"apiVersion": "v1"}})
	require.NoError(t, err)
	_, err = svc.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Config: map[string]string{"apiVersion": "v3"}})
	require.Error(t, err)
	_, err = svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeEasyPay, Name: "invalid", SupportedTypes: []string{payment.TypeAlipay}, Config: map[string]string{"apiVersion": "v3"}})
	require.Error(t, err)
}

func TestEasyPayConfig_MasksRSAKeysAndPreservesOmittedSecrets(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}
	inst, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeEasyPay, Name: "draft", SupportedTypes: []string{payment.TypeAlipay}, Config: map[string]string{"apiVersion": "v2", "pid": "merchant", "pkey": "secret-md5", "privateKey": "secret-private", "publicKey": "secret-public"}})
	require.NoError(t, err)
	for _, field := range []string{"pkey", "privateKey", "publicKey"} {
		require.True(t, isSensitiveProviderConfigField(payment.TypeEasyPay, field))
	}
	created, err := svc.ProviderInstanceResponse(inst)
	require.NoError(t, err)
	inst, err = svc.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Config: map[string]string{"privateKey": "", "publicKey": "", "pkey": ""}})
	require.NoError(t, err)
	updated, err := svc.ProviderInstanceResponse(inst)
	require.NoError(t, err)
	listed, err := svc.ListProviderInstancesWithConfig(ctx)
	require.NoError(t, err)
	for _, dto := range []ProviderInstanceResponse{created, updated, listed[0]} {
		body, err := json.Marshal(dto)
		require.NoError(t, err)
		require.NotContains(t, string(body), "secret-")
		require.Equal(t, "v2", dto.Config["apiVersion"])
		require.Equal(t, "merchant", dto.Config["pid"])
		require.Equal(t, []string{payment.TypeAlipay}, dto.SupportedTypes)
	}
	stored, err := svc.decryptConfig(inst.Config)
	require.NoError(t, err)
	require.Equal(t, "secret-private", stored["privateKey"])
	require.Equal(t, "secret-public", stored["publicKey"])
	require.Equal(t, "secret-md5", stored["pkey"])
	redacted := RedactAuditBody([]byte(`{"config":{"privateKey":"secret-private","publicKey":"secret-public","pkey":"secret-md5"}}`), "application/json")
	require.NotContains(t, redacted, "secret-")
}

func TestEasyPaySnapshot_ProtocolValidatedByQueryWebhookAndRefundConsumers(t *testing.T) {
	for _, version := range []string{"", "v1", "v2", "invalid"} {
		t.Run("snapshot="+version, func(t *testing.T) {
			ctx := context.Background()
			svc, order, prov := newManagedRefundFixture(t)
			snapshot := order.ProviderSnapshot
			if version == "" {
				delete(snapshot, "api_version")
			} else {
				snapshot["api_version"] = version
			}
			var err error
			order, err = svc.entClient.PaymentOrder.UpdateOneID(order.ID).SetProviderSnapshot(snapshot).Save(ctx)
			require.NoError(t, err)
			for name, call := range map[string]func() (payment.Provider, error){
				"query":  func() (payment.Provider, error) { return svc.getOrderProvider(ctx, order) },
				"refund": func() (payment.Provider, error) { return svc.getRefundProvider(ctx, order) },
				"webhook": func() (payment.Provider, error) {
					return svc.GetWebhookProvider(ctx, payment.TypeEasyPay, order.OutTradeNo)
				},
			} {
				actual, err := call()
				if version == "v2" {
					require.NoError(t, err, name)
					require.Same(t, prov, actual)
				} else {
					require.Error(t, err, name)
					require.Nil(t, actual)
				}
			}
		})
	}
}

func TestEasyPaySnapshot_DefaultsLegacyConfigToV1(t *testing.T) {
	for _, version := range []string{"", "v1", " V2 "} {
		snapshot := buildPaymentOrderProviderSnapshot(&payment.InstanceSelection{InstanceID: "1", ProviderKey: payment.TypeEasyPay, Config: map[string]string{"apiVersion": version}}, CreateOrderRequest{})
		expected, err := payment.NormalizeEasyPayAPIVersion(version)
		require.NoError(t, err)
		require.Equal(t, expected, snapshot["api_version"])
		parsed := psOrderProviderSnapshot(&dbent.PaymentOrder{ProviderSnapshot: snapshot})
		require.Equal(t, expected, parsed.APIVersion)
	}
}

func TestEasyPayConfig_UnresolvedRefundProtectsProviderCredentialsAndDeletion(t *testing.T) {
	for _, status := range []string{OrderStatusRefundRequested, OrderStatusRefunding, OrderStatusRefundPending} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			svc, order, _ := newManagedRefundFixture(t)
			_, err := svc.entClient.PaymentOrder.UpdateOneID(order.ID).SetStatus(status).Save(ctx)
			require.NoError(t, err)
			id, err := strconv.ParseInt(*order.ProviderInstanceID, 10, 64)
			require.NoError(t, err)
			configSvc := &PaymentConfigService{entClient: svc.entClient}
			for _, field := range []string{"pid", "apiBase", "privateKey", "publicKey"} {
				_, err = configSvc.UpdateProviderInstance(ctx, id, UpdateProviderInstanceRequest{Config: map[string]string{field: "changed"}})
				require.Equal(t, "PENDING_ORDERS", infraerrors.Reason(err), field)
			}
			err = configSvc.DeleteProviderInstance(ctx, id)
			require.Equal(t, "PENDING_ORDERS", infraerrors.Reason(err))
			_, err = configSvc.UpdateProviderInstance(ctx, id, UpdateProviderInstanceRequest{Enabled: boolPtrValue(false)})
			require.Equal(t, "PENDING_ORDERS", infraerrors.Reason(err))
		})
	}
}

func TestEasyPayConfig_StaleSelectionCannotCreateFirstOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	inst, err := client.PaymentProviderInstance.Create().SetName("selection").SetProviderKey(payment.TypeEasyPay).SetConfig(`{"apiVersion":"v2","pid":"changed"}`).SetSupportedTypes(payment.TypeAlipay).SetEnabled(true).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	for _, config := range []map[string]string{{"apiVersion": "v1", "pid": "changed"}, {"apiVersion": "v2", "pid": "old"}} {
		tx, err := client.Tx(ctx)
		require.NoError(t, err)
		err = svc.validateSelectedEasyPayInstance(ctx, tx.Client(), &payment.InstanceSelection{InstanceID: strconv.FormatInt(inst.ID, 10), ProviderKey: payment.TypeEasyPay, Config: config})
		require.Equal(t, "PROVIDER_SELECTION_CHANGED", infraerrors.Reason(err))
		require.NoError(t, tx.Rollback())
	}
	count, err := client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}
