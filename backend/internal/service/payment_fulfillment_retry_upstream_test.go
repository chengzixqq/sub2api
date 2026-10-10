//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type retryQueryErrorProvider struct {
	*paymentOrderLifecycleQueryProvider
}

func (p *retryQueryErrorProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, errors.New("query unavailable")
}

func TestRetryFulfillmentRequiresVerifiedUpstreamPayment(t *testing.T) {
	tests := []struct {
		name     string
		response *payment.QueryOrderResponse
		queryErr bool
		wantErr  string
	}{
		{name: "query failed", queryErr: true, wantErr: "upstream query failed"},
		{name: "unpaid", response: &payment.QueryOrderResponse{TradeNo: "real-trade", Status: payment.ProviderStatusPending, Amount: 88}, wantErr: "not paid"},
		{name: "wrong amount", response: &payment.QueryOrderResponse{TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 87}, wantErr: "amount mismatch"},
		{name: "wrong order", response: &payment.QueryOrderResponse{TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88, Metadata: map[string]string{"out_trade_no": "another-order"}}, wantErr: "order ID mismatch"},
		{name: "missing transaction", response: &payment.QueryOrderResponse{Status: payment.ProviderStatusPaid, Amount: 88}, wantErr: "transaction id is missing"},
		{name: "wrong transaction", response: &payment.QueryOrderResponse{TradeNo: "another-trade", Status: payment.ProviderStatusPaid, Amount: 88}, wantErr: "transaction id mismatch"},
		{name: "paid", response: &payment.QueryOrderResponse{TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newPaymentNotificationFixture(t, tt.response)
			var err error
			fx.order, err = fx.client.PaymentOrder.UpdateOneID(fx.order.ID).
				SetStatus(OrderStatusFailed).
				SetPaidAt(time.Now().Add(-time.Minute)).
				SetPaymentTradeNo("real-trade").
				Save(fx.ctx)
			require.NoError(t, err)
			if tt.queryErr {
				registry := payment.NewRegistry()
				registry.Register(&retryQueryErrorProvider{paymentOrderLifecycleQueryProvider: fx.provider})
				fx.service.registry = registry
			}

			err = fx.service.RetryFulfillment(fx.ctx, fx.order.ID)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			reloaded, reloadErr := fx.client.PaymentOrder.Get(fx.ctx, fx.order.ID)
			require.NoError(t, reloadErr)
			if tt.wantErr != "" {
				require.Equal(t, OrderStatusFailed, reloaded.Status)
			} else {
				require.Equal(t, OrderStatusCompleted, reloaded.Status)
			}
		})
	}
}

func TestVerifyPaidUpstreamOrderRejectsWrongMerchant(t *testing.T) {
	fx := newPaymentNotificationFixture(t, nil)
	fx.order.ProviderSnapshot = map[string]interface{}{"merchant_app_id": "expected-app", "provider_key": payment.TypeAlipay}
	_, _, err := fx.service.verifyPaidUpstreamOrder(fx.ctx, fx.order, &payment.QueryOrderResponse{
		TradeNo: "real-trade", Status: payment.ProviderStatusPaid, Amount: 88,
		Metadata: map[string]string{"app_id": "different-app"},
	}, payment.TypeAlipay, "")
	require.ErrorContains(t, err, "app_id mismatch")
}
