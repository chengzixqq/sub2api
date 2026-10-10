//go:build unit

package service

import (
	"context"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestInvokeProvider_StripsClientReturnQueryBeforeResumeTokenAndProvider(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("return-url@example.com").SetPasswordHash("hash").SetUsername("return-url").Save(ctx)
	require.NoError(t, err)
	resume := NewPaymentResumeService([]byte("return-url-test-signing-key"))
	svc := &PaymentService{entClient: client, resumeService: resume}
	cfg := &PaymentConfig{MaxPendingOrders: 3, OrderTimeoutMin: 30}
	sel := &payment.InstanceSelection{
		InstanceID: "1", ProviderKey: payment.TypeEasyPay, SupportedTypes: payment.TypeAlipay, PaymentMode: "popup",
		Config: map[string]string{
			"pid": "test-merchant", "pkey": "test-key", "apiBase": "https://provider.example",
			"notifyUrl": "https://app.example.com/payment/notify", "returnUrl": "https://app.example.com/payment/result", "paymentMode": "popup",
		},
	}
	req := CreateOrderRequest{
		UserID: user.ID, Amount: 10, PaymentType: payment.TypeAlipay, OrderType: payment.OrderTypeBalance,
		SrcHost: "app.example.com", ReturnURL: "https://app.example.com/payment/result?from=checkout&trade_status=TRADE_SUCCESS&order_id=attacker&resume_token=attacker#fragment",
	}
	order, err := svc.createOrderInTx(ctx, req, &User{ID: user.ID, Email: user.Email}, nil, cfg, 10, 10, 0, 10, 0, sel)
	require.NoError(t, err)

	result, err := svc.invokeProvider(ctx, order, req, cfg, 10, "10.00", 10, nil, sel)
	require.NoError(t, err)
	claims, err := resume.ParseToken(result.ResumeToken)
	require.NoError(t, err)
	require.Equal(t, order.ID, claims.OrderID)
	require.Equal(t, "https://app.example.com/payment/result", claims.CanonicalReturnURL,
		"the actual service entry must strip client query before signing resume state")
	payURL, err := url.Parse(result.PayURL)
	require.NoError(t, err)
	require.Equal(t, "https://app.example.com/payment/result", payURL.Query().Get("return_url"))
}
