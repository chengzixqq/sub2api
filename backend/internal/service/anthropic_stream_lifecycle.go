package service

import (
	"io"
	"log/slog"
	"sync"
	"time"
)

// anthropicStreamScannerLifecycle coordinates the scanner goroutine with the
// response body that it reads. A handler must stop this lifecycle before it
// returns so an in-flight Read cannot outlive the request and race a reused
// HTTP/1 connection's EOF notification.
type anthropicStreamScannerLifecycle struct {
	done        chan struct{}
	scannerDone <-chan struct{}
	body        io.Closer
	idleReader  *upstreamIdleReader
	stopOnce    sync.Once
}

func newAnthropicStreamScannerLifecycle(
	body io.Closer,
	idleReader *upstreamIdleReader,
	scannerDone <-chan struct{},
	done chan struct{},
) *anthropicStreamScannerLifecycle {
	return &anthropicStreamScannerLifecycle{
		done:        done,
		scannerDone: scannerDone,
		body:        body,
		idleReader:  idleReader,
	}
}

// stopAndWait prevents new scanner events, cancels the network read through
// resp.Body.Close, and waits until the scanner goroutine has fully exited.
// The operation is idempotent because handlers have several return paths and
// the outer forwarder also closes the response body.
func (l *anthropicStreamScannerLifecycle) stopAndWait() {
	if l == nil {
		return
	}
	started := time.Now()
	var closeDuration time.Duration
	l.stopOnce.Do(func() {
		close(l.done)
		if l.idleReader != nil {
			l.idleReader.Stop()
		}
		if l.body != nil {
			closeStarted := time.Now()
			_ = l.body.Close()
			closeDuration = time.Since(closeStarted)
		}
	})
	if l.scannerDone != nil {
		<-l.scannerDone
	}
	waitDuration := time.Since(started)
	if closeDuration >= 100*time.Millisecond || waitDuration >= 100*time.Millisecond {
		slog.Debug("anthropic_stream_cleanup_slow",
			"body_close_ms", closeDuration.Milliseconds(),
			"scanner_wait_ms", waitDuration.Milliseconds(),
			"scanner_delayed", waitDuration >= 100*time.Millisecond,
		)
	}
}
