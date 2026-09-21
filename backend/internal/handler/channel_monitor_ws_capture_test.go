//go:build unit

package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type monitorWSSink struct{ events []service.ChannelMonitorEvent }

func (s *monitorWSSink) Submit(e service.ChannelMonitorEvent) bool {
	s.events = append(s.events, e)
	return true
}

func TestChannelMonitorWS_RetryAndLocalTurnReset(t *testing.T) {
	sink := &monitorWSSink{}
	ledger := newChannelMonitorWSLedger(sink, nil, service.ChannelMonitorEvent{Platform: "openai", GroupID: 1})
	start, observe := ledger.attempt(11)
	start(1, time.Now(), "gpt-one")
	observe(1, &service.OpenAIForwardResult{UpstreamTerminalEvent: "response.completed", Usage: service.OpenAIUsage{OutputTokens: 3}}, nil)
	start(2, time.Now(), "gpt-two")
	observe(2, &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 2}}, errors.New("connection interrupted"))
	retryStart, retryObserve := ledger.attempt(12)
	retryStart(1, time.Now(), "gpt-two")
	retryObserve(1, &service.OpenAIForwardResult{UpstreamTerminalEvent: "response.completed", Usage: service.OpenAIUsage{OutputTokens: 4}}, nil)
	ledger.close(nil)
	require.Len(t, sink.events, 2)
	require.Equal(t, int64(3), sink.events[0].OutputTokens)
	require.Equal(t, int64(6), sink.events[1].OutputTokens)
	require.Len(t, sink.events[1].Attempts, 2)
	require.Equal(t, "gpt-two", sink.events[1].RequestedModel)
	require.NotEqual(t, sink.events[0].RequestID, sink.events[1].RequestID)
	_, err := uuid.Parse(sink.events[1].RequestID)
	require.NoError(t, err)
}

func TestChannelMonitorWS_PartialCancelAndIdleClose(t *testing.T) {
	sink := &monitorWSSink{}
	ledger := newChannelMonitorWSLedger(sink, nil, service.ChannelMonitorEvent{})
	start, observe := ledger.attempt(11)
	start(1, time.Now(), "gpt-one")
	first := 12
	observe(1, &service.OpenAIForwardResult{FirstTokenMs: &first, Usage: service.OpenAIUsage{InputTokens: 10, OutputTokens: 4}}, context.Canceled)
	ledger.close(context.Canceled)
	ledger.close(context.Canceled)
	require.Len(t, sink.events, 1)
	require.Equal(t, "cancelled", sink.events[0].Outcome)
	require.Equal(t, int64(4), sink.events[0].OutputTokens)
	require.True(t, sink.events[0].OutputSeen)
	require.False(t, sink.events[0].TerminalSeen)

	idle := newChannelMonitorWSLedger(sink, nil, service.ChannelMonitorEvent{})
	_, idleObserve := idle.attempt(11)
	idleObserve(1, nil, context.Canceled)
	idle.close(nil)
	require.Len(t, sink.events, 1)
}

func TestChannelMonitorWS_RetryAttemptsBoundedAndUnknownNotHealthy(t *testing.T) {
	sink := &monitorWSSink{}
	ledger := newChannelMonitorWSLedger(sink, nil, service.ChannelMonitorEvent{})
	for range 20 {
		start, observe := ledger.attempt(11)
		start(1, time.Now(), "gpt-one")
		observe(1, &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 1}}, errors.New("broken stream"))
	}
	ledger.close(errors.New("broken stream"))
	require.Len(t, sink.events, 1)
	require.Len(t, sink.events[0].Attempts, 16)
	require.True(t, sink.events[0].AttemptsTruncated)
	require.Equal(t, "unknown", sink.events[0].Outcome)
	require.Equal(t, int64(20), sink.events[0].OutputTokens)
}

func TestChannelMonitorWS_TerminalFailureCanRetryWithoutDoubleFinal(t *testing.T) {
	sink := &monitorWSSink{}
	ledger := newChannelMonitorWSLedger(sink, nil, service.ChannelMonitorEvent{})
	start, observe := ledger.attempt(11)
	start(1, time.Now(), "gpt-one")
	failed := &service.OpenAIForwardResult{UpstreamTerminalEvent: "response.failed", Usage: service.OpenAIUsage{OutputTokens: 2}}
	observe(1, failed, &service.UpstreamFailoverError{StatusCode: 503})
	observe(1, failed, &service.UpstreamFailoverError{StatusCode: 503})
	require.Empty(t, sink.events)
	start, observe = ledger.attempt(12)
	start(1, time.Now(), "gpt-one")
	observe(1, &service.OpenAIForwardResult{UpstreamTerminalEvent: "response.completed", Usage: service.OpenAIUsage{OutputTokens: 3}}, nil)
	ledger.close(nil)
	require.Len(t, sink.events, 1)
	require.Len(t, sink.events[0].Attempts, 2)
	require.Equal(t, int64(5), sink.events[0].OutputTokens)
	require.Equal(t, "success", sink.events[0].Outcome)
}
