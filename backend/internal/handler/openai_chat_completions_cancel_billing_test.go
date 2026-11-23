//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type chatCancelBillingRepo struct {
	compatStreamUsageBillingRepo
}

func (r *chatCancelBillingRepo) ApplyWithUsageLog(ctx context.Context, cmd *service.UsageBillingCommand, _ *service.UsageLog) (*service.UsageBillingApplyResult, error) {
	return r.Apply(ctx, cmd)
}

type chatCancelBillingReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r chatCancelBillingReader) Read(p []byte) (int, error) {
	r.cancel()
	return r.Reader.Read(p)
}

type chatCancelBillingWriter struct {
	gin.ResponseWriter
	ctx context.Context
}

func (w chatCancelBillingWriter) WriteString(s string) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.WriteString(s)
}

const openAIChatStreamBillingRequest = `{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`

func TestOpenAIChatCompletions_RawStreamCancelBilling(t *testing.T) {
	const content = `data: {"id":"chatcmpl_cancel","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n"
	const usage = `data: {"id":"chatcmpl_cancel","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":21,"completion_tokens":3,"total_tokens":24}}` + "\n\n"
	const zeroUsage = `data: {"id":"chatcmpl_cancel","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}` + "\n\n"
	for _, tc := range []struct {
		name       string
		before     string
		after      string
		queuePing  bool
		wantDebits int
		wantInput  int
		wantOutput int
		wantCost   float64
	}{
		{name: "delivered_without_usage", before: content, wantDebits: 1, wantInput: 6, wantOutput: 1, wantCost: 0.0000300},
		{name: "usage_after_cancel", before: content, after: usage, wantDebits: 1, wantInput: 21, wantOutput: 3, wantCost: 0.0000975},
		{name: "zero_usage_after_cancel", before: content, after: zeroUsage, wantDebits: 1, wantInput: 6, wantOutput: 1, wantCost: 0.0000300},
		{name: "before_first_byte"},
		{name: "after_ping_before_content", after: content, queuePing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var tail io.Reader = cancelOnReadErr{err: context.Canceled, cancel: cancel}
			if tc.after != "" {
				tail = chatCancelBillingReader{Reader: strings.NewReader(tc.after), cancel: cancel}
			}
			reader := io.MultiReader(strings.NewReader(tc.before), tail)
			ledger := &chatCancelBillingRepo{}
			rec, upstream := serveOpenAIChatUpstreamStreamBilling(t, ctx, "/v1/chat/completions", openAIChatStreamBillingRequest, reader, ledger, tc.queuePing)

			require.ErrorIs(t, ctx.Err(), context.Canceled)
			require.Equal(t, 1, upstream.calls())
			wantBody := tc.before
			if tc.queuePing {
				wantBody = ":\n\n" + wantBody
			}
			require.Equal(t, wantBody, rec.Body.String())
			require.Len(t, ledger.commands, tc.wantDebits)
			if tc.wantDebits != 0 {
				cmd := ledger.commands[0]
				require.Equal(t, tc.wantInput, cmd.InputTokens)
				require.Equal(t, tc.wantOutput, cmd.OutputTokens)
				require.InDelta(t, tc.wantCost, cmd.BalanceCost, 1e-12)
			}
		})
	}
}

