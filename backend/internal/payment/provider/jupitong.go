package provider

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

const (
	jupitongBaseURL         = "https://spay.huiyiy.cn"
	jupitongMaxResponseSize = 1 << 20
	jupitongMaxNotifySize   = 16 << 10
	jupitongMaxNotifyFields = 64
)

// Jupitong implements the official JuFuTong OpenAPI, not the EasyPay protocol.
type Jupitong struct {
	instanceID string
	config     map[string]string
	httpClient *http.Client
}

var _ payment.Provider = (*Jupitong)(nil)
var _ payment.MerchantIdentityProvider = (*Jupitong)(nil)

// NewJupitong accepts the merchant pid, MD5 pkey, and webhook URL. The API host
// is deliberately fixed so configuration cannot redirect signed requests.
func NewJupitong(instanceID string, config map[string]string) (*Jupitong, error) {
	for _, key := range []string{"pid", "pkey", "notifyUrl"} {
		if strings.TrimSpace(config[key]) == "" {
			return nil, fmt.Errorf("jupitong config missing required key: %s", key)
		}
	}
	if signType := strings.TrimSpace(config["signType"]); signType != "" && signType != "MD5" {
		return nil, fmt.Errorf("jupitong supports MD5 signatures only")
	}
	if apiBase := strings.TrimSpace(config["apiBase"]); apiBase != "" && apiBase != jupitongBaseURL {
		return nil, fmt.Errorf("jupitong API host must be %s", jupitongBaseURL)
	}
	if err := jupitongValidateURL(config["notifyUrl"]); err != nil {
		return nil, fmt.Errorf("jupitong notifyUrl: %w", err)
	}
	if config["returnUrl"] != "" {
		if err := jupitongValidateURL(config["returnUrl"]); err != nil {
			return nil, fmt.Errorf("jupitong returnUrl: %w", err)
		}
	}
	cfg := make(map[string]string, len(config))
	for key, value := range config {
		cfg[key] = value
	}
	return &Jupitong{
		instanceID: instanceID,
		config:     cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (j *Jupitong) Name() string        { return "JuFuTong" }
func (j *Jupitong) ProviderKey() string { return payment.TypeJupitong }
func (j *Jupitong) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeAlipay, payment.TypeWxpay}
}
func (j *Jupitong) MerchantIdentityMetadata() map[string]string {
	return map[string]string{"pid": j.config["pid"]}
}

func (j *Jupitong) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	if req.PaymentType != payment.TypeAlipay && req.PaymentType != payment.TypeWxpay {
		return nil, fmt.Errorf("jupitong unsupported payment type: %s", req.PaymentType)
	}
	if strings.TrimSpace(req.OrderID) == "" || strings.TrimSpace(req.Subject) == "" || strings.TrimSpace(req.ClientIP) == "" {
		return nil, errors.New("jupitong create requires order ID, subject, and client IP")
	}
	amount, err := jupitongAmount(req.Amount)
	if err != nil {
		return nil, fmt.Errorf("jupitong amount: %w", err)
	}
	notifyURL := req.NotifyURL
	if notifyURL == "" {
		notifyURL = j.config["notifyUrl"]
	}
	returnURL := req.ReturnURL
	if returnURL == "" {
		returnURL = j.config["returnUrl"]
	}
	if err := jupitongValidateURL(notifyURL); err != nil {
		return nil, fmt.Errorf("jupitong notify URL: %w", err)
	}
	if returnURL != "" {
		if err := jupitongValidateURL(returnURL); err != nil {
			return nil, fmt.Errorf("jupitong return URL: %w", err)
		}
	}
	params := map[string]string{
		"pid": j.config["pid"], "out_trade_no": req.OrderID,
		"total_amount": amount, "subject": req.Subject,
		"paytype_code": req.PaymentType, "notify_url": notifyURL,
		"client_ip": req.ClientIP, "timestamp": strconv.FormatInt(time.Now().Unix(), 10),
	}
	// The official SDK signs raw k=v pairs. Query-bearing return URLs would
	// introduce extra separators into that canonical string, so omit this
	// optional field until a queryless return route is available.
	if returnURL != "" && !jupitongAmbiguousValue(returnURL) {
		params["return_url"] = returnURL
	}
	if channelID := strings.TrimSpace(j.config["channelId"]); channelID != "" {
		params["channel_id"] = channelID
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			TradeNo    string `json:"trade_no"`
			OutTradeNo string `json:"out_trade_no"`
			PayURL     string `json:"pay_url"`
		} `json:"data"`
	}
	if err := j.post(ctx, "/openapi/pay/create", params, &resp); err != nil {
		return nil, fmt.Errorf("jupitong create: %w", err)
	}
	if resp.Code != 1 {
		return nil, fmt.Errorf("jupitong create rejected: code %d: %s", resp.Code, resp.Msg)
	}
	if resp.Data.OutTradeNo != req.OrderID || strings.TrimSpace(resp.Data.TradeNo) == "" {
		return nil, errors.New("jupitong create returned mismatched order or missing trade number")
	}
	if err := jupitongValidateURL(resp.Data.PayURL); err != nil {
		return nil, fmt.Errorf("jupitong pay URL: %w", err)
	}
	return &payment.CreatePaymentResponse{TradeNo: resp.Data.TradeNo, PayURL: resp.Data.PayURL}, nil
}

