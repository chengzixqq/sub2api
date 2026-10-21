package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	pushSendTimeout        = 10 * time.Second
	pushResponseBodyLimit  = 4096
	pushProviderReasonMax  = 120
	pushInterruptionUrgent = "time-sensitive"
	pushInterruptionActive = "active"
	pushUserAgent          = "sub2api-push/1"
)

// errPushProviderRejected is returned when the provider answered with a non-success status.
var errPushProviderRejected = errors.New("push provider rejected the notification")

// PushTarget is a device with its decrypted credentials.
type PushTarget struct {
	Platform    string
	Token       string
	Secret      string
	Endpoint    string
	BundleID    string
	Environment string
}

// PushSendResult describes the provider's answer. DisableReason is set when the target is permanently invalid;
// Retryable when a later attempt may succeed.
type PushSendResult struct {
	ProviderStatus int
	ProviderReason string
	DisableReason  string
	Retryable      bool
}

// PushSender delivers one message to one target; a nil error means the provider accepted it.
type PushSender interface {
	Send(ctx context.Context, target PushTarget, msg *PushMessage) (PushSendResult, error)
}

// PushSenders routes a message to the sender of the target's platform.
type PushSenders struct {
	byPlatform map[string]PushSender
}

// NewPushSenders builds the APNs sender (configured through PushConfigService) and the Bark / ntfy / webhook senders.
// The generic channels dial through safeDialContext unless security.url_allowlist.allow_private_hosts is set, so a
// target whose DNS later points at a private address is refused when sending.
func NewPushSenders(cfg *config.Config, apnsConfig *PushConfigService) *PushSenders {
	allowPrivate := cfg != nil && cfg.Security.URLAllowlist.AllowPrivateHosts
	channelClient := newPushChannelHTTPClient(allowPrivate)
	var resolve func(context.Context) (*PushAPNsRuntime, error)
	if apnsConfig != nil {
		resolve = apnsConfig.Resolve
	} else {
		resolve = func(context.Context) (*PushAPNsRuntime, error) { return nil, ErrPushNotConfigured }
	}
	return &PushSenders{byPlatform: map[string]PushSender{
		PushPlatformIOS:     newAPNsSender(resolve, newAPNsHTTPClient()),
		PushPlatformBark:    &barkSender{client: channelClient},
		PushPlatformNtfy:    &ntfySender{client: channelClient},
		PushPlatformWebhook: &webhookSender{client: channelClient, now: time.Now},
	}}
}

// Send delivers msg with the sender of target.Platform.
func (s *PushSenders) Send(ctx context.Context, target PushTarget, msg *PushMessage) (PushSendResult, error) {
	if s == nil {
		return PushSendResult{}, errors.New("push senders not configured")
	}
	sender, ok := s.byPlatform[target.Platform]
	if !ok || sender == nil {
		return PushSendResult{ProviderReason: "unsupported platform"}, fmt.Errorf("unsupported push platform %q", target.Platform)
	}
	return sender.Send(ctx, target, msg)
}

func newPushChannelHTTPClient(allowPrivate bool) *http.Client {
	dial := safeDialContext
	if allowPrivate {
		dial = (&net.Dialer{Timeout: pushSendTimeout, KeepAlive: 30 * time.Second}).DialContext
	}
	return newPushChannelHTTPClientWith(&http.Transport{
		// No proxy: a proxy would dial on our behalf and bypass the private-address check.
		Proxy:                 nil,
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   pushSendTimeout,
		ResponseHeaderTimeout: pushSendTimeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
	})
}

// newPushChannelHTTPClientWith never follows redirects: a redirect could point at a private address.
func newPushChannelHTTPClientWith(rt http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:   pushSendTimeout,
		Transport: rt,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// postPushJSON posts body and maps the response: 2xx accepted, 410 disables the target, 408 / 429 / 5xx and
// network errors are retried, other statuses fail the delivery.
func postPushJSON(ctx context.Context, client *http.Client, endpoint string, body []byte, headers map[string]string) (PushSendResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return PushSendResult{ProviderReason: "invalid endpoint"}, errors.New("invalid push endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", pushUserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return pushNetworkResult(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, pushResponseBodyLimit))
	res := PushSendResult{ProviderStatus: resp.StatusCode, ProviderReason: pushProviderReason(raw, resp.Status)}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return res, nil
	case resp.StatusCode == http.StatusGone:
		res.DisableReason = PushDisabledEndpointGone
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		res.Retryable = true
	}
	return res, errPushProviderRejected
}

