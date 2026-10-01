//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// swapMonitorHTTPClient 临时替换 monitorHTTPClient 为不带 SSRF 校验的普通 client，
// 让 httptest (127.0.0.1) 能连通。测试结束后恢复。
func swapMonitorHTTPClient(t *testing.T) {
	t.Helper()
	orig := monitorHTTPClient
	monitorHTTPClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { monitorHTTPClient = orig })
}

// captureHandler 把每次收到的请求 body 和 headers 存起来，测试断言用。
type captureHandler struct {
	mu          sync.Mutex
	lastBody    map[string]any
	lastHeaders http.Header
	respondText string // 写到 Anthropic content[0].text 里（校验用）
	status      int
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var parsed map[string]any
	_ = json.NewDecoder(r.Body).Decode(&parsed)
	h.mu.Lock()
	h.lastHeaders = r.Header.Clone()
	h.lastBody = parsed
	h.mu.Unlock()

	status := h.status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// 构造 Anthropic 格式的响应：content[0].text = h.respondText
	_ = json.NewEncoder(w).Encode(map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": h.respondText},
		},
	})
}

type monitorCapturedRequest struct {
	lastBody    map[string]any
	lastHeaders http.Header
	lastPath    string
}

func (h *captureHandler) snapshot() monitorCapturedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Decoded request maps are immutable after publication. Each request gets
	// a new map, so a snapshot remains stable while another handler runs.
	return monitorCapturedRequest{lastBody: h.lastBody, lastHeaders: h.lastHeaders.Clone()}
}

func setupFakeAnthropic(t *testing.T, handler *captureHandler) string {
	t.Helper()
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

type openAICaptureHandler struct {
	mu                        sync.Mutex
	lastBody                  map[string]any
	lastHeaders               http.Header
	lastPath                  string
	status                    int
	rawResponse               string
	responsesLeadingReasoning bool
}

func (h *openAICaptureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var parsed map[string]any
	_ = json.NewDecoder(r.Body).Decode(&parsed)
	h.mu.Lock()
	h.lastHeaders = r.Header.Clone()
	h.lastPath = r.URL.Path
	h.lastBody = parsed
	h.mu.Unlock()

	status := h.status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if h.rawResponse != "" {
		_, _ = w.Write([]byte(h.rawResponse))
		return
	}

	answer := answerFromOpenAIRequest(parsed)
	if r.URL.Path == providerOpenAIResponsesPath {
		output := []map[string]any{}
		if h.responsesLeadingReasoning {
			output = append(output, map[string]any{
				"type":    "reasoning",
				"summary": []any{},
			})
		}
		output = append(output, map[string]any{
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": answer},
			},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": output,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"content": answer}}},
	})
}

func (h *openAICaptureHandler) snapshot() monitorCapturedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return monitorCapturedRequest{lastBody: h.lastBody, lastHeaders: h.lastHeaders.Clone(), lastPath: h.lastPath}
}

func setupFakeOpenAI(t *testing.T, handler *openAICaptureHandler) string {
	t.Helper()
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func answerFromOpenAIRequest(body map[string]any) string {
	prompt, _ := body["input"].(string)
	if prompt == "" {
		if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
			if msg, ok := messages[0].(map[string]any); ok {
				prompt, _ = msg["content"].(string)
			}
		}
	}
	return answerFromChallengePrompt(prompt)
}

var challengeQuestionRegex = regexp.MustCompile(`Q: (\d+) ([+-]) (\d+) = \?\nA:$`)

func answerFromChallengePrompt(prompt string) string {
	m := challengeQuestionRegex.FindStringSubmatch(prompt)
	if len(m) != 4 {
		return "0"
	}
	left, _ := strconv.Atoi(m[1])
	right, _ := strconv.Atoi(m[3])
	if m[2] == "+" {
		return strconv.Itoa(left + right)
	}
	return strconv.Itoa(left - right)
}