func (j *Jupitong) QueryOrder(ctx context.Context, orderID string) (*payment.QueryOrderResponse, error) {
	if strings.TrimSpace(orderID) == "" {
		return nil, errors.New("jupitong query requires order ID")
	}
	params := map[string]string{
		"pid": j.config["pid"], "out_trade_no": orderID,
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			TradeNo     string          `json:"trade_no"`
			OutTradeNo  string          `json:"out_trade_no"`
			TotalAmount json.RawMessage `json:"total_amount"`
			TradeStatus string          `json:"trade_status"`
		} `json:"data"`
	}
	if err := j.post(ctx, "/openapi/pay/query", params, &resp); err != nil {
		return nil, fmt.Errorf("jupitong query: %w", err)
	}
	if resp.Code != 1 {
		return nil, fmt.Errorf("jupitong query rejected: code %d: %s", resp.Code, resp.Msg)
	}
	if resp.Data.OutTradeNo == "" || resp.Data.OutTradeNo != orderID {
		return nil, errors.New("jupitong query returned mismatched or missing order ID")
	}
	status, err := jupitongStatus(resp.Data.TradeStatus)
	if err != nil {
		return nil, err
	}
	var amount float64
	if status == payment.ProviderStatusPaid {
		if strings.TrimSpace(resp.Data.TradeNo) == "" {
			return nil, errors.New("jupitong paid query missing upstream trade number")
		}
		amount, err = jupitongQueryAmount(resp.Data.TotalAmount)
		if err != nil {
			return nil, fmt.Errorf("jupitong paid query amount: %w", err)
		}
	}
	return &payment.QueryOrderResponse{
		TradeNo: resp.Data.TradeNo, Status: status, Amount: amount,
		Metadata: map[string]string{"pid": j.config["pid"], "out_trade_no": resp.Data.OutTradeNo},
	}, nil
}

func jupitongQueryAmount(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 || len(raw) > 64 {
		return 0, errors.New("amount is missing or too long")
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, errors.New("invalid amount string")
		}
	} else {
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return 0, errors.New("amount must be a JSON number or string")
		}
		text = number.String()
	}
	amount, err := decimal.NewFromString(text)
	if err != nil {
		return 0, errors.New("amount must be positive decimal")
	}
	if amount.Exponent() < -64 || amount.Exponent() > 64 {
		return 0, errors.New("amount exponent is out of range")
	}
	if !amount.GreaterThan(decimal.Zero) {
		return 0, errors.New("amount must be positive decimal")
	}
	cents := amount.Mul(decimal.NewFromInt(100))
	if !cents.Equal(cents.Truncate(0)) || cents.GreaterThan(decimal.NewFromInt(1<<63-1)) {
		return 0, errors.New("amount must fit whole cents")
	}
	result, _ := amount.Float64()
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, errors.New("amount must be finite")
	}
	return result, nil
}

