package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type apnsRecordedRequest struct {
	proto   int
	path    string
	headers http.Header
	body    map[string]any
}

func newAPNsTestServer(t *testing.T) (*httptest.Server, *[]apnsRecordedRequest, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var seen []apnsRecordedRequest
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		seen = append(seen, apnsRecordedRequest{proto: r.ProtoMajor, path: r.URL.Path, headers: r.Header.Clone(), body: body})
		mu.Unlock()
		token := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		reply := func(status int, reason string) {
			w.WriteHeader(status)
			if reason != "" {
				_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
			}
		}
		switch token {
		case "gone":
			reply(http.StatusGone, "Unregistered")
		case "bad":
			reply(http.StatusBadRequest, "BadDeviceToken")
		case "topic":
			reply(http.StatusBadRequest, "DeviceTokenNotForTopic")
		case "auth":
			reply(http.StatusForbidden, "InvalidProviderToken")
		case "busy":
			reply(http.StatusTooManyRequests, "TooManyRequests")
		case "down":
			reply(http.StatusServiceUnavailable, "ServiceUnavailable")
		default:
			reply(http.StatusOK, "")
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &seen, &mu
}

func TestAPNsSender_StatusMappingAndRequest(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	runtime := &PushAPNsRuntime{TeamID: "TEAM123456", KeyID: "KEY1234567", Topic: "com.example.fallback", Environment: "production", PrivateKey: key, Fingerprint: "fp"}
	srv, seen, mu := newAPNsTestServer(t)
	sender := newAPNsSender(func(context.Context) (*PushAPNsRuntime, error) { return runtime, nil }, srv.Client())
	sender.baseURLs = map[string]string{pushEnvProduction: srv.URL + "/production", pushEnvSandbox: srv.URL + "/sandbox"}

	msg := &PushMessage{
		Type: PushEventOpsAlertFired, Title: "[P1] 错误率", Body: "当前 12.3", ThreadID: "ops-alert",
		InterruptionLevel: "time-sensitive", Severity: "P1", Site: "api.example.com", CollapseID: "ops_alert.fired:881",
		Data: map[string]any{"alert_event_id": int64(881), "rule_id": int64(3)},
	}
	target := PushTarget{Platform: PushPlatformIOS, Token: "deadbeef", BundleID: "com.example.sub2mobile", Environment: "sandbox"}
	res, err := sender.Send(context.Background(), target, msg)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.ProviderStatus)

	mu.Lock()
	req := (*seen)[0]
	mu.Unlock()
	require.Equal(t, 2, req.proto, "APNs requires HTTP/2")
	require.Equal(t, "/sandbox/3/device/deadbeef", req.path, "the device environment picks the APNs host")
	require.Equal(t, "com.example.sub2mobile", req.headers.Get("apns-topic"))
	require.Equal(t, "alert", req.headers.Get("apns-push-type"))
	require.Equal(t, "10", req.headers.Get("apns-priority"))
	require.Equal(t, "ops_alert.fired:881", req.headers.Get("apns-collapse-id"))
	require.NotEmpty(t, req.headers.Get("apns-expiration"))

	auth := req.headers.Get("authorization")
	require.True(t, strings.HasPrefix(auth, "bearer "), auth)
	parsed, err := jwt.Parse(strings.TrimPrefix(auth, "bearer "), func(tok *jwt.Token) (any, error) {
		require.Equal(t, "ES256", tok.Method.Alg())
		require.Equal(t, "KEY1234567", tok.Header["kid"])
		return &key.PublicKey, nil
	})
	require.NoError(t, err)
	claims := parsed.Claims.(jwt.MapClaims)
	require.Equal(t, "TEAM123456", claims["iss"])
	require.InDelta(t, float64(time.Now().Unix()), claims["iat"], 60)

	aps := req.body["aps"].(map[string]any)
	require.Equal(t, map[string]any{"title": "[P1] 错误率", "body": "当前 12.3"}, aps["alert"])
	require.Equal(t, "ops-alert", aps["thread-id"])
	require.Equal(t, "time-sensitive", aps["interruption-level"])
	require.Equal(t, "api.example.com", req.body["site"])
	require.Equal(t, PushEventOpsAlertFired, req.body["type"])
	require.EqualValues(t, 881, req.body["alert_event_id"])
	encoded, _ := json.Marshal(req.body)
	require.NotContains(t, string(encoded), "deadbeef", "the payload carries no token")

	_, err = sender.Send(context.Background(), PushTarget{Platform: PushPlatformIOS, Token: "cafe"}, msg)
	require.NoError(t, err)
	mu.Lock()
	second := (*seen)[1]
	mu.Unlock()
	require.Equal(t, "/production/3/device/cafe", second.path, "environment falls back to the site config")
	require.Equal(t, "com.example.fallback", second.headers.Get("apns-topic"), "topic falls back to apns_topic")
	require.Equal(t, auth, second.headers.Get("authorization"), "the provider token is cached")

	cases := []struct {
		token     string
		status    int
		reason    string
		disable   string
		retryable bool
	}{
		{"gone", 410, "Unregistered", PushDisabledUnregistered, false},
		{"bad", 400, "BadDeviceToken", PushDisabledBadDeviceToken, false},
		{"topic", 400, "DeviceTokenNotForTopic", PushDisabledTopicMismatch, false},
		{"auth", 403, "InvalidProviderToken", "", false},
		{"busy", 429, "TooManyRequests", "", true},
		{"down", 503, "ServiceUnavailable", "", true},
	}
	for _, tc := range cases {
		res, err := sender.Send(context.Background(), PushTarget{Platform: PushPlatformIOS, Token: tc.token, BundleID: "com.example.sub2mobile"}, msg)
		require.Error(t, err, tc.token)
		require.Equal(t, tc.status, res.ProviderStatus, tc.token)
		require.Equal(t, tc.reason, res.ProviderReason, tc.token)
		require.Equal(t, tc.disable, res.DisableReason, tc.token)
		require.Equal(t, tc.retryable, res.Retryable, tc.token)
	}
}

