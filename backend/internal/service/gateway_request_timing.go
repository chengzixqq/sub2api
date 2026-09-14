package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type GatewayTimingPhase uint8

const (
	GatewayTimingUserAdmission GatewayTimingPhase = iota
	GatewayTimingUserWait
	GatewayTimingAccountSelect
	GatewayTimingAccountWait
	GatewayTimingMessageQueue
	GatewayTimingRetryWait
	GatewayTimingForward
	GatewayTimingClientAcquire
	GatewayTimingConnectionAcquire
	GatewayTimingResponseHeaders
	GatewayTimingResponseBody
	GatewayTimingDownstreamWrite
	gatewayTimingPhaseCount
)

var gatewayTimingPhaseNames = [...]string{
	"user_admission", "user_wait", "account_select", "account_wait", "message_queue",
	"retry_wait", "forward", "http_client_acquire", "http_connection_acquire",
	"upstream_headers", "upstream_body", "downstream_write",
}

type gatewayRequestTimingKey struct{}

// GatewayRequestTiming stores bounded numeric observations only. Nested phases
// intentionally overlap: connection acquisition is part of response-header wait.
type GatewayRequestTiming struct {
	mu              sync.Mutex
	started         time.Time
	durations       [gatewayTimingPhaseCount]time.Duration
	calls           [gatewayTimingPhaseCount]int64
	attempts        int64
	openBodies      int64
	maxRequestBytes int64
	responseBytes   int64
	firstByte       *time.Duration
}

type GatewayRequestTimingSnapshot struct {
	StartedAt               time.Time        `json:"handler_started_at"`
	TotalMS                 int64            `json:"handler_total_ms"`
	PhaseMS                 map[string]int64 `json:"phase_ms"`
	PhaseCalls              map[string]int64 `json:"phase_calls"`
	UpstreamAttempts        int64            `json:"upstream_attempts"`
	UpstreamBodiesOpen      int64            `json:"upstream_bodies_open"`
	MaxDeclaredRequestBytes *int64           `json:"max_declared_request_bytes,omitempty"`
	UpstreamResponseBytes   int64            `json:"upstream_response_bytes"`
	FirstUpstreamByteMS     *int64           `json:"first_upstream_byte_ms,omitempty"`
}

func WithGatewayRequestTiming(ctx context.Context) (context.Context, *GatewayRequestTiming) {
	if existing := GatewayRequestTimingFromContext(ctx); existing != nil {
		return ctx, existing
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timing := &GatewayRequestTiming{started: time.Now(), maxRequestBytes: -1}
	return context.WithValue(ctx, gatewayRequestTimingKey{}, timing), timing
}

func GatewayRequestTimingFromContext(ctx context.Context) *GatewayRequestTiming {
	if ctx == nil {
		return nil
	}
	timing, _ := ctx.Value(gatewayRequestTimingKey{}).(*GatewayRequestTiming)
	return timing
}

func MeasureGatewayTiming(ctx context.Context, phase GatewayTimingPhase) func() {
	timing := GatewayRequestTimingFromContext(ctx)
	if timing == nil || phase >= gatewayTimingPhaseCount {
		return func() {}
	}
	started := time.Now()
	var once sync.Once
	return func() { once.Do(func() { timing.record(phase, time.Since(started)) }) }
}

func (t *GatewayRequestTiming) record(phase GatewayTimingPhase, elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	t.mu.Lock()
	t.durations[phase] += elapsed
	t.calls[phase]++
	t.mu.Unlock()
}

func (t *GatewayRequestTiming) Record(phase GatewayTimingPhase, elapsed time.Duration) {
	if t != nil && phase < gatewayTimingPhaseCount {
		t.record(phase, elapsed)
	}
}

func (t *GatewayRequestTiming) Snapshot() GatewayRequestTimingSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	snapshot := GatewayRequestTimingSnapshot{
		StartedAt: t.started.UTC(), TotalMS: time.Since(t.started).Milliseconds(),
		PhaseMS: make(map[string]int64), PhaseCalls: make(map[string]int64),
		UpstreamAttempts: t.attempts, UpstreamBodiesOpen: t.openBodies,
		UpstreamResponseBytes: t.responseBytes,
	}
	if t.maxRequestBytes >= 0 {
		bytes := t.maxRequestBytes
		snapshot.MaxDeclaredRequestBytes = &bytes
	}
	for phase, count := range t.calls {
		if count > 0 {
			name := gatewayTimingPhaseNames[phase]
			snapshot.PhaseMS[name] = t.durations[phase].Milliseconds()
			snapshot.PhaseCalls[name] = count
		}
	}
	if t.firstByte != nil {
		ms := t.firstByte.Milliseconds()
		snapshot.FirstUpstreamByteMS = &ms
	}
	return snapshot
}