func (j *Jupitong) VerifyNotification(_ context.Context, rawBody string, headers map[string]string) (*payment.PaymentNotification, error) {
	contentType, err := jupitongNotificationContentType(headers)
	if err != nil {
		return nil, err
	}
	values, err := ParseJupitongNotification(rawBody, contentType)
	if err != nil {
		return nil, fmt.Errorf("jupitong parse notification: %w", err)
	}
	params := make(map[string]string, len(values))
	for key, entries := range values {
		if len(entries) != 1 {
			return nil, fmt.Errorf("jupitong duplicate notification parameter: %s", key)
		}
		params[key] = entries[0]
	}
	for _, key := range []string{"pid", "out_trade_no", "trade_status", "sign"} {
		if strings.TrimSpace(params[key]) == "" {
			return nil, fmt.Errorf("jupitong notification missing %s", key)
		}
	}
	if params["pid"] != j.config["pid"] {
		return nil, errors.New("jupitong merchant pid mismatch")
	}
	// JuFuTong's official SDK treats an omitted sign_type as MD5 and
	// normalizes case. Keep that compatibility while rejecting every other
	// algorithm, including RSA, before signature verification.
	if signType := strings.TrimSpace(params["sign_type"]); signType != "" && !strings.EqualFold(signType, "MD5") {
		return nil, errors.New("jupitong unsupported notification signature type")
	}
	if err := jupitongVerify(params, j.config["pkey"]); err != nil {
		return nil, fmt.Errorf("jupitong notification signature: %w", err)
	}
	status, err := jupitongStatus(params["trade_status"])
	if err != nil {
		return nil, err
	}
	var amount float64
	if status == payment.ProviderStatusPaid {
		for _, key := range []string{"trade_no", "total_amount"} {
			if strings.TrimSpace(params[key]) == "" {
				return nil, fmt.Errorf("jupitong successful notification missing %s", key)
			}
		}
		amount, err = jupitongAmountFloat(params["total_amount"])
		if err != nil {
			return nil, fmt.Errorf("jupitong notification amount: %w", err)
		}
	}
	if raw, ok := params["timestamp"]; ok && strings.TrimSpace(raw) != "" {
		if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
			return nil, errors.New("jupitong notification timestamp invalid")
		}
	}
	if raw, ok := params["paytype_code"]; ok && strings.TrimSpace(raw) != "" {
		if raw != payment.TypeAlipay && raw != payment.TypeWxpay {
			return nil, errors.New("jupitong notification payment type unsupported")
		}
	}
	// JuFuTong currently emits multipart callbacks with an empty subject (and
	// may omit or leave channel_id empty), despite documenting both fields as
	// required for payment creation/callbacks. These descriptive fields are
	// already covered by the signed payload and are not used to authorize an
	// order. Keep accepting their empty form values after all identity,
	// signature, status, trade-number, and amount checks above; rejecting them
	// makes a real paid callback fail before the authoritative order lookup.
	if status == payment.ProviderStatusPaid {
		status = payment.ProviderStatusSuccess
	}
	return &payment.PaymentNotification{
		TradeNo: params["trade_no"], OrderID: params["out_trade_no"],
		Amount: amount, Status: status, RawData: rawBody,
		Metadata: map[string]string{"pid": params["pid"], "out_trade_no": params["out_trade_no"]},
	}, nil
}

