package provider

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// EasyPayV2 is deliberately separate from the legacy MD5 protocol. A failure
// must never silently downgrade an RSA instance to V1.
type EasyPayV2 struct {
	instanceID string
	config     map[string]string
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	httpClient *http.Client
	now        func() time.Time
}

func NewEasyPayV2(instanceID string, config map[string]string) (*EasyPayV2, error) {
	version, err := payment.NormalizeEasyPayAPIVersion(config["apiVersion"])
	if err != nil || version != payment.EasyPayAPIVersionV2 {
		return nil, errors.New("easypay V2 requires apiVersion v2")
	}
	for _, key := range []string{"pid", "privateKey", "publicKey", "apiBase", "notifyUrl", "returnUrl"} {
		if strings.TrimSpace(config[key]) == "" {
			return nil, fmt.Errorf("easypay V2 config missing required key: %s", key)
		}
	}
	cfg := cloneStringMap(config)
	base := strings.TrimRight(strings.TrimSpace(cfg["apiBase"]), "/")
	for _, suffix := range []string{"/api/pay/submit", "/api/pay/create", "/api/pay/query", "/api/pay/refund", "/api/pay/refundquery"} {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			base = base[:len(base)-len(suffix)]
			break
		}
	}
	cfg["apiBase"], err = validateEasyPayAPIBase(base)
	if err != nil {
		return nil, err
	}
	cfg["pid"] = strings.TrimSpace(cfg["pid"])
	privateKey, err := parseEasyPayV2PrivateKey(cfg["privateKey"])
	if err != nil {
		return nil, err
	}
	publicKey, err := parseEasyPayV2PublicKey(cfg["publicKey"])
	if err != nil {
		return nil, err
	}
	return &EasyPayV2{instanceID: instanceID, config: cfg, privateKey: privateKey, publicKey: publicKey, httpClient: newEasyPayHTTPClient(cfg["apiBase"]), now: time.Now}, nil
}

func (e *EasyPayV2) Name() string        { return "EasyPay V2" }
func (e *EasyPayV2) ProviderKey() string { return payment.TypeEasyPay }
func (e *EasyPayV2) legacyHelpers() *EasyPay {
	return &EasyPay{instanceID: e.instanceID, config: e.config, httpClient: e.httpClient}
}
func (e *EasyPayV2) SupportedTypes() []payment.PaymentType { return e.legacyHelpers().SupportedTypes() }
func (e *EasyPayV2) MerchantIdentityMetadata() map[string]string {
	return map[string]string{"pid": e.config["pid"]}
}

func (e *EasyPayV2) requestParams(params map[string]string) (map[string]string, error) {
	params = cloneStringMap(params)
	params["pid"] = e.config["pid"]
	params["timestamp"] = strconv.FormatInt(e.now().Unix(), 10)
	params["sign_type"] = "RSA"
	sign, err := easyPayV2Sign(params, e.privateKey)
	if err != nil {
		return nil, err
	}
	params["sign"] = sign
	return params, nil
}

// execute never follows HTTP redirects or retries a mutation. Error response
// bodies are deliberately omitted from errors: providers can echo credentials.
func (e *EasyPayV2) execute(ctx context.Context, action string, params map[string]string) (map[string]string, error) {
	params, err := e.requestParams(params)
	if err != nil {
		return nil, err
	}
	body, status, err := e.legacyHelpers().postRaw(ctx, e.config["apiBase"]+"/api/pay/"+action, params)
	if err != nil {
		return nil, fmt.Errorf("easypay V2 %s transport failed", action)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("easypay V2 %s HTTP %d", action, status)
	}
	result, err := decodeEasyPayV2Object(body)
	if err != nil {
		return nil, err
	}
	code, err := easyPayV2Integer(result, "code")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("easypay V2 %s returned code %d", action, code)
	}
	if err := easyPayV2Verify(result, e.publicKey, e.now()); err != nil {
		return nil, err
	}
	if pid, present := result["pid"]; present && pid != e.config["pid"] {
		return nil, errors.New("easypay V2 merchant pid mismatch")
	}
	return result, nil
}

