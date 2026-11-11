package openai_ws_v2

import (
	"context"
	"fmt"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestRelay_CacheWriteTTLSnapshots(t *testing.T) {
	const initial = `{"type":"response.in_progress","response":{"id":"resp_ttl","usage":{"input_tokens":100,"output_tokens":1,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":80}}}}}`
	for _, tc := range []struct {
		name, details           string
		total, want30m, want12h int
	}{
		{"missing split preserves known TTL", ``, 100, 20, 80},
		{"new aggregate drops old split", ``, 120, 0, 0},
		{"explicit new split replaces old split", `,"cache_write_tokens_details":{"ttl_30m_tokens":70,"ttl_12h_tokens":30}`, 100, 70, 30},
		{"partial split uses aggregate price", `,"cache_write_tokens_details":{"ttl_12h_tokens":80}`, 100, 0, 0},
		{"inconsistent split uses aggregate price", `,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":70}`, 100, 0, 0},
	} {
		for _, terminal := range []string{"response.completed", "response.failed"} {
			t.Run(tc.name+"/"+terminal, func(t *testing.T) {
				end := fmt.Sprintf(`{"type":%q,"response":{"id":"resp_ttl","usage":{"input_tokens":%d,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":%d%s}}}}`, terminal, tc.total, tc.total, tc.details)
				upstream := newPassthroughTestFrameConn([]passthroughTestFrame{
					{msgType: coderws.MessageText, payload: []byte(initial)},
					{msgType: coderws.MessageText, payload: []byte(initial)},
					{msgType: coderws.MessageText, payload: []byte(end)},
				}, true)
				client := newPassthroughTestFrameConn(nil, false)
				var turns []RelayTurnResult
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				result, _ := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"gpt-6-luna"}`), RelayOptions{
					OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) },
				})
				require.Len(t, turns, 1)
				for _, usage := range []Usage{turns[0].Usage, result.Usage} {
					require.Equal(t, tc.total, usage.CacheCreationInputTokens)
					require.Equal(t, tc.want30m, usage.CacheCreation30mInputTokens)
					require.Equal(t, tc.want12h, usage.CacheCreation12hInputTokens)
				}
			})
		}
	}
}

func TestRelay_CacheWriteTTLIncompleteTurn(t *testing.T) {
	upstream := newPassthroughTestFrameConn([]passthroughTestFrame{{msgType: coderws.MessageText, payload: []byte(`{"type":"response.in_progress","response":{"id":"resp_partial","usage":{"input_tokens":100,"output_tokens":1,"input_tokens_details":{"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":80}}}}}`)}}, true)
	client := newPassthroughTestFrameConn(nil, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, _ := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"gpt-6-luna"}`), RelayOptions{})
	require.NotNil(t, result.IncompleteTurn)
	require.Equal(t, 100, result.IncompleteTurn.Usage.CacheCreationInputTokens)
	require.Equal(t, 20, result.IncompleteTurn.Usage.CacheCreation30mInputTokens)
	require.Equal(t, 80, result.IncompleteTurn.Usage.CacheCreation12hInputTokens)
}