func TestRunCheckForModel_OffMode_PreservesDefaultBody(t *testing.T) {
	h := &captureHandler{respondText: "the answer is 42"}
	endpoint := setupFakeAnthropic(t, h)

	// 跑一次 off 模式（opts=nil），确认默认 body 行为未变
	_ = runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", nil)

	captured := h.snapshot()
	if captured.lastBody["model"] != "claude-x" {
		t.Errorf("default body should contain model=claude-x, got %v", captured.lastBody["model"])
	}
	if _, ok := captured.lastBody["messages"]; !ok {
		t.Error("default body should contain messages")
	}
	if captured.lastHeaders.Get("x-api-key") != "sk-fake" {
		t.Errorf("expected adapter's x-api-key header, got %q", captured.lastHeaders.Get("x-api-key"))
	}
}

func TestRunCheckForModel_OpenAI_DefaultChatRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", nil)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("default chat request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	captured := h.snapshot()
	if captured.lastPath != providerOpenAIPath {
		t.Fatalf("expected chat completions path %q, got %q", providerOpenAIPath, captured.lastPath)
	}
	if captured.lastBody["model"] != "gpt-test" {
		t.Errorf("chat body should contain model=gpt-test, got %v", captured.lastBody["model"])
	}
	if _, ok := captured.lastBody["messages"]; !ok {
		t.Error("chat body should contain messages")
	}
	if _, ok := captured.lastBody["instructions"]; ok {
		t.Error("chat body must not contain top-level instructions")
	}
	if captured.lastBody["stream"] != false {
		t.Errorf("chat body should set stream=false, got %v", captured.lastBody["stream"])
	}
	if captured.lastHeaders.Get("Authorization") != "Bearer sk-openai" {
		t.Errorf("expected bearer auth header, got %q", captured.lastHeaders.Get("Authorization"))
	}
}

func TestRunCheckForModel_OpenCodeGo_DefaultChatRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenCodeGo, endpoint, "opencode-key", "gpt-5.3-codex", nil)
	if res.Status != MonitorStatusOperational {
		t.Fatalf("OpenCode Go request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	captured := h.snapshot()
	if captured.lastPath != providerOpenAIPath {
		t.Fatalf("expected OpenCode Go Chat Completions path %q, got %q", providerOpenAIPath, captured.lastPath)
	}
	if captured.lastBody["model"] != "gpt-5.3-codex" {
		t.Errorf("OpenCode Go body should contain model, got %v", captured.lastBody["model"])
	}
	if captured.lastHeaders.Get("Authorization") != "Bearer opencode-key" {
		t.Errorf("expected OpenCode Go bearer auth header, got %q", captured.lastHeaders.Get("Authorization"))
	}
}

func TestRunCheckForModel_OpenCodeGo_ResponsesRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenCodeGo, endpoint, "opencode-key", "gpt-5.3-codex", &CheckOptions{APIMode: MonitorAPIModeResponses})
	if res.Status != MonitorStatusOperational {
		t.Fatalf("OpenCode Go Responses request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	captured := h.snapshot()
	if captured.lastPath != providerOpenAIResponsesPath {
		t.Fatalf("expected OpenCode Go Responses path %q, got %q", providerOpenAIResponsesPath, captured.lastPath)
	}
	if _, ok := captured.lastBody["instructions"]; !ok {
		t.Error("OpenCode Go Responses body should contain instructions")
	}
	if _, ok := captured.lastBody["input"]; !ok {
		t.Error("OpenCode Go Responses body should contain input")
	}
}

func TestOpenCodeGoResponseBodyMergeProtectsRoutingFields(t *testing.T) {
	body, err := buildRequestBody(
		providerOpenAIResponsesAdapter,
		MonitorProviderOpenCodeGo,
		MonitorAPIModeResponses,
		"gpt-5.3-codex",
		"Q: 1 + 2 = ?\nA:",
		&CheckOptions{BodyOverrideMode: MonitorBodyOverrideModeMerge, BodyOverride: map[string]any{
			"model": "attacker-model", "instructions": "", "input": "", "stream": true,
		}},
	)
	if err != nil {
		t.Fatalf("merge body should succeed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode merged body: %v", err)
	}
	if decoded["model"] != "gpt-5.3-codex" || decoded["stream"] != false {
		t.Fatalf("routing fields must remain protected: %#v", decoded)
	}
}

func TestGrokMonitorConfiguration(t *testing.T) {
	if err := validateProvider(MonitorProviderGrok); err != nil {
		t.Fatalf("grok provider should be supported: %v", err)
	}
	if got := normalizeMonitorPrimaryModel(MonitorProviderGrok, MonitorCheckModeProbe, ""); got != MonitorDefaultGrokModel {
		t.Fatalf("expected default Grok model %q, got %q", MonitorDefaultGrokModel, got)
	}
	if err := validateAPIMode(MonitorProviderGrok, MonitorAPIModeChatCompletions); err != nil {
		t.Fatalf("grok chat_completions mode should be valid: %v", err)
	}
	if err := validateAPIMode(MonitorProviderGrok, MonitorAPIModeResponses); err == nil {
		t.Fatal("grok responses mode should be rejected by channel monitoring")
	}
	if err := validateReplaceRequestBody(MonitorProviderGrok, MonitorAPIModeChatCompletions, map[string]any{}); err == nil {
		t.Fatal("grok replace-mode body should require messages")
	}
}

func TestRunCheckForModel_Grok_DefaultChatRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "xai-key", MonitorDefaultGrokModel, nil)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("Grok request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	if res.LatencyMs == nil {
		t.Fatal("Grok request should record latency")
	}
	captured := h.snapshot()
	if captured.lastPath != providerGrokPath {
		t.Fatalf("expected Grok chat completions path %q, got %q", providerGrokPath, captured.lastPath)
	}
	if captured.lastBody["model"] != MonitorDefaultGrokModel {
		t.Errorf("Grok body should contain model=%s, got %v", MonitorDefaultGrokModel, captured.lastBody["model"])
	}
	if _, ok := captured.lastBody["messages"]; !ok {
		t.Error("Grok body should contain messages")
	}
	if captured.lastBody["stream"] != false {
		t.Errorf("Grok body should set stream=false, got %v", captured.lastBody["stream"])
	}
	if captured.lastHeaders.Get("Authorization") != "Bearer xai-key" {
		t.Errorf("expected Grok bearer auth header, got %q", captured.lastHeaders.Get("Authorization"))
	}
}

