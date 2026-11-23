//go:build unit

package service

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponseDelivery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		chunks    []string
		delivered bool
	}{
		{"chat delta split across writes", []string{"data: {\"choices\":[", "{\"delta\":{\"content\":\"hi\"}}]}\n\n"}, true},
		{"responses reasoning", []string{"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"proof\"}\n\n"}, true},
		{"messages tool arguments", []string{"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n"}, true},
		{"empty streamed tool skeleton", []string{`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}` + "\n\n"}, false},
		{"streamed tool arguments", []string{`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}` + "\n\n"}, true},
		{"object text delta", []string{`data: {"type":"response.output_text.delta","delta":{}}`}, false},
		{"array text delta", []string{`data: {"type":"response.output_text.delta","delta":[]}`}, false},
		{"tool id only", []string{`data: {"choices":[{"delta":{"tool_calls":[{"id":"call_1"}]}}]}`}, false},
		{"audio delta", []string{`data: {"choices":[{"delta":{"audio":{"data":"AQID"}}}]}`}, true},
		{"audio message", []string{`{"choices":[{"message":{"audio":{"data":"AQID"}}}]}`}, true},
		{"audio transcript delta", []string{`data: {"choices":[{"delta":{"audio":{"transcript":"hello"}}}]}`}, true},
		{"audio transcript message", []string{`{"choices":[{"message":{"audio":{"transcript":"hello"}}}]}`}, true},
		{"audio object transcript", []string{`{"choices":[{"message":{"audio":{"transcript":{}}}}]}`}, false},
		{"audio data in duplicate parent", []string{`{"choices":[{"message":{"audio":{},"audio":{"data":"AQID"}}}]}`}, true},
		{"audio id only", []string{`{"choices":[{"message":{"audio":{"id":"audio_1"}}}]}`}, false},
		{"final refusal", []string{`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"refusal","refusal":"no"}]}}`}, true},
		{"terminal text", []string{`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}`}, true},
		{"compact object", []string{`{"output":[{"type":"compaction","encrypted_content":{}}]}`}, false},
		{"compact array", []string{`{"output":[{"type":"compaction","encrypted_content":[]}]}`}, false},
		{"encrypted compact", []string{`{"output":[{"type":"compaction","encrypted_content":"abc"}]}`}, true},
		{"metadata", []string{": ping\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: [DONE]\n\n"}, false},
		{"chat empty content array", []string{`{"choices":[{"message":{"role":"assistant","content":[]}}]}`}, false},
		{"chat tool", []string{`{"choices":[{"message":{"tool_calls":[{"function":{"name":"lookup","arguments":"{}"}}]}}]}`}, true},
		{"responses text", []string{`{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`}, true},
		{"nullable responses error", []string{`{"error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`}, true},
		{"messages thinking", []string{`{"content":[{"type":"thinking","thinking":"proof"}]}`}, true},
		{"nonstream split writes", []string{`{"choices":[`, `{"message":{"content":"hi"}}]}`}, true},
		{"encrypted reasoning", []string{`{"output":[{"type":"reasoning","encrypted_content":"proof"}]}`}, true},
		{"image output", []string{`{"content":[{"type":"image","source":{"type":"base64","data":"AQID"}}]}`}, true},
		{"unrelated item name", []string{`{"output":[{"type":"message","name":"assistant","content":[]}]}`}, false},
		{"empty output", []string{`{"output":[{"type":"message","content":[{"type":"output_text","text":""}]}]}`}, false},
		{"error", []string{`{"type":"error","error":{"message":"failed"}}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := []byte(strings.Join(tc.chunks, ""))
			require.Equal(t, tc.delivered, bufferedOpenAIContentDelivered(c, &OpenAIUsage{}, body, len(body)))
			require.False(t, bufferedOpenAIContentDelivered(c, &OpenAIUsage{InputTokens: 1}, body, len(body)))
			require.False(t, bufferedOpenAIContentDelivered(c, &OpenAIUsage{}, body, 0))
		})
	}
}

func TestOpenAIFinalDeliveryAllocations(t *testing.T) {
	// AllocsPerRun counts allocations process-wide. Service workers from other
	// tests can still be active when the full package runs.
	const child = "SUB2API_TEST_FINAL_DELIVERY_ALLOCS"
	if os.Getenv(child) != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestOpenAIFinalDeliveryAllocations$")
		cmd.Env = append(os.Environ(), child+"=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}
	for _, tc := range []struct {
		event, payload string
		want           bool
	}{
		{"response.output_item.done", `{"item":{"type":"message","content":[{"type":"output_text","text":""}]}}`, false},
		{"response.completed", `{"response":{"output":[{"type":"message","content":[{"type":"refusal","refusal":"declined"}]}]}}`, true},
		{"response.completed", `{"response":{"out\u0070ut":[{"type":"message","content":[{"type":"ref\u0075sal","refusal":"first\nsecond"}]}]}}`, true},
		{"response.output_item.done", `{"it\u0065m":{"type":"message","content":["skip\nme",false,3,null,{"type":"output_text","text":"\n"}]}}`, true},
		{"response.output_item.done", `{"item":{"type":"message","content":[{"type":"refusal","refusal":"` + strings.Repeat(`x\n`, 16384) + `"}]}}`, true},
	} {
		require.Equal(t, tc.want, openAIFinalEventHasContent(tc.payload, tc.event))
		require.Zero(t, testing.AllocsPerRun(100, func() { openAIFinalEventHasContent(tc.payload, tc.event) }), "%s, %d bytes", tc.event, len(tc.payload))
	}
}

func TestOpenAIStringDeltas(t *testing.T) {
	for _, event := range []string{"response.output_text.delta", "response.reasoning_summary_text.delta",
		"response.reasoning_text.delta", "response.refusal.delta", "response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta", "response.audio.delta", "response.audio_transcript.delta"} {
		for _, value := range []string{"{}", "[]", "false", "17", "null", `""`, `" "`} {
			_, delivered := openAIResponsesStreamContent(`{"delta":`+value+`}`, event)
			require.Equal(t, value == `" "`, delivered, "%s %s", event, value)
		}
	}
	for _, field := range []string{"content", "reasoning", "reasoning_content", "refusal"} {
		_, delivered := openAIChatStreamContent(`{"choices":[{"delta":{"` + field + `":{}}}]}`)
		require.False(t, delivered, field)
	}
}

func TestOpenAIChatStreamContentBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		delivered     bool
	}{
		{"text block", `[{},{"type":"text","text":"hi"}]`, true},
		{"refusal block", `[{"type":"refusal","refusal":"declined"}]`, true},
		{"empty array", `[]`, false},
		{"empty block", `[{}]`, false},
		{"empty text", `[{"type":"text","text":""}]`, false},
		{"object", `{"type":"text","text":"hi"}`, false},
		{"number", `12`, false},
		{"boolean", `true`, false},
		{"null", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"choices":[{"delta":{"content":` + tc.content + `}}]}`
			_, delivered := openAIChatStreamContent(payload)
			require.Equal(t, tc.delivered, delivered)
		})
	}
	t.Run("string without allocations", func(t *testing.T) {
		const payload = `{"choices":[{"delta":{"content":"hi"}}]}`
		_, delivered := openAIChatStreamContent(payload)
		require.True(t, delivered)
		require.Zero(t, testing.AllocsPerRun(100, func() {
			openAIChatStreamContent(payload)
		}))
	})
}
