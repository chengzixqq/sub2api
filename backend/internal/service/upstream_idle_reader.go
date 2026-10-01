package service

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUpstreamIdleTimeout identifies a timeout caused by the absence of bytes
// from the upstream response. Callers can classify it without parsing log text.
var ErrUpstreamIdleTimeout = errors.New("upstream idle timeout")

// upstreamIdleReader tracks upstream bytes, independently of SSE framing and
// downstream writes. Only the scanner goroutine updates lastRead and pauses
// accounting while delivering buffered events; the stream handler owns the timer.
type upstreamIdleReader struct {
	reader   io.Reader
	interval time.Duration
	started  time.Time
	lastRead atomic.Int64
	timer    *time.Timer
	pauseMu  sync.Mutex
	pausedAt time.Time
}

func newUpstreamIdleReader(reader io.Reader, interval time.Duration) *upstreamIdleReader {
	r := &upstreamIdleReader{reader: reader, interval: interval, started: time.Now()}
	if interval > 0 {
		r.timer = time.NewTimer(interval)
	}
	return r
}

func (r *upstreamIdleReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.interval > 0 {
		r.lastRead.Store(time.Since(r.started).Nanoseconds())
	}
	return n, err
}

func (r *upstreamIdleReader) C() <-chan time.Time {
	if r.timer == nil {
		return nil
	}
	return r.timer.C
}

// pause/resume exclude local event backpressure from upstream idle time. They
// are paired by the scanner goroutine, which cannot Read while delivering an
// event. In particular a slow downstream must not quarantine a healthy upstream.
func (r *upstreamIdleReader) pause() {
	if r.interval <= 0 {
		return
	}
	r.pauseMu.Lock()
	r.pausedAt = time.Now()
	r.pauseMu.Unlock()
}

func (r *upstreamIdleReader) resume() {
	if r.interval <= 0 {
		return
	}
	r.pauseMu.Lock()
	if !r.pausedAt.IsZero() {
		r.lastRead.Add(time.Since(r.pausedAt).Nanoseconds())
		r.pausedAt = time.Time{}
	}
	r.pauseMu.Unlock()
}

func (r *upstreamIdleReader) idleFor() time.Duration {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	return r.idleForLocked()
}

func (r *upstreamIdleReader) idleForLocked() time.Duration {
	lastRead := time.Duration(r.lastRead.Load())
	if !r.pausedAt.IsZero() {
		return r.pausedAt.Sub(r.started) - lastRead
	}
	return time.Since(r.started) - lastRead
}

// Expired rearms to the remaining idle deadline instead of polling another
// full interval. Read timestamps retain monotonic time across wall-clock changes.
func (r *upstreamIdleReader) Expired() bool {
	if r.timer == nil {
		return false
	}
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	if !r.pausedAt.IsZero() {
		r.timer.Reset(r.interval)
		return false
	}
	remaining := r.interval - r.idleForLocked()
	if remaining > 0 {
		r.timer.Reset(remaining)
		return false
	}
	return true
}

func (r *upstreamIdleReader) Stop() {
	if r.timer != nil {
		r.timer.Stop()
	}
}
