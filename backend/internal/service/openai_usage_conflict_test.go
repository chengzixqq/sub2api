//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamUsageConflictNeedsReportedZero(t *testing.T) {
	for _, tc := range []struct {
		report   string
		conflict bool
	}{
		{`{}`, false}, {`null`, false}, {`{"other":0}`, false},
		{`{"output_tokens":null,"input_tokens_details":{"cached_tokens":null}}`, false},
		{`{"input_tokens":0}`, true}, {`{"output_tokens_details":{"image_tokens":0}}`, true},
		{`{"total_tokens":0}`, true}, {`{"completion_tokens_details":{"reasoning_tokens":0}}`, true},
	} {
		t.Run(tc.report, func(t *testing.T) {
			want := OpenAIUsage{InputTokens: 5, OutputTokens: 3, RetainedAfterZero: tc.conflict}
			payload := []byte(`{"type":"response.completed","response":{"usage":` + tc.report + `}}`)
			for _, mode := range []string{"stream", "buffered"} {
				got, _ := runMessagesTerminalUsage(t, mode, messagesTerminalUsagePayload(`{"input_tokens":5,"output_tokens":3}`, tc.report, ""))
				require.Equal(t, want, got)
			}
			got := OpenAIUsage{InputTokens: 5, OutputTokens: 3}
			(&OpenAIGatewayService{}).parseOpenAIImagesSSEUsageBytes(payload, &got)
			require.Equal(t, want, got)
		})
	}
}

func TestOpenAIBufferedUsageConflict(t *testing.T) {
	const first = `{"type":"response.in_progress","response":{"usage":{"input_tokens":75,"output_tokens":1,"input_tokens_details":{"cached_tokens":72}}}}`
	for _, tc := range []struct {
		name     string
		response string
		conflict bool
	}{
		{"absent", `{}`, false},
		{"null", `{"usage":null}`, false},
		{"empty", `{"usage":{}}`, true},
		{"unrelated", `{"usage":{"other":0}}`, true},
		{"null counters", `{"usage":{"input_tokens":null,"output_tokens":null}}`, true},
		{"reported zero", `{"usage":{"output_tokens":0}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstUsage := OpenAIUsage{InputTokens: 75, OutputTokens: 1, CacheReadInputTokens: 72}
			want := firstUsage
			want.RetainedAfterZero = tc.conflict
			final := `{"type":"response.completed","response":` + tc.response + `}`
			t.Run("SSE frames", func(t *testing.T) {
				got := (&OpenAIGatewayService{}).parseSSEUsageFromBody("data: " + first + "\n\ndata: " + final + "\n\n")
				require.Equal(t, want, *got)
			})
			t.Run("WS frames", func(t *testing.T) {
				var got OpenAIUsage
				observeOpenAIWSResponseUsage([]byte(first), &got, true)
				observeOpenAIWSResponseUsage([]byte(final), &got, true)
				require.Equal(t, want, got)
			})
			t.Run("WS final response", func(t *testing.T) {
				got := firstUsage
				populateOpenAIUsageFromResponseJSON([]byte(tc.response), &got)
				require.Equal(t, want, got)
			})
		})
	}
}
