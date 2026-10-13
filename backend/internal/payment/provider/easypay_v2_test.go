package provider

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

type easyPayV2Fixture struct {
	provider           *EasyPayV2
	merchant, platform *rsa.PrivateKey
	now                time.Time
}

func newEasyPayV2Fixture(t *testing.T, handler func(http.ResponseWriter, *http.Request, *easyPayV2Fixture)) *easyPayV2Fixture {
	t.Helper()
	f := &easyPayV2Fixture{merchant: testEasyPayV2Key(t), platform: testEasyPayV2Key(t), now: time.Unix(1721206072, 0)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		p := map[string]string{}
		for k, v := range r.PostForm {
			if len(v) != 1 {
				t.Errorf("duplicate request field %s", k)
			}
			p[k] = v[0]
		}
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		if p["pid"] != "1001" {
			t.Errorf("pid=%s", p["pid"])
		}
		if err := easyPayV2Verify(p, &f.merchant.PublicKey, f.now); err != nil {
			t.Errorf("request signature: %v", err)
		}
		handler(w, r, f)
	}))
	t.Cleanup(server.Close)
	privateDER, _ := x509.MarshalPKCS8PrivateKey(f.merchant)
	publicDER, _ := x509.MarshalPKIXPublicKey(&f.platform.PublicKey)
	var err error
	f.provider, err = NewEasyPayV2("v2-instance", map[string]string{"apiVersion": "v2", "pid": "1001", "privateKey": base64.StdEncoding.EncodeToString(privateDER), "publicKey": base64.StdEncoding.EncodeToString(publicDER), "apiBase": server.URL, "notifyUrl": "https://merchant.example/notify", "returnUrl": "https://merchant.example/return?source=payment"})
	if err != nil {
		t.Fatal(err)
	}
	f.provider.now = func() time.Time { return f.now }
	return f
}

func (f *easyPayV2Fixture) response(w http.ResponseWriter, params map[string]string) {
	params = cloneStringMap(params)
	params["code"] = "0"
	params["timestamp"] = strconv.FormatInt(f.now.Unix(), 10)
	params["sign_type"] = "RSA"
	params["sign"], _ = easyPayV2Sign(params, f.platform)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(params)
}

func TestEasyPayV2CreateAndPopup(t *testing.T) {
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		if r.URL.Path != "/api/pay/create" || r.PostForm.Get("method") != "jump" || r.PostForm.Get("device") != "mobile" {
			t.Errorf("unexpected create %s %v", r.URL.Path, r.PostForm)
		}
		if r.PostForm.Get("return_url") != "https://merchant.example/return?source=payment" {
			t.Error("V2 return URL was altered")
		}
		if r.PostForm.Has("cid") || r.PostForm.Has("channel_id") {
			t.Error("legacy channel was mapped")
		}
		f.response(w, map[string]string{"trade_no": "upstream-1", "pay_type": "jump", "pay_info": "https://cashier.example/pay?id=1&x=2"})
	})
	f.provider.config["cidAlipay"] = "112"
	req := payment.CreatePaymentRequest{OrderID: "order-1", PaymentType: "alipay", Amount: "1.00", Subject: "充值 & a=b", ClientIP: "127.0.0.1", IsMobile: true}
	result, err := f.provider.CreatePayment(context.Background(), req)
	if err != nil || result.TradeNo != "upstream-1" || result.PayURL != "https://cashier.example/pay?id=1&x=2" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	f.provider.config["paymentMode"] = "popup"
	result, err = f.provider.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(result.PayURL)
	if err != nil || u.Path != "/api/pay/submit" {
		t.Fatalf("popup=%s err=%v", result.PayURL, err)
	}
	p := map[string]string{}
	for k, v := range u.Query() {
		p[k] = v[0]
	}
	if err := easyPayV2Verify(p, &f.merchant.PublicKey, f.now); err != nil {
		t.Fatal(err)
	}
}

func TestEasyPayV2CreateRejectsUnsupportedAndUnverifiedResponses(t *testing.T) {
	var responseType, responseURL string
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		f.response(w, map[string]string{"trade_no": "t-1", "pay_type": responseType, "pay_info": responseURL})
	})
	for _, tc := range [][2]string{{"html", "<script>bad</script>"}, {"urlscheme", "alipays://platformapi/startapp"}, {"jump", "javascript:alert(1)"}, {"jump", "https://u:p@cashier.example"}, {"jump", ""}} {
		responseType, responseURL = tc[0], tc[1]
		if _, err := f.provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "o-1", Amount: "1.00"}); err == nil {
			t.Fatalf("accepted %v", tc)
		}
	}
}

