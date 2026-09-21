package handler

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (h *OpenAIGatewayHandler) SetChannelMonitorCollector(collector *service.ChannelMonitorCollector) {
	h.channelMonitorCollector = collector
}

// A proxy invocation restarts local turn numbers on account failover. Only
// completed logical turns advance this ledger's base; retries reuse its ID.
type channelMonitorWSLedger struct {
	mu        sync.Mutex
	sink      service.ChannelMonitorEventSink
	begin     func() func()
	template  service.ChannelMonitorEvent
	namespace uuid.UUID
	finalized int
	active    int
	event     *service.ChannelMonitorEvent
	end       func()
	closed    bool
}

func newChannelMonitorWSLedger(sink service.ChannelMonitorEventSink, begin func() func(), template service.ChannelMonitorEvent) *channelMonitorWSLedger {
	return &channelMonitorWSLedger{sink: sink, begin: begin, template: template, namespace: uuid.New()}
}

func (l *channelMonitorWSLedger) attempt(accountID int64) (func(int, time.Time, string), func(int, *service.OpenAIForwardResult, error)) {
	l.mu.Lock()
	base := l.finalized
	l.mu.Unlock()
	var attemptStart time.Time
	var attemptTurn int
	return func(turn int, at time.Time, model string) {
			l.mu.Lock()
			defer l.mu.Unlock()
			logical := base + turn
			if l.closed || turn <= 0 || logical <= l.finalized {
				return
			}
			if at.IsZero() {
				at = time.Now()
			}
			if l.event != nil && l.active != logical {
				l.flushLocked(nil)
			}
			if l.event == nil {
				e := l.template
				e.Source, e.Protocol, e.Stream = "traffic", "responses_websocket", true
				e.RequestID = uuid.NewSHA1(l.namespace, []byte(strconv.Itoa(logical))).String()
				e.SessionID = l.namespace.String()
				e.StartedAt, e.RequestedModel = at.UTC(), model
				e.Outcome = "unknown"
				l.event, l.active = &e, logical
				if l.begin != nil {
					l.end = l.begin()
				}
			}
			attemptStart, attemptTurn = at, logical
		}, func(turn int, result *service.OpenAIForwardResult, err error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			logical := base + turn
			if l.closed || l.event == nil || l.active != logical || attemptTurn != logical {
				return
			}
			e := l.event
			now := time.Now().UTC()
			outcome, category, terminal := channelMonitorWSOutcome(result, err)
			e.Outcome, e.ErrorCategory, e.TerminalSeen = outcome, category, terminal
			e.CompletedAt = now
			if result != nil {
				e.InputTokens += int64(result.Usage.InputTokens)
				e.OutputTokens += int64(result.Usage.OutputTokens)
				e.CacheCreationTokens += int64(result.Usage.CacheCreationInputTokens)
				e.CacheReadTokens += int64(result.Usage.CacheReadInputTokens)
				e.OutputSeen = e.OutputSeen || result.FirstTokenMs != nil || result.Usage.OutputTokens > 0
				if result.UpstreamResponseModel != "" {
					e.ResponseModel = result.UpstreamResponseModel
				}
				if e.FirstOutputMs == nil && result.FirstTokenMs != nil {
					first := attemptStart.Sub(e.StartedAt).Milliseconds() + int64(*result.FirstTokenMs)
					e.FirstOutputMs = &first
				}
			}
			if len(e.Attempts) < service.ChannelMonitorObservationMaxAttempts {
				e.Attempts = append(e.Attempts, service.ChannelMonitorAttempt{Sequence: len(e.Attempts) + 1, AccountID: accountID, StartedAt: attemptStart.UTC(), CompletedAt: now, Outcome: outcome, ErrorCategory: category, DurationMs: now.Sub(attemptStart).Milliseconds()})
			} else {
				e.AttemptsTruncated = true
			}
			attemptTurn = 0
			if terminal && (err == nil || outcome == "success") {
				l.flushLocked(nil)
			}
		}
}

func (l *channelMonitorWSLedger) close(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.flushLocked(err)
	l.closed = true
}

func (l *channelMonitorWSLedger) flushLocked(err error) {
	if l.event == nil {
		return
	}
	e := l.event
	if err != nil {
		outcome, category, _ := channelMonitorWSOutcome(nil, err)
		if outcome == "cancelled" || e.Outcome == "unknown" {
			e.Outcome, e.ErrorCategory = outcome, category
		}
	}
	if e.CompletedAt.IsZero() {
		e.CompletedAt = time.Now().UTC()
	}
	e.DurationMs = e.CompletedAt.Sub(e.StartedAt).Milliseconds()
	if l.sink != nil {
		l.sink.Submit(*e)
	}
	if l.end != nil {
		l.end()
		l.end = nil
	}
	l.finalized, l.event = l.active, nil
}

func channelMonitorWSOutcome(result *service.OpenAIForwardResult, err error) (string, string, bool) {
	if result != nil {
		switch result.UpstreamTerminalEvent {
		case "response.completed", "response.done":
			return "success", "", true
		case "response.failed", "error":
			return "channel_error", "upstream_error", true
		case "response.incomplete":
			return "unknown", "incomplete_stream", true
		}
	}
	if errors.Is(err, context.Canceled) || service.IsOpenAIWSSessionPreemptedError(err) || (err != nil && openAIWSIngressEndedByClient(err)) || (result != nil && result.ClientDisconnect) {
		return "cancelled", "client_cancelled", false
	}
	var closeErr *service.OpenAIWSClientCloseError
	if errors.As(err, &closeErr) && closeErr.StatusCode() == coderws.StatusPolicyViolation {
		return "client_error", "local_policy", false
	}
	var upstream *service.UpstreamFailoverError
	if errors.As(err, &upstream) {
		return "channel_error", "upstream_error", false
	}
	return "unknown", "incomplete_stream", false
}