// ParseJupitongNotification parses form and multipart callbacks for both order
// lookup and signature verification. Multipart field values are kept verbatim.
func ParseJupitongNotification(rawBody, contentType string) (url.Values, error) {
	if len(rawBody) > jupitongMaxNotifySize {
		return nil, errors.New("notification too large")
	}
	mediaType := "application/x-www-form-urlencoded"
	mediaParams := map[string]string{}
	if strings.TrimSpace(contentType) != "" {
		var err error
		mediaType, mediaParams, err = mime.ParseMediaType(contentType)
		if err != nil {
			return nil, fmt.Errorf("invalid content type: %w", err)
		}
		mediaType = strings.ToLower(mediaType)
	}

	switch mediaType {
	case "application/x-www-form-urlencoded":
		parsed, err := parseJupitongForm(rawBody)
		if err != nil {
			return nil, err
		}
		values := make(url.Values, len(parsed))
		fieldCount := 0
		for name, entries := range parsed {
			if !validJupitongFieldName(name) {
				return nil, errors.New("invalid notification field name")
			}
			fieldCount += len(entries)
			if fieldCount > jupitongMaxNotifyFields {
				return nil, errors.New("too many notification fields")
			}
			if len(entries) != 1 {
				return nil, fmt.Errorf("duplicate notification parameter: %s", name)
			}
			values[name] = []string{entries[0]}
		}
		return values, nil
	case "multipart/form-data":
		boundary := mediaParams["boundary"]
		if boundary == "" {
			return nil, errors.New("multipart boundary missing")
		}
		reader := multipart.NewReader(strings.NewReader(rawBody), boundary)
		values := make(url.Values)
		fieldCount := 0
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("invalid multipart body: %w", err)
			}
			fieldCount++
			if fieldCount > jupitongMaxNotifyFields {
				return nil, errors.New("too many notification fields")
			}
			name, err := jupitongMultipartFieldName(part)
			if err != nil {
				return nil, err
			}
			if _, exists := values[name]; exists {
				return nil, fmt.Errorf("duplicate notification parameter: %s", name)
			}
			value, err := io.ReadAll(io.LimitReader(part, jupitongMaxNotifySize+1))
			if err != nil {
				return nil, fmt.Errorf("read multipart field %s: %w", name, err)
			}
			if len(value) > jupitongMaxNotifySize {
				return nil, errors.New("multipart field too large")
			}
			if !utf8.Valid(value) {
				return nil, fmt.Errorf("multipart field %s is not UTF-8", name)
			}
			values[name] = []string{string(value)}
		}
		return values, nil
	default:
		return nil, errors.New("unsupported notification content type")
	}
}

func jupitongMultipartFieldName(part *multipart.Part) (string, error) {
	dispositions := part.Header.Values("Content-Disposition")
	if len(dispositions) != 1 {
		return "", errors.New("multipart field must have exactly one content disposition")
	}
	disposition, params, err := mime.ParseMediaType(dispositions[0])
	if err != nil || !strings.EqualFold(disposition, "form-data") {
		return "", errors.New("invalid multipart content disposition")
	}
	if _, exists := params["filename"]; exists {
		return "", errors.New("multipart files are not accepted")
	}
	for key := range params {
		if key != "name" {
			return "", errors.New("unsupported multipart disposition parameter")
		}
	}
	name := params["name"]
	if !validJupitongFieldName(name) {
		return "", errors.New("invalid multipart field name")
	}
	for key, values := range part.Header {
		switch strings.ToLower(key) {
		case "content-disposition":
		case "content-transfer-encoding":
			return "", errors.New("multipart transfer encoding is not accepted")
		case "content-type":
			if len(values) != 1 {
				return "", errors.New("duplicate multipart content type")
			}
			mediaType, _, err := mime.ParseMediaType(values[0])
			if err != nil || !strings.EqualFold(mediaType, "text/plain") {
				return "", errors.New("non-scalar multipart field")
			}
		default:
			return "", errors.New("unsupported multipart part header")
		}
	}
	return name, nil
}

func validJupitongFieldName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func jupitongNotificationContentType(headers map[string]string) (string, error) {
	var contentType string
	found := false
	for name, value := range headers {
		if !strings.EqualFold(name, "content-type") {
			continue
		}
		if found {
			return "", errors.New("duplicate notification content type header")
		}
		contentType = value
		found = true
	}
	return contentType, nil
}

