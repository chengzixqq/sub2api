package service

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// OpsUpstreamAttemptClock remembers when the newest upstream dispatch of one
// gateway request started. The HTTP upstream marks every dispatch; the next
// attempt event of the same account claims that start once, so events gain
// started_at_unix_ms / duration_ms without touching each failure site.
//
// Paths that never pass through the HTTP upstream (WebSocket, SDK clients)
// leave the clock empty and their events carry no timing rather than a wrong
// one. Only the newest dispatch is kept: a request has one in flight at a time.
type OpsUpstreamAttemptClock struct {
	mu          sync.Mutex
	accountID   int64
	startedAtMs int64
	claimed     bool
}

type opsUpstreamAttemptClockKey struct{}

const opsUpstreamAttemptClockGinKey = "ops_upstream_attempt_clock"

// WithOpsUpstreamAttemptClock returns ctx carrying a clock; an existing clock
// is reused.
func WithOpsUpstreamAttemptClock(ctx context.Context) (context.Context, *OpsUpstreamAttemptClock) {
	if existing := OpsUpstreamAttemptClockFromContext(ctx); existing != nil {
		return ctx, existing
	}
	if ctx == nil {
		ctx = context.Background()
	}
	clock := &OpsUpstreamAttemptClock{}
	return context.WithValue(ctx, opsUpstreamAttemptClockKey{}, clock), clock
}

func OpsUpstreamAttemptClockFromContext(ctx context.Context) *OpsUpstreamAttemptClock {
	if ctx == nil {
		return nil
	}
	clock, _ := ctx.Value(opsUpstreamAttemptClockKey{}).(*OpsUpstreamAttemptClock)
	return clock
}

// InstallOpsUpstreamAttemptClock attaches one clock to the gin context and to
// the request context (which upstream requests derive from). Idempotent.
func InstallOpsUpstreamAttemptClock(c *gin.Context) *OpsUpstreamAttemptClock {
	if c == nil {
		return nil
	}
	if clock := opsUpstreamAttemptClockFromGin(c); clock != nil {
		return clock
	}
	clock := &OpsUpstreamAttemptClock{}
	if c.Request != nil {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), opsUpstreamAttemptClockKey{}, clock))
	}
	c.Set(opsUpstreamAttemptClockGinKey, clock)
	return clock
}

// MarkOpsUpstreamAttemptStart records that a dispatch to accountID starts now.
// Without a clock it is one context lookup: no lock, no allocation.
func MarkOpsUpstreamAttemptStart(ctx context.Context, accountID int64) {
	if accountID <= 0 || ctx == nil {
		return
	}
	clock, _ := ctx.Value(opsUpstreamAttemptClockKey{}).(*OpsUpstreamAttemptClock)
	if clock == nil {
		return
	}
	clock.mark(accountID, time.Now().UnixMilli())
}

func (k *OpsUpstreamAttemptClock) mark(accountID, startedAtMs int64) {
	k.mu.Lock()
	k.accountID = accountID
	k.startedAtMs = startedAtMs
	k.claimed = false
	k.mu.Unlock()
}

// Pending reports the newest dispatch not yet claimed by an attempt event.
func (k *OpsUpstreamAttemptClock) Pending() (accountID, startedAtUnixMs int64, ok bool) {
	if k == nil {
		return 0, 0, false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.claimed || k.accountID <= 0 || k.startedAtMs <= 0 {
		return 0, 0, false
	}
	return k.accountID, k.startedAtMs, true
}

// claim hands the pending start to one event of the same account that ended
// no earlier than the dispatch began.
func (k *OpsUpstreamAttemptClock) claim(accountID, atUnixMs int64) int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.claimed || accountID <= 0 || k.accountID != accountID || k.startedAtMs <= 0 {
		return 0
	}
	if atUnixMs > 0 && k.startedAtMs > atUnixMs {
		return 0
	}
	k.claimed = true
	return k.startedAtMs
}

func opsUpstreamAttemptClockFromGin(c *gin.Context) *OpsUpstreamAttemptClock {
	if v, ok := c.Get(opsUpstreamAttemptClockGinKey); ok {
		if clock, ok := v.(*OpsUpstreamAttemptClock); ok && clock != nil {
			return clock
		}
	}
	if c.Request != nil {
		return OpsUpstreamAttemptClockFromContext(c.Request.Context())
	}
	return nil
}

// stampOpsUpstreamAttemptTiming fills the attempt start from the clock unless
// the caller supplied one, then derives the duration up to AtUnixMs.
func stampOpsUpstreamAttemptTiming(c *gin.Context, ev *OpsUpstreamErrorEvent) {
	if ev.StartedAtUnixMs <= 0 && ev.AccountID > 0 {
		if clock := opsUpstreamAttemptClockFromGin(c); clock != nil {
			ev.StartedAtUnixMs = clock.claim(ev.AccountID, ev.AtUnixMs)
		}
	}
	if ev.StartedAtUnixMs > 0 && ev.DurationMs == nil {
		duration := ev.AtUnixMs - ev.StartedAtUnixMs
		if duration < 0 {
			duration = 0
		}
		ev.DurationMs = &duration
	}
	ev.StartedAtUnixMs, ev.DurationMs = sanitizeOpsUpstreamAttemptTiming(ev.StartedAtUnixMs, ev.DurationMs)
}

// sanitizeOpsUpstreamAttemptTiming keeps the timing pair consistent: no start
// means no duration, and durations are never negative. It never writes
// through the caller's pointer.
func sanitizeOpsUpstreamAttemptTiming(startedAtMs int64, durationMs *int64) (int64, *int64) {
	if startedAtMs <= 0 {
		return 0, nil
	}
	if durationMs == nil || *durationMs >= 0 {
		return startedAtMs, durationMs
	}
	zero := int64(0)
	return startedAtMs, &zero
}
