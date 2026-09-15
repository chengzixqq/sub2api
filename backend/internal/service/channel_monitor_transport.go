package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type channelMonitorAttemptsKey struct{}

type ChannelMonitorAttemptRecorder struct {
	mu        sync.Mutex
	started   time.Time
	attempts  []ChannelMonitorAttempt
	truncated bool
}

// WithChannelMonitorAttempts explicitly opts a business request into transport
// observation. Authentication, health and arbitrary outbound requests remain
// untouched unless this context marker is present.
func WithChannelMonitorAttempts(ctx context.Context) (context.Context, *ChannelMonitorAttemptRecorder) {
	if ctx == nil {
		ctx = context.Background()
	}
	r := &ChannelMonitorAttemptRecorder{started: time.Now()}
	return context.WithValue(ctx, channelMonitorAttemptsKey{}, r), r
}

func channelMonitorAttemptsFromContext(ctx context.Context) *ChannelMonitorAttemptRecorder {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(channelMonitorAttemptsKey{}).(*ChannelMonitorAttemptRecorder)
	return r
}

func (r *ChannelMonitorAttemptRecorder) add(a ChannelMonitorAttempt) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempts) >= ChannelMonitorObservationMaxAttempts {
		r.truncated = true
		return
	}
	a.Sequence = len(r.attempts) + 1
	r.attempts = append(r.attempts, a)
}

func (r *ChannelMonitorAttemptRecorder) Snapshot() ([]ChannelMonitorAttempt, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]ChannelMonitorAttempt(nil), r.attempts...)
	return out, r.truncated
}

type channelMonitorRoundTripper struct {
	base      http.RoundTripper
	recorder  *ChannelMonitorAttemptRecorder
	accountID int64
}

func (t channelMonitorRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.recorder.add(ChannelMonitorAttempt{AccountID: t.accountID, StartedAt: start, CompletedAt: time.Now(), Outcome: "unknown", ErrorCategory: "transport_error", DurationMs: time.Since(start).Milliseconds()})
		return nil, err
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	outcome, category := "unknown", "http_complete"
	if status >= 400 {
		outcome = "channel_error"
		category = channelMonitorHTTPErrorCategory(status)
	}
	t.recorder.add(ChannelMonitorAttempt{AccountID: t.accountID, StartedAt: start, CompletedAt: time.Now(), Outcome: outcome, ErrorCategory: category, HTTPStatus: status, DurationMs: time.Since(start).Milliseconds()})
	return resp, nil
}

func channelMonitorHTTPErrorCategory(status int) string {
	switch status {
	case 401, 403:
		return "upstream_auth"
	case 402:
		return "upstream_balance"
	case 413:
		return "request_too_large"
	case 408, 429:
		return "upstream_capacity"
	}
	if status >= 500 {
		return "upstream_error"
	}
	return "upstream_http"
}

// ObserveChannelMonitorHTTPClient wraps only an explicitly marked business
// request. The returned response and body are not modified.
func ObserveChannelMonitorHTTPClient(client *http.Client, req *http.Request, accountID int64) *http.Client {
	if client == nil || req == nil {
		return client
	}
	recorder := channelMonitorAttemptsFromContext(req.Context())
	if recorder == nil || !channelMonitorBusinessURL(req.URL) {
		return client
	}
	clone := *client
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = channelMonitorRoundTripper{base: base, recorder: recorder, accountID: accountID}
	return &clone
}

func channelMonitorBusinessURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || host == "127.0.0.1" {
		return false
	}
	p := strings.ToLower(strings.TrimRight(u.Path, "/"))
	// Match canonical generation endpoints even when an upstream uses a
	// custom base path (for example /proxy/messages). Keep this allowlist
	// narrow so OAuth, quota and health calls are never recorded as attempts.
	return strings.HasSuffix(p, "/messages") ||
		strings.HasSuffix(p, "/chat/completions") ||
		strings.HasSuffix(p, "/responses") ||
		strings.HasSuffix(p, ":generatecontent") ||
		strings.HasSuffix(p, ":streamgeneratecontent")
}
