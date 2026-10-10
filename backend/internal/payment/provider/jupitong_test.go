package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

type jupitongTransport func(*http.Request) (*http.Response, error)

func (f jupitongTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jupitongTestProvider(t *testing.T, transport jupitongTransport) *Jupitong {
	t.Helper()
	j, err := NewJupitong("instance-1", map[string]string{
		"pid": "20880001", "pkey": "secret", "notifyUrl": "https://merchant.example/notify",
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport != nil {
		j.httpClient.Transport = transport
	}
	return j
}

func jupitongJSONResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestJupitong_SignMatchesOfficialGoSDKAndRejectsAmbiguity(t *testing.T) {
	sign, err := jupitongSign(map[string]string{"b": "2", "a": "1", "sign_type": "MD5"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if sign != "9F565CCD686CFA5DC3B06B3A89E4E3AD" {
		t.Fatalf("unexpected SDK signature: %s", sign)
	}
	for _, params := range []map[string]string{
		{"a": "1&b=2"}, {"a": "1=2"}, {"a": "line\nfeed"}, {"bad&key": "1"},
	} {
		if _, err := jupitongSign(params, "secret"); err == nil {
			t.Fatalf("accepted ambiguous parameters: %#v", params)
		}
	}
}

func TestJupitong_CreatePaymentSignsOfficialForm(t *testing.T) {
	j := jupitongTestProvider(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != "https://spay.huiyiy.cn/openapi/pay/create" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("unexpected content type: %s", req.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"pid": "20880001", "out_trade_no": "ORDER-1", "total_amount": "100.00",
			"subject": "Recharge", "paytype_code": "alipay", "client_ip": "192.0.2.1",
			"notify_url": "https://merchant.example/notify", "sign_type": "MD5",
		} {
			if got := form.Get(key); got != want {
				t.Errorf("form %s = %q, want %q", key, got, want)
			}
		}
		if form.Has("return_url") {
			t.Fatalf("ambiguous return_url must be omitted: %q", form.Get("return_url"))
		}
		params := make(map[string]string)
		for key := range form {
			params[key] = form.Get(key)
		}
		if err := jupitongVerify(params, "secret"); err != nil {
			t.Fatalf("invalid outbound signature: %v", err)
		}
		return jupitongJSONResponse(`{"code":1,"msg":"success","data":{"trade_no":"P-1","out_trade_no":"ORDER-1","pay_url":"https://cashier.example/pay/1"}}`), nil
	})
	got, err := j.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID: "ORDER-1", Amount: "100.00", Subject: "Recharge", PaymentType: payment.TypeAlipay,
		ClientIP:  "192.0.2.1",
		ReturnURL: "https://merchant.example/payment/result?order_id=42&out_trade_no=ORDER-1&status=success",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TradeNo != "P-1" || got.PayURL != "https://cashier.example/pay/1" {
		t.Fatalf("unexpected create result: %+v", got)
	}
	_, err = j.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID: "ORDER-2", Amount: "100.00", Subject: "foo&total_amount=0.01",
		PaymentType: payment.TypeAlipay, ClientIP: "192.0.2.1",
	})
	if err == nil {
		t.Fatal("ambiguous subject was accepted")
	}
}

func TestJupitong_QueryOrderChecksAuthoritativeIdentifiers(t *testing.T) {
	tests := []struct {
		name, body, wantStatus string
		wantAmount             float64
		wantError              bool
	}{
		{"paid string amount", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":"100.00","trade_status":"TRADE_SUCCESS"}}`, payment.ProviderStatusPaid, 100, false},
		{"paid numeric amount", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":1.00,"trade_status":"TRADE_SUCCESS"}}`, payment.ProviderStatusPaid, 1, false},
		{"paid numeric exponent", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":1e2,"trade_status":"TRADE_SUCCESS"}}`, payment.ProviderStatusPaid, 100, false},
		{"mismatched order", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"OTHER","total_amount":"100.00","trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"missing trade number", `{"code":1,"data":{"out_trade_no":"ORDER-1","total_amount":"100.00","trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"missing amount", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"fractional cent", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":1.001,"trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"malformed amount", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":"not-money","trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"wrong amount type", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":true,"trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"overflow amount", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":1e309,"trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"huge positive exponent", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":1e100000000,"trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"huge negative exponent", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":1e-100000000,"trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"zero amount", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":0,"trade_status":"TRADE_SUCCESS"}}`, "", 0, true},
		{"frozen", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":"100.00","trade_status":"TRADE_FREEZE"}}`, payment.ProviderStatusFailed, 0, false},
		{"unknown status", `{"code":1,"data":{"trade_no":"P-1","out_trade_no":"ORDER-1","total_amount":"100.00","trade_status":"UNKNOWN"}}`, "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := jupitongTestProvider(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://spay.huiyiy.cn/openapi/pay/query" {
					t.Fatalf("unexpected query URL: %s", req.URL)
				}
				body, _ := io.ReadAll(req.Body)
				form, _ := url.ParseQuery(string(body))
				if form.Get("out_trade_no") != "ORDER-1" || form.Get("trade_no") != "" {
					t.Fatalf("unexpected query identifiers: %v", form)
				}
				return jupitongJSONResponse(tt.body), nil
			})
			got, err := j.QueryOrder(context.Background(), "ORDER-1")
			if (err != nil) != tt.wantError {
				t.Fatalf("QueryOrder() = %+v, %v; want error %t", got, err, tt.wantError)
			}
			if err == nil && (got.Status != tt.wantStatus || got.Amount != tt.wantAmount || got.Metadata["pid"] != "20880001" || got.Metadata["out_trade_no"] != "ORDER-1") {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

func jupitongSignedNotification(t *testing.T, overrides map[string]string) string {
	t.Helper()
	params := map[string]string{
		"pid": "20880001", "trade_no": "P-1", "out_trade_no": "ORDER-1",
		"total_amount": "100.00", "subject": "Recharge", "paytype_code": "alipay",
		"channel_id": "42", "trade_status": "TRADE_SUCCESS", "timestamp": "1720000000",
		"sign_type": "MD5",
	}
	for key, value := range overrides {
		params[key] = value
	}
	sign, err := jupitongSign(params, "secret")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	form.Set("sign", sign)
	return form.Encode()
}

func jupitongOfficialMinimalNotification(t *testing.T, status string, includeAmount bool) string {
	t.Helper()
	params := map[string]string{
		"pid": "20880001", "out_trade_no": "ORDER-1", "trade_status": status,
	}
	if status == "TRADE_SUCCESS" {
		params["trade_no"] = "P-1"
	}
	if includeAmount {
		params["total_amount"] = "100.00"
	}
	sign, err := jupitongSign(params, "secret")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	form.Set("sign", sign)
	return form.Encode()
}

func TestJupitong_VerifyNotificationAcceptsOfficialMinimalCallback(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	notification, err := j.VerifyNotification(context.Background(), jupitongOfficialMinimalNotification(t, "TRADE_SUCCESS", true), nil)
	if err != nil {
		t.Fatalf("official minimal callback rejected: %v", err)
	}
	if notification.Status != payment.ProviderStatusSuccess || notification.TradeNo != "P-1" || notification.OrderID != "ORDER-1" || notification.Amount != 100 {
		t.Fatalf("unexpected notification: %+v", notification)
	}
}

func TestJupitong_VerifyNotificationAcceptsMultipartCallback(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	body, contentType := jupitongMultipartNotification(t, jupitongSignedNotification(t, nil))
	notification, err := j.VerifyNotification(context.Background(), body, map[string]string{"content-type": contentType})
	if err != nil {
		t.Fatalf("valid multipart callback rejected: %v", err)
	}
	if notification.Status != payment.ProviderStatusSuccess || notification.TradeNo != "P-1" || notification.OrderID != "ORDER-1" {
		t.Fatalf("unexpected notification: %+v", notification)
	}
}

func TestJupitong_VerifyNotificationAcceptsEmptyDescriptiveMultipartFields(t *testing.T) {
	for _, field := range []string{"subject", "channel_id"} {
		t.Run(field, func(t *testing.T) {
			j := jupitongTestProvider(t, nil)
			body, contentType := jupitongMultipartNotification(t, jupitongSignedNotification(t, map[string]string{
				field: "",
			}))
			notification, err := j.VerifyNotification(context.Background(), body, map[string]string{"content-type": contentType})
			if err != nil {
				t.Fatalf("paid callback with empty %s rejected: %v", field, err)
			}
			if notification.Status != payment.ProviderStatusSuccess || notification.TradeNo != "P-1" || notification.OrderID != "ORDER-1" || notification.Amount != 100 {
				t.Fatalf("unexpected notification: %+v", notification)
			}
		})
	}
}

func jupitongMultipartNotification(t *testing.T, formBody string) (string, string) {
	t.Helper()
	values, err := url.ParseQuery(formBody)
	if err != nil {
		t.Fatal(err)
	}
	fields := make([]jupitongMultipartTestField, 0, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, value := range values[key] {
			fields = append(fields, jupitongMultipartTestField{name: key, value: value})
		}
	}
	return jupitongMultipartFields(t, fields)
}

type jupitongMultipartTestField struct {
	name   string
	value  string
	header textproto.MIMEHeader
}

func jupitongMultipartFields(t *testing.T, fields []jupitongMultipartTestField) (string, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range fields {
		header := field.header
		if header == nil {
			header = textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"`, field.name)}}
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, field.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), writer.FormDataContentType()
}

func jupitongMultipartTestFields(t *testing.T, formBody string) []jupitongMultipartTestField {
	t.Helper()
	values, err := url.ParseQuery(formBody)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]jupitongMultipartTestField, 0, len(values))
	for _, key := range keys {
		for _, value := range values[key] {
			fields = append(fields, jupitongMultipartTestField{name: key, value: value})
		}
	}
	return fields
}

func TestJupitong_ParseMultipartNotificationRejectsAmbiguityAndMalformedBodies(t *testing.T) {
	good := jupitongMultipartTestFields(t, jupitongSignedNotification(t, nil))
	cases := []struct {
		name        string
		fields      []jupitongMultipartTestField
		contentType string
		truncate    bool
	}{
		{name: "duplicate sign", fields: append(append([]jupitongMultipartTestField(nil), good...), jupitongMultipartTestField{name: "sign", value: good[0].value})},
		{name: "duplicate pid", fields: append(append([]jupitongMultipartTestField(nil), good...), jupitongMultipartTestField{name: "pid", value: "20880001"})},
		{name: "duplicate order id", fields: append(append([]jupitongMultipartTestField(nil), good...), jupitongMultipartTestField{name: "out_trade_no", value: "ORDER-1"})},
		{name: "file with empty filename", fields: append(append([]jupitongMultipartTestField(nil), good...), jupitongMultipartTestField{
			name: "upload", value: "scalar-looking", header: textproto.MIMEHeader{"Content-Disposition": {`form-data; name="upload"; filename=""`}},
		})},
		{name: "duplicate content disposition", fields: []jupitongMultipartTestField{{
			name: good[0].name, value: good[0].value,
			header: textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"`, good[0].name), fmt.Sprintf(`form-data; name="%s"`, good[0].name)}},
		}}},
		{name: "transfer encoding", fields: []jupitongMultipartTestField{{
			name: good[0].name, value: good[0].value,
			header: textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"`, good[0].name)}, "Content-Transfer-Encoding": {"binary"}},
		}}},
		{name: "empty field name", fields: []jupitongMultipartTestField{{name: "", value: "x"}}},
		{name: "invalid field name", fields: []jupitongMultipartTestField{{name: "bad-name", value: "x"}}},
		{name: "too many fields", fields: append(append([]jupitongMultipartTestField(nil), good...), make([]jupitongMultipartTestField, jupitongMaxNotifyFields+1-len(good))...)},
		{name: "body too large", fields: []jupitongMultipartTestField{{name: "payload", value: strings.Repeat("x", jupitongMaxNotifySize)}}},
		{name: "truncated body", fields: good, truncate: true},
		{name: "wrong media type", fields: good, contentType: "application/json"},
		{name: "missing boundary", fields: good, contentType: "multipart/form-data"},
		{name: "body boundary mismatch", fields: good, contentType: "multipart/form-data; boundary=not-the-body-boundary"},
		{name: "malformed boundary", fields: good, contentType: `multipart/form-data; boundary="unterminated`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType := jupitongMultipartFields(t, tc.fields)
			if tc.contentType != "" {
				contentType = tc.contentType
			}
			if tc.truncate {
				_, params, err := mime.ParseMediaType(contentType)
				if err != nil {
					t.Fatal(err)
				}
				body = strings.TrimSuffix(body, "--"+params["boundary"]+"--\r\n")
			}
			if _, err := ParseJupitongNotification(body, contentType); err == nil {
				t.Fatal("malformed or ambiguous multipart notification was accepted")
			}
		})
	}
}

func TestJupitong_VerifyMultipartNotificationChecksSignatureAndNonPaidStatuses(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	fields := jupitongMultipartTestFields(t, jupitongSignedNotification(t, nil))
	for i := range fields {
		if fields[i].name == "total_amount" {
			fields[i].value = "0.01"
		}
	}
	body, contentType := jupitongMultipartFields(t, fields)
	if _, err := j.VerifyNotification(context.Background(), body, map[string]string{"content-type": contentType}); err == nil {
		t.Fatal("tampered multipart signature was accepted")
	}

	for _, status := range []string{"TRADE_FREEZE", "TRADE_UNFREEZE", "TRADE_REFUND"} {
		form := jupitongSignedNotification(t, map[string]string{"trade_status": status, "trade_no": "", "total_amount": ""})
		body, contentType := jupitongMultipartNotification(t, form)
		notification, err := j.VerifyNotification(context.Background(), body, map[string]string{"content-type": contentType})
		if err != nil {
			t.Fatalf("%s multipart notification rejected: %v", status, err)
		}
		if notification.Status == payment.ProviderStatusSuccess || notification.Status == payment.ProviderStatusPaid {
			t.Fatalf("non-paid %s notification became success: %+v", status, notification)
		}
	}
}

func TestJupitong_ParseNotificationRejectsUnsupportedContentTypeAndHeaderDuplicates(t *testing.T) {
	if _, err := ParseJupitongNotification("pid=20880001", "text/plain"); err == nil {
		t.Fatal("unsupported content type was accepted")
	}
	j := jupitongTestProvider(t, nil)
	good := jupitongSignedNotification(t, nil)
	if _, err := j.VerifyNotification(context.Background(), good, map[string]string{"content-type": "application/x-www-form-urlencoded", "Content-Type": "multipart/form-data; boundary=x"}); err == nil {
		t.Fatal("duplicate content-type headers were accepted")
	}
}

func TestJupitong_VerifyNotificationAcceptsMinimalUnpaidCallback(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	for _, status := range []string{"WAIT_BUYER_PAY", "TRADE_CLOSED", "TRADE_REFUND"} {
		notification, err := j.VerifyNotification(context.Background(), jupitongOfficialMinimalNotification(t, status, false), nil)
		if err != nil {
			t.Fatalf("%s callback rejected: %v", status, err)
		}
		if notification.Status == payment.ProviderStatusSuccess || notification.Status == payment.ProviderStatusPaid {
			t.Fatalf("unsafe %s notification: %+v", status, notification)
		}
	}
}

func TestJupitong_VerifyNotificationFailClosed(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	good := jupitongSignedNotification(t, nil)
	notification, err := j.VerifyNotification(context.Background(), good, nil)
	if err != nil || notification.Status != payment.ProviderStatusSuccess || notification.Metadata["pid"] != "20880001" {
		t.Fatalf("valid callback: %+v, %v", notification, err)
	}
	for _, status := range []string{"WAIT_BUYER_PAY", "TRADE_CLOSED", "TRADE_REFUND", "TRADE_FREEZE", "TRADE_UNFREEZE"} {
		n, err := j.VerifyNotification(context.Background(), jupitongSignedNotification(t, map[string]string{"trade_status": status}), nil)
		if err != nil || n.Status == payment.ProviderStatusSuccess || n.Status == payment.ProviderStatusPaid {
			t.Fatalf("unsafe %s status: %+v, %v", status, n, err)
		}
	}
	bad := []string{
		good + "&pid=20880001",
		strings.Replace(good, "pid=20880001", "pid=20880002", 1),
		strings.Replace(good, "total_amount=100.00", "total_amount=0.01", 1),
		strings.Replace(good, "sign_type=MD5", "sign_type=RSA", 1),
		jupitongSignedNotification(t, map[string]string{"pid": "20880002"}),
		jupitongSignedNotification(t, map[string]string{"total_amount": "NaN"}),
		jupitongSignedNotification(t, map[string]string{"trade_no": ""}),
	}
	for i, body := range bad {
		if n, err := j.VerifyNotification(context.Background(), body, nil); err == nil {
			t.Errorf("invalid notification %d accepted: %+v", i, n)
		}
	}
}

func TestJupitong_VerifyNotificationAcceptsMD5SignTypeVariants(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	good := jupitongSignedNotification(t, nil)
	withoutType := removeJupitongField(good, "sign_type")
	for name, body := range map[string]string{
		"omitted":   withoutType,
		"lowercase": strings.Replace(withoutType, "sign=", "sign_type=md5&sign=", 1),
	} {
		if notification, err := j.VerifyNotification(context.Background(), body, nil); err != nil || notification.Status != payment.ProviderStatusSuccess {
			t.Errorf("%s sign_type variant rejected: %+v, %v", name, notification, err)
		}
	}
}

func removeJupitongField(body, field string) string {
	parts := strings.Split(body, "&")
	filtered := parts[:0]
	for _, part := range parts {
		if !strings.HasPrefix(part, field+"=") {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "&")
}

func TestJupitong_VerifyNotificationAcceptsRawSemicolonSeparators(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	base := jupitongSignedNotification(t, nil)
	for name, body := range map[string]string{
		"semicolon":           strings.ReplaceAll(base, "&", ";"),
		"semicolon and space": strings.ReplaceAll(base, "&", "; "),
		"semicolon and tab":   strings.ReplaceAll(base, "&", ";\t"),
	} {
		t.Run(name, func(t *testing.T) {
			notification, err := j.VerifyNotification(context.Background(), body, nil)
			if err != nil {
				t.Fatalf("callback rejected: %v", err)
			}
			if notification.Status != payment.ProviderStatusSuccess || notification.TradeNo != "P-1" || notification.OrderID != "ORDER-1" {
				t.Fatalf("unexpected notification: %+v", notification)
			}
		})
	}
}

func TestJupitong_VerifyNotificationAcceptsSeparatorPaddingWithoutTrimmingEncodedNames(t *testing.T) {
	j := jupitongTestProvider(t, nil)
	good := jupitongSignedNotification(t, nil)
	padded := strings.ReplaceAll(good, "&", ";  ")
	if _, err := j.VerifyNotification(context.Background(), padded, nil); err != nil {
		t.Fatalf("separator padding callback rejected: %v", err)
	}
	encodedName := strings.Replace(good, "out_trade_no=ORDER-1", "%20out_trade_no=ORDER-1", 1)
	if _, err := j.VerifyNotification(context.Background(), encodedName, nil); err == nil {
		t.Fatal("encoded whitespace in parameter name was accepted")
	}
}

func TestJupitongFormPreservesEncodedSemicolonAndRejectsMalformedEscapes(t *testing.T) {
	body := jupitongSignedNotification(t, map[string]string{"subject": "Recharge;batch"})
	params, err := parseJupitongForm(body)
	if err != nil {
		t.Fatalf("encoded semicolon callback rejected: %v", err)
	}
	if got := params["subject"]; len(got) != 1 || got[0] != "Recharge;batch" {
		t.Fatalf("encoded semicolon was split or altered: %#v", got)
	}
	j := jupitongTestProvider(t, nil)
	if _, err := j.VerifyNotification(context.Background(), body, nil); err != nil {
		t.Fatalf("encoded semicolon signature verification failed: %v", err)
	}
	if _, err := parseJupitongForm("subject=%ZZ"); err == nil {
		t.Fatal("malformed URL escape was accepted")
	}

	duplicate := strings.ReplaceAll(jupitongSignedNotification(t, nil), "&", "; ") + ";  pid=20880001"
	if _, err := j.VerifyNotification(context.Background(), duplicate, nil); err == nil || !strings.Contains(err.Error(), "duplicate notification parameter: pid") {
		t.Fatalf("raw-separator duplicate was not rejected: %v", err)
	}
}

func TestJupitong_RejectsUnsafeHostAndOversizedResponse(t *testing.T) {
	_, err := NewJupitong("instance", map[string]string{
		"pid": "20880001", "pkey": "secret", "notifyUrl": "https://merchant.example/notify",
		"apiBase": "https://attacker.example",
	})
	if err == nil {
		t.Fatal("custom API host was accepted")
	}
	j := jupitongTestProvider(t, func(*http.Request) (*http.Response, error) {
		return jupitongJSONResponse(strings.Repeat("x", jupitongMaxResponseSize+1)), nil
	})
	if _, err := j.QueryOrder(context.Background(), "ORDER-1"); err == nil {
		t.Fatal("oversized response was accepted")
	}
	if _, err := j.Refund(context.Background(), payment.RefundRequest{}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatal("refund was unexpectedly supported")
	}
}
