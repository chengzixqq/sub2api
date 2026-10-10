package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEasyPayQueryOrderStatusMapping(t *testing.T) {
	t.Parallel()

	const orderID = "order-123"
	tests := []struct {
		name        string
		body        string
		wantStatus  string
		wantTradeNo string
		wantAmount  float64
		wantErr     bool
	}{
		{
			name:        "top level trade success is paid",
			body:        `{"code":1,"pid":"pid-1","out_trade_no":"order-123","trade_status":"TRADE_SUCCESS","status":0,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "waiting trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"WAITING","status":1,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "empty trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"","status":1,"money":"12.34"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "",
			wantAmount:  12.34,
		},
		{
			name:        "nested data trade success is paid",
			body:        `{"code":1,"data":{"pid":"pid-1","out_trade_no":"order-123","trade_status":"TRADE_SUCCESS","status":0,"money":"9.99","trade_no":"data-456"}}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "data-456",
			wantAmount:  9.99,
		},
		{
			name:        "legacy numeric paid status remains compatible",
			body:        `{"code":1,"pid":"pid-1","out_trade_no":"order-123","status":1,"money":"3.21","trade_no":"gateway-789"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "gateway-789",
			wantAmount:  3.21,
		},
		{
			name:        "legacy numeric non paid status is pending",
			body:        `{"code":1,"status":0,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "",
			wantAmount:  3.21,
		},
		{
			name:        "missing fields are pending",
			body:        `{"code":1}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "",
		},
		{name: "query rejection cannot report paid", body: `{"code":0,"status":1,"trade_no":"gateway-1","pid":"pid-1","out_trade_no":"order-123"}`, wantErr: true},
		{name: "paid query requires real trade number", body: `{"code":1,"status":1,"pid":"pid-1","out_trade_no":"order-123","money":"3.21"}`, wantErr: true},
		{name: "paid query requires order ID", body: `{"code":1,"status":1,"pid":"pid-1","trade_no":"gateway-1","money":"3.21"}`, wantErr: true},
		{name: "paid query requires merchant ID", body: `{"code":1,"status":1,"out_trade_no":"order-123","trade_no":"gateway-1","money":"3.21"}`, wantErr: true},
		{name: "wrong order ID rejected", body: `{"code":1,"status":1,"pid":"pid-1","out_trade_no":"order-456","trade_no":"gateway-1","money":"3.21"}`, wantErr: true},
		{name: "wrong merchant ID rejected", body: `{"code":1,"status":1,"pid":"pid-2","out_trade_no":"order-123","trade_no":"gateway-1","money":"3.21"}`, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotForm url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want %q", r.Method, http.MethodPost)
				}
				if r.URL.Path != "/api.php" {
					t.Errorf("path = %q, want /api.php", r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				gotForm = make(url.Values, len(r.PostForm))
				for key, values := range r.PostForm {
					gotForm[key] = append([]string(nil), values...)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			resp, err := provider.QueryOrder(context.Background(), orderID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("QueryOrder accepted invalid response: %+v", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("QueryOrder returned error: %v", err)
			}
			if resp.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (response=%+v)", resp.Status, tt.wantStatus, resp)
			}
			if resp.TradeNo != tt.wantTradeNo {
				t.Fatalf("trade_no = %q, want %q", resp.TradeNo, tt.wantTradeNo)
			}
			if resp.Amount != tt.wantAmount {
				t.Fatalf("amount = %v, want %v", resp.Amount, tt.wantAmount)
			}
			if resp.Status == payment.ProviderStatusPaid && (resp.Metadata["pid"] != "pid-1" || resp.Metadata["out_trade_no"] != orderID) {
				t.Fatalf("paid metadata = %v, want upstream pid/order", resp.Metadata)
			}
			for key, want := range map[string]string{
				"act":          "order",
				"pid":          "pid-1",
				"key":          "pkey-1",
				"out_trade_no": orderID,
			} {
				if got := gotForm.Get(key); got != want {
					t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, gotForm)
				}
			}
		})
	}
}

func TestEasyPayQueryOrderRejectsSuccessfulBodyOnHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":1,"status":1,"pid":"pid-1","out_trade_no":"order-123","trade_no":"gateway-1","money":"3.21"}`))
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL)
	resp, err := provider.QueryOrder(context.Background(), "order-123")
	if err == nil {
		t.Fatalf("QueryOrder accepted an HTTP error response: %+v", resp)
	}
	if !strings.Contains(err.Error(), "HTTP status 502") {
		t.Fatalf("QueryOrder error = %v, want HTTP status 502", err)
	}
}

func TestEasyPayQueryOrderNumericPID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "top level integer", body: `{"code":1,"pid":123456,"out_trade_no":"order-123","status":1,"money":"1.00","trade_no":"gateway-1"}`},
		{name: "nested integer", body: `{"code":1,"data":{"pid":123456,"out_trade_no":"order-123","status":1,"money":"1.00","trade_no":"gateway-1"}}`},
		{name: "quoted integer", body: `{"code":1,"pid":"123456","out_trade_no":"order-123","status":1,"money":"1.00","trade_no":"gateway-1"}`},
		{name: "wrong integer", body: `{"code":1,"pid":123457,"out_trade_no":"order-123","status":1,"money":"1.00","trade_no":"gateway-1"}`, wantErr: true},
		{name: "fractional number", body: `{"code":1,"pid":123456.0,"out_trade_no":"order-123","status":1,"money":"1.00","trade_no":"gateway-1"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			provider.config["pid"] = "123456"
			resp, err := provider.QueryOrder(context.Background(), "order-123")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("QueryOrder accepted invalid pid: %+v", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("QueryOrder returned error: %v", err)
			}
			if resp.Status != payment.ProviderStatusPaid || resp.Metadata["pid"] != "123456" || resp.Metadata["out_trade_no"] != "order-123" {
				t.Fatalf("QueryOrder response = %+v, want paid order with matching pid", resp)
			}
		})
	}
}

func TestEasyPayQueryOrderRejectsBlankTradeNoBeforeRequest(t *testing.T) {
	t.Parallel()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL)
	if _, err := provider.QueryOrder(context.Background(), "  "); err == nil {
		t.Fatal("QueryOrder accepted a blank trade number")
	}
	if called {
		t.Fatal("QueryOrder issued a request for a blank trade number")
	}
}