func TestEasyPayV2QueryStatusAndBindings(t *testing.T) {
	status, pid, order, money := "1", "1001", "o-1", "1.00"
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		if r.URL.Path != "/api/pay/query" || r.PostForm.Get("out_trade_no") != "o-1" {
			t.Errorf("unexpected query %s %v", r.URL.Path, r.PostForm)
		}
		f.response(w, map[string]string{"trade_no": "t-1", "out_trade_no": order, "pid": pid, "status": status, "money": money})
	})
	for input, want := range map[string]string{"0": "pending", "1": "paid", "2": "refunded", "3": "pending", "4": "pending"} {
		status = input
		got, err := f.provider.QueryOrder(context.Background(), "o-1")
		if err != nil || got.Status != want {
			t.Fatalf("status=%s got=%+v err=%v", input, got, err)
		}
		if got.Metadata["out_trade_no"] != "o-1" || got.Metadata["pid"] != "1001" {
			t.Fatalf("query lost verified order or merchant identity: %+v", got.Metadata)
		}
	}
	for _, tc := range [][4]string{{"1", "other", "o-1", "1.00"}, {"1", "1001", "other", "1.00"}, {"1.5", "1001", "o-1", "1.00"}, {"5", "1001", "o-1", "1.00"}, {"1", "1001", "o-1", "NaN"}} {
		status, pid, order, money = tc[0], tc[1], tc[2], tc[3]
		if _, err := f.provider.QueryOrder(context.Background(), "o-1"); err == nil {
			t.Fatalf("accepted %v", tc)
		}
	}
}

func TestEasyPayV2QueryOptionalMerchantStillRequiresSignedOrderIdentity(t *testing.T) {
	var response map[string]string
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		f.response(w, response)
	})
	for _, tc := range []struct {
		name, pid, order, trade string
		omitPID, omitOrder      bool
		wantErr                 bool
	}{
		{name: "merchant present", pid: "1001", order: "o-1", trade: "t-1"},
		{name: "merchant omitted", omitPID: true, order: "o-1", trade: "t-1"},
		{name: "merchant empty", order: "o-1", trade: "t-1", wantErr: true},
		{name: "merchant wrong", pid: "other", order: "o-1", trade: "t-1", wantErr: true},
		{name: "order omitted", omitPID: true, omitOrder: true, trade: "t-1", wantErr: true},
		{name: "order empty", omitPID: true, trade: "t-1", wantErr: true},
		{name: "order wrong", omitPID: true, order: "other", trade: "t-1", wantErr: true},
		{name: "trade empty", omitPID: true, order: "o-1", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response = map[string]string{"status": "1", "money": "1.00", "trade_no": tc.trade}
			if !tc.omitPID {
				response["pid"] = tc.pid
			}
			if !tc.omitOrder {
				response["out_trade_no"] = tc.order
			}
			got, err := f.provider.QueryOrder(context.Background(), "o-1")
			if tc.wantErr {
				if err == nil || got != nil {
					t.Fatalf("accepted unbound query response: got=%+v err=%v", got, err)
				}
				return
			}
			if err != nil || got == nil {
				t.Fatalf("rejected signed bound query response: got=%+v err=%v", got, err)
			}
			if got.Metadata["pid"] != "1001" || got.Metadata["out_trade_no"] != "o-1" || got.Status != payment.ProviderStatusPaid {
				t.Fatalf("lost pinned merchant or signed order identity: %+v", got)
			}
		})
	}
}

