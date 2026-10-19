//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestComputeAccountTestMarked(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t0.Add(time.Hour)

	cases := []struct {
		name   string
		before Account
		after  Account
		want   AccountTestResultMarked
	}{
		{"unchanged", Account{Status: StatusActive}, Account{Status: StatusActive}, AccountTestResultMarked{}},
		{"active to error", Account{Status: StatusActive}, Account{Status: StatusError, ErrorMessage: "x", ErrorAt: &t1}, AccountTestResultMarked{Error: true}},
		{"error rewritten", Account{Status: StatusError, ErrorMessage: "x", ErrorAt: &t0}, Account{Status: StatusError, ErrorMessage: "x", ErrorAt: &t1}, AccountTestResultMarked{Error: true}},
		{"error untouched", Account{Status: StatusError, ErrorMessage: "x", ErrorAt: &t0}, Account{Status: StatusError, ErrorMessage: "x", ErrorAt: &t0}, AccountTestResultMarked{}},
		{"recovered", Account{Status: StatusError, ErrorMessage: "x", ErrorAt: &t0, RateLimitResetAt: &t2}, Account{Status: StatusActive}, AccountTestResultMarked{}},
		{"rate limit set", Account{Status: StatusActive}, Account{Status: StatusActive, RateLimitedAt: &t1, RateLimitResetAt: &t2}, AccountTestResultMarked{RateLimited: true}},
		{"rate limit pushed", Account{Status: StatusActive, RateLimitResetAt: &t1}, Account{Status: StatusActive, RateLimitResetAt: &t2}, AccountTestResultMarked{RateLimited: true}},
		{"rate limit kept", Account{Status: StatusActive, RateLimitedAt: &t0, RateLimitResetAt: &t2}, Account{Status: StatusActive, RateLimitedAt: &t0, RateLimitResetAt: &t2}, AccountTestResultMarked{}},
		{"temp unsched set", Account{Status: StatusActive}, Account{Status: StatusActive, TempUnschedulableUntil: &t2, TempUnschedulableReason: "402"}, AccountTestResultMarked{TempUnschedulable: true}},
		{"temp unsched pushed", Account{Status: StatusActive, TempUnschedulableUntil: &t1}, Account{Status: StatusActive, TempUnschedulableUntil: &t2}, AccountTestResultMarked{TempUnschedulable: true}},
		{"temp unsched reason changed", Account{Status: StatusActive, TempUnschedulableUntil: &t2, TempUnschedulableReason: "a"}, Account{Status: StatusActive, TempUnschedulableUntil: &t2, TempUnschedulableReason: "b"}, AccountTestResultMarked{TempUnschedulable: true}},
		{"temp unsched kept", Account{Status: StatusActive, TempUnschedulableUntil: &t2, TempUnschedulableReason: "a"}, Account{Status: StatusActive, TempUnschedulableUntil: &t2, TempUnschedulableReason: "a"}, AccountTestResultMarked{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, after := tc.before, tc.after
			require.Equal(t, tc.want, computeAccountTestMarked(&before, &after))
		})
	}
}

func TestAccountTestResultJSON_FailureKeepsDataSuccessFalse(t *testing.T) {
	c, _ := newTestContext()
	beginAccountTestTrace(c)
	markAccountTestTraceFinished(c)

	recovery := &SuccessfulTestRecoveryResult{ClearedError: true}
	before := &Account{Status: StatusActive, Schedulable: true}
	after := &Account{Status: StatusError, ErrorMessage: "Authentication failed (401): bad", Schedulable: false}
	result := BuildAccountTestResult(c, false, before, after, recovery)

	raw, err := json.Marshal(TestEvent{Type: "test_result", Success: result.Success, Data: result})
	require.NoError(t, err)
	var event map[string]any
	require.NoError(t, json.Unmarshal(raw, &event))
	require.Equal(t, "test_result", event["type"])
	require.NotContains(t, event, "success")

	data := event["data"].(map[string]any)
	require.Contains(t, data, "success")
	require.Equal(t, false, data["success"])
	require.Nil(t, data["recovery"])
	require.NotContains(t, data, "usage")
	require.Contains(t, data, "first_token_ms")
	require.Nil(t, data["first_token_ms"])
	require.Contains(t, data, "upstream_status")
	require.Nil(t, data["upstream_status"])
	require.NotEmpty(t, data["started_at"])

	account := data["account"].(map[string]any)
	require.Equal(t, StatusError, account["status"])
	require.Equal(t, false, account["schedulable"])
	require.Equal(t, AccountErrorKindAuth, account["error_kind"])
	require.Contains(t, account["error_summary"], "Authentication failed (401)")
	require.Contains(t, account, "rate_limit_reset_at")
	require.Nil(t, account["rate_limit_reset_at"])

	marked := data["marked"].(map[string]any)
	require.Equal(t, true, marked["error"])
	require.Equal(t, false, marked["rate_limited"])
	require.Equal(t, false, marked["temp_unschedulable"])
}