func TestRunCheckForModel_Grok_UpstreamFailure(t *testing.T) {
	h := &openAICaptureHandler{status: http.StatusTooManyRequests}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "xai-key", MonitorDefaultGrokModel, nil)

	if res.Status != MonitorStatusError {
		t.Fatalf("Grok 429 should be recorded as error, got status=%s message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "upstream HTTP 429") {
		t.Fatalf("Grok failure should preserve upstream status, got %q", res.Message)
	}
	if res.LatencyMs == nil {
		t.Fatal("Grok failure should still record latency")
	}
}

func TestRunCheckForModel_Grok_RedactsXAIKeyFromUpstreamBody(t *testing.T) {
	h := &openAICaptureHandler{
		status:      http.StatusUnauthorized,
		rawResponse: `{"error":{"message":"invalid API key xai-secret"}}`,
	}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "request-key", MonitorDefaultGrokModel, nil)

	if res.Status != MonitorStatusError {
		t.Fatalf("Grok upstream failure should be recorded as error, got %s", res.Status)
	}
	if strings.Contains(res.Message, "xai-secret") {
		t.Fatalf("Grok error message leaked xAI key: %q", res.Message)
	}
	if !strings.Contains(res.Message, "xai-***REDACTED***") {
		t.Fatalf("Grok error message should contain redaction marker, got %q", res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponses_DefaultRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", &CheckOptions{
		APIMode: MonitorAPIModeResponses,
	})

	if res.Status != MonitorStatusOperational {
		t.Fatalf("default responses request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	captured := h.snapshot()
	if captured.lastPath != providerOpenAIResponsesPath {
		t.Fatalf("expected responses path %q, got %q", providerOpenAIResponsesPath, captured.lastPath)
	}
	if captured.lastBody["model"] != "gpt-test" {
		t.Errorf("responses body should contain model=gpt-test, got %v", captured.lastBody["model"])
	}
	instructions, _ := captured.lastBody["instructions"].(string)
	if strings.TrimSpace(instructions) == "" {
		t.Error("responses body should contain non-empty instructions")
	}
	input, _ := captured.lastBody["input"].(string)
	if strings.TrimSpace(input) == "" {
		t.Error("responses body should contain non-empty input")
	}
	if _, ok := captured.lastBody["messages"]; ok {
		t.Error("responses body must not contain chat messages")
	}
	if captured.lastBody["stream"] != false {
		t.Errorf("responses body should set stream=false, got %v", captured.lastBody["stream"])
	}
	if captured.lastHeaders.Get("Authorization") != "Bearer sk-openai" {
		t.Errorf("expected bearer auth header, got %q", captured.lastHeaders.Get("Authorization"))
	}
}

func TestRunCheckForModel_OpenAIResponses_SkipsLeadingReasoningItem(t *testing.T) {
	h := &openAICaptureHandler{responsesLeadingReasoning: true}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-5.5", &CheckOptions{
		APIMode: MonitorAPIModeResponses,
	})

	if res.Status != MonitorStatusOperational {
		t.Fatalf("responses request should find text after leading reasoning item, got status=%s message=%q", res.Status, res.Message)
	}
	captured := h.snapshot()
	if captured.lastPath != providerOpenAIResponsesPath {
		t.Fatalf("expected responses path %q, got %q", providerOpenAIResponsesPath, captured.lastPath)
	}
}

