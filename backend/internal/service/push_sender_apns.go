package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/golang-jwt/jwt/v5"
)

const (
	apnsProductionURL = "https://api.push.apple.com"
	apnsSandboxURL    = "https://api.sandbox.push.apple.com"
	// apnsTokenRefresh renews the provider token before Apple's one-hour limit (and not more often than every
	// 20 minutes, which Apple throttles).
	apnsTokenRefresh = 50 * time.Minute
	apnsExpiration   = time.Hour
	// apnsSettingsUnavailable is the provider reason (and device last_error) when the APNs settings cannot be read.
	apnsSettingsUnavailable = "APNs settings unavailable"
)

// errAPNsSettingsUnavailable: reading the APNs settings failed (database hiccup, timeout). Unlike
// ErrPushNotConfigured the push is retried; the storage error itself is only logged.
var errAPNsSettingsUnavailable = errors.New("apns settings unavailable")

// apnsSender pushes to APNs over HTTP/2 with an ES256 provider token signed by the site's p8 key.
type apnsSender struct {
	resolve  func(context.Context) (*PushAPNsRuntime, error)
	client   *http.Client
	baseURLs map[string]string
	now      func() time.Time

	mu       sync.Mutex
	tokenKey string
	token    string
	tokenAt  time.Time
}

func newAPNsSender(resolve func(context.Context) (*PushAPNsRuntime, error), client *http.Client) *apnsSender {
	return &apnsSender{
		resolve:  resolve,
		client:   client,
		baseURLs: map[string]string{pushEnvProduction: apnsProductionURL, pushEnvSandbox: apnsSandboxURL},
		now:      time.Now,
	}
}

// newAPNsHTTPClient talks to Apple's fixed hosts, so it may use the environment proxy.
func newAPNsHTTPClient() *http.Client {
	return &http.Client{
		Timeout: pushSendTimeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   pushSendTimeout,
			ResponseHeaderTimeout: pushSendTimeout,
			MaxIdleConns:          4,
			IdleConnTimeout:       5 * time.Minute,
		},
	}
}

// providerToken returns the cached provider token, signing a new one when the key changed, it is older than
// apnsTokenRefresh, or refresh is set (Apple reported it expired).
func (s *apnsSender) providerToken(rt *PushAPNsRuntime, refresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rt.TeamID + "|" + rt.KeyID + "|" + rt.Fingerprint
	now := s.now()
	if !refresh && s.tokenKey == key && s.token != "" && now.Sub(s.tokenAt) < apnsTokenRefresh {
		return s.token, nil
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": rt.TeamID, "iat": now.Unix()})
	tok.Header["kid"] = rt.KeyID
	signed, err := tok.SignedString(rt.PrivateKey)
	if err != nil {
		return "", err
	}
	s.tokenKey, s.token, s.tokenAt = key, signed, now
	return signed, nil
}

func (s *apnsSender) Send(ctx context.Context, target PushTarget, msg *PushMessage) (PushSendResult, error) {
	rt, err := s.resolve(ctx)
	if errors.Is(err, ErrPushNotConfigured) {
		return PushSendResult{}, err
	}
	if err != nil {
		logger.LegacyPrintf("service.push", "[Push] read APNs settings failed: %v", err)
		return PushSendResult{ProviderReason: apnsSettingsUnavailable, Retryable: true}, errAPNsSettingsUnavailable
	}
	topic := target.BundleID
	if topic == "" {
		topic = rt.Topic
	}
	if topic == "" || rt.PrivateKey == nil {
		return PushSendResult{}, ErrPushNotConfigured
	}
	env := target.Environment
	if env == "" {
		env = rt.Environment
	}
	base, ok := s.baseURLs[env]
	if !ok {
		base = s.baseURLs[pushEnvProduction]
	}
	body, err := json.Marshal(apnsPayload(msg))
	if err != nil {
		return PushSendResult{}, err
	}
	res, err := s.post(ctx, rt, base+"/3/device/"+url.PathEscape(target.Token), topic, msg, body, false)
	if res.ProviderReason == "ExpiredProviderToken" {
		res, err = s.post(ctx, rt, base+"/3/device/"+url.PathEscape(target.Token), topic, msg, body, true)
	}
	return res, err
}

func (s *apnsSender) post(ctx context.Context, rt *PushAPNsRuntime, endpoint, topic string, msg *PushMessage, body []byte, refresh bool) (PushSendResult, error) {
	bearer, err := s.providerToken(rt, refresh)
	if err != nil {
		return PushSendResult{ProviderReason: "cannot sign provider token"}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return PushSendResult{}, err
	}
	req.Header.Set("authorization", "bearer "+bearer)
	req.Header.Set("apns-topic", topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", strconv.FormatInt(s.now().Add(apnsExpiration).Unix(), 10))
	if msg.CollapseID != "" {
		req.Header.Set("apns-collapse-id", msg.CollapseID)
	}
	req.Header.Set("content-type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return pushNetworkResult(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, pushResponseBodyLimit))
	if resp.StatusCode == http.StatusOK {
		return PushSendResult{ProviderStatus: resp.StatusCode}, nil
	}
	var decoded struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &decoded)
	res := PushSendResult{ProviderStatus: resp.StatusCode, ProviderReason: sanitizePushText(decoded.Reason, pushProviderReasonMax)}
	if res.ProviderReason == "" {
		res.ProviderReason = sanitizePushText(resp.Status, pushProviderReasonMax)
	}
	switch {
	case resp.StatusCode == http.StatusGone:
		res.DisableReason = PushDisabledUnregistered
	case res.ProviderReason == "BadDeviceToken":
		res.DisableReason = PushDisabledBadDeviceToken
	case res.ProviderReason == "DeviceTokenNotForTopic":
		res.DisableReason = PushDisabledTopicMismatch
	case res.ProviderReason == "ExpiredProviderToken":
		res.Retryable = true
	case resp.StatusCode == http.StatusForbidden:
		// InvalidProviderToken etc.: the site's APNs settings are wrong, not the device.
		logger.LegacyPrintf("service.push", "[Push] APNs rejected the provider token (%s); check apns_team_id / apns_key_id / apns_private_key", res.ProviderReason)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		res.Retryable = true
	}
	return res, errPushProviderRejected
}

// apnsPayload is {aps:{alert:{title,body}, thread-id, interruption-level, sound}, type, site, severity, <ids>}.
func apnsPayload(msg *PushMessage) map[string]any {
	aps := map[string]any{
		"alert": map[string]string{"title": msg.Title, "body": msg.Body},
		"sound": "default",
	}
	if msg.ThreadID != "" {
		aps["thread-id"] = msg.ThreadID
	}
	if msg.InterruptionLevel != "" {
		aps["interruption-level"] = msg.InterruptionLevel
	}
	payload := map[string]any{}
	for k, v := range msg.Data {
		payload[k] = v
	}
	payload["aps"] = aps
	payload["type"] = msg.Type
	if msg.Site != "" {
		payload["site"] = msg.Site
	}
	if msg.Severity != "" {
		payload["severity"] = msg.Severity
	}
	return payload
}