func (e *EasyPayV2) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	if strings.TrimSpace(req.OrderID) == "" {
		return nil, errors.New("easypay V2 create missing order identifier")
	}
	if _, err := easyPayV2Money(req.Amount); err != nil {
		return nil, err
	}
	notifyURL, returnURL := req.NotifyURL, req.ReturnURL
	if notifyURL == "" {
		notifyURL = e.config["notifyUrl"]
	}
	if returnURL == "" {
		returnURL = e.config["returnUrl"]
	}
	params := map[string]string{"type": e.legacyHelpers().upstreamPaymentType(req.PaymentType), "out_trade_no": req.OrderID, "notify_url": notifyURL, "return_url": returnURL, "name": req.Subject, "money": req.Amount, "clientip": req.ClientIP}
	params["device"] = "pc"
	if req.IsMobile {
		params["device"] = "mobile"
	}
	// V2 channel_id denotes an onboarded merchant. Legacy cid/cidAlipay/
	// cidWxpay are intentionally not translated into it.
	if strings.EqualFold(strings.TrimSpace(e.config["paymentMode"]), paymentModePopup) {
		signed, err := e.requestParams(params)
		if err != nil {
			return nil, err
		}
		query := url.Values{}
		for key, value := range signed {
			query.Set(key, value)
		}
		return &payment.CreatePaymentResponse{PayURL: e.config["apiBase"] + "/api/pay/submit?" + query.Encode()}, nil
	}
	params["method"] = "jump"
	result, err := e.execute(ctx, "create", params)
	if err != nil {
		return nil, err
	}
	if result["pay_type"] != "jump" {
		return nil, errors.New("easypay V2 returned unsupported pay_type for method jump")
	}
	payURL := resolveEasyPayReturnedRef(e.config["apiBase"], result["pay_info"])
	if err := validateEasyPayV2JumpURL(payURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(result["trade_no"]) == "" {
		return nil, errors.New("easypay V2 create missing trade_no")
	}
	if orderID, present := result["out_trade_no"]; present && orderID != req.OrderID {
		return nil, errors.New("easypay V2 create order mismatch")
	}
	return &payment.CreatePaymentResponse{TradeNo: result["trade_no"], PayURL: payURL}, nil
}

func validateEasyPayV2JumpURL(raw string) error {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t\x00\\") {
		return errors.New("easypay V2 invalid jump URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("easypay V2 invalid jump URL")
	}
	return nil
}

// QueryOrder receives our merchant order number, matching the existing service
// contract. A signed response must bind it to the configured merchant.
func (e *EasyPayV2) QueryOrder(ctx context.Context, orderID string) (*payment.QueryOrderResponse, error) {
	if strings.TrimSpace(orderID) == "" {
		return nil, errors.New("easypay V2 query missing order identifier")
	}
	result, err := e.execute(ctx, "query", map[string]string{"out_trade_no": orderID})
	if err != nil {
		return nil, err
	}
	// Standard V2 query responses may omit pid. execute checks it whenever
	// present; the signed order ID must still bind this response to our request.
	if result["out_trade_no"] != orderID || strings.TrimSpace(result["trade_no"]) == "" {
		return nil, errors.New("easypay V2 query merchant or order mismatch")
	}
	amount, err := easyPayV2Amount(result["money"])
	if err != nil {
		return nil, err
	}
	code, err := easyPayV2Integer(result, "status")
	if err != nil {
		return nil, err
	}
	status := payment.ProviderStatusPending
	switch code {
	case 0, 3, 4: // Frozen/preauthorized are never credited as paid.
	case 1:
		status = payment.ProviderStatusPaid
	case 2:
		status = payment.ProviderStatusRefunded
	default:
		return nil, errors.New("easypay V2 unknown payment status")
	}
	metadata := e.MerchantIdentityMetadata()
	metadata["out_trade_no"] = result["out_trade_no"]
	return &payment.QueryOrderResponse{TradeNo: result["trade_no"], Status: status, Amount: amount, PaidAt: result["endtime"], Metadata: metadata}, nil
}

func (e *EasyPayV2) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	values, err := url.ParseQuery(rawBody)
	if err != nil {
		return nil, errors.New("easypay V2 invalid notification encoding")
	}
	params := map[string]string{}
	for key, value := range values {
		if len(value) != 1 || key == "" || strings.ContainsAny(key, "&=\r\n\x00[]") {
			return nil, errors.New("easypay V2 duplicate or invalid notification field")
		}
		params[key] = value[0]
	}
	if err := easyPayV2Verify(params, e.publicKey, e.now()); err != nil {
		return nil, err
	}
	if params["pid"] != e.config["pid"] {
		return nil, errors.New("easypay V2 merchant pid mismatch")
	}
	if strings.TrimSpace(params["trade_no"]) == "" || strings.TrimSpace(params["out_trade_no"]) == "" {
		return nil, errors.New("easypay V2 notification missing order identifier")
	}
	if params["trade_status"] != tradeStatusSuccess {
		return nil, errors.New("easypay V2 notification is not TRADE_SUCCESS")
	}
	amount, err := easyPayV2Amount(params["money"])
	if err != nil {
		return nil, err
	}
	return &payment.PaymentNotification{TradeNo: params["trade_no"], OrderID: params["out_trade_no"], Amount: amount, Status: payment.ProviderStatusSuccess, RawData: rawBody, Metadata: e.MerchantIdentityMetadata()}, nil
}