// parseJupitongForm retains the legacy raw separator handling used by form callbacks.
// Split before decoding so encoded separators remain part of their field values.
func parseJupitongForm(raw string) (map[string][]string, error) {
	values := make(map[string][]string)
	if raw == "" {
		return values, nil
	}
	for start := 0; start <= len(raw); {
		// Only discard ASCII field padding at the raw boundary. Do not trim
		// decoded names, which would turn an encoded/malformed name into a
		// different signed parameter.
		for start < len(raw) && (raw[start] == ' ' || raw[start] == '\t') {
			start++
		}
		end := start
		for end < len(raw) && raw[end] != '&' && raw[end] != ';' {
			end++
		}
		field := raw[start:end]
		if field != "" {
			keyPart, valuePart, hasValue := strings.Cut(field, "=")
			if !hasValue {
				valuePart = ""
			}
			keyPart = strings.TrimLeft(keyPart, " \t")
			key, err := url.QueryUnescape(keyPart)
			if err != nil {
				return nil, fmt.Errorf("invalid parameter name: %w", err)
			}
			value, err := url.QueryUnescape(valuePart)
			if err != nil {
				return nil, fmt.Errorf("invalid parameter %s: %w", key, err)
			}
			values[key] = append(values[key], value)
		}
		if end == len(raw) {
			break
		}
		start = end + 1
	}
	return values, nil
}

func (j *Jupitong) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, errors.New("jupitong refund is not supported")
}

func (j *Jupitong) post(ctx context.Context, path string, params map[string]string, out any) error {
	sign, err := jupitongSign(params, j.config["pkey"])
	if err != nil {
		return err
	}
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	form.Set("sign_type", "MD5")
	form.Set("sign", sign)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, jupitongBaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := j.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jupitongMaxResponseSize+1))
	if err != nil {
		return err
	}
	if len(body) > jupitongMaxResponseSize {
		return errors.New("jupitong response too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("jupitong response has trailing content")
	}
	return nil
}

// The official Go SDK concatenates sorted raw k=v pairs. Reject ambiguous
// delimiters before signing rather than accepting a second canonical form.
func jupitongSign(params map[string]string, key string) (string, error) {
	keys := make([]string, 0, len(params))
	for name := range params {
		if name != "sign" && name != "sign_type" && params[name] != "" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	for _, name := range keys {
		if name == "" {
			return "", errors.New("empty signing parameter name")
		}
		for _, char := range name {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
				return "", fmt.Errorf("invalid signing parameter name: %s", name)
			}
		}
		value := params[name]
		if jupitongAmbiguousValue(value) {
			return "", fmt.Errorf("ambiguous signing parameter: %s", name)
		}
		parts = append(parts, name+"="+value)
	}
	parts = append(parts, "key="+key)
	sum := md5.Sum([]byte(strings.Join(parts, "&")))
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

func jupitongAmbiguousValue(value string) bool {
	return !utf8.ValidString(value) || strings.ContainsAny(value, "&=\r\n\x00") ||
		strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func jupitongVerify(params map[string]string, key string) error {
	provided := params["sign"]
	if len(provided) != 32 || provided != strings.ToUpper(provided) {
		return errors.New("invalid MD5 signature format")
	}
	expected, err := jupitongSign(params, key)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return errors.New("invalid MD5 signature")
	}
	return nil
}

func jupitongStatus(status string) (string, error) {
	switch status {
	case "WAIT_BUYER_PAY", "TRADE_UNFREEZE":
		return payment.ProviderStatusPending, nil
	case "TRADE_SUCCESS":
		return payment.ProviderStatusPaid, nil
	case "TRADE_REFUND":
		return payment.ProviderStatusRefunded, nil
	case "TRADE_CLOSED", "TRADE_FREEZE":
		return payment.ProviderStatusFailed, nil
	default:
		return "", fmt.Errorf("jupitong unknown trade status: %s", status)
	}
}

func jupitongAmount(raw string) (string, error) {
	amount, err := jupitongAmountFloat(raw)
	if err != nil {
		return "", err
	}
	if strconv.FormatFloat(amount, 'f', 2, 64) != raw {
		return "", errors.New("amount must have exactly two decimal places")
	}
	return raw, nil
}

func jupitongAmountFloat(raw string) (float64, error) {
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil || amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, errors.New("amount must be finite and positive")
	}
	return amount, nil
}

func jupitongValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("expected absolute HTTPS URL")
	}
	return nil
}
