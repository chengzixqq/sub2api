//go:build unit

package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/stretchr/testify/require"
)

type paymentNotificationFixture struct {
	client   *dbent.Client
	ctx      context.Context
	order    *dbent.PaymentOrder
	service  *PaymentService
	provider *paymentOrderLifecycleQueryProvider
}

func newPaymentNotificationFixture(t *testing.T, upstream *payment.QueryOrderResponse) paymentNotificationFixture {
	t.Helper()
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	user, err := client.User.Create().
		SetEmail("notify-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.com").
		SetPasswordHash("hash").
		SetUsername("notify-user").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("NOTIFY-" + strconv.FormatInt(time.Now().UnixNano(), 10)).
		SetOutTradeNo("sub2_notify_" + strconv.FormatInt(time.Now().UnixNano(), 10)).
		SetPaymentType(payment.TypeAlipay).
		SetProviderKey(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Username: user.Username}}
	userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		userRepo.getByIDUser.Balance += amount
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{codesByCode: map[string]*RedeemCode{
		order.RechargeCode: {ID: 1, Code: order.RechargeCode, Type: RedeemTypeBalance, Value: order.Amount, Status: StatusUnused},
	}}
	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{resp: upstream}
	registry.Register(provider)
	return paymentNotificationFixture{
		client: client, ctx: ctx, order: order, provider: provider,
		service: &PaymentService{
			entClient: client, registry: registry, providersLoaded: true, userRepo: userRepo,
			redeemService: NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil),
		},
	}
}

func TestPaymentNotificationRequiresPaidUpstreamOrder(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "upstream-paid", Status: payment.ProviderStatusPaid, Amount: 88})
	err := fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "callback-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.NoError(t, err)
	require.Equal(t, 1, fx.provider.queryCalls)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}

func TestPaymentNotificationRejectsPendingUpstreamOrder(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "upstream-pending", Status: payment.ProviderStatusPending, Amount: 88})
	err := fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "callback-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.ErrorContains(t, err, "not paid")
	require.Equal(t, 1, fx.provider.queryCalls)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
}

func TestPaymentNotificationRejectsUpstreamAmountMismatch(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "upstream-wrong-amount", Status: payment.ProviderStatusPaid, Amount: 87})
	err := fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "callback-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.ErrorContains(t, err, "upstream amount mismatch")
	require.Equal(t, 1, fx.provider.queryCalls)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
}

func TestPaymentNotificationRejectsOneCentUnderpayment(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "upstream-underpaid", Status: payment.ProviderStatusPaid, Amount: 9.99})
	_, err := fx.client.PaymentOrder.UpdateOneID(fx.order.ID).SetPayAmount(10).Save(fx.ctx)
	require.NoError(t, err)
	err = fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "callback-trade", Amount: 9.99, Status: payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.ErrorContains(t, err, "amount mismatch")
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
	require.Empty(t, reloaded.PaymentTradeNo)
}

func TestPaymentNotificationRejectsOneCentUnderpaymentFromUpstream(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "upstream-underpaid", Status: payment.ProviderStatusPaid, Amount: 9.99})
	_, err := fx.client.PaymentOrder.UpdateOneID(fx.order.ID).SetPayAmount(10).Save(fx.ctx)
	require.NoError(t, err)
	err = fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "callback-trade", Amount: 10, Status: payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.ErrorContains(t, err, "upstream amount mismatch")
	require.Equal(t, 1, fx.provider.queryCalls)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
	require.Empty(t, reloaded.PaymentTradeNo)
}

func TestPaymentNotificationRejectsDifferentUpstreamTransaction(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "queried-trade", Status: payment.ProviderStatusPaid, Amount: 88})
	_, err := fx.client.PaymentOrder.UpdateOneID(fx.order.ID).SetPaymentTradeNo("created-trade").Save(fx.ctx)
	require.NoError(t, err)
	err = fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "callback-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.ErrorContains(t, err, "transaction id mismatch")
	require.Equal(t, 1, fx.provider.queryCalls)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
	require.Equal(t, "created-trade", reloaded.PaymentTradeNo)
}

func TestPaymentNotificationReplacesLocalOrderReferenceWithUpstreamTransaction(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88})
	_, err := fx.client.PaymentOrder.UpdateOneID(fx.order.ID).SetPaymentTradeNo(fx.order.OutTradeNo).Save(fx.ctx)
	require.NoError(t, err)
	err = fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "real-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.NoError(t, err)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "real-trade", reloaded.PaymentTradeNo)
}

func TestPaymentNotificationRejectsUnverifiedUpstreamEvidence(t *testing.T) {
	tests := []struct {
		name     string
		response *payment.QueryOrderResponse
		wantErr  string
	}{
		{name: "missing response", wantErr: "not paid"},
		{name: "missing transaction", response: &payment.QueryOrderResponse{Status: payment.ProviderStatusPaid, Amount: 88}, wantErr: "transaction id is missing"},
		{name: "different order", response: &payment.QueryOrderResponse{TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88, Metadata: map[string]string{"out_trade_no": "another-order"}}, wantErr: "order ID mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newPaymentNotificationFixture(t, tt.response)
			err := fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
				OrderID: fx.order.OutTradeNo, TradeNo: "forged-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
			}, payment.TypeAlipay)
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, 1, fx.provider.queryCalls)
			reloaded, reloadErr := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
			require.NoError(t, reloadErr)
			require.Equal(t, OrderStatusPending, reloaded.Status)
			require.Empty(t, reloaded.PaymentTradeNo)
		})
	}
}

