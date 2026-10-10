// Package provider contains concrete payment provider implementations.
package provider

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// EasyPay constants.
const (
	easypayCodeSuccess     = 1
	easypayStatusPaid      = 1
	easypayHTTPTimeout     = 10 * time.Second
	maxEasypayResponseSize = 1 << 20 // 1MB
	maxEasypayErrorSummary = 512
	tradeStatusSuccess     = "TRADE_SUCCESS"
	signTypeMD5            = "MD5"
	paymentModePopup       = "popup"
	deviceMobile           = "mobile"
)

// EasyPay implements payment.Provider for the EasyPay aggregation platform.
type EasyPay struct {
	instanceID string
	config     map[string]string
	httpClient *http.Client
}

type easyPayCustomMethod struct {
	Type         string `json:"type"`
	UpstreamType string `json:"upstreamType"`
	DisplayName  string `json:"displayName"`
}

// NewEasyPay creates a new EasyPay provider.
// config keys: pid, pkey, apiBase, notifyUrl, returnUrl, cid, cidAlipay, cidWxpay
func NewEasyPay(instanceID string, config map[string]string) (*EasyPay, error) {
	for _, k := range []string{"pid", "pkey", "apiBase", "notifyUrl", "returnUrl"} {
		if strings.TrimSpace(config[k]) == "" {
			return nil, fmt.Errorf("easypay config missing required key: %s", k)
		}
	}
	cfg := make(map[string]string, len(config))
	for k, v := range config {
		cfg[k] = v
	}
	apiBase, err := validateEasyPayAPIBase(cfg["apiBase"])
	if err != nil {
		return nil, err
	}
	cfg["apiBase"] = apiBase
	return &EasyPay{
		instanceID: instanceID,
		config:     cfg,
		httpClient: newEasyPayHTTPClient(apiBase),
	}, nil
}

func newEasyPayHTTPClient(apiBase string) *http.Client {
	origin := easyPayOrigin(apiBase)
	return &http.Client{
		Timeout: easypayHTTPTimeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if easyPayOrigin(req.URL.String()) != origin {
				return fmt.Errorf("easypay redirect crossed configured origin")
			}
			return http.ErrUseLastResponse
		},
	}
}

func easyPayOrigin(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// validateEasyPayAPIBase prevents a cleartext provider endpoint from becoming
// an authoritative source for paid status. Loopback HTTP remains available
// for deterministic local tests and development; non-loopback deployments
// must use HTTPS.
func validateEasyPayAPIBase(apiBase string) (string, error) {
	base := strings.TrimSpace(apiBase)
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("easypay apiBase must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("easypay apiBase must not contain userinfo, query, or fragment")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("easypay apiBase must use HTTPS outside loopback")
		}
	default:
		return "", errors.New("easypay apiBase must use HTTPS")
	}
	return normalizeEasyPayAPIBase(base), nil
}

func normalizeEasyPayAPIBase(apiBase string) string {
	base := strings.TrimSpace(apiBase)
	if base == "" {
		return ""
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.RawPath = ""
		parsed.Path = trimEasyPayEndpointPath(parsed.Path)
		return strings.TrimRight(parsed.String(), "/")
	}
	return strings.TrimRight(trimEasyPayEndpointPath(base), "/")
}

func trimEasyPayEndpointPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	lower := strings.ToLower(path)
	for _, endpoint := range []string{"/submit.php", "/mapi.php", "/api.php"} {
		if strings.HasSuffix(lower, endpoint) {
			return strings.TrimRight(path[:len(path)-len(endpoint)], "/")
		}
	}
	return path
}

func (e *EasyPay) apiBase() string {
	if e == nil {
		return ""
	}
	return normalizeEasyPayAPIBase(e.config["apiBase"])
}

func (e *EasyPay) Name() string        { return "EasyPay" }
func (e *EasyPay) ProviderKey() string { return payment.TypeEasyPay }
func (e *EasyPay) SupportedTypes() []payment.PaymentType {
	types := []payment.PaymentType{payment.TypeAlipay, payment.TypeWxpay}
	for _, method := range e.customMethods() {
		if method.Type != "" {
			types = append(types, method.Type)
		}
	}
	return types
}