func TestAccountTestResult_NilSnapshotsAndRecoveryOnSuccess(t *testing.T) {
	c, _ := newTestContext()
	beginAccountTestTrace(c)

	result := BuildAccountTestResult(c, true, nil, nil, &SuccessfulTestRecoveryResult{ClearedRateLimit: true})
	require.True(t, result.Success)
	require.Nil(t, result.Account)
	require.Nil(t, result.Marked)
	require.NotNil(t, result.Recovery)
	require.True(t, result.Recovery.ClearedRateLimit)

	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"account":null`)
	require.Contains(t, string(raw), `"marked":null`)
	require.Contains(t, string(raw), `"recovery":{"cleared_error":false,"cleared_rate_limit":true}`)
	require.NotContains(t, string(raw), `"error_kind"`)
}

func TestAccountTestTrace_ClaudeStreamRecordsStatusFirstTokenAndUsage(t *testing.T) {
	c, recorder := newTestContext()
	account := &Account{
		ID:          41,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-ant-test"},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	resp := newJSONResponse(http.StatusOK, strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":38}}`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n\n"))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}

	require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault))

	body := recorder.Body.String()
	require.Contains(t, body, `"type":"test_start"`)
	require.Contains(t, body, `"type":"content"`)
	require.Contains(t, body, `"type":"test_complete"`)
	require.NotContains(t, body, `"type":"test_result"`, "test_result is written by the handler, not the service")

	snapshot, ok := AccountTestTraceFrom(c)
	require.True(t, ok)
	require.Equal(t, "hello", snapshot.ResponseText)
	require.NotEmpty(t, snapshot.Model)

	result := BuildAccountTestResult(c, true, nil, nil, nil)
	require.NotNil(t, result.UpstreamStatus)
	require.Equal(t, http.StatusOK, *result.UpstreamStatus)
	require.Equal(t, &AccountTestUsage{InputTokens: 12, OutputTokens: 38}, result.Usage)
	require.NotNil(t, result.FirstTokenMs)
	require.LessOrEqual(t, *result.FirstTokenMs, result.TotalMs)
	require.GreaterOrEqual(t, result.TotalMs, int64(0))
	require.False(t, result.StartedAt.IsZero())
}

func TestAccountTestTrace_OpenAI401RecordsStatusAndSetsError(t *testing.T) {
	c, _ := newTestContext()
	account := &Account{
		ID:          80,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(http.StatusUnauthorized, `{"error":"bad token"}`)}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}

	require.Error(t, svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeDefault))
	require.Equal(t, account.ID, repo.setErrorID)

	now := time.Now()
	before := &Account{Status: StatusActive}
	after := &Account{Status: StatusError, ErrorMessage: repo.setErrorMsg, ErrorAt: &now}
	result := BuildAccountTestResult(c, false, before, after, nil)
	require.NotNil(t, result.UpstreamStatus)
	require.Equal(t, http.StatusUnauthorized, *result.UpstreamStatus)
	require.Nil(t, result.FirstTokenMs)
	require.Nil(t, result.Recovery)
	require.Equal(t, &AccountTestResultMarked{Error: true}, result.Marked)
	snapshot, _ := AccountTestTraceFrom(c)
	require.Contains(t, snapshot.ErrorMessage, "API returned 401")
}

func TestAccountTestTrace_UpstreamStatusFallbackFromErrorText(t *testing.T) {
	c, _ := newTestContext()
	beginAccountTestTrace(c)
	_ = (&AccountTestService{}).sendErrorAndEnd(c, "Grok TTS returned 503: x")
	snapshot, ok := AccountTestTraceFrom(c)
	require.True(t, ok)
	require.NotNil(t, snapshot.UpstreamStatus)
	require.Equal(t, 503, *snapshot.UpstreamStatus)

	c, _ = newTestContext()
	beginAccountTestTrace(c)
	noteAccountTestUpstreamStatus(c, http.StatusOK)
	_ = (&AccountTestService{}).sendErrorAndEnd(c, "Stream ended before response.completed (500)")
	snapshot, _ = AccountTestTraceFrom(c)
	require.Equal(t, http.StatusOK, *snapshot.UpstreamStatus, "an explicitly recorded status wins over the text fallback")

	// Without a trace (other callers of the helpers) nothing panics.
	c, _ = newTestContext()
	_ = (&AccountTestService{}).sendErrorAndEnd(c, "API returned 401: x")
	noteAccountTestUpstreamStatus(c, http.StatusOK)
	_, ok = AccountTestTraceFrom(c)
	require.False(t, ok)
}

