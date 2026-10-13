//go:build unit

package service

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// Use actual RSA notifications and query responses across the provider/service
// boundary: a provider-only status check cannot detect missing identity metadata.
func TestEasyPayV2NotificationFulfillmentRequiresVerifiedQueryAndIsIdempotent(t *testing.T) {
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	platform, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(merchant)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	require.NoError(t, err)

	for _, tc := range []struct {
		name, status, money, orderOverride, wantErr string
		wrongSigningKey                             bool
		omitPID, emptyPID, wrongPID, omitOrder      bool
	}{
		{name: "paid once", status: "1", money: "88.00"},
		{name: "paid once without optional query pid", status: "1", money: "88.00", omitPID: true},
		{name: "empty query pid", status: "1", money: "88.00", emptyPID: true, wantErr: "merchant pid mismatch"},
		{name: "wrong query pid", status: "1", money: "88.00", wrongPID: true, wantErr: "merchant pid mismatch"},
		{name: "missing query order without pid", status: "1", money: "88.00", omitPID: true, omitOrder: true, wantErr: "merchant or order mismatch"},
		{name: "wrong query order without pid", status: "1", money: "88.00", omitPID: true, orderOverride: "another-order", wantErr: "merchant or order mismatch"},
		{name: "unpaid", status: "0", money: "88.00", wantErr: "not paid"},
		{name: "wrong amount", status: "1", money: "87.99", wantErr: "amount mismatch"},
		{name: "wrong order", status: "1", money: "88.00", orderOverride: "another-order", wantErr: "merchant or order mismatch"},
		{name: "merchant signature cannot replace platform signature", status: "1", money: "88.00", wrongSigningKey: true, wantErr: "invalid signature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPaymentNotificationFixture(t, nil)
			var queryCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queryCalls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/pay/query" {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse upstream request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				params := map[string]string{}
				for key, values := range r.PostForm {
					if len(values) != 1 {
						t.Errorf("duplicate upstream field %s", key)
					}
					params[key] = values[0]
				}
				signature, err := base64.StdEncoding.DecodeString(params["sign"])
				digest := sha256.Sum256([]byte(easyPayV2FulfillmentTestSignContent(params)))
				if err != nil || rsa.VerifyPKCS1v15(&merchant.PublicKey, crypto.SHA256, digest[:], signature) != nil || params["pid"] != "merchant-v2" || params["out_trade_no"] != fx.order.OutTradeNo {
					t.Error("upstream query did not use the pinned merchant/order and merchant signature")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				orderID := fx.order.OutTradeNo
				if tc.orderOverride != "" {
					orderID = tc.orderOverride
				}
				response := map[string]string{
					"code": "0", "pid": "merchant-v2", "out_trade_no": orderID,
					"trade_no": "real-v2-trade", "status": tc.status, "money": tc.money,
					"timestamp": strconv.FormatInt(time.Now().Unix(), 10), "sign_type": "RSA",
				}
				if tc.omitPID {
					delete(response, "pid")
				} else if tc.emptyPID {
					response["pid"] = ""
				} else if tc.wrongPID {
					response["pid"] = "another-merchant"
				}
				if tc.omitOrder {
					delete(response, "out_trade_no")
				}
				signingKey := platform
				if tc.wrongSigningKey {
					signingKey = merchant
				}
				response["sign"] = easyPayV2FulfillmentTestSign(t, response, signingKey)
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Errorf("write upstream response: %v", err)
				}
			}))
			defer server.Close()

			instance, err := fx.client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeEasyPay).SetName("pinned-v2").SetSupportedTypes("alipay").SetEnabled(true).
				SetConfig(encryptWebhookProviderConfig(t, map[string]string{
					"apiVersion": "v2", "pid": "merchant-v2", "apiBase": server.URL,
					"privateKey": base64.StdEncoding.EncodeToString(privateDER), "publicKey": base64.StdEncoding.EncodeToString(publicDER),
					"notifyUrl": "https://example.com/notify", "returnUrl": "https://example.com/return",
				})).Save(fx.ctx)
			require.NoError(t, err)
			instanceID := strconv.FormatInt(instance.ID, 10)
			fx.order, err = fx.client.PaymentOrder.UpdateOneID(fx.order.ID).
				SetProviderKey(payment.TypeEasyPay).SetProviderInstanceID(instanceID).
				SetProviderSnapshot(map[string]any{"schema_version": 2, "provider_key": payment.TypeEasyPay,
					"provider_instance_id": instanceID, "api_version": "v2", "merchant_id": "merchant-v2"}).Save(fx.ctx)
			require.NoError(t, err)
			fx.service.loadBalancer = newWebhookProviderTestLoadBalancer(fx.client)
			prov, err := fx.service.GetWebhookProvider(fx.ctx, payment.TypeEasyPay, fx.order.OutTradeNo)
			require.NoError(t, err)

			callback := map[string]string{
				"pid": "merchant-v2", "out_trade_no": fx.order.OutTradeNo, "trade_no": "real-v2-trade",
				"trade_status": "TRADE_SUCCESS", "money": "88.00", "sign_type": "RSA",
				"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
			}
			callback["sign"] = easyPayV2FulfillmentTestSign(t, callback, platform)
			form := url.Values{}
			for key, value := range callback {
				form.Set(key, value)
			}
			notification, err := prov.VerifyNotification(fx.ctx, form.Encode(), nil)
			require.NoError(t, err)
			err = fx.service.HandlePaymentNotification(fx.ctx, notification, payment.TypeEasyPay)
			userRepo := fx.service.userRepo.(*mockUserRepo)
			redeemRepo := fx.service.redeemService.redeemRepo.(*paymentOrderLifecycleRedeemRepo)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				reloaded, reloadErr := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
				require.NoError(t, reloadErr)
				require.Equal(t, OrderStatusPending, reloaded.Status)
				require.Zero(t, userRepo.getByIDUser.Balance)
				require.Empty(t, redeemRepo.useCalls)
				return
			}
			require.NoError(t, err)
			require.NoError(t, fx.service.HandlePaymentNotification(fx.ctx, notification, payment.TypeEasyPay))
			reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, reloaded.Status)
			require.Equal(t, "real-v2-trade", reloaded.PaymentTradeNo)
			require.Equal(t, int32(1), queryCalls.Load())
			require.Equal(t, float64(88), userRepo.getByIDUser.Balance)
			require.Len(t, redeemRepo.useCalls, 1)
		})
	}
}

func easyPayV2FulfillmentTestSignContent(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key != "sign" && key != "sign_type" && strings.TrimSpace(value) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	return strings.Join(parts, "&")
}

func easyPayV2FulfillmentTestSign(t *testing.T, params map[string]string, key *rsa.PrivateKey) string {
	t.Helper()
	digest := sha256.Sum256([]byte(easyPayV2FulfillmentTestSignContent(params)))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(signature)
}