func (e *EasyPay) MerchantIdentityMetadata() map[string]string {
	if e == nil {
		return nil
	}
	pid := strings.TrimSpace(e.config["pid"])
	if pid == "" {
		return nil
	}
	return map[string]string{"pid": pid}
}

func (e *EasyPay) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	// Payment mode determined by instance config, not payment type.
	// "popup" → hosted page (submit.php); "qrcode"/default → API call (mapi.php).
	mode := e.config["paymentMode"]
	if mode == paymentModePopup {
		return e.createRedirectPayment(req)
	}
	return e.createAPIPayment(ctx, req)
}

// createRedirectPayment builds a submit.php URL for browser redirect.
// No server-side API call — the user is redirected to EasyPay's hosted page.
// TradeNo is empty; it arrives via the notify callback after payment.
func (e *EasyPay) createRedirectPayment(req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount,
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	if err := validateEasyPayLegacySignParams(params); err != nil {
		return nil, err
	}
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	payURL := e.apiBase() + "/submit.php?" + q.Encode()
	return &payment.CreatePaymentResponse{PayURL: payURL}, nil
}

// createAPIPayment calls mapi.php to get payurl/qrcode (existing behavior).
func (e *EasyPay) createAPIPayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount, "clientip": req.ClientIP,
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	if err := validateEasyPayLegacySignParams(params); err != nil {
		return nil, err
	}
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	body, err := e.post(ctx, e.apiBase()+"/mapi.php", params)
	if err != nil {
		return nil, fmt.Errorf("easypay create: %w", err)
	}
	var resp struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		TradeNo string `json:"trade_no"`
		PayURL  string `json:"payurl"`
		PayURL2 string `json:"payurl2"` // H5 mobile payment URL
		QRCode  string `json:"qrcode"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("easypay parse: %w", err)
	}
	if resp.Code != easypayCodeSuccess {
		return nil, fmt.Errorf("easypay error: %s", resp.Msg)
	}
	payURL := resp.PayURL
	if req.IsMobile && resp.PayURL2 != "" {
		payURL = resp.PayURL2
	}
	base := e.apiBase()
	return &payment.CreatePaymentResponse{
		TradeNo: resp.TradeNo,
		PayURL:  resolveEasyPayReturnedRef(base, payURL),
		QRCode:  resolveEasyPayReturnedRef(base, resp.QRCode),
	}, nil
}

// resolveEasyPayReturnedRef absolutizes a payurl/payurl2/qrcode reference that
// mapi.php returned, resolving it against the instance's configured apiBase.
//
// EasyPay-compatible upstreams disagree on this field: some answer with a full
// checkout URL, others with a site-root-relative path such as
// "/api/pay/toapp/<order>". createRedirectPayment already builds an absolute URL
// itself (apiBase + "/submit.php?..."), but the mapi.php branch stored whatever
// came back verbatim, and nothing downstream repairs it —
// sanitizeCreatePaymentResponseDetails only strips NUL bytes before the value is
// persisted to pay_url/qr_code. The frontend then feeds qr_code straight into
// QRCode.toCanvas (PaymentQRCodeView.renderQR), so a relative path becomes a QR
// whose payload is a bare path: WeChat renders it as text, and pay_url resolves
// against the gateway's own domain and 404s.
//
// That is precisely the outcome the Alipay provider already refuses to produce —
// "Setting it as QRCode would let the frontend render an unscannable image"
// (createPagePayTrade) — so EasyPay is brought in line with the same rule.
//
// Only references beginning with "/" are resolved. Anything carrying a scheme is
// already actionable and is returned untouched, which covers both absolute
// https:// checkout URLs and app deep links (weixin://, wxp://, alipays://). A
// reference without a leading slash is left alone as well: QR payloads are
// frequently opaque tokens rather than paths, and rewriting "OrderToken123" into
// "<apiBase>/OrderToken123" would break a payload that works today — strictly
// worse than the bug this fixes.
func resolveEasyPayReturnedRef(apiBase, ref string) string {
	trimmed := strings.TrimSpace(ref)
	if !strings.HasPrefix(trimmed, "/") {
		return ref
	}
	base, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return ref
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme != "" {
		return ref
	}
	return base.ResolveReference(parsed).String()
}

// resolveURLs returns (notifyURL, returnURL) preferring request values,
// falling back to instance config.
func (e *EasyPay) resolveURLs(req payment.CreatePaymentRequest) (string, string) {
	notifyURL := req.NotifyURL
	if notifyURL == "" {
		notifyURL = e.config["notifyUrl"]
	}
	returnURL := req.ReturnURL
	if returnURL == "" {
		returnURL = e.config["returnUrl"]
	}
	// EasyPay's legacy signature joins decoded values with '&'. The result
	// page can recover the order from the browser's payment snapshot, while
	// query parameters here would make the outbound signature ambiguous.
	if parsed, err := url.Parse(returnURL); err == nil && parsed.IsAbs() && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		returnURL = parsed.String()
	}
	return notifyURL, returnURL
}

func (e *EasyPay) customMethods() []easyPayCustomMethod {
	if e == nil {
		return nil
	}
	raw := strings.TrimSpace(e.config["customMethods"])
	if raw == "" {
		return nil
	}
	var methods []easyPayCustomMethod
	if err := json.Unmarshal([]byte(raw), &methods); err != nil {
		return nil
	}
	result := make([]easyPayCustomMethod, 0, len(methods))
	for _, method := range methods {
		method.Type = strings.TrimSpace(method.Type)
		method.UpstreamType = strings.TrimSpace(method.UpstreamType)
		method.DisplayName = strings.TrimSpace(method.DisplayName)
		if method.Type == "" || method.UpstreamType == "" {
			continue
		}
		result = append(result, method)
	}
	return result
}

func (e *EasyPay) upstreamPaymentType(paymentType string) string {
	paymentType = strings.TrimSpace(paymentType)
	for _, method := range e.customMethods() {
		if paymentType == method.Type {
			return method.UpstreamType
		}
	}
	return paymentType
}

func (e *EasyPay) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	if strings.TrimSpace(tradeNo) == "" {
		return nil, errors.New("easypay query missing order identifier")
	}
	params := map[string]string{
		"act": "order", "pid": e.config["pid"],
		"key": e.config["pkey"], "out_trade_no": tradeNo,
	}
	body, err := e.post(ctx, e.apiBase()+"/api.php", params)
	if err != nil {
		return nil, fmt.Errorf("easypay query: %w", err)
	}
	type easyPayQueryData struct {
		TradeStatus *string          `json:"trade_status"`
		Status      *int             `json:"status"`
		Money       *string          `json:"money"`
		TradeNo     *string          `json:"trade_no"`
		OutTradeNo  *string          `json:"out_trade_no"`
		PID         *json.RawMessage `json:"pid"`
	}
	var resp struct {
		Code        int              `json:"code"`
		Msg         string           `json:"msg"`
		TradeStatus *string          `json:"trade_status"`
		Status      *int             `json:"status"`
		Money       *string          `json:"money"`
		TradeNo     *string          `json:"trade_no"`
		OutTradeNo  *string          `json:"out_trade_no"`
		PID         *json.RawMessage `json:"pid"`
		Data        easyPayQueryData `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("easypay parse query: %w", err)
	}
	if resp.Code != easypayCodeSuccess {
		return nil, fmt.Errorf("easypay query rejected: code %d: %s", resp.Code, resp.Msg)
	}
	returnedOrderNo := resp.OutTradeNo
	if returnedOrderNo == nil {
		returnedOrderNo = resp.Data.OutTradeNo
	}
	metadata := map[string]string{}
	if returnedOrderNo != nil {
		if *returnedOrderNo != tradeNo {
			return nil, fmt.Errorf("easypay query returned mismatched out_trade_no")
		}
		metadata["out_trade_no"] = *returnedOrderNo
	}
	returnedPID := resp.PID
	if returnedPID == nil {
		returnedPID = resp.Data.PID
	}
	if returnedPID != nil {
		pid, err := parseEasyPayQueryPID(*returnedPID)
		if err != nil {
			return nil, fmt.Errorf("easypay query returned invalid pid: %w", err)
		}
		if pid != e.config["pid"] {
			return nil, fmt.Errorf("easypay query returned mismatched pid")
		}
		metadata["pid"] = pid
	}
	status := payment.ProviderStatusPending
	if resp.TradeStatus != nil {
		if *resp.TradeStatus == tradeStatusSuccess {
			status = payment.ProviderStatusPaid
		}
	} else if resp.Data.TradeStatus != nil {
		if *resp.Data.TradeStatus == tradeStatusSuccess {
			status = payment.ProviderStatusPaid
		}
	} else if resp.Status != nil {
		if *resp.Status == easypayStatusPaid {
			status = payment.ProviderStatusPaid
		}
	} else if resp.Data.Status != nil && *resp.Data.Status == easypayStatusPaid {
		status = payment.ProviderStatusPaid
	}

	money := ""
	if resp.Money != nil {
		money = *resp.Money
	} else if resp.Data.Money != nil {
		money = *resp.Data.Money
	}
	responseTradeNo := ""
	if resp.TradeNo != nil {
		if *resp.TradeNo != "" {
			responseTradeNo = *resp.TradeNo
		}
	} else if resp.Data.TradeNo != nil && *resp.Data.TradeNo != "" {
		responseTradeNo = *resp.Data.TradeNo
	}
	if status == payment.ProviderStatusPaid && strings.TrimSpace(responseTradeNo) == "" {
		return nil, fmt.Errorf("easypay paid query missing upstream trade_no")
	}
	if status == payment.ProviderStatusPaid && (returnedOrderNo == nil || returnedPID == nil) {
		return nil, fmt.Errorf("easypay paid query missing upstream out_trade_no or pid")
	}

	amount, _ := strconv.ParseFloat(money, 64)
	return &payment.QueryOrderResponse{
		TradeNo:  responseTradeNo,
		Status:   status,
		Amount:   amount,
		Metadata: metadata,
	}, nil
}

