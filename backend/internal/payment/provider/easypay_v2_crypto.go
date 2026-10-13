package provider

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// V2 signs raw, sorted scalar values; unlike V1 it never appends a shared key.
// Arrays, objects, null and whitespace-only values are excluded by the PHP SDK.
func easyPayV2SignContent(params map[string]string) string {
	keys := make([]string, 0, len(params))
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
	return strings.Join(parts, "&")
}

func easyPayV2Sign(params map[string]string, key *rsa.PrivateKey) (string, error) {
	digest := sha256.Sum256([]byte(easyPayV2SignContent(params)))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("easypay V2 request signing failed")
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func easyPayV2Verify(params map[string]string, key *rsa.PublicKey, now time.Time) error {
	// The published SDK defaults an omitted sign_type to RSA. The algorithm
	// remains pinned here; an explicit alternative can never select MD5.
	if signType := params["sign_type"]; signType != "" && signType != "RSA" {
		return errors.New("easypay V2 invalid sign_type")
	}
	ts, err := strconv.ParseInt(params["timestamp"], 10, 64)
	if err != nil || strconv.FormatInt(ts, 10) != params["timestamp"] || ts < now.Unix()-300 || ts > now.Unix()+300 {
		return errors.New("easypay V2 timestamp outside 300-second window")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(params["sign"])
	if err != nil || len(signature) != key.Size() {
		return errors.New("easypay V2 invalid signature encoding")
	}
	digest := sha256.Sum256([]byte(easyPayV2SignContent(params)))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return errors.New("easypay V2 invalid signature")
	}
	return nil
}

func decodeEasyPayV2Object(body []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("easypay V2 expected JSON object")
	}
	params := map[string]string{}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, errors.New("easypay V2 invalid JSON field")
		}
		key, ok := token.(string)
		if !ok || seen[key] || strings.ContainsAny(key, "&=\r\n\x00") {
			return nil, errors.New("easypay V2 duplicate or invalid JSON field")
		}
		seen[key] = true
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, errors.New("easypay V2 invalid JSON value")
		}
		switch v := value.(type) {
		case string:
			params[key] = v
		case json.Number:
			params[key] = string(v)
		case bool:
			switch key {
			case "code", "status", "pid", "timestamp", "money", "trade_no", "out_trade_no", "refund_no", "out_refund_no", "sign", "sign_type", "pay_type", "pay_info":
				return nil, errors.New("easypay V2 invalid protocol field type")
			}
			if v {
				params[key] = "1"
			} else {
				params[key] = ""
			}
		case nil, []any, map[string]any:
			if key == "pid" {
				return nil, errors.New("easypay V2 invalid protocol field type")
			}
			// PHP excludes these values from its signature input.
		default:
			return nil, errors.New("easypay V2 unsupported JSON value")
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("easypay V2 incomplete JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("easypay V2 trailing JSON data")
	}
	return params, nil
}

func easyPayV2KeyDER(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if block, rest := pem.Decode([]byte(raw)); block != nil {
		if len(bytes.TrimSpace(rest)) != 0 {
			return nil, errors.New("easypay V2 key contains trailing data")
		}
		return block.Bytes, nil
	}
	der, err := base64.StdEncoding.Strict().DecodeString(strings.Join(strings.Fields(raw), ""))
	if err != nil {
		return nil, errors.New("easypay V2 key must be PEM or base64 DER")
	}
	return der, nil
}

func parseEasyPayV2PrivateKey(raw string) (*rsa.PrivateKey, error) {
	der, err := easyPayV2KeyDER(raw)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(der)
		if parseErr != nil {
			return nil, errors.New("easypay V2 invalid RSA private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("easypay V2 private key must be RSA")
		}
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("easypay V2 RSA private key must be at least 2048 bits")
	}
	if err := key.Validate(); err != nil {
		return nil, errors.New("easypay V2 invalid RSA private key")
	}
	return key, nil
}

func parseEasyPayV2PublicKey(raw string) (*rsa.PublicKey, error) {
	der, err := easyPayV2KeyDER(raw)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		parsed, parseErr := x509.ParsePKIXPublicKey(der)
		if parseErr != nil {
			return nil, errors.New("easypay V2 invalid RSA public key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("easypay V2 public key must be RSA")
		}
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("easypay V2 RSA public key must be at least 2048 bits")
	}
	return key, nil
}

func easyPayV2Integer(params map[string]string, field string) (int, error) {
	raw, ok := params[field]
	value, err := strconv.Atoi(raw)
	if !ok || err != nil || strconv.Itoa(value) != raw {
		return 0, fmt.Errorf("easypay V2 invalid %s", field)
	}
	return value, nil
}
