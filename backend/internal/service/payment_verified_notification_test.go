//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestVerifiedPaymentNotification_UsesExactProviderOnce(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{
		TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88,
	})
	otherProvider := &paymentOrderLifecycleQueryProvider{resp: &payment.QueryOrderResponse{
		Status: payment.ProviderStatusPending,
	}}
	fx.service.registry = payment.NewRegistry()
	fx.service.registry.Register(otherProvider)
	n := &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "real-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}

	require.NoError(t, fx.service.HandleVerifiedPaymentNotification(fx.ctx, n, fx.provider))
	require.Equal(t, 1, fx.provider.queryCalls)
	require.Zero(t, otherProvider.queryCalls)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "real-trade", reloaded.PaymentTradeNo)

	// Completed callbacks must still succeed when the upstream becomes unavailable.
	fx.provider.resp = nil
	require.NoError(t, fx.service.HandleVerifiedPaymentNotification(fx.ctx, n, fx.provider))
	require.Equal(t, 1, fx.provider.queryCalls)
	require.Zero(t, otherProvider.queryCalls)
}

func TestVerifiedPaymentNotification_UnknownOrderDoesNotQuery(t *testing.T) {
	fx := newPaymentNotificationFixture(t, nil)
	err := fx.service.HandleVerifiedPaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: "unknown-order", TradeNo: "real-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, fx.provider)
	require.ErrorIs(t, err, ErrOrderNotFound)
	require.Zero(t, fx.provider.queryCalls)
}

func TestVerifiedPaymentNotification_RejectsCallbackTradeMismatch(t *testing.T) {
	fx := newPaymentNotificationFixture(t, &payment.QueryOrderResponse{
		TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88,
	})
	err := fx.service.HandleVerifiedPaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "different-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, fx.provider)
	require.ErrorContains(t, err, "transaction id mismatch with notification")
	require.Equal(t, 1, fx.provider.queryCalls)
	reloaded, err := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
	require.Empty(t, reloaded.PaymentTradeNo)
}

func TestVerifiedPaymentNotification_RejectsMissingProvider(t *testing.T) {
	fx := newPaymentNotificationFixture(t, nil)
	err := fx.service.HandleVerifiedPaymentNotification(fx.ctx, &payment.PaymentNotification{
		OrderID: fx.order.OutTradeNo, TradeNo: "real-trade", Amount: 88, Status: payment.NotificationStatusSuccess,
	}, nil)
	require.ErrorContains(t, err, "verified payment provider is missing")
	require.Zero(t, fx.provider.queryCalls)
}
