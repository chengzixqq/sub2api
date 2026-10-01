package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type monitorProbeAccountSelector interface {
	SelectAccountForModel(context.Context, *int64, string, string) (*Account, error)
}
type ChannelMonitorProbeExecutor struct {
	selector    monitorProbeAccountSelector
	client      *http.Client
	billing     *BillingService
	validateURL func(string) error
	acquire     func(context.Context, int64, int) (*AcquireResult, error)
}

func NewChannelMonitorProbeExecutor(gateway *GatewayService, billing *BillingService) *ChannelMonitorProbeExecutor {
	client := *monitorHTTPClient
	client.Timeout = ChannelMonitorProbeTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	executor := &ChannelMonitorProbeExecutor{client: &client, billing: billing, validateURL: validateEndpoint}
	if gateway != nil {
		executor.selector = gateway
		executor.acquire = gateway.tryAcquireAccountSlot
	}
	return executor
}

// Execute only uses the account selector and protocol adapter. It deliberately never
// enters a gateway forwarding/billing handler, accepts no external marker or body,
// and sends at most one non-streaming POST without redirects, retry or failover.
func (e *ChannelMonitorProbeExecutor) Execute(ctx context.Context, run *ChannelMonitorProbeRun) {
	started := time.Now()
	run.Status = "error"
	defer func() {
		now := time.Now().UTC()
		run.CompletedAt = &now
		run.LatencyMS = time.Since(started).Milliseconds()
	}()
	if e == nil || e.selector == nil {
		run.ErrorClass = "executor_unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, ChannelMonitorProbeTimeout)
	defer cancel()
	a, err := e.selector.SelectAccountForModel(ctx, &run.GroupID, "", run.Model)
	if err != nil || a == nil {
		run.ErrorClass = "no_available_account"
		return
	}
	run.AccountID = &a.ID
	// OAuth, custom proxies and composite protocol rewrites need their own proven
	// single-call adapter. Failing closed here cannot unexpectedly bill or bypass a proxy.
	if a.Type != AccountTypeAPIKey || a.ProxyID != nil || a.IsShadow() || a.Platform == PlatformAntigravity || a.IsHeaderOverrideEnabled() {
		run.ErrorClass = "unsupported_account"
		return
	}
	model := a.GetMappedModel(run.Model)
	if (ChannelMonitorProbeTarget{GroupID: run.GroupID, Model: model, Protocol: run.Protocol}).Validate() != nil {
		run.ErrorClass = "unsupported_model"
		return
	}
	adapter, base, ok := monitorProbeAdapter(a, run.Protocol, model)
	if !ok || base == "" {
		run.ErrorClass = "unsupported_protocol"
		return
	}
	if e.validateURL != nil && e.validateURL(base) != nil {
		run.ErrorClass = "endpoint_rejected"
		return
	}
	if e.acquire != nil {
		slot, err := e.acquire(ctx, a.ID, a.Concurrency)
		if err != nil || slot == nil || !slot.Acquired {
			run.ErrorClass = "account_busy"
			return
		}
		if slot.ReleaseFunc != nil {
			defer slot.ReleaseFunc()
		}
	}
	key := a.GetCredential("api_key")
	if strings.TrimSpace(key) == "" {
		run.ErrorClass = "missing_credentials"
		return
	}
	data, err := adapter.buildBody(model, "Reply with OK.")
	if err != nil {
		run.ErrorClass = "request_invalid"
		return
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil {
		run.ErrorClass = "request_invalid"
		return
	}
	switch run.Protocol {
	case "openai_responses":
		body["max_output_tokens"] = 64
		body["tool_choice"] = "none"
	case "gemini":
		body["generationConfig"] = map[string]any{"maxOutputTokens": 64, "thinkingConfig": map[string]any{"thinkingBudget": 0}}
	default:
		body["max_tokens"] = 64
	}
	delete(body, "tools")
	if run.Protocol != "gemini" {
		body["stream"] = false
	}
	data, err = json.Marshal(body)
	if err != nil {
		run.ErrorClass = "request_invalid"
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(base, adapter.buildPath(model)), bytes.NewReader(data))
	if err != nil {
		run.ErrorClass = "request_invalid"
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range adapter.buildHeaders(key) {
		req.Header.Set(k, v)
	}
	client := e.client
	if client == nil {
		run.ErrorClass = "executor_unavailable"
		return
	}
	// Only an attempted upstream request is health evidence. Coverage/configuration
	// failures above leave Success nil and must not turn a channel red.
	failed := false
	run.Success = &failed
	resp, err := client.Do(req)
	if err != nil {
		run.ErrorClass = "upstream_transport"
		if ctx.Err() != nil {
			run.ErrorClass = "timeout"
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, monitorResponseMaxBytes+1))
	if err != nil || len(raw) > monitorResponseMaxBytes {
		run.ErrorClass = "invalid_response"
		return
	}
	if json.Valid(raw) {
		e.auditUsage(run, model, raw)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		run.ErrorClass = "upstream_http"
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusUnprocessableEntity {
			run.Success = nil
			run.ErrorClass = "unsupported_request"
		}
		return
	}
	errorField := gjson.GetBytes(raw, "error")
	if !json.Valid(raw) || (errorField.Exists() && errorField.Type != gjson.Null) || gjson.GetBytes(raw, "status").String() == "failed" {
		run.ErrorClass = "upstream_error"
		return
	}
	text := extractMonitorResponseText(adapter, raw)
	if run.Protocol == "openai_responses" {
		text = extractOpenAIResponsesText(raw)
	}
	if strings.TrimSpace(text) == "" {
		run.ErrorClass = "empty_output"
		return
	}
	success := true
	run.Success = &success
	run.Status = "healthy"
	run.ErrorClass = ""
}

// Usage is upstream cost evidence even when generation fails or has no output.
// This calculator has no user balance, subscription or usage-log write surface.
func (e *ChannelMonitorProbeExecutor) auditUsage(run *ChannelMonitorProbeRun, model string, raw []byte) {
	usage := gjson.GetBytes(raw, "usage")
	if !usage.Exists() {
		usage = gjson.GetBytes(raw, "response.usage")
	}
	var cacheRead, cacheCreation int64
	inputIncludesCache := true
	switch run.Protocol {
	case "openai_chat":
		run.InputTokens = usage.Get("prompt_tokens").Int()
		run.OutputTokens = usage.Get("completion_tokens").Int()
		cacheRead = usage.Get("prompt_tokens_details.cached_tokens").Int()
	case "gemini":
		run.InputTokens = gjson.GetBytes(raw, "usageMetadata.promptTokenCount").Int()
		run.OutputTokens = gjson.GetBytes(raw, "usageMetadata.candidatesTokenCount").Int() + gjson.GetBytes(raw, "usageMetadata.thoughtsTokenCount").Int()
		cacheRead = gjson.GetBytes(raw, "usageMetadata.cachedContentTokenCount").Int()
	default:
		run.InputTokens = usage.Get("input_tokens").Int()
		run.OutputTokens = usage.Get("output_tokens").Int()
		cacheRead = usage.Get("input_tokens_details.cached_tokens").Int()
		if run.Protocol == "anthropic" {
			cacheRead = usage.Get("cache_read_input_tokens").Int()
			cacheCreation = usage.Get("cache_creation_input_tokens").Int()
			inputIncludesCache = false
		}
	}
	if run.InputTokens < 0 {
		run.InputTokens = 0
	}
	if run.OutputTokens < 0 {
		run.OutputTokens = 0
	}
	if cacheRead < 0 {
		cacheRead = 0
	}
	if cacheCreation < 0 {
		cacheCreation = 0
	}
	input := run.InputTokens
	if inputIncludesCache {
		if cacheRead > input {
			cacheRead = input
		}
		input -= cacheRead
	}
	if e.billing != nil && run.InputTokens+run.OutputTokens+cacheRead+cacheCreation > 0 {
		if cost, err := e.billing.CalculateCost(model, UsageTokens{InputTokens: int(input), OutputTokens: int(run.OutputTokens), CacheReadTokens: int(cacheRead), CacheCreationTokens: int(cacheCreation)}, 1); err == nil && cost != nil {
			run.UpstreamCostUSD = &cost.TotalCost
		}
	}
}

func monitorProbeAdapter(a *Account, protocol, model string) (providerAdapter, string, bool) {
	if a.IsOpenCodeGo() {
		upstream := a.ResolveOpenCodeGoUpstreamProtocol(model)
		want := map[string]string{"anthropic": APIProtocolAnthropic, "openai_chat": APIProtocolChatCompletions, "openai_responses": APIProtocolResponses}[protocol]
		if upstream != want {
			return providerAdapter{}, "", false
		}
		base := a.GetOpenAIBaseURL()
		if a.IsAdaptiveAPIProtocol() {
			base = a.GetCNProtocolBaseURL(upstream)
		} else if upstream == APIProtocolAnthropic {
			base = a.GetAnthropicProtocolBaseURL()
		}
		switch upstream {
		case APIProtocolAnthropic:
			return providerAdapters[MonitorProviderAnthropic], base, true
		case APIProtocolResponses:
			return providerOpenAIResponsesAdapter, base, true
		case APIProtocolChatCompletions:
			return providerOpenCodeGoChatAdapter, base, true
		}
	}
	switch protocol {
	case "anthropic":
		if a.Platform == PlatformAnthropic {
			return providerAdapters[MonitorProviderAnthropic], a.GetBaseURL(), true
		}
		if a.IsAnthropicProtocol() {
			return providerAdapters[MonitorProviderAnthropic], a.GetAnthropicProtocolBaseURL(), true
		}
	case "gemini":
		if a.Platform == PlatformGemini {
			return providerAdapters[MonitorProviderGemini], a.GetGeminiBaseURL("https://generativelanguage.googleapis.com"), true
		}
	case "openai_chat":
		if a.IsGrok() {
			return providerGrokChatAdapter, a.GetGrokBaseURL(), true
		}
		if a.IsOpenAICompatible() && a.GetAPIProtocol() == APIProtocolChatCompletions {
			adapter, ok := providerAdapters[a.Platform]
			return adapter, a.GetOpenAIBaseURL(), ok
		}
	case "openai_responses":
		if a.Platform == PlatformOpenAI {
			return providerOpenAIResponsesAdapter, a.GetOpenAIBaseURL(), true
		}
	}
	return providerAdapter{}, "", false
}