func parseEasyPayQueryPID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("empty pid")
	}
	if raw[0] == '"' {
		var pid string
		if err := json.Unmarshal(raw, &pid); err != nil {
			return "", err
		}
		return pid, nil
	}
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return "", errors.New("pid must be a string or decimal integer")
		}
	}
	return string(raw), nil
}

func (e *EasyPay) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	values, err := url.ParseQuery(rawBody)
	if err != nil {
		return nil, fmt.Errorf("parse notify: %w", err)
	}
	for key, entries := range values {
		if len(entries) != 1 {
			return nil, fmt.Errorf("duplicate notify parameter: %s", key)
		}
	}
	// url.ParseQuery already decodes values — no additional decode needed.
	params := make(map[string]string)
	for k := range values {
		if !easyPayNotifyAllowedParams[k] {
			return nil, fmt.Errorf("unexpected notify param: %s", k)
		}
		params[k] = values.Get(k)
	}
	if err := validateEasyPayLegacySignParams(params); err != nil {
		return nil, err
	}
	sign := params["sign"]
	if sign == "" {
		return nil, fmt.Errorf("missing sign")
	}
	if signType := strings.TrimSpace(params["sign_type"]); !strings.EqualFold(signType, signTypeMD5) {
		return nil, fmt.Errorf("unsupported sign type: %s", signType)
	}
	if pid := strings.TrimSpace(params["pid"]); pid == "" || pid != strings.TrimSpace(e.config["pid"]) {
		return nil, fmt.Errorf("merchant pid mismatch")
	}
	if !easyPayVerifySign(params, e.config["pkey"], sign) {
		return nil, fmt.Errorf("invalid signature")
	}
	for _, field := range []string{"out_trade_no", "trade_status"} {
		if strings.TrimSpace(params[field]) == "" {
			return nil, fmt.Errorf("missing notify parameter: %s", field)
		}
	}
	status := payment.ProviderStatusFailed
	if params["trade_status"] == tradeStatusSuccess {
		status = payment.ProviderStatusSuccess
		for _, field := range []string{"trade_no", "money"} {
			if strings.TrimSpace(params[field]) == "" {
				return nil, fmt.Errorf("missing notify parameter: %s", field)
			}
		}
	}
	var amount float64
	if params["money"] != "" {
		amount, err = strconv.ParseFloat(params["money"], 64)
		if err != nil || !isFinitePositive(amount) {
			return nil, fmt.Errorf("invalid notify amount")
		}
	}

	metadata := e.MerchantIdentityMetadata()
	if pid := strings.TrimSpace(params["pid"]); pid != "" {
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["pid"] = pid
	}
	return &payment.PaymentNotification{
		TradeNo: params["trade_no"], OrderID: params["out_trade_no"],
		Amount: amount, Status: status, RawData: rawBody, Metadata: metadata,
	}, nil
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (e *EasyPay) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	attempts := e.refundAttempts(req)
	if len(attempts) == 0 {
		return nil, fmt.Errorf("easypay refund missing order identifier")
	}
	var firstErr error
	for i, attempt := range attempts {
		body, status, err := e.postRaw(ctx, e.apiBase()+"/api.php?act=refund", attempt.params)
		if err != nil {
			return nil, fmt.Errorf("easypay refund request: %w", err)
		}
		if err := parseEasyPayRefundResponse(status, body); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if i+1 < len(attempts) && isEasyPayRefundOrderNotFound(err) {
				continue
			}
			return nil, err
		}
		return &payment.RefundResponse{RefundID: attempt.refundID, Status: payment.ProviderStatusSuccess}, nil
	}
	return nil, firstErr
}