func TestPaymentNotificationIsIdempotentAfterUpstreamConfirmation(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88})
	n := &payment.PaymentNotification{OrderID: fx.order.OutTradeNo, TradeNo: "forged-trade", Amount: 88, Status: payment.NotificationStatusSuccess}
	require.NoError(t, fx.service.HandlePaymentNotification(fx.ctx, n, payment.TypeAlipay))
	require.NoError(t, fx.service.HandlePaymentNotification(fx.ctx, n, payment.TypeAlipay))
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "real-trade", reloaded.PaymentTradeNo)
}

func TestValidateUpstreamOrderIdentityRequiresEasyPayAndJupitongOrderID(t *testing.T) {
	order := &dbent.PaymentOrder{OutTradeNo: "expected-order"}
	for _, key := range []string{payment.TypeEasyPay, "jupitong"} {
		require.ErrorContains(t, validateUpstreamOrderIdentity(order, key, nil), "missing")
		require.ErrorContains(t, validateUpstreamOrderIdentity(order, key, map[string]string{"out_trade_no": "different-order"}), "mismatch")
		require.NoError(t, validateUpstreamOrderIdentity(order, key, map[string]string{"out_trade_no": order.OutTradeNo}))
	}
}

func TestPaymentNotificationQueriesProviderPinnedToOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api.php", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"trade_status":"TRADE_SUCCESS","money":"88","trade_no":"pinned-upstream","out_trade_no":"` + r.FormValue("out_trade_no") + `","pid":"merchant-1"}`))
	}))
	defer server.Close()

	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{TradeNo: "unused-registry-provider", Status: payment.ProviderStatusPaid, Amount: 88})
	instance, err := fx.client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("pinned-easypay").
		SetConfig(encryptWebhookProviderConfig(t, map[string]string{
			"pid": "merchant-1", "pkey": "test-key", "apiBase": server.URL,
			"notifyUrl": "https://example.com/notify", "returnUrl": "https://example.com/return",
		})).
		SetSupportedTypes("alipay").
		SetEnabled(true).
		Save(fx.ctx)
	require.NoError(t, err)
	fx.order, err = fx.client.PaymentOrder.UpdateOneID(fx.order.ID).
		SetProviderKey(payment.TypeEasyPay).
		SetProviderInstanceID(strconv.FormatInt(instance.ID, 10)).
		Save(fx.ctx)
	require.NoError(t, err)
	fx.service.loadBalancer = newWebhookProviderTestLoadBalancer(fx.client)

	err = fx.service.HandlePaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "callback-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, payment.TypeEasyPay)
	require.NoError(t, err)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "pinned-upstream", reloaded.PaymentTradeNo)
}

func TestEasyPayDelimiterCollisionCallbackCannotCreditUnpaidOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api.php", r.URL.Path)
		_, _ = w.Write([]byte(`{"code":1,"trade_status":"WAIT_BUYER_PAY","money":"88","out_trade_no":"` + r.FormValue("out_trade_no") + `","pid":"merchant-1"}`))
	}))
	defer server.Close()

	fx := newPaymentNotificationFixture(t, nil)
	config := map[string]string{
		"pid": "merchant-1", "pkey": "local-test-key", "apiBase": server.URL,
		"notifyUrl": "https://example.com/notify", "returnUrl": "https://example.com/return",
	}
	instance, err := fx.client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("pinned-easypay").
		SetConfig(encryptWebhookProviderConfig(t, config)).
		SetSupportedTypes("alipay").
		SetEnabled(true).
		Save(fx.ctx)
	require.NoError(t, err)
	fx.order, err = fx.client.PaymentOrder.UpdateOneID(fx.order.ID).
		SetProviderInstanceID(strconv.FormatInt(instance.ID, 10)).
		Save(fx.ctx)
	require.NoError(t, err)
	fx.service.loadBalancer = newWebhookProviderTestLoadBalancer(fx.client)

	// The legacy signing text is identical when trade_status is embedded in
	// trade_no and when it appears as a separate callback field.
	base := "money=88&out_trade_no=" + fx.order.OutTradeNo + "&pid=merchant-1&trade_no=FAKE&trade_status=TRADE_SUCCESS"
	sum := md5.Sum([]byte(base + config["pkey"]))
	values := url.Values{
		"money": {"88"}, "out_trade_no": {fx.order.OutTradeNo}, "pid": {"merchant-1"},
		"trade_no": {"FAKE"}, "trade_status": {"TRADE_SUCCESS"},
		"sign_type": {"MD5"}, "sign": {hex.EncodeToString(sum[:])},
	}
	prov, err := provider.NewEasyPay("test", config)
	require.NoError(t, err)
	n, err := prov.VerifyNotification(fx.ctx, values.Encode(), nil)
	require.NoError(t, err)
	require.Equal(t, payment.NotificationStatusSuccess, n.Status)

	err = fx.service.HandlePaymentNotification(fx.ctx, n, payment.TypeEasyPay)
	require.ErrorContains(t, err, "not paid")
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
	require.Empty(t, reloaded.PaymentTradeNo)
}
