package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEasyPayRefundSuccessCodeDialect(t *testing.T) {
	for _, tc := range []struct {
		body     string
		expected int
		ok       bool
	}{
		{`{"code":1}`, 1, true}, {`{"code":"1"}`, 1, true},
		{`{"code":0}`, 0, true}, {`{"code":"0"}`, 0, true},
		{`{"code":0}`, 1, false}, {`{"code":1}`, 0, false},
		{`{"code":1.5}`, 1, false}, {`{"code":0.5}`, 0, false},
		{`{"code":1.0000000000000001}`, 1, false}, {`{"code":1e0}`, 1, false},
		{`{"code":true}`, 1, false}, {`{}`, 0, false},
	} {
		if err := parseEasyPayRefundResponse(200, []byte(tc.body), tc.expected); (err == nil) != tc.ok {
			t.Fatalf("body=%s expected=%d: %v", tc.body, tc.expected, err)
		}
	}
}

func TestEasyPayRefundDialectConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"code":0}`)) }))
	defer server.Close()
	for _, configured := range []string{"", "0", "1", "1.5", "either"} {
		cfg := map[string]string{"pid": "1001", "pkey": "key", "apiBase": server.URL, "notifyUrl": "https://merchant.example/notify", "returnUrl": "https://merchant.example/return", "v1RefundSuccessCode": configured}
		p, err := NewEasyPay("test", cfg)
		if configured == "1.5" || configured == "either" {
			if err == nil {
				t.Fatalf("accepted invalid configuration %q", configured)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Refund(context.Background(), payment.RefundRequest{OrderID: "o-1", Amount: "1.00"})
		if (err == nil) != (configured == "0") {
			t.Fatalf("configured=%q error=%v", configured, err)
		}
	}
}

func TestEasyPayCreateURLSchemeFallback(t *testing.T) {
	for _, tc := range []struct {
		body, want string
		fail       bool
	}{
		{`{"code":1,"urlscheme":"alipays://platformapi/startapp?appId=1"}`, "alipays://platformapi/startapp?appId=1", false},
		{`{"code":1,"urlscheme":"weixin://wxpay/bizpayurl?pr=1"}`, "weixin://wxpay/bizpayurl?pr=1", false},
		{`{"code":1,"payurl":"https://pay.example/pc","payurl2":"https://pay.example/mobile","urlscheme":"alipays://pay"}`, "https://pay.example/mobile", false},
		{`{"code":1,"urlscheme":"javascript:alert(1)"}`, "", true},
		{`{"code":1,"urlscheme":"file://host/file"}`, "", true},
		{`{"code":1,"urlscheme":"alipays://user@host/pay"}`, "", true},
		{`{"code":1}`, "", true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer srv.Close()
			p := newTestEasyPay(t, srv.URL)
			resp, err := p.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "o-1", Amount: "1.00", PaymentType: "alipay", IsMobile: true})
			if (err != nil) != tc.fail {
				t.Fatalf("response=%+v error=%v", resp, err)
			}
			if !tc.fail && resp.PayURL != tc.want {
				t.Fatalf("pay URL=%q want=%q", resp.PayURL, tc.want)
			}
		})
	}
}