func serveOpenAIChatUpstreamStreamBilling(t *testing.T, ctx context.Context, path, body string, upstreamBody io.Reader, ledger service.UsageBillingRepository, queuePing bool) (*httptest.ResponseRecorder, *preContentFailoverUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	groupID := int64(9101)
	accounts := preContentAccounts(1)
	accounts[0].Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
	repo := &grokCredentialHandlerRepo{accounts: accounts, missingOnGet: map[int64]bool{}}
	upstream := &preContentFailoverUpstream{respond: func(int) (*http.Response, error) {
		return preContentSSEResponse(io.NopCloser(upstreamBody)), nil
	}}
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billingCache := service.NewBillingCacheService(&messagesProbeCache{newHandlerInflightCache(100)}, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		repo, &fakeFailureSinkUsageLogRepo{}, ledger, &fakeFailureSinkUserRepo{}, &fakeFailureSinkSubRepo{}, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billingCache, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	apiKey := &service.APIKey{
		ID: 9102, UserID: 9103, GroupID: &groupID,
		User:  &service.User{ID: 9103, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, RateMultiplier: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
	c.Writer = chatCancelBillingWriter{ResponseWriter: c.Writer, ctx: ctx}
	if queuePing {
		_, err := c.Writer.WriteString(":\n\n")
		require.NoError(t, err)
		c.Writer.Flush()
	}

	switch path {
	case "/v1/messages":
		h.Messages(c)
	case "/v1/responses":
		h.Responses(c)
	case "/v1/chat/completions":
		h.ChatCompletions(c)
	default:
		t.Fatalf("unexpected route %q", path)
	}
	return rec, upstream
}

func TestOpenAIChatUpstreamConversionStreamBillsLastPositiveUsage(t *testing.T) {
	for _, route := range []struct{ path, body string }{
		{"/v1/messages", openAIChatStreamBillingRequest},
		{"/v1/responses", `{"model":"gpt-5.4","input":"hello","stream":true}`},
	} {
		for _, tc := range []struct {
			name, finishUsage     string
			wantInput, wantOutput int
			wantCost              float64
		}{
			{"zero finish", `{"prompt_tokens":0,"completion_tokens":0}`, 5, 3, 0.00005750},
			{"positive finish", `{"prompt_tokens":9,"completion_tokens":4}`, 9, 4, 0.00008250},
		} {
			t.Run(route.path+"/"+tc.name, func(t *testing.T) {
				sse := `data: {"id":"chatcmpl_convert","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" +
					`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}` + "\n\n" +
					`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":` + tc.finishUsage + "}\n\n" +
					"data: [DONE]\n\n"
				ledger := &chatCancelBillingRepo{}
				rec, upstream := serveOpenAIChatUpstreamStreamBilling(t, t.Context(), route.path, route.body, strings.NewReader(sse), ledger, false)

				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, 1, upstream.calls())
				require.Len(t, ledger.commands, 1)
				cmd := ledger.commands[0]
				require.Equal(t, tc.wantInput, cmd.InputTokens)
				require.Equal(t, tc.wantOutput, cmd.OutputTokens)
				require.InDelta(t, tc.wantCost, cmd.BalanceCost, 1e-12)
			})
		}
	}
}

func TestOpenAIChatCompletions_StreamBillsDeliveredContentBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, content         string
		wantInput, wantOutput int
		wantCost              float64
	}{
		{"text block", `[{"type":"text","text":"hi"}]`, 6, 1, 0.00003000},
		{"empty array", `[]`, 0, 0, 0},
		{"empty block", `[{}]`, 0, 0, 0},
		{"empty text", `[{"type":"text","text":""}]`, 0, 0, 0},
		{"object", `{}`, 0, 0, 0},
		{"number", `12`, 0, 0, 0},
		{"boolean", `true`, 0, 0, 0},
		{"null", `null`, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sse := `data: {"id":"chatcmpl_blocks","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":` + tc.content + `}}]}` + "\n\n" +
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n"
			ledger := &chatCancelBillingRepo{}
			rec, upstream := serveOpenAIChatUpstreamStreamBilling(t, t.Context(), "/v1/chat/completions", openAIChatStreamBillingRequest, strings.NewReader(sse), ledger, false)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, sse, rec.Body.String())
			require.Equal(t, 1, upstream.calls())
			require.Len(t, ledger.commands, 1)
			cmd := ledger.commands[0]
			require.Equal(t, tc.wantInput, cmd.InputTokens)
			require.Equal(t, tc.wantOutput, cmd.OutputTokens)
			require.InDelta(t, tc.wantCost, cmd.BalanceCost, 1e-12)
		})
	}
}
