//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestWrappedNativeSearch(t *testing.T) {
	for _, wrap := range []string{`"data":%s`, `"data":{"response":%s}`, `"d\u0061ta" : %s`} {
		for _, tc := range []struct {
			report string
			want   int
		}{
			{"", 1}, {`,"tool_usage":{"web_search":{"num_requests":2}}`, 2},
			{`,"tool_usage":{"web_search":{"num_requests":0}}`, 0},
		} {
			response := `{"status":"completed","output":[{"type":"web_search_call","id":"a"}],"usage":{"input_tokens":10,"output_tokens":4}` + tc.report + "}"
			envelope := fmt.Sprintf(wrap, response)
			body := []byte("{" + envelope + "}")
			_, direct := responseImageAndSearchCounts(body)
			require.Equal(t, tc.want, direct, envelope)
			require.Equal(t, tc.want, countOpenAINativeSearchJSON(body), envelope)
			sse := "data: " + `{"type":"response.completed",` + envelope + "}\n\n"
			_, buffered := bufferedImageAndSearchCounts(sse, true)
			require.Equal(t, tc.want, buffered, envelope)
			require.Equal(t, tc.want, countOpenAINativeSearchSSE(sse), envelope)
			payload := `{"type":"response.completed",` + envelope + "}"
			var event apicompat.ResponsesStreamEvent
			require.NoError(t, json.Unmarshal([]byte(payload), &event))
			var converted openAINativeSearchCounter
			converted.observeConverted(payload, &event)
			require.Equal(t, tc.want, converted.count(), envelope)
		}
	}
}

func TestConvertedSearchOrdinaryTerminalAllocations(t *testing.T) {
	for _, content := range []string{"hello", `web_search_call \u4e2d`, `web_search_call \u0064ata`} {
		payload := fmt.Sprintf(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"%s"}]}],"usage":{"input_tokens":7,"output_tokens":3}}}`, content)
		var event apicompat.ResponsesStreamEvent
		require.NoError(t, json.Unmarshal([]byte(payload), &event))
		var counter openAINativeSearchCounter
		require.Zero(t, testing.AllocsPerRun(32, func() { counter.observeConverted(payload, &event) }), content)
		require.Zero(t, counter.count())
	}
}

func TestConvertedSearchTopLevelTerminal(t *testing.T) {
	const payload = `{"type":"response.completed","output":[{"type":"web_search_call","id":"a"}],"tool_usage":{"web_search":{"num_requests":2}}}`
	var event apicompat.ResponsesStreamEvent
	require.NoError(t, json.Unmarshal([]byte(payload), &event))
	var counter openAINativeSearchCounter
	counter.observeConverted(payload, &event)
	require.Equal(t, 2, counter.count())
}

func TestWrappedFailureCannotDiscountEarlierSearch(t *testing.T) {
	const done = "data: " + `{"type":"response.output_item.done","item":{"id":"a","type":"web_search_call","status":"completed"}}` + "\n\n"
	for _, terminal := range []string{
		`{"type":"response.completed","response":{"status":"failed"},"tool_usage":{"web_search":{"num_requests":0}}}`,
		`{"type":"response.completed","data":{"type":"response.failed","response":{"tool_usage":{"web_search":{"num_requests":0}}}}}`,
	} {
		require.Equal(t, 1, countOpenAINativeSearchSSE(done+"data: "+terminal+"\n\n"))
	}
}

func TestConvertedSearchProtocolWordsInSeparateTextParts(t *testing.T) {
	const output = `[{"type":"message","content":[{"type":"output_text","text":"data"},{"type":"output_text","text":"web_search_call"}]}]`
	for _, fields := range []string{`"response":{"output":` + output + "}", `"output":` + output} {
		payload := `{"type":"response.completed","usage":{"input_tokens":2,"output_tokens":1},` + fields + "}"
		var event apicompat.ResponsesStreamEvent
		require.NoError(t, json.Unmarshal([]byte(payload), &event))
		var counter openAINativeSearchCounter
		require.Zero(t, testing.AllocsPerRun(32, func() { counter.observeConverted(payload, &event) }))
		require.Zero(t, counter.count())
	}
}
