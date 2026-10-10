package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestEasyPayCreatePaymentWithOrderReturnQuery(t *testing.T) {
	const returnBase = "https://app.example.com/payment/result"
	returnWithOrder := returnBase + "?order_id=42&out_trade_no=sub2_42&resume_token=token&status=success"
	for _, mode := range []string{"qrcode", paymentModePopup} {
		t.Run(mode, func(t *testing.T) {
			var posted url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/mapi.php", r.URL.Path)
				require.NoError(t, r.ParseForm())
				posted = r.PostForm
				_, _ = w.Write([]byte(`{"code":1,"trade_no":"upstream-42","payurl":"https://cashier.example.com/42"}`))
			}))
			defer server.Close()

			prov, err := NewEasyPay("test", map[string]string{
				"pid": "merchant-1", "pkey": "test-key", "apiBase": server.URL,
				"paymentMode": mode, "notifyUrl": "https://app.example.com/api/v1/payment/easypay/notify",
				"returnUrl": returnBase,
			})
			require.NoError(t, err)
			resp, err := prov.CreatePayment(context.Background(), payment.CreatePaymentRequest{
				OrderID: "sub2_42", PaymentType: payment.TypeAlipay, Amount: "12.50", Subject: "Recharge",
				ReturnURL: returnWithOrder,
			})
			require.NoError(t, err)
			var values url.Values
			if mode == paymentModePopup {
				parsed, parseErr := url.Parse(resp.PayURL)
				require.NoError(t, parseErr)
				require.Equal(t, "/submit.php", parsed.Path)
				values = parsed.Query()
			} else {
				require.Equal(t, "upstream-42", resp.TradeNo)
				values = posted
			}
			require.Equal(t, returnBase, values.Get("return_url"))
			require.Equal(t, "sub2_42", values.Get("out_trade_no"))
			params := make(map[string]string, len(values))
			for key := range values {
				params[key] = values.Get(key)
			}
			require.NoError(t, validateEasyPayLegacySignParams(params))
			require.True(t, easyPayVerifySign(params, "test-key", values.Get("sign")))
		})
	}
}