// PublishGatewayTiming fills existing Ops columns without a schema migration.
// Detailed queue and transport phases remain in the request's structured log.
func PublishGatewayTiming(c *gin.Context) (GatewayRequestTimingSnapshot, bool) {
	if c == nil || c.Request == nil {
		return GatewayRequestTimingSnapshot{}, false
	}
	timing := GatewayRequestTimingFromContext(c.Request.Context())
	if timing == nil {
		return GatewayRequestTimingSnapshot{}, false
	}
	snapshot := timing.Snapshot()
	var routingMS int64
	var routingObserved bool
	for _, name := range []string{"user_admission", "account_select", "account_wait", "message_queue", "retry_wait"} {
		if ms, ok := snapshot.PhaseMS[name]; ok {
			routingMS += ms
			routingObserved = true
		}
	}
	if routingObserved {
		SetOpsLatencyMs(c, OpsRoutingLatencyMsKey, routingMS)
	}
	if ms, ok := snapshot.PhaseMS["upstream_headers"]; ok {
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, ms)
	}
	if ms, ok := snapshot.PhaseMS["upstream_body"]; ok {
		SetOpsLatencyMs(c, OpsResponseLatencyMsKey, ms)
	}
	return snapshot, true
}

// TraceGatewayUpstream counts application-level HTTP dispatches, including
// retries. Redirects and transparent transport retries are not separate calls.
func TraceGatewayUpstream(req *http.Request) (*http.Request, func(*http.Response, error)) {
	if req == nil {
		return req, func(*http.Response, error) {}
	}
	timing := GatewayRequestTimingFromContext(req.Context())
	if timing == nil {
		return req, func(*http.Response, error) {}
	}
	started := time.Now()
	timing.mu.Lock()
	timing.attempts++
	if req.ContentLength > timing.maxRequestBytes {
		timing.maxRequestBytes = req.ContentLength
	}
	timing.mu.Unlock()
	var connectionMu sync.Mutex
	var connectionStarted time.Time
	trace := &httptrace.ClientTrace{
		GetConn: func(string) {
			connectionMu.Lock()
			connectionStarted = time.Now()
			connectionMu.Unlock()
		},
		GotConn: func(httptrace.GotConnInfo) {
			connectionMu.Lock()
			began := connectionStarted
			connectionStarted = time.Time{}
			connectionMu.Unlock()
			if !began.IsZero() {
				timing.record(GatewayTimingConnectionAcquire, time.Since(began))
			}
		},
		GotFirstResponseByte: func() {
			timing.mu.Lock()
			if timing.firstByte == nil {
				elapsed := time.Since(timing.started)
				timing.firstByte = &elapsed
			}
			timing.mu.Unlock()
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	var once sync.Once
	return req, func(resp *http.Response, err error) {
		once.Do(func() {
			timing.record(GatewayTimingResponseHeaders, time.Since(started))
			if err != nil || resp == nil || resp.Body == nil {
				return
			}
			timing.mu.Lock()
			timing.openBodies++
			timing.mu.Unlock()
			resp.Body = &gatewayTimingBody{ReadCloser: resp.Body, timing: timing, started: time.Now()}
		})
	}
}

type gatewayTimingBody struct {
	io.ReadCloser
	timing  *GatewayRequestTiming
	started time.Time
	once    sync.Once
	err     error
}

func (b *gatewayTimingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timing.mu.Lock()
		b.timing.responseBytes += int64(n)
		b.timing.mu.Unlock()
	}
	return n, err
}

func (b *gatewayTimingBody) Close() error {
	b.once.Do(func() {
		b.err = b.ReadCloser.Close()
		b.timing.record(GatewayTimingResponseBody, time.Since(b.started))
		b.timing.mu.Lock()
		b.timing.openBodies--
		b.timing.mu.Unlock()
	})
	return b.err
}
