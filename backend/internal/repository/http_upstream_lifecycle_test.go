package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestUpstreamHealthQuarantineRequiresThreeTransportFailuresAndAllowsHalfOpenProbe(t *testing.T) {
	svc := &httpUpstreamService{cfg: &config.Config{Gateway: config.GatewayConfig{
		UpstreamHealth: config.GatewayUpstreamHealthConfig{
			Enabled: true, FailureThreshold: 3, WindowSeconds: 60, TTLSeconds: 30,
		},
	}}}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.test"}}
	key := upstreamHealthKey(req, "http://proxy.test:8080", 7, "default")
	now := time.Unix(100, 0)
	if !svc.upstreamRouteAllowed(key, now) {
		t.Fatal("new route must be allowed")
	}
	for i := 0; i < 2; i++ {
		svc.recordUpstreamHealthFailure(key, now.Add(time.Duration(i)*time.Second), io.ErrUnexpectedEOF)
		if !svc.upstreamRouteAllowed(key, now.Add(time.Duration(i)*time.Second)) {
			t.Fatal("route quarantined before threshold")
		}
	}
	svc.recordUpstreamHealthFailure(key, now.Add(2*time.Second), io.ErrUnexpectedEOF)
	if svc.upstreamRouteAllowed(key, now.Add(3*time.Second)) {
		t.Fatal("route should be quarantined after threshold")
	}
	if !svc.upstreamRouteAllowed(key, now.Add(33*time.Second)) {
		t.Fatal("half-open probe should be admitted after TTL")
	}
	if svc.upstreamRouteAllowed(key, now.Add(33*time.Second)) {
		t.Fatal("only one half-open probe should be admitted")
	}
	svc.recordUpstreamHealthSuccess(key)
	if !svc.upstreamRouteAllowed(key, now.Add(34*time.Second)) {
		t.Fatal("successful probe should restore the route")
	}
}

func TestUpstreamHealthIgnoresCanceledAndNonTransportErrors(t *testing.T) {
	svc := &httpUpstreamService{cfg: &config.Config{Gateway: config.GatewayConfig{
		UpstreamHealth: config.GatewayUpstreamHealthConfig{Enabled: true, FailureThreshold: 1},
	}}}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.test"}}
	key := upstreamHealthKey(req, "", 8, "default")
	now := time.Unix(200, 0)
	svc.recordUpstreamHealthFailure(key, now, context.Canceled)
	svc.recordUpstreamHealthFailure(key, now, errors.New("application rejected"))
	if !svc.upstreamRouteAllowed(key, now) {
		t.Fatal("non-transport errors must not quarantine the route")
	}
}

func TestUpstreamHealthKeyDoesNotRetainProxyCredentials(t *testing.T) {
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.test:443"}}
	key := upstreamHealthKey(req, "http://user:secret@example.test:8080/path", 9, "default")
	if strings.Contains(key, "secret") || strings.Contains(key, "user:") {
		t.Fatalf("proxy credentials leaked into health key: %q", key)
	}
	if !strings.Contains(key, "host:api.example.test:443") {
		t.Fatalf("health key should include normalized host and port: %q", key)
	}
}

func TestUpstreamHealthBodyOnlyMarksEOFAsSuccess(t *testing.T) {
	var successes, failures int
	body := &upstreamHealthBody{
		ReadCloser: io.NopCloser(strings.NewReader("ok")),
		onEOF:      func() { successes++ },
		onError:    func(error) { failures++ },
	}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if successes != 1 || failures != 0 {
		t.Fatalf("terminal callbacks = success %d failure %d, want 1/0", successes, failures)
	}
}