func TestAccountTestUpstreamStatusParsing(t *testing.T) {
	cases := map[string]*int{
		"API returned 401: bad":                           intPtrForTraceTest(401),
		"Grok Realtime WS handshake failed (HTTP 403): x": intPtrForTraceTest(403),
		"上游 API 返回 429":                                   intPtrForTraceTest(429),
		"Authentication failed (500)":                     intPtrForTraceTest(500),
		"upstream returned 999":                           nil,
		"Request failed: dial tcp: timeout":               nil,
		"returned 4012 rows":                              nil,
	}
	for message, want := range cases {
		require.Equal(t, want, accountTestUpstreamStatus(0, message), message)
	}
	require.Equal(t, intPtrForTraceTest(200), accountTestUpstreamStatus(200, "API returned 401"))
	require.Equal(t, intPtrForTraceTest(401), accountTestUpstreamStatus(700, "API returned 401"))
}

func TestAccountTestTrace_ResponsesChatAndGeminiUsage(t *testing.T) {
	svc := &AccountTestService{}

	c, _ := newTestContext()
	beginAccountTestTrace(c)
	require.NoError(t, svc.processOpenAIStream(c, strings.NewReader(
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":7}}}\n\n")))
	snapshot, _ := AccountTestTraceFrom(c)
	require.Equal(t, &AccountTestUsage{InputTokens: 5, OutputTokens: 7}, snapshot.Usage)
	require.NotNil(t, snapshot.FirstOutputAt)

	c, _ = newTestContext()
	beginAccountTestTrace(c)
	require.NoError(t, svc.processOpenAIChatCompletionsStream(c, strings.NewReader(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n"+
			"data: [DONE]\n\n")))
	snapshot, _ = AccountTestTraceFrom(c)
	require.Equal(t, &AccountTestUsage{InputTokens: 3, OutputTokens: 4}, snapshot.Usage)

	c, _ = newTestContext()
	beginAccountTestTrace(c)
	require.NoError(t, svc.processOpenAIChatCompletionsStream(c, strings.NewReader(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")))
	snapshot, _ = AccountTestTraceFrom(c)
	require.Nil(t, snapshot.Usage, "chat completions without upstream usage leaves usage absent")

	c, _ = newTestContext()
	beginAccountTestTrace(c)
	require.NoError(t, svc.processGeminiStream(c, strings.NewReader(
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"o\"}]}}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\n\n"+
			"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"k\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":6}}}\n\n")))
	snapshot, _ = AccountTestTraceFrom(c)
	require.Equal(t, &AccountTestUsage{InputTokens: 2, OutputTokens: 6}, snapshot.Usage)
}

func TestAccountTestTrace_UsageAccumulatesAcrossEndpoints(t *testing.T) {
	c, _ := newTestContext()
	beginAccountTestTrace(c)
	addAccountTestUsage(c, 3, 4)
	addAccountTestUsage(c, 10, 20)
	snapshot, _ := AccountTestTraceFrom(c)
	require.Equal(t, &AccountTestUsage{InputTokens: 13, OutputTokens: 24}, snapshot.Usage)
}

func TestParseTestSSEOutput_IgnoresTestResult(t *testing.T) {
	base := "data: {\"type\":\"test_start\",\"model\":\"m\"}\n\n" +
		"data: {\"type\":\"content\",\"text\":\"hello\"}\n\n" +
		"data: {\"type\":\"error\",\"error\":\"API returned 401: x\"}\n\n"
	c, _ := newTestContext()
	beginAccountTestTrace(c)
	raw, err := json.Marshal(TestEvent{Type: "test_result", Data: BuildAccountTestResult(c, false, nil, nil, nil)})
	require.NoError(t, err)
	withResult := base + "data: " + string(raw) + "\n\n"

	text1, err1 := parseTestSSEOutput(base)
	text2, err2 := parseTestSSEOutput(withResult)
	require.Equal(t, text1, text2)
	require.Equal(t, err1, err2)
	require.Equal(t, "hello", text2)
	require.Equal(t, "API returned 401: x", err2)
}

func intPtrForTraceTest(v int) *int { return &v }
