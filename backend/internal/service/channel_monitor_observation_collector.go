package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type ChannelMonitorCollectorOptions struct {
	QueueSize         int
	BatchSize         int
	FlushInterval     time.Duration
	WriteTimeout      time.Duration
	HeartbeatInterval time.Duration
}

type ChannelMonitorCollector struct {
	repo     ChannelMonitorObservationRepository
	options  ChannelMonitorCollectorOptions
	queue    chan ChannelMonitorEvent
	stop     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	started  bool
	closed   bool
	session  ChannelMonitorObservationSession
	dropped  atomic.Int64
	inFlight atomic.Int64
	enabled  atomic.Bool
	configMu sync.RWMutex
	config   ChannelMonitorObservationConfig
}

func NewChannelMonitorCollector(repo ChannelMonitorObservationRepository, options ChannelMonitorCollectorOptions) *ChannelMonitorCollector {
	if options.QueueSize <= 0 {
		options.QueueSize = 8192
	}
	if options.QueueSize > 65536 {
		options.QueueSize = 65536
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 128
	}
	if options.BatchSize > 512 {
		options.BatchSize = 512
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = time.Second
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = 3 * time.Second
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = 15 * time.Second
	}
	c := &ChannelMonitorCollector{repo: repo, options: options, queue: make(chan ChannelMonitorEvent, options.QueueSize), stop: make(chan struct{}), done: make(chan struct{})}
	c.enabled.Store(true)
	c.config = DefaultChannelMonitorObservationConfig()
	return c
}

func (c *ChannelMonitorCollector) SetConfig(cfg ChannelMonitorObservationConfig) {
	if c == nil {
		return
	}
	c.configMu.Lock()
	c.config = cfg
	c.configMu.Unlock()
	c.enabled.Store(cfg.Enabled)
}

func (c *ChannelMonitorCollector) Start() {
	if c == nil || c.repo == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.closed {
		return
	}
	c.started = true
	c.session = ChannelMonitorObservationSession{ID: uuid.NewString(), StartedAt: time.Now().UTC()}
	go c.run()
}

func (c *ChannelMonitorCollector) Submit(event ChannelMonitorEvent) bool {
	if c == nil || !c.enabled.Load() {
		return false
	}
	normalized, err := NormalizeChannelMonitorEvent(event, time.Now())
	if err != nil {
		c.dropped.Add(1)
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started || c.closed {
		c.dropped.Add(1)
		return false
	}
	normalized.SessionID = c.session.ID
	select {
	case c.queue <- normalized:
		return true
	default:
		c.dropped.Add(1)
		return false
	}
}

// Begin tracks in-flight business requests without persisting an event per token.
func (c *ChannelMonitorCollector) Begin() func() {
	if c == nil {
		return func() {}
	}
	c.inFlight.Add(1)
	var once sync.Once
	return func() { once.Do(func() { c.inFlight.Add(-1) }) }
}

func (c *ChannelMonitorCollector) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if !c.started {
		c.closed = true
		c.mu.Unlock()
		return nil
	}
	if !c.closed {
		c.closed = true
		close(c.stop)
	}
	c.mu.Unlock()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *ChannelMonitorCollector) QueueDepth() int {
	if c == nil {
		return 0
	}
	return len(c.queue)
}

func (c *ChannelMonitorCollector) updateProgress(now time.Time, batchLength int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session.PendingEvents = int64(len(c.queue) + batchLength)
	if c.session.PendingEvents == 0 && c.session.LastWriteError == "" {
		through := now
		c.session.DataThrough = &through
	}
}

// advanceDataThrough records the newest completed event that has been
// durably written.  The collector may still have newer events waiting in its
// queue, but the successfully flushed prefix is valid coverage and should not
// be hidden behind that queue.  This also keeps long-running requests (which
// increase in_flight without producing events) from looking like a writer
// backlog.
func (c *ChannelMonitorCollector) advanceDataThrough(through time.Time) {
	if c == nil || through.IsZero() {
		return
	}
	through = through.UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session.DataThrough == nil || through.After(*c.session.DataThrough) {
		copy := through
		c.session.DataThrough = &copy
	}
}