func TestRunCheckForModel_OpenAIResponsesReplaceMissingInstructionsFailsLocally(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", &CheckOptions{
		APIMode:          MonitorAPIModeResponses,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride: map[string]any{
			"model": "gpt-test",
			"input": "hello",
		},
	})

	if res.Status != MonitorStatusError {
		t.Fatalf("invalid responses replace body should fail locally as error, got status=%s", res.Status)
	}
	if !strings.Contains(res.Message, "instructions and input are required") {
		t.Errorf("expected local validation message about instructions/input, got %q", res.Message)
	}
	captured := h.snapshot()
	if captured.lastPath != "" {
		t.Errorf("invalid replace body should fail before HTTP request, got path %q", captured.lastPath)
	}
}

func TestRunCheckForModel_MergeMode_UserFieldsWinButDenyListProtects(t *testing.T) {
	h := &captureHandler{respondText: "the answer is 42"}
	endpoint := setupFakeAnthropic(t, h)

	opts := &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeMerge,
		BodyOverride: map[string]any{
			"system":     "You are Claude Code...",
			"max_tokens": float64(999),   // 应该覆盖默认 50
			"model":      "hacked-model", // 应该被黑名单挡住，保留原 model
			"messages":   []any{},        // 同上，被挡
		},
		ExtraHeaders: map[string]string{
			"User-Agent":     "claude-cli/1.0",
			"Content-Length": "999", // 黑名单
			"x-custom":       "ok",
		},
	}
	_ = runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	captured := h.snapshot()
	if captured.lastBody["system"] != "You are Claude Code..." {
		t.Errorf("merge mode should inject system, got %v", captured.lastBody["system"])
	}
	// max_tokens 覆盖生效
	if mt, ok := captured.lastBody["max_tokens"].(float64); !ok || mt != 999 {
		t.Errorf("merge mode should override max_tokens to 999, got %v", captured.lastBody["max_tokens"])
	}
	// model 在黑名单 — 应该保留默认值
	if captured.lastBody["model"] != "claude-x" {
		t.Errorf("model should be protected by deny list, got %v", captured.lastBody["model"])
	}
	// messages 在黑名单 — 应该保留默认值（非空）
	msgs, _ := captured.lastBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Error("messages should be protected by deny list (kept default, non-empty)")
	}
	// header 合并
	if captured.lastHeaders.Get("User-Agent") != "claude-cli/1.0" {
		t.Errorf("extra User-Agent should override, got %q", captured.lastHeaders.Get("User-Agent"))
	}
	if captured.lastHeaders.Get("x-custom") != "ok" {
		t.Errorf("extra custom header should be present, got %q", captured.lastHeaders.Get("x-custom"))
	}
	// Content-Length 黑名单：会被 net/http 自动重算，但不应由用户的 "999" 决定。
	// 我们无法直接断言丢弃（http.Client 总会填上），只断言请求成功即可。
}