func TestEasyPayV2NotificationExtensionsAndRejections(t *testing.T) {
	f := newEasyPayV2Fixture(t, func(http.ResponseWriter, *http.Request, *easyPayV2Fixture) {
		t.Fatal("callback must not make requests")
	})
	valid := map[string]string{"pid": "1001", "trade_no": "t-1", "out_trade_no": "o-1", "trade_status": "TRADE_SUCCESS", "money": "1.00", "timestamp": strconv.FormatInt(f.now.Unix(), 10), "sign_type": "RSA", "future_extension": "scalar value & x=y"}
	encode := func(p map[string]string) string {
		p = cloneStringMap(p)
		p["sign"], _ = easyPayV2Sign(p, f.platform)
		q := url.Values{}
		for k, v := range p {
			q.Set(k, v)
		}
		return q.Encode()
	}
	raw := encode(valid)
	if got, err := f.provider.VerifyNotification(context.Background(), raw, nil); err != nil || got.OrderID != "o-1" || got.Status != "success" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := f.provider.VerifyNotification(context.Background(), raw+"&money=1.00", nil); err == nil {
		t.Fatal("accepted duplicate field")
	}
	if _, err := f.provider.VerifyNotification(context.Background(), strings.Replace(raw, "money=1.00", "money=2.00", 1), nil); err == nil {
		t.Fatal("accepted tampered callback")
	}
	for key, value := range map[string]string{"pid": "other", "trade_no": "", "out_trade_no": "", "money": "NaN", "trade_status": "WAIT_BUYER_PAY", "timestamp": strconv.FormatInt(f.now.Unix()-301, 10), "sign_type": "MD5"} {
		p := cloneStringMap(valid)
		p[key] = value
		if _, err := f.provider.VerifyNotification(context.Background(), encode(p), nil); err == nil {
			t.Fatalf("accepted %s=%s", key, value)
		}
	}
}

func TestEasyPayV2RefundStableIDAndQuery(t *testing.T) {
	queryStatus := "1"
	calls := 0
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		calls++
		if r.PostForm.Get("out_refund_no") != "refund-1" || r.PostForm.Has("refund_no") {
			t.Errorf("unstable refund form %v", r.PostForm)
		}
		p := map[string]string{"trade_no": "t-1", "out_refund_no": "refund-1", "refund_no": "platform-refund", "money": "1.00"}
		if r.URL.Path == "/api/pay/refundquery" {
			p["status"] = queryStatus
			p["out_trade_no"] = "o-1"
		} else if r.URL.Path != "/api/pay/refund" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		f.response(w, p)
	})
	got, err := f.provider.Refund(context.Background(), payment.RefundRequest{OrderID: "o-1", TradeNo: "t-1", RefundID: "refund-1", Amount: "1.00"})
	if err != nil || got.Status != "success" || got.RefundID != "refund-1" || got.Metadata["refund_no"] != "platform-refund" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	for input, want := range map[string]string{"0": "failed", "1": "success"} {
		queryStatus = input
		got, err = f.provider.QueryRefund(context.Background(), payment.RefundQueryRequest{OrderID: "o-1", TradeNo: "t-1", RefundID: "refund-1", Amount: "1.00"})
		if err != nil || got.Status != want || got.RefundID != "refund-1" {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestEasyPayV2RefundUnknownPreservesPending(t *testing.T) {
	mode := ""
	calls := 0
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		calls++
		switch mode {
		case "eof":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test response writer does not support hijacking")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack test connection: %v", err)
				return
			}
			_ = conn.Close()
		case "http":
			w.WriteHeader(502)
		case "json":
			_, _ = w.Write([]byte("not json"))
		case "signature":
			_, _ = w.Write([]byte(`{"code":0,"timestamp":"1721206072","sign_type":"RSA","sign":"invalid"}`))
		case "rejected":
			_, _ = w.Write([]byte(`{"code":1,"msg":"unknown result"}`))
		default:
			p := map[string]string{"trade_no": "t-1", "out_trade_no": "o-1", "out_refund_no": "refund-1", "refund_no": "platform-refund", "money": "1.00", "status": "1"}
			p[mode] = "mismatch"
			f.response(w, p)
		}
	})
	for _, v := range []string{"eof", "http", "json", "signature", "rejected", "trade_no", "out_refund_no", "money"} {
		mode = v
		before := calls
		got, err := f.provider.Refund(context.Background(), payment.RefundRequest{OrderID: "o-1", TradeNo: "t-1", RefundID: "refund-1", Amount: "1.00"})
		if err == nil || got == nil || got.Status != "pending" || got.RefundID != "refund-1" {
			t.Fatalf("mode=%s got=%+v err=%v", mode, got, err)
		}
		if calls != before+1 {
			t.Fatal("ambiguous refund was retried")
		}
		if got, err := f.provider.QueryRefund(context.Background(), payment.RefundQueryRequest{OrderID: "o-1", TradeNo: "t-1", RefundID: "refund-1", Amount: "1.00"}); err == nil || got != nil {
			t.Fatalf("query mode=%s got=%+v err=%v", mode, got, err)
		}
	}
}