func TestUpstreamHealthBodyDoesNotTreatApplicationErrorAsSuccess(t *testing.T) {
	svc := &httpUpstreamService{cfg: &config.Config{Gateway: config.GatewayConfig{
		UpstreamHealth: config.GatewayUpstreamHealthConfig{Enabled: true},
	}}}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.test"}}
	key := upstreamHealthKey(req, "", 11, "default")
	wrapped := svc.wrapUpstreamHealthBody(io.NopCloser(strings.NewReader("bad")), key, http.StatusBadRequest)
	if _, err := io.ReadAll(wrapped); err != nil {
		t.Fatalf("read body: %v", err)
	}
	state := svc.getUpstreamHealthState(key)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.windowStart.IsZero() || state.failureCount != 0 {
		t.Fatalf("application error changed health state: window=%v failures=%d", state.windowStart, state.failureCount)
	}
}

func TestClassifyUpstreamReadErrorKeepsOnlySafeCategories(t *testing.T) {
	if got := classifyUpstreamReadError(io.ErrUnexpectedEOF); got != "stale_eof" {
		t.Fatalf("unexpected EOF class = %q", got)
	}
	if got := classifyUpstreamReadError(context.Canceled); got != "canceled" {
		t.Fatalf("canceled class = %q", got)
	}
}

func TestUpstreamHealthHalfOpenProbeCanBeRetriedAfterDeadline(t *testing.T) {
	svc := &httpUpstreamService{cfg: &config.Config{Gateway: config.GatewayConfig{
		UpstreamHealth: config.GatewayUpstreamHealthConfig{Enabled: true, FailureThreshold: 1, TTLSeconds: 1},
	}}}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.test"}}
	key := upstreamHealthKey(req, "", 10, "default")
	now := time.Unix(300, 0)
	svc.recordUpstreamHealthFailure(key, now, io.ErrUnexpectedEOF)
	if !svc.upstreamRouteAllowed(key, now.Add(2*time.Second)) {
		t.Fatal("first half-open probe should be admitted")
	}
	if svc.upstreamRouteAllowed(key, now.Add(2*time.Second)) {
		t.Fatal("probe should be single-flight before its deadline")
	}
	if !svc.upstreamRouteAllowed(key, now.Add(13*time.Second)) {
		t.Fatal("expired half-open probe should be replaceable")
	}
}

type lifecycleReadCloser struct {
	readStarted chan struct{}
	closed      chan struct{}
	once        sync.Once
}

func newLifecycleReadCloser() *lifecycleReadCloser {
	return &lifecycleReadCloser{readStarted: make(chan struct{}), closed: make(chan struct{})}
}

func (b *lifecycleReadCloser) Read([]byte) (int, error) {
	select {
	case <-b.readStarted:
	default:
		close(b.readStarted)
	}
	<-b.closed
	return 0, io.EOF
}

func (b *lifecycleReadCloser) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestCancelOnCloseBodyCancelsBeforeClosingAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	underlying := newLifecycleReadCloser()
	body := &cancelOnCloseBody{ReadCloser: underlying, cancel: cancel}

	if err := body.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("request context was not canceled")
	}
	if err := body.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestTrackedBodyClosesUnderlyingBodyOnlyOnce(t *testing.T) {
	underlying := &countingReadCloser{}
	callbackCount := 0
	body := &trackedBody{ReadCloser: underlying, onClose: func() { callbackCount++ }}
	if err := body.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if underlying.closeCount != 1 || callbackCount != 1 {
		t.Fatalf("close counts = body %d callback %d, want 1/1", underlying.closeCount, callbackCount)
	}
}

type countingReadCloser struct {
	closeCount int
}

func (b *countingReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (b *countingReadCloser) Close() error {
	b.closeCount++
	return nil
}

func TestDecompressedBodyCloseInterruptsBlockedRead(t *testing.T) {
	underlying := newLifecycleReadCloser()
	body := &decompressedBody{reader: underlying, closer: underlying}
	readDone := make(chan error, 1)
	go func() {
		_, err := body.Read(make([]byte, 1))
		readDone <- err
	}()
	select {
	case <-underlying.readStarted:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- body.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not interrupt blocked read")
	}
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("blocked read did not exit")
	}
	if err := body.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}