// pushNetworkResult describes a transport error without the request URL (it may embed a token).
func pushNetworkResult(err error) (PushSendResult, error) {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	reason := sanitizePushText(err.Error(), pushProviderReasonMax)
	if strings.Contains(reason, "blocked by SSRF policy") {
		return PushSendResult{ProviderReason: "blocked by SSRF policy (private address)"}, err
	}
	return PushSendResult{ProviderReason: reason, Retryable: true}, err
}

// pushProviderReason extracts a short reason from a provider response body.
func pushProviderReason(raw []byte, status string) string {
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) == nil {
		for _, key := range []string{"reason", "message", "error"} {
			if s, ok := decoded[key].(string); ok && strings.TrimSpace(s) != "" {
				return sanitizePushText(s, pushProviderReasonMax)
			}
		}
	}
	if text := sanitizePushText(string(raw), pushProviderReasonMax); text != "" && decoded == nil {
		return text
	}
	return sanitizePushText(status, pushProviderReasonMax)
}

// pushFlatPayload is the generic JSON body: type, site, delivery, text and the IDs at the top level.
func pushFlatPayload(msg *PushMessage) map[string]any {
	payload := map[string]any{}
	for k, v := range msg.Data {
		payload[k] = v
	}
	payload["type"] = msg.Type
	payload["title"] = msg.Title
	payload["body"] = msg.Body
	payload["occurred_at"] = msg.OccurredAt.UTC().Format(time.RFC3339)
	if msg.Site != "" {
		payload["site"] = msg.Site
	}
	if msg.Severity != "" {
		payload["severity"] = msg.Severity
	}
	if msg.DeliveryID != "" {
		payload["delivery_id"] = msg.DeliveryID
	}
	return payload
}

// webhookSender posts the flat JSON payload. With a secret it signs ts + "." + body with HMAC-SHA256
// (X-Sub2-Signature: sha256=<hex>, X-Sub2-Timestamp: unix seconds).
type webhookSender struct {
	client *http.Client
	now    func() time.Time
}

func (s *webhookSender) Send(ctx context.Context, target PushTarget, msg *PushMessage) (PushSendResult, error) {
	body, err := json.Marshal(pushFlatPayload(msg))
	if err != nil {
		return PushSendResult{}, err
	}
	ts := strconv.FormatInt(s.now().Unix(), 10)
	headers := map[string]string{
		"X-Sub2-Event":     msg.Type,
		"X-Sub2-Delivery":  msg.DeliveryID,
		"X-Sub2-Timestamp": ts,
	}
	if target.Secret != "" {
		mac := hmac.New(sha256.New, []byte(target.Secret))
		_, _ = mac.Write([]byte(ts + "."))
		_, _ = mac.Write(body)
		headers["X-Sub2-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	return postPushJSON(ctx, s.client, target.Endpoint, body, headers)
}

// barkSender uses Bark's JSON API: POST {server}/push with device_key.
type barkSender struct {
	client *http.Client
}

func (s *barkSender) Send(ctx context.Context, target PushTarget, msg *PushMessage) (PushSendResult, error) {
	level := pushInterruptionActive
	if msg.InterruptionLevel == pushInterruptionUrgent {
		level = "timeSensitive"
	}
	body, err := json.Marshal(map[string]any{
		"device_key": target.Token,
		"title":      msg.Title,
		"body":       msg.Body,
		"group":      msg.ThreadID,
		"level":      level,
	})
	if err != nil {
		return PushSendResult{}, err
	}
	return postPushJSON(ctx, s.client, strings.TrimRight(target.Endpoint, "/")+"/push", body, nil)
}

// ntfySender publishes as JSON to the server root (avoids non-ASCII header encoding of the title).
type ntfySender struct {
	client *http.Client
}

// ntfyPriority maps alert severity to ntfy priority (5 = max); other events use the default 3.
func ntfyPriority(msg *PushMessage) int {
	if !isPushOpsAlertEvent(msg.Type) || msg.Severity == "" {
		return 3
	}
	return 5 - pushSeverityRank(msg.Severity)
}

func (s *ntfySender) Send(ctx context.Context, target PushTarget, msg *PushMessage) (PushSendResult, error) {
	body, err := json.Marshal(map[string]any{
		"topic":    target.Token,
		"title":    msg.Title,
		"message":  msg.Body,
		"priority": ntfyPriority(msg),
		"tags":     []string{msg.Type},
	})
	if err != nil {
		return PushSendResult{}, err
	}
	var headers map[string]string
	if target.Secret != "" {
		headers = map[string]string{"Authorization": "Bearer " + target.Secret}
	}
	return postPushJSON(ctx, s.client, strings.TrimRight(target.Endpoint, "/")+"/", body, headers)
}