func TestEasyPayV2RedirectDoesNotLeakRequest(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true }))
	defer target.Close()
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	got, err := f.provider.Refund(context.Background(), payment.RefundRequest{OrderID: "o-1", TradeNo: "t-1", RefundID: "refund-1", Amount: "1.00"})
	if err == nil || got.Status != "pending" || received {
		t.Fatalf("got=%+v err=%v redirected=%v", got, err, received)
	}
}

func TestEasyPayV2RefundTimeoutAndMissingID(t *testing.T) {
	f := newEasyPayV2Fixture(t, func(w http.ResponseWriter, r *http.Request, f *easyPayV2Fixture) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	})
	f.provider.httpClient.Timeout = 10 * time.Millisecond
	got, err := f.provider.Refund(context.Background(), payment.RefundRequest{OrderID: "o-1", TradeNo: "t-1", RefundID: "refund-1", Amount: "1.00"})
	if err == nil || got == nil || got.Status != payment.ProviderStatusPending || got.RefundID != "refund-1" {
		t.Fatalf("timeout result=%+v error=%v", got, err)
	}
	if got, err := f.provider.Refund(context.Background(), payment.RefundRequest{OrderID: "o-1", TradeNo: "t-1", Amount: "1.00"}); err == nil || got != nil {
		t.Fatalf("missing refund ID result=%+v error=%v", got, err)
	}
}

func TestEasyPayV2ConfigRejectsInvalidKeysAndCleartext(t *testing.T) {
	f := newEasyPayV2Fixture(t, func(http.ResponseWriter, *http.Request, *easyPayV2Fixture) {})
	for _, tc := range [][2]string{{"privateKey", "INVALID-SECRET"}, {"publicKey", "INVALID-PUBLIC"}, {"apiBase", "http://pay.example.com"}, {"apiBase", "https://user:password@pay.example.com"}, {"apiBase", "https://pay.example.com/?secret=1"}} {
		cfg := cloneStringMap(f.provider.config)
		cfg[tc[0]] = tc[1]
		if _, err := NewEasyPayV2("test", cfg); err == nil {
			t.Fatalf("accepted invalid %s", tc[0])
		} else if strings.Contains(err.Error(), tc[1]) {
			t.Fatalf("echoed raw configuration in error: %v", err)
		}
	}
	for _, endpoint := range []string{"/api/pay/create", "/api/pay/submit/", "/api/pay/refundquery"} {
		cfg := cloneStringMap(f.provider.config)
		cfg["apiBase"] += endpoint
		p, err := NewEasyPayV2("test", cfg)
		if err != nil || p.config["apiBase"] != f.provider.config["apiBase"] {
			t.Fatalf("base=%s result=%+v err=%v", cfg["apiBase"], p, err)
		}
	}
}

func TestEasyPayFactoryVersionSelection(t *testing.T) {
	f := newEasyPayV2Fixture(t, func(http.ResponseWriter, *http.Request, *easyPayV2Fixture) {})
	for _, version := range []string{"v2", " V2 "} {
		cfg := cloneStringMap(f.provider.config)
		cfg["apiVersion"] = version
		p, err := CreateProvider(payment.TypeEasyPay, "test", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*EasyPayV2); !ok {
			t.Fatalf("version %s produced %T", version, p)
		}
	}
	for _, version := range []string{"v3", "2", "md5"} {
		cfg := cloneStringMap(f.provider.config)
		cfg["apiVersion"] = version
		if _, err := CreateProvider(payment.TypeEasyPay, "test", cfg); err == nil {
			t.Fatalf("accepted %s", version)
		}
	}
	for _, version := range []string{"", "v1", " V1 "} {
		cfg := cloneStringMap(f.provider.config)
		cfg["apiVersion"] = version
		cfg["pkey"] = "legacy-key"
		p, err := CreateProvider(payment.TypeEasyPay, "test", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*EasyPay); !ok {
			t.Fatalf("version %s produced %T", version, p)
		}
	}
}