func TestAPNsSender_NotConfigured(t *testing.T) {
	sender := newAPNsSender(func(context.Context) (*PushAPNsRuntime, error) { return nil, ErrPushNotConfigured }, http.DefaultClient)
	_, err := sender.Send(context.Background(), PushTarget{Platform: PushPlatformIOS, Token: "aa"}, &PushMessage{})
	require.ErrorIs(t, err, ErrPushNotConfigured)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	noTopic := newAPNsSender(func(context.Context) (*PushAPNsRuntime, error) {
		return &PushAPNsRuntime{TeamID: "TEAM123456", KeyID: "KEY1234567", PrivateKey: key}, nil
	}, http.DefaultClient)
	_, err = noTopic.Send(context.Background(), PushTarget{Platform: PushPlatformIOS, Token: "aa"}, &PushMessage{})
	require.ErrorIs(t, err, ErrPushNotConfigured, "without a bundle_id or apns_topic there is nothing to push to")
}

type recordedChannelRequest struct {
	method  string
	path    string
	headers http.Header
	raw     []byte
}

func newChannelTestServer(t *testing.T, status int) (*httptest.Server, func() recordedChannelRequest) {
	t.Helper()
	var mu sync.Mutex
	var last recordedChannelRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = recordedChannelRequest{method: r.Method, path: r.URL.Path, headers: r.Header.Clone(), raw: raw}
		mu.Unlock()
		if status == http.StatusFound {
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"handled"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() recordedChannelRequest {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func pushTestAlertMessage() *PushMessage {
	return &PushMessage{
		Type: PushEventOpsAlertFired, Title: "[P0] 上游错误", Body: "当前 50", ThreadID: "ops-alert",
		InterruptionLevel: "time-sensitive", Severity: "P0", Site: "api.example.com", DeliveryID: "pd_42",
		OccurredAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		Data:       map[string]any{"alert_event_id": int64(881)},
	}
}

func TestWebhookSender_SignatureAndPayload(t *testing.T) {
	srv, last := newChannelTestServer(t, http.StatusOK)
	now := time.Date(2026, 10, 21, 8, 0, 5, 0, time.UTC)
	sender := &webhookSender{client: srv.Client(), now: func() time.Time { return now }}

	res, err := sender.Send(context.Background(), PushTarget{Platform: PushPlatformWebhook, Endpoint: srv.URL + "/hook", Secret: "s3cret"}, pushTestAlertMessage())
	require.NoError(t, err)
	require.Equal(t, 200, res.ProviderStatus)
	req := last()
	require.Equal(t, http.MethodPost, req.method)
	require.Equal(t, "/hook", req.path)
	require.Equal(t, PushEventOpsAlertFired, req.headers.Get("X-Sub2-Event"))
	require.Equal(t, "pd_42", req.headers.Get("X-Sub2-Delivery"))
	require.Equal(t, "1792569605", req.headers.Get("X-Sub2-Timestamp"))
	mac := hmac.New(sha256.New, []byte("s3cret"))
	_, _ = mac.Write([]byte("1792569605." + string(req.raw)))
	require.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), req.headers.Get("X-Sub2-Signature"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(req.raw, &body))
	require.Equal(t, PushEventOpsAlertFired, body["type"])
	require.Equal(t, "api.example.com", body["site"])
	require.Equal(t, "pd_42", body["delivery_id"])
	require.Equal(t, "P0", body["severity"])
	require.EqualValues(t, 881, body["alert_event_id"])
	require.Equal(t, "[P0] 上游错误", body["title"])
	require.NotContains(t, string(req.raw), "s3cret")

	_, err = sender.Send(context.Background(), PushTarget{Platform: PushPlatformWebhook, Endpoint: srv.URL + "/hook"}, pushTestAlertMessage())
	require.NoError(t, err)
	require.Empty(t, last().headers.Get("X-Sub2-Signature"), "no secret, no signature")
}

func TestPushChannelSenders_StatusMapping(t *testing.T) {
	cases := []struct {
		status    int
		disable   string
		retryable bool
	}{
		{http.StatusGone, PushDisabledEndpointGone, false},
		{http.StatusInternalServerError, "", true},
		{http.StatusTooManyRequests, "", true},
		{http.StatusBadRequest, "", false},
		{http.StatusFound, "", false},
	}
	for _, tc := range cases {
		srv, _ := newChannelTestServer(t, tc.status)
		sender := &webhookSender{client: newPushChannelHTTPClientWith(srv.Client().Transport), now: time.Now}
		res, err := sender.Send(context.Background(), PushTarget{Platform: PushPlatformWebhook, Endpoint: srv.URL}, pushTestAlertMessage())
		require.Error(t, err, tc.status)
		require.Equal(t, tc.status, res.ProviderStatus, "redirects are not followed")
		require.Equal(t, tc.disable, res.DisableReason, tc.status)
		require.Equal(t, tc.retryable, res.Retryable, tc.status)
	}
}

func TestBarkAndNtfySender_Request(t *testing.T) {
	srv, last := newChannelTestServer(t, http.StatusOK)
	bark := &barkSender{client: srv.Client()}
	_, err := bark.Send(context.Background(), PushTarget{Platform: PushPlatformBark, Endpoint: srv.URL, Token: "BarkKey123"}, pushTestAlertMessage())
	require.NoError(t, err)
	req := last()
	require.Equal(t, "/push", req.path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(req.raw, &body))
	require.Equal(t, "BarkKey123", body["device_key"])
	require.Equal(t, "[P0] 上游错误", body["title"])
	require.Equal(t, "当前 50", body["body"])
	require.Equal(t, "ops-alert", body["group"])
	require.Equal(t, "timeSensitive", body["level"])

	ntfy := &ntfySender{client: srv.Client()}
	_, err = ntfy.Send(context.Background(), PushTarget{Platform: PushPlatformNtfy, Endpoint: srv.URL, Token: "sub2-alerts", Secret: "tk_abc"}, pushTestAlertMessage())
	require.NoError(t, err)
	req = last()
	require.Equal(t, "/", req.path, "JSON publishing posts to the server root")
	require.Equal(t, "Bearer tk_abc", req.headers.Get("Authorization"))
	body = nil
	require.NoError(t, json.Unmarshal(req.raw, &body))
	require.Equal(t, "sub2-alerts", body["topic"])
	require.Equal(t, "[P0] 上游错误", body["title"])
	require.Equal(t, "当前 50", body["message"])
	require.EqualValues(t, 5, body["priority"], "P0 maps to ntfy priority 5")

	msg := pushTestAlertMessage()
	msg.Severity = ""
	msg.Type = PushEventAccountError
	_, err = ntfy.Send(context.Background(), PushTarget{Platform: PushPlatformNtfy, Endpoint: srv.URL, Token: "sub2-alerts"}, msg)
	require.NoError(t, err)
	req = last()
	require.Empty(t, req.headers.Get("Authorization"))
	body = nil
	require.NoError(t, json.Unmarshal(req.raw, &body))
	require.EqualValues(t, 3, body["priority"])
}

func TestPushChannelClient_BlocksPrivateAddressesAtDial(t *testing.T) {
	srv, _ := newChannelTestServer(t, http.StatusOK)
	sender := &webhookSender{client: newPushChannelHTTPClient(false), now: time.Now}
	res, err := sender.Send(context.Background(), PushTarget{Platform: PushPlatformWebhook, Endpoint: srv.URL}, pushTestAlertMessage())
	require.Error(t, err, "DNS rebinding to a private address is refused when sending")
	require.False(t, res.Retryable)
	require.Contains(t, res.ProviderReason, "SSRF")

	allowed := &webhookSender{client: newPushChannelHTTPClient(true), now: time.Now}
	_, err = allowed.Send(context.Background(), PushTarget{Platform: PushPlatformWebhook, Endpoint: srv.URL}, pushTestAlertMessage())
	require.NoError(t, err, "allow_private_hosts lets LAN targets through")
}

func TestPushSenders_UnknownPlatform(t *testing.T) {
	senders := &PushSenders{byPlatform: map[string]PushSender{}}
	_, err := senders.Send(context.Background(), PushTarget{Platform: "android"}, &PushMessage{})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrPushNotConfigured))
}