type easyPayRefundAttempt struct {
	params   map[string]string
	refundID string
}

func (e *EasyPay) refundAttempts(req payment.RefundRequest) []easyPayRefundAttempt {
	base := map[string]string{
		"pid": e.config["pid"], "key": e.config["pkey"], "money": req.Amount,
	}
	var attempts []easyPayRefundAttempt
	if orderID := strings.TrimSpace(req.OrderID); orderID != "" {
		params := cloneStringMap(base)
		params["out_trade_no"] = orderID
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: orderID})
	}
	if tradeNo := strings.TrimSpace(req.TradeNo); tradeNo != "" {
		params := cloneStringMap(base)
		params["trade_no"] = tradeNo
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: tradeNo})
	}
	return attempts
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func isEasyPayRefundOrderNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(msg, "订单编号不存在") ||
		strings.Contains(msg, "订单不存在") ||
		strings.Contains(lower, "order not found") ||
		strings.Contains(lower, "not exist")
}

func parseEasyPayRefundResponse(status int, body []byte) error {
	summary := summarizeEasyPayResponse(body)
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("easypay refund HTTP %d: %s", status, summary)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("easypay refund empty response (HTTP %d): %s", status, summary)
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") ||
		(strings.HasPrefix(lower, "<") && strings.Contains(lower, "html")) {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}

	var resp struct {
		Code any    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}
	if !easyPayResponseCodeIsSuccess(resp.Code) {
		msg := strings.TrimSpace(resp.Msg)
		if msg == "" {
			msg = summary
		}
		return fmt.Errorf("easypay refund failed (HTTP %d): %s", status, msg)
	}
	return nil
}

