package service

import (
	"io"
	"sync/atomic"
	"time"
)

// upstreamIdleReader tracks upstream bytes, independently of SSE framing and
// downstream writes. Only the reader goroutine updates lastRead; the stream
// handler owns the timer.
type upstreamIdleReader struct {
	reader   io.Reader
	interval time.Duration
	started  time.Time
	lastRead atomic.Int64
	timer    *time.Timer
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

func (r *upstreamIdleReader) idleFor() time.Duration {
	lastRead := time.Duration(r.lastRead.Load())
	return time.Since(r.started) - lastRead
}

// Expired rearms to the remaining idle deadline instead of polling another
// full interval. Read timestamps retain monotonic time across wall-clock changes.
func (r *upstreamIdleReader) Expired() bool {
	if r.timer == nil {
		return false
	}
	remaining := r.interval - r.idleFor()
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