func (c *ChannelMonitorCollector) run() {
	defer close(c.done)
	flushTimer := time.NewTicker(c.options.FlushInterval)
	defer flushTimer.Stop()
	heartbeatTimer := time.NewTicker(c.options.HeartbeatInterval)
	defer heartbeatTimer.Stop()
	batch := make([]ChannelMonitorEvent, 0, c.options.BatchSize)
	var pendingGaps []ChannelMonitorObservationGap
	lastHeartbeat := c.session.StartedAt
	addGap := func(start, end time.Time, reason string, lost int64) {
		if len(pendingGaps) >= 64 {
			pendingGaps[0].EndedAt = end
			pendingGaps[0].LostEvents += lost
			return
		}
		pendingGaps = append(pendingGaps, ChannelMonitorObservationGap{StartedAt: start, EndedAt: end, Reason: reason, LostEvents: lost})
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), c.options.WriteTimeout)
			err = c.repo.StoreBatch(ctx, batch)
			cancel()
			if err == nil || errors.Is(err, ErrChannelMonitorObservationCapacity) {
				break
			}
		}
		if err != nil {
			reason := "write_failed"
			if errors.Is(err, ErrChannelMonitorObservationCapacity) {
				reason = "capacity"
			}
			start, end := batch[0].CompletedAt, batch[0].CompletedAt
			for _, event := range batch[1:] {
				if event.CompletedAt.Before(start) {
					start = event.CompletedAt
				}
				if event.CompletedAt.After(end) {
					end = event.CompletedAt
				}
			}
			addGap(start, end.Add(time.Nanosecond), reason, int64(len(batch)))
			c.session.LastWriteError = reason
			slog.Warn("channel_monitor.batch_write_failed", "reason", reason, "events", len(batch), "error", err)
		} else {
			now := time.Now().UTC()
			c.session.LastIngestedAt = &now
			c.session.LastWriteError = ""
			// DataThrough is event time, not wall-clock ingest time.  Advance it
			// for the flushed prefix even when newer events remain queued.
			var through time.Time
			for _, event := range batch {
				if event.CompletedAt.After(through) {
					through = event.CompletedAt
				}
			}
			c.advanceDataThrough(through)
		}
		for i := range batch {
			batch[i] = ChannelMonitorEvent{}
		}
		batch = batch[:0]
		c.updateProgress(time.Now().UTC(), 0)
	}
	heartbeat := func(ending bool) {
		now := time.Now().UTC()
		if dropped := c.dropped.Swap(0); dropped > 0 {
			addGap(lastHeartbeat, now, "queue_loss", dropped)
			c.session.DroppedEvents += dropped
		}
		c.session.HeartbeatAt = now
		c.session.InFlight = c.inFlight.Load()
		if ending {
			// Stop is the end of this collector session. Requests still in flight
			// cannot safely submit after the queue is closed, so record an explicit
			// coverage gap and close the session instead of leaving a stale open
			// session forever after a process restart.
			if c.session.InFlight > 0 {
				addGap(now, now.Add(time.Nanosecond), "shutdown_loss", c.session.InFlight)
				c.session.DroppedEvents += c.session.InFlight
				c.session.InFlight = 0
			}
			c.session.EndedAt = &now
		}
		if len(pendingGaps) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), c.options.WriteTimeout)
			err := c.repo.RecordGaps(ctx, c.session.ID, pendingGaps)
			cancel()
			if err == nil {
				pendingGaps = nil
			} else {
				c.session.LastWriteError = "gap_write_failed"
				slog.Warn("channel_monitor.gap_write_failed", "gaps", len(pendingGaps), "error", err)
			}
		}
		c.updateProgress(now, len(batch))
		ctx, cancel := context.WithTimeout(context.Background(), c.options.WriteTimeout)
		err := c.repo.Heartbeat(ctx, c.session)
		cancel()
		if err != nil {
			addGap(lastHeartbeat, now, "heartbeat_failed", 0)
			slog.Warn("channel_monitor.heartbeat_failed", "error", err)
		}
		lastHeartbeat = now
		ctx, cancel = context.WithTimeout(context.Background(), c.options.WriteTimeout)
		cfg, err := c.repo.GetConfig(ctx)
		cancel()
		if err == nil && cfg != nil {
			c.SetConfig(*cfg)
		}
	}
	heartbeat(false)
	for {
		select {
		case e := <-c.queue:
			batch = append(batch, e)
			if len(batch) >= c.options.BatchSize {
				flush()
			}
		case <-flushTimer.C:
			flush()
		case <-heartbeatTimer.C:
			flush()
			heartbeat(false)
			ctx, cancel := context.WithTimeout(context.Background(), c.options.WriteTimeout)
			if err := c.repo.Maintain(ctx, time.Now()); err != nil {
				slog.Warn("channel_monitor.maintenance_failed", "error", err)
			}
			cancel()
		case <-c.stop:
			for {
				select {
				case e := <-c.queue:
					batch = append(batch, e)
					if len(batch) >= c.options.BatchSize {
						flush()
					}
				default:
					flush()
					heartbeat(true)
					return
				}
			}
		}
	}
}