func (e *EasyPayV2) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	if strings.TrimSpace(req.RefundID) == "" || strings.TrimSpace(req.OrderID) == "" {
		return nil, errors.New("easypay V2 refund requires stable refund and order identifiers")
	}
	if _, err := easyPayV2Money(req.Amount); err != nil {
		return nil, err
	}
	tradeNo := req.TradeNo
	if strings.TrimSpace(tradeNo) == "" {
		order, err := e.QueryOrder(ctx, req.OrderID)
		if err != nil {
			return nil, fmt.Errorf("easypay V2 resolve refund order: %w", err)
		}
		tradeNo = order.TradeNo
	}
	params := map[string]string{"trade_no": tradeNo, "money": req.Amount, "out_refund_no": req.RefundID}
	result, err := e.execute(ctx, "refund", params)
	// A dispatched mutation with no verified success is ambiguous. Keep the
	// original idempotency key and reconcile through refundquery, never retry
	// under another key or report a definitive failure here.
	pending := &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusPending}
	if err != nil {
		return pending, err
	}
	if err := e.validateRefundResult(result, req.RefundID, req.OrderID, tradeNo, req.Amount, false); err != nil {
		return pending, err
	}
	return &payment.RefundResponse{RefundID: req.RefundID, Status: payment.ProviderStatusSuccess, Metadata: map[string]string{"refund_no": result["refund_no"]}}, nil
}

func (e *EasyPayV2) QueryRefund(ctx context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	if strings.TrimSpace(req.RefundID) == "" || strings.TrimSpace(req.OrderID) == "" {
		return nil, errors.New("easypay V2 refund query requires refund and order identifiers")
	}
	result, err := e.execute(ctx, "refundquery", map[string]string{"out_refund_no": req.RefundID})
	if err != nil {
		return nil, err
	}
	if err := e.validateRefundResult(result, req.RefundID, req.OrderID, req.TradeNo, req.Amount, true); err != nil {
		return nil, err
	}
	code, err := easyPayV2Integer(result, "status")
	if err != nil {
		return nil, err
	}
	status := payment.ProviderStatusFailed
	switch code {
	case 0:
	case 1:
		status = payment.ProviderStatusSuccess
	default:
		return nil, errors.New("easypay V2 unknown refund status")
	}
	return &payment.RefundResponse{RefundID: req.RefundID, Status: status, Metadata: map[string]string{"refund_no": result["refund_no"]}}, nil
}

func (e *EasyPayV2) validateRefundResult(result map[string]string, refundID, orderID, tradeNo, amount string, query bool) error {
	if result["out_refund_no"] != refundID || strings.TrimSpace(result["refund_no"]) == "" || strings.TrimSpace(result["trade_no"]) == "" {
		return errors.New("easypay V2 refund identifier mismatch")
	}
	if tradeNo != "" && result["trade_no"] != tradeNo {
		return errors.New("easypay V2 refund trade mismatch")
	}
	if query && result["out_trade_no"] != orderID {
		return errors.New("easypay V2 refund order mismatch")
	}
	if returnedOrder, present := result["out_trade_no"]; present && returnedOrder != orderID {
		return errors.New("easypay V2 refund order mismatch")
	}
	want, err := easyPayV2Money(amount)
	if err != nil {
		return err
	}
	got, err := easyPayV2Money(result["money"])
	if err != nil || got.Cmp(want) != 0 {
		return errors.New("easypay V2 refund amount mismatch")
	}
	return nil
}

// Compare decimal money exactly; do not round malformed fractions or exponent
// notation into a valid refund amount.
func easyPayV2Money(raw string) (*big.Int, error) {
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 18 {
		return nil, errors.New("easypay V2 invalid money")
	}
	frac := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return nil, errors.New("easypay V2 invalid money")
		}
		frac = parts[1] + strings.Repeat("0", 2-len(parts[1]))
	}
	value := parts[0] + frac
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, errors.New("easypay V2 invalid money")
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.Sign() <= 0 {
		return nil, errors.New("easypay V2 invalid money")
	}
	return n, nil
}

func easyPayV2Amount(raw string) (float64, error) {
	if _, err := easyPayV2Money(raw); err != nil {
		return 0, err
	}
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil || !isFinitePositive(amount) {
		return 0, errors.New("easypay V2 invalid money")
	}
	return amount, nil
}

var _ payment.Provider = (*EasyPayV2)(nil)
var _ payment.RefundQueryProvider = (*EasyPayV2)(nil)
var _ payment.MerchantIdentityProvider = (*EasyPayV2)(nil)
