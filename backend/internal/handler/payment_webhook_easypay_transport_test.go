//go:build unit

package handler

import (
	"context"
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	providerimpl "github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The wrapper records the real RSA verifier's result, then stops before account
// mutation. SQL expectations independently assert which order was used to select
// the provider, so routing and signature verification must use the same payload.
type easyPayTransportVerifier struct {
	payment.Provider
	raw          string
	notification *payment.PaymentNotification
	verifyErr    error
}

func (p *easyPayTransportVerifier) VerifyNotification(ctx context.Context, raw string, headers map[string]string) (*payment.PaymentNotification, error) {
	p.raw = raw
	p.notification, p.verifyErr = p.Provider.VerifyNotification(ctx, raw, headers)
	return nil, p.verifyErr
}

func newEasyPayTransportFixture(t *testing.T) (*providerimpl.EasyPayV2, *rsa.PrivateKey) {
	t.Helper()
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	platform, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(merchant)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	require.NoError(t, err)
	provider, err := providerimpl.NewEasyPayV2("transport-v2", map[string]string{
		"apiVersion": "v2", "pid": "1001",
		"privateKey": base64.StdEncoding.EncodeToString(privateDER),
		"publicKey":  base64.StdEncoding.EncodeToString(publicDER),
		"apiBase":    "https://gateway.example/GatewayV2/",
		"notifyUrl":  "https://merchant.example/api/v1/payment/webhook/easypay",
		"returnUrl":  "https://merchant.example/payment/result",
	})
	require.NoError(t, err)
	return provider, platform
}

func signedEasyPayTransportForm(t *testing.T, key *rsa.PrivateKey, overrides map[string]string) string {
	t.Helper()
	params := map[string]string{
		"pid": "1001", "trade_no": "platform-order-1", "out_trade_no": "transport-order-1",
		"money": "1.02", "type": "alipay", "trade_status": "TRADE_SUCCESS",
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10), "sign_type": "RSA",
		"addtime": "2026-10-14 01:34:50", "endtime": "2026-10-14 01:35:11",
		"api_trade_no": "alipay-order-1", "buyer": "buyer***@example.com",
		"name": "充值 & 套餐=标准", "param": "",
	}
	for k, v := range overrides {
		params[k] = v
	}
	var keys []string
	for k, v := range params {
		if k != "sign" && k != "sign_type" && strings.TrimSpace(v) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	params["sign"] = base64.StdEncoding.EncodeToString(signature)
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	return values.Encode()
}

func easyPayTransportHandler(t *testing.T, verifier *easyPayTransportVerifier, routeOrder string) (*PaymentWebhookHandler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.MatchExpectationsInOrder(false)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	if routeOrder != "" {
		mock.ExpectQuery(`SELECT .* FROM "payment_orders" WHERE "payment_orders"\."out_trade_no" = \$1`).
			WithArgs(routeOrder).WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectQuery(`SELECT COUNT\(.* FROM "payment_provider_instances"`).
			WithArgs(payment.TypeEasyPay).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT .* FROM "payment_provider_instances"`).
			WithoutArgs().WillReturnRows(sqlmock.NewRows([]string{"id"}))
	}
	registry := payment.NewRegistry()
	registry.Register(verifier)
	svc := service.NewPaymentService(client, registry, nil, nil, nil, nil, nil, nil, nil)
	return NewPaymentWebhookHandler(svc, registry), mock
}

func runEasyPayTransportRequest(h *PaymentWebhookHandler, method, query, body, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/api/v1/payment/webhook/easypay?"+query, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	h.EasyPayNotify(ctx)
	return recorder
}

func TestEasyPayWebhook_TransportRSA(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider, key := newEasyPayTransportFixture(t)
	form := signedEasyPayTransportForm(t, key, nil)
	for _, tc := range []struct {
		name, method, query, body, contentType string
	}{
		{"get_query", http.MethodGet, form, "", ""},
		{"post_form", http.MethodPost, "", form, "application/x-www-form-urlencoded"},
		{"post_empty_body_query", http.MethodPost, form, "", ""},
		{"post_empty_form_body_query", http.MethodPost, form, "", "application/x-www-form-urlencoded"},
		{"post_empty_json_body_query", http.MethodPost, form, "", "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &easyPayTransportVerifier{Provider: provider}
			h, mock := easyPayTransportHandler(t, verifier, "transport-order-1")
			response := runEasyPayTransportRequest(h, tc.method, tc.query, tc.body, tc.contentType)
			t.Logf("status=%d verified_body_bytes=%d verification_error=%v", response.Code, len(verifier.raw), verifier.verifyErr)
			require.NoError(t, verifier.verifyErr)
			require.Equal(t, form, verifier.raw)
			require.NoError(t, mock.ExpectationsWereMet(), "order routing must read the selected signed payload")
			require.NotNil(t, verifier.notification)
			require.Equal(t, "transport-order-1", verifier.notification.OrderID)
			require.Equal(t, 1.02, verifier.notification.Amount)
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "success", response.Body.String())
		})
	}
}

func TestEasyPayWebhook_TransportV1Compatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider, err := providerimpl.NewEasyPay("transport-v1", map[string]string{
		"apiVersion": "v1", "pid": "1001", "pkey": "test-only-key",
		"apiBase": "https://gateway.example/", "notifyUrl": "https://merchant.example/notify",
		"returnUrl": "https://merchant.example/return",
	})
	require.NoError(t, err)
	content := "money=1.02&out_trade_no=transport-order-1&pid=1001&trade_no=platform-order-1&trade_status=TRADE_SUCCESS"
	digest := md5.Sum([]byte(content + "test-only-key"))
	form := content + "&sign_type=MD5&sign=" + hex.EncodeToString(digest[:])
	for _, tc := range []struct{ name, method, query, body string }{
		{"get_query", http.MethodGet, form, ""},
		{"post_form", http.MethodPost, "", form},
		{"post_empty_body_query", http.MethodPost, form, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &easyPayTransportVerifier{Provider: provider}
			h, mock := easyPayTransportHandler(t, verifier, "transport-order-1")
			response := runEasyPayTransportRequest(h, tc.method, tc.query, tc.body, "application/x-www-form-urlencoded")
			require.NoError(t, mock.ExpectationsWereMet())
			require.NoError(t, verifier.verifyErr)
			require.NotNil(t, verifier.notification)
			require.Equal(t, 1.02, verifier.notification.Amount)
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "success", response.Body.String())
		})
	}
}

func TestEasyPayWebhook_RejectsAmbiguousOrMalformedTransportBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, key := newEasyPayTransportFixture(t)
	form := signedEasyPayTransportForm(t, key, nil)
	for _, tc := range []struct {
		name, method, query, body string
	}{
		{"post_both_sources", http.MethodPost, form, form},
		{"post_other_query_order", http.MethodPost, "out_trade_no=another-order", form},
		{"post_split_signature", http.MethodPost, "out_trade_no=transport-order-1", strings.Replace(form, "out_trade_no=transport-order-1&", "", 1)},
		{"post_nonempty_invalid_body", http.MethodPost, form, " "},
		{"get_both_sources", http.MethodGet, form, form},
		{"get_duplicate_order", http.MethodGet, form + "&out_trade_no=another-order", ""},
		{"post_duplicate_amount", http.MethodPost, "", form + "&money=1.02"},
		{"post_encoded_duplicate", http.MethodPost, form + "&out%5ftrade_no=transport-order-1", ""},
		{"get_bad_encoding", http.MethodGet, form + "&bad=%ZZ", ""},
		{"post_bad_encoding", http.MethodPost, "", form + "&bad=%ZZ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// With no service, any attempted order lookup would panic. Parsing
			// failures must be rejected before touching database or provider state.
			var response *httptest.ResponseRecorder
			require.NotPanics(t, func() {
				response = runEasyPayTransportRequest(&PaymentWebhookHandler{}, tc.method, tc.query, tc.body, "application/x-www-form-urlencoded")
			})
			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}

func TestEasyPayWebhook_QueryTransportStillRequiresValidRSA(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider, key := newEasyPayTransportFixture(t)
	valid := signedEasyPayTransportForm(t, key, nil)
	for _, tc := range []struct{ name, query string }{
		{"tampered_amount", strings.Replace(valid, "money=1.02", "money=8.02", 1)},
		{"tampered_extension", strings.Replace(valid, "api_trade_no=alipay-order-1", "api_trade_no=other", 1)},
		{"expired_timestamp", signedEasyPayTransportForm(t, key, map[string]string{"timestamp": strconv.FormatInt(time.Now().Unix()-600, 10)})},
		{"explicit_md5", signedEasyPayTransportForm(t, key, map[string]string{"sign_type": "MD5"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &easyPayTransportVerifier{Provider: provider}
			h, mock := easyPayTransportHandler(t, verifier, "transport-order-1")
			response := runEasyPayTransportRequest(h, http.MethodPost, tc.query, "", "")
			require.NoError(t, mock.ExpectationsWereMet())
			require.Equal(t, tc.query, verifier.raw)
			require.Error(t, verifier.verifyErr)
			require.Nil(t, verifier.notification)
			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}

func TestEasyPayWebhook_RejectsOversizedTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ name, method, query, body string }{
		{"body", http.MethodPost, "", strings.Repeat("x", maxWebhookBodySize+1)},
		{"get_query", http.MethodGet, strings.Repeat("x", maxWebhookBodySize+1), ""},
		{"post_query", http.MethodPost, strings.Repeat("x", maxWebhookBodySize+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response *httptest.ResponseRecorder
			require.NotPanics(t, func() {
				response = runEasyPayTransportRequest(&PaymentWebhookHandler{}, tc.method, tc.query, tc.body, "")
			})
			require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
		})
	}
}

type failingEasyPayBody struct{}

func (failingEasyPayBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingEasyPayBody) Close() error             { return nil }

func TestEasyPayWebhook_BodyReadFailureDoesNotFallBackToQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/payment/webhook/easypay?out_trade_no=order", nil)
	request.Body = failingEasyPayBody{}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	(&PaymentWebhookHandler{}).EasyPayNotify(ctx)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