func TestRunCheckForModel_ReplaceMode_FullBodyUsedAndChallengeSkipped(t *testing.T) {
	// replace 模式下我们的 body 完全自定义，challenge 数学题不会出现在请求里，
	// 上游也不会回正确答案 — 但只要 2xx + 响应文本非空，就算 operational
	h := &captureHandler{respondText: "any non-empty text"}
	endpoint := setupFakeAnthropic(t, h)

	userBody := map[string]any{
		"model":      "user-forced-model",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens": float64(10),
		"system":     "You are someone else",
	}
	opts := &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     userBody,
	}
	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	// 请求 body = 用户提供的原样
	captured := h.snapshot()
	if captured.lastBody["model"] != "user-forced-model" {
		t.Errorf("replace mode should use user's model, got %v", captured.lastBody["model"])
	}
	if captured.lastBody["system"] != "You are someone else" {
		t.Errorf("replace mode should use user's system, got %v", captured.lastBody["system"])
	}
	// challenge 虽然没命中，但由于 replace 模式跳过 challenge 校验 + 响应非空 → operational
	if res.Status != MonitorStatusOperational {
		t.Errorf("replace mode with 2xx + non-empty text should be operational, got status=%s message=%q",
			res.Status, res.Message)
	}
}

func TestRunCheckForModel_ReplaceMode_EmptyResponseIsFailed(t *testing.T) {
	h := &captureHandler{respondText: ""} // 上游 200 但 content[0].text 为空
	endpoint := setupFakeAnthropic(t, h)

	opts := &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     map[string]any{"model": "x", "messages": []any{}},
	}
	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	if res.Status != MonitorStatusFailed {
		t.Errorf("replace mode with empty text should be failed, got status=%s", res.Status)
	}
	if !strings.Contains(res.Message, "replace-mode") {
		t.Errorf("failure message should hint replace-mode, got %q", res.Message)
	}
}

func TestExtractAnthropicMonitorText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "text block after thinking",
			body: `{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"2"}]}`,
			want: "2",
		},
		{
			name: "single text block",
			body: `{"content":[{"type":"text","text":"2"}]}`,
			want: "2",
		},
		{
			name: "thinking only",
			body: `{"content":[{"type":"thinking","thinking":""}]}`,
			want: "",
		},
		{
			name: "multiple text blocks",
			body: `{"content":[{"type":"text","text":"answer"},{"type":"tool_use","name":"x"},{"type":"text","text":"2"}]}`,
			want: "answer\n2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAnthropicMonitorText([]byte(tt.body))
			if got != tt.want {
				t.Fatalf("extractAnthropicMonitorText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateChallenge_AnthropicTextAfterThinking(t *testing.T) {
	body := []byte(`{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"答案是 2"}]}`)
	respText := extractAnthropicMonitorText(body)

	if !validateChallenge(respText, "2") {
		t.Fatalf("validateChallenge(%q, %q) = false, want true", respText, "2")
	}
}

func TestGeminiMonitorBodyIncludesExplicitUserRole(t *testing.T) {
	adapter := providerAdapters[MonitorProviderGemini]
	body, err := adapter.buildBody("gemini-3.6-flash", "Reply with only 7.")
	if err != nil {
		t.Fatalf("buildBody() error = %v", err)
	}

	var payload struct {
		Contents []struct {
			Role string `json:"role"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(payload.Contents) != 1 {
		t.Fatalf("contents length = %d, want 1", len(payload.Contents))
	}
	if payload.Contents[0].Role != "user" {
		t.Fatalf("contents[0].role = %q, want user", payload.Contents[0].Role)
	}
}
