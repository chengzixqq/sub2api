package provider

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testEasyPayV2Key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestEasyPayV2CanonicalAndSignature(t *testing.T) {
	now := time.Unix(1721206072, 0)
	params, err := decodeEasyPayV2Object([]byte(`{"timestamp":"1721206072","code":0,"money":"1.00","extension":" 空 格 & x=y ","empty":"  ","object":{"a":1},"array":[1],"null":null,"sign_type":"RSA"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "code=0&extension= 空 格 & x=y &money=1.00&timestamp=1721206072"
	if got := easyPayV2SignContent(params); got != want {
		t.Fatalf("content=%q want=%q", got, want)
	}
	key := testEasyPayV2Key(t)
	params["sign"], err = easyPayV2Sign(params, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := easyPayV2Verify(params, &key.PublicKey, now); err != nil {
		t.Fatal(err)
	}
	delete(params, "sign_type")
	if err := easyPayV2Verify(params, &key.PublicKey, now); err != nil {
		t.Fatalf("documented default RSA: %v", err)
	}
	params["money"] = "2.00"
	if err := easyPayV2Verify(params, &key.PublicKey, now); err == nil {
		t.Fatal("accepted modified amount")
	}
}

func TestEasyPayV2TimestampAndKeyBoundaries(t *testing.T) {
	key, other := testEasyPayV2Key(t), testEasyPayV2Key(t)
	now := time.Unix(1721206072, 0)
	for _, delta := range []int64{-301, -300, 0, 300, 301} {
		p := map[string]string{"sign_type": "RSA", "timestamp": strconv.FormatInt(now.Unix()+delta, 10)}
		p["sign"], _ = easyPayV2Sign(p, key)
		err := easyPayV2Verify(p, &key.PublicKey, now)
		if (err == nil) != (delta >= -300 && delta <= 300) {
			t.Fatalf("delta=%d err=%v", delta, err)
		}
		if err := easyPayV2Verify(p, &other.PublicKey, now); err == nil {
			t.Fatal("accepted wrong key")
		}
	}
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	pkix, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	for _, raw := range []string{string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})), base64.StdEncoding.EncodeToString(pkcs8), string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))} {
		if _, err := parseEasyPayV2PrivateKey(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkix})), base64.StdEncoding.EncodeToString(pkix), base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PublicKey(&key.PublicKey))} {
		if _, err := parseEasyPayV2PublicKey(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEasyPayV2RejectsAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{`{"code":0,"code":1}`, `{"code":0} {}`, `[]`, `{"code":0`, `{"status":true}`, `{"money":true}`, strings.Repeat(" ", 3)} {
		if _, err := decodeEasyPayV2Object([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
