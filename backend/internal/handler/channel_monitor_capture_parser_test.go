package handler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorParser_Protocols(t *testing.T) {
	tests := []struct {
		name, protocol, body     string
		stream                   bool
		output, complete, failed bool
	}{
		{"anthropic tool", "anthropic", `{"type":"message","model":"claude-opus","content":[{"type":"tool_use","name":"lookup","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":2}}`, false, true, true, false},
		{"anthropic fallback", "anthropic", "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus\",\"usage\":{\"input_tokens\":3}}}\r\n\r\nevent: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\r\n\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n", true, true, true, false},
		{"anthropic empty", "anthropic", "data: {\"type\":\"message_stop\"}\n\n", true, false, true, false},
		{"anthropic partial", "anthropic", "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"partial\"}}\n\n", true, true, false, false},
		{"anthropic error", "anthropic", "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n", true, false, false, true},
		{"chat refusal", "openai_chat", `{"choices":[{"index":0,"message":{"refusal":"declined"},"finish_reason":"stop"}]}`, false, true, true, false},
		{"chat tool stream", "openai_chat", "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"lookup\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", true, true, true, false},
		{"responses completed", "openai_responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5\",\"usage\":{\"input_tokens\":3,\"output_tokens\":4}}}\n\n", true, true, true, false},
		{"responses incomplete", "openai_responses", `{"status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`, false, true, false, false},
		{"responses queued", "openai_responses", `{"status":"queued","id":"task"}`, false, false, false, false},
		{"gemini tool", "gemini", `{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4}}`, false, true, true, false},
		{"gemini refusal", "gemini", `{"promptFeedback":{"blockReason":"SAFETY"}}`, false, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newChannelMonitorParser(tt.protocol, tt.stream)
			for i := range len(tt.body) {
				p.feed([]byte(tt.body[i : i+1]))
			}
			p.finish()
			require.Equal(t, tt.output, p.output)
			require.Equal(t, tt.complete, p.complete)
			require.Equal(t, tt.failed, p.failed)
			require.False(t, p.limited)
		})
	}
}

func TestChannelMonitorParser_BoundsAndMalformed(t *testing.T) {
	for _, stream := range []bool{false, true} {
		p := newChannelMonitorParser("anthropic", stream)
		p.feed([]byte(strings.Repeat("x", channelMonitorCaptureLimit+1)))
		p.finish()
		require.True(t, p.limited)
		require.LessOrEqual(t, cap(p.buffer), channelMonitorCaptureLimit)
	}
	p := newChannelMonitorParser("anthropic", true)
	p.feed([]byte("data: {bad}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	p.finish()
	require.True(t, p.malformed)
}

func TestChannelMonitorParser_DoesNotSumCumulativeUsage(t *testing.T) {
	p := newChannelMonitorParser("anthropic", true)
	p.feed([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"cache_read_input_tokens\":3}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n"))
	p.finish()
	require.EqualValues(t, 7, p.inputTokens)
	require.EqualValues(t, 3, p.cacheReadTokens)
	require.EqualValues(t, 5, p.outputTokens)
}

func TestChannelMonitorParser_ChatIncompleteChoice(t *testing.T) {
	p := newChannelMonitorParser("openai_chat", true)
	p.feed([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: [DONE]\n\n"))
	p.finish()
	require.False(t, p.complete)
}
