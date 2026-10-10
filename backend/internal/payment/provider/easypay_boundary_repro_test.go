package provider

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// TestEasyPaySignatureBoundaryReproduction is an offline regression fixture for
// the legacy EasyPay signing rule. It never contacts a payment provider and
// uses a synthetic merchant key and order identifiers.
func TestEasyPaySignatureBoundaryReproduction(t *testing.T) {
	const key = "local-reproduction-key"

	// Two semantic payloads produce the same legacy signing text because the
	// first payload places a decoded '&trade_status=' inside trade_no.
	legacyPayload := map[string]string{
		"money":        "1.00",
		"out_trade_no": "LOCAL-ORDER-1",
		"pid":          "LOCAL-PID",
		"trade_no":     "FAKE-TRADE&trade_status=TRADE_SUCCESS",
	}
	callbackPayload := map[string]string{
		"money":        "1.00",
		"out_trade_no": "LOCAL-ORDER-1",
		"pid":          "LOCAL-PID",
		"trade_no":     "FAKE-TRADE",
		"trade_status": "TRADE_SUCCESS",
	}

	legacyText := legacyEasyPaySigningText(legacyPayload)
	callbackText := legacyEasyPaySigningText(callbackPayload)
	legacySign := easyPaySign(legacyPayload, key)
	callbackSign := easyPaySign(callbackPayload, key)
	if legacyText != callbackText || legacySign != callbackSign {
		t.Fatalf("fixture no longer reproduces the legacy collision: text=%q/%q sign=%q/%q", legacyText, callbackText, legacySign, callbackSign)
	}

	if err := validateEasyPayLegacySignParams(legacyPayload); err == nil {
		t.Fatal("delimiter-bearing legacy signing input must be rejected")
	}
	if easyPayVerifySign(legacyPayload, key, legacySign) {
		t.Fatal("legacy verifier accepted a delimiter-bearing input")
	}
	if err := validateEasyPayLegacySignParams(callbackPayload); err != nil {
		t.Fatalf("ordinary legacy callback became incompatible: %v", err)
	}

	hardenedLegacySign := hardenedEasyPaySign(legacyPayload, key)
	hardenedCallbackSign := hardenedEasyPaySign(callbackPayload, key)
	if hardenedLegacySign == hardenedCallbackSign {
		t.Fatalf("URL-encoded signing control still collides: %q", hardenedLegacySign)
	}

	// The benign-looking forged callback is indistinguishable under raw MD5.
	// Fulfillment must independently query the provider before crediting.
}

func legacyEasyPaySigningText(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			_ = b.WriteByte('&')
		}
		_, _ = b.WriteString(k)
		_ = b.WriteByte('=')
		_, _ = b.WriteString(params[k])
	}
	return b.String()
}

func hardenedEasyPaySign(params map[string]string, pkey string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(params[k]))
	}
	h := md5.Sum([]byte(strings.Join(parts, "&") + pkey))
	return hex.EncodeToString(h[:])
}
