package provider

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEasyPayVerifyNotification(t *testing.T) {
	t.Parallel()

	const key = "test-key"
	base := map[string]string{
		"pid": "merchant-1", "sign_type": "MD5", "out_trade_no": "order-1",
		"trade_no": "upstream-1", "money": "12.50", "trade_status": tradeStatusSuccess,
	}
	tests := []struct {
		name       string
		change     func(map[string]string)
		appendForm string
		wantStatus string
		wantErr    string
	}{
		{name: "paid", wantStatus: payment.ProviderStatusSuccess},
		{name: "failed without trade number or amount", change: func(p map[string]string) {
			p["trade_status"] = "TRADE_CLOSED"
			delete(p, "trade_no")
			delete(p, "money")
		}, wantStatus: payment.ProviderStatusFailed},
		{name: "missing upstream trade number", change: func(p map[string]string) { delete(p, "trade_no") }, wantErr: "trade_no"},
		{name: "missing amount", change: func(p map[string]string) { delete(p, "money") }, wantErr: "money"},
		{name: "zero amount", change: func(p map[string]string) { p["money"] = "0" }, wantErr: "amount"},
		{name: "NaN amount", change: func(p map[string]string) { p["money"] = "NaN" }, wantErr: "amount"},
		{name: "infinite amount", change: func(p map[string]string) { p["money"] = "+Inf" }, wantErr: "amount"},
		{name: "wrong merchant", change: func(p map[string]string) { p["pid"] = "merchant-2" }, wantErr: "pid"},
		{name: "missing sign type", change: func(p map[string]string) { delete(p, "sign_type") }, wantErr: "sign type"},
		{name: "unsupported sign type", change: func(p map[string]string) { p["sign_type"] = "RSA" }, wantErr: "sign type"},
		{name: "duplicate field", appendForm: "&money=12.50", wantErr: "duplicate"},
		{name: "delimiter-bearing trade number", change: func(p map[string]string) {
			p["trade_no"] = "upstream-1&trade_status=TRADE_SUCCESS"
		}, wantErr: "unsafe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			params := make(map[string]string, len(base))
			for k, v := range base {
				params[k] = v
			}
			if tt.change != nil {
				tt.change(params)
			}
			form := url.Values{}
			for k, v := range params {
				form.Set(k, v)
			}
			form.Set("sign", easyPaySign(params, key))
			provider := &EasyPay{config: map[string]string{"pid": "merchant-1", "pkey": key}}
			notification, err := provider.VerifyNotification(context.Background(), form.Encode()+tt.appendForm, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyNotification: %v", err)
			}
			if notification.Status != tt.wantStatus || notification.OrderID != "order-1" {
				t.Fatalf("notification = %+v, want status %q", notification, tt.wantStatus)
			}
			if tt.wantStatus == payment.ProviderStatusFailed && (notification.TradeNo != "" || notification.Amount != 0) {
				t.Fatalf("failed notification with omitted fields = %+v", notification)
			}
		})
	}
}

func TestEasyPayCreatePaymentRejectsAmbiguousLegacySignature(t *testing.T) {
	t.Parallel()
	provider := &EasyPay{config: map[string]string{
		"paymentMode": paymentModePopup, "pid": "merchant-1", "pkey": "test-key", "apiBase": "https://example.com",
		"notifyUrl": "https://example.com/notify", "returnUrl": "https://example.com/return",
	}}
	_, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID: "order&money=1", PaymentType: payment.TypeAlipay, Amount: "1.00", Subject: "Test",
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe easypay legacy signing parameter") {
		t.Fatalf("CreatePayment error = %v, want unsafe legacy signing input", err)
	}
}