func easyPayResponseCodeIsSuccess(code any) bool {
	switch v := code.(type) {
	case float64:
		return int(v) == easypayCodeSuccess
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return err == nil && n == easypayCodeSuccess
	default:
		return false
	}
}

func summarizeEasyPayResponse(body []byte) string {
	summary := strings.Join(strings.Fields(string(body)), " ")
	if summary == "" {
		return "<empty>"
	}
	if len(summary) > maxEasypayErrorSummary {
		truncated := summary[:maxEasypayErrorSummary]
		for len(truncated) > 0 && !utf8.ValidString(truncated) {
			truncated = truncated[:len(truncated)-1]
		}
		return truncated + "..."
	}
	return summary
}

func (e *EasyPay) resolveCID(paymentType string) string {
	if strings.HasPrefix(paymentType, "alipay") {
		if v := e.config["cidAlipay"]; v != "" {
			return v
		}
		return e.config["cid"]
	}
	if v := e.config["cidWxpay"]; v != "" {
		return v
	}
	return e.config["cid"]
}

func (e *EasyPay) post(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
	body, status, err := e.postRaw(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("easypay upstream returned HTTP status %d", status)
	}
	return body, nil
}

func (e *EasyPay) postRaw(ctx context.Context, endpoint string, params map[string]string) ([]byte, int, error) {
	apiBase, err := validateEasyPayAPIBase(e.config["apiBase"])
	if err != nil {
		return nil, 0, err
	}
	origin := easyPayOrigin(apiBase)
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme == "" || target.Host == "" || target.User != nil {
		return nil, 0, errors.New("easypay endpoint must be an absolute URL without userinfo")
	}
	if easyPayOrigin(target.String()) != origin {
		return nil, 0, errors.New("easypay endpoint origin does not match configured apiBase")
	}
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := e.httpClient
	if client == nil {
		client = newEasyPayHTTPClient(apiBase)
	}
	// Do not let a caller-provided client re-enable redirects for requests that
	// carry the merchant key. Keep its transport and proxy settings, but force
	// the same-origin, no-follow policy for this request.
	clientCopy := *client
	clientCopy.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
		if easyPayOrigin(next.URL.String()) != origin {
			return fmt.Errorf("easypay redirect crossed configured origin")
		}
		return http.ErrUseLastResponse
	}
	if clientCopy.Timeout == 0 {
		clientCopy.Timeout = easypayHTTPTimeout
	}
	resp, err := clientCopy.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Request == nil || easyPayOrigin(resp.Request.URL.String()) != origin {
		return nil, resp.StatusCode, errors.New("easypay response origin does not match configured apiBase")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEasypayResponseSize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func easyPaySign(params map[string]string, pkey string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	for i, k := range keys {
		if i > 0 {
			_ = buf.WriteByte('&')
		}
		_, _ = buf.WriteString(k + "=" + params[k])
	}
	_, _ = buf.WriteString(pkey)
	hash := md5.Sum([]byte(buf.String()))
	return hex.EncodeToString(hash[:])
}

// Legacy EasyPay signatures concatenate decoded values without escaping.
// Delimiters or controls would make that representation ambiguous.
func validateEasyPayLegacySignParams(params map[string]string) error {
	for key, value := range params {
		if key == "sign" || key == "sign_type" || value == "" {
			continue
		}
		if strings.ContainsAny(key, "&=") || strings.ContainsAny(value, "&=") ||
			strings.IndexFunc(key, unicode.IsControl) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("unsafe easypay legacy signing parameter: %s", key)
		}
	}
	return nil
}

func easyPayVerifySign(params map[string]string, pkey string, sign string) bool {
	if validateEasyPayLegacySignParams(params) != nil {
		return false
	}
	return hmac.Equal([]byte(easyPaySign(params, pkey)), []byte(sign))
}

// easyPayNotifyAllowedParams is the exact parameter set a genuine EasyPay
// (彩虹易支付-compatible) async notification carries. Order-creation-only
// fields (notify_url, return_url, cid, device, clientip, ...) must never
// appear in a callback: because the sign base string concatenates values
// unescaped, a signed order URL whose return_url embeds e.g.
// "trade_status=TRADE_SUCCESS" could otherwise be replayed as a forged
// payment-success notification (issue #7881). Rejecting unknown keys closes
// the whole smuggling class; genuinely paid orders rejected by an exotic
// upstream variant are still recovered by the upstream QueryOrder reconcile
// path.
var easyPayNotifyAllowedParams = map[string]bool{
	"pid":          true,
	"trade_no":     true,
	"out_trade_no": true,
	"type":         true,
	"name":         true,
	"money":        true,
	"trade_status": true,
	"param":        true,
	"sign":         true,
	"sign_type":    true,
}
