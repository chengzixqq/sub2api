package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorWSHooks_TerminalAndPartialSnapshots(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, complete := range []bool{false, true} {
			name := mode + "/partial"
			if complete {
				name = mode + "/complete"
			}
			t.Run(name, func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Security.URLAllowlist.Enabled = false
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.APIKeyEnabled = true, true
				cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2, cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true, true
				cfg.Gateway.OpenAIWS.IngressModeDefault = mode
				cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
				cfg.Gateway.OpenAIWS.ReadTimeoutSeconds, cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2, 2
				upstream := &openAIWSCaptureConn{events: [][]byte{
					[]byte(`{"type":"response.created","response":{"id":"resp_monitor","model":"gpt-test","usage":{"input_tokens":8,"output_tokens":3}}}`),
					[]byte(`{"type":"response.output_text.delta","response_id":"resp_monitor","delta":"x"}`),
				}}
				if complete {
					upstream.events = append(upstream.events, []byte(`{"type":"response.completed","response":{"id":"resp_monitor","model":"gpt-test","usage":{"input_tokens":8,"output_tokens":4}}}`))
				}
				dialer := &openAIWSCaptureDialer{conn: upstream}
				pool := newOpenAIWSConnPool(cfg)
				pool.setClientDialerForTest(dialer)
				defer pool.Close()
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, openaiWSPassthroughDialer: dialer}
				account := &Account{ID: 991, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_mode": mode}}
				type observation struct {
					turn   int
					result *OpenAIForwardResult
					err    error
				}
				observed := make(chan observation, 8)
				billed := make(chan observation, 8)
				starts := make(chan int, 8)
				hooks := &OpenAIWSIngressHooks{
					ObserveTurnStart:      func(turn int, at time.Time, model string) { starts <- turn },
					ObserveMonitorAttempt: func(turn int, result *OpenAIForwardResult, err error) { observed <- observation{turn, result, err} },
					AfterTurn:             func(turn int, result *OpenAIForwardResult, err error) { billed <- observation{turn, result, err} },
				}
				done := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						done <- err
						return
					}
					defer func() { _ = conn.CloseNow() }()
					ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
					defer cancel()
					_, first, err := conn.Read(ctx)
					if err != nil {
						done <- err
						return
					}
					gc, _ := gin.CreateTestContext(httptest.NewRecorder())
					gc.Request = r
					done <- svc.ProxyResponsesWebSocketFromClient(ctx, gc, conn, account, "test", first, hooks)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
				require.NoError(t, err)
				defer func() { _ = client.CloseNow() }()
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-test","stream":true}`)))
				for i := 0; i < 2; i++ {
					_, _, err = client.Read(ctx)
					require.NoError(t, err)
				}
				if complete {
					_, _, err = client.Read(ctx)
					require.NoError(t, err)
					_ = client.Close(coderws.StatusNormalClosure, "done")
				} else {
					_, _, err = client.Read(ctx)
					require.Error(t, err)
				}
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("websocket did not finish")
				}
				require.NotEmpty(t, starts)
				require.NotEmpty(t, observed)
				got := <-observed
				require.Equal(t, 1, got.turn)
				require.NotNil(t, got.result)
				require.Equal(t, 8, got.result.Usage.InputTokens)
				if complete {
					require.Equal(t, 4, got.result.Usage.OutputTokens)
					require.Equal(t, "response.completed", got.result.UpstreamTerminalEvent)
				} else {
					require.Equal(t, 3, got.result.Usage.OutputTokens)
					require.Empty(t, got.result.UpstreamTerminalEvent)
					require.NotEmpty(t, billed)
					require.Nil(t, (<-billed).result, "monitor snapshot must not change billing callback")
				}
			})
		}
	}
}
