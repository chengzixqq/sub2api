package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

// For an early local response the published global snapshot and the selected
// account already contain the alignment fields. Avoid a settings query.
func (s *GatewayService) countTokensEarlyPolicy(c *gin.Context, account *Account) ClaudeCustomizationSettings {
	policy := s.publishedClaudePolicy(c, account)
	if settings := s.settingService; settings != nil {
		if snapshot := settings.alignmentRejectCache.Load(); snapshot != nil {
			settings.tryRefreshAlignmentSnapshot(c.Request.Context(), snapshot)
		}
	}
	c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{accountID: account.ID, policy: policy})
	return policy
}

func (s *GatewayService) publishedClaudePolicy(c *gin.Context, account *Account) ClaudeCustomizationSettings {
	if value, ok := c.Get(claudeCustomizationRequestContextKey); ok {
		if cached, ok := value.(claudeCustomizationRequestPolicy); ok && cached.accountID == account.ID {
			return cached.policy
		}
	}
	policy := DefaultClaudeCustomizationSettings()
	if settings := s.settingService; settings != nil {
		if snapshot := settings.alignmentRejectCache.Load(); snapshot != nil {
			policy = snapshot.global
		}
	}
	if overrides, ok := account.Extra[AccountClaudeCustomizationExtraKey].(map[string]any); ok {
		applyClaudeAccountOverrides(&policy, overrides, account.Type)
	}
	return policy
}

// ForwardCountTokens 转发 count_tokens 请求到上游 API
// 特点：不记录使用量、仅支持非流式响应
func (s *GatewayService) ForwardCountTokens(ctx context.Context, c *gin.Context, account *Account, parsed *ParsedRequest) error {
	if parsed == nil {
		s.countTokensError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return fmt.Errorf("parse request: empty request")
	}
	// Antigravity 账户不支持 count_tokens，返回 404 让客户端 fallback 到本地估算。
	// 返回 nil 避免 handler 层记录为错误，也不设置 ops 上游错误上下文。
	// 凭据只能走其他协议的 OAuth 账号同理，且必须在取 token、建请求之前拦下。
	if account != nil && (account.Platform == PlatformAntigravity || !anthropicMessagesCredentialAllowed(account)) {
		policy := s.countTokensEarlyPolicy(c, account)
		if err := s.settingService.checkUnknownBeta(ctx, c, account, policy); err != nil {
			return err
		}
		s.countTokensError(c, http.StatusNotFound, "not_found_error", "count_tokens endpoint is not supported for this platform")
		return nil
	}

	validationModel := parsed.Model
	if account != nil {
		if account.IsBedrock() {
			if resolved, ok := ResolveBedrockModelID(account, validationModel); ok {
				validationModel = resolved
			}
		} else if account.Type == AccountTypeAPIKey {
			validationModel = account.GetMappedModel(validationModel)
		}
	}
	if account != nil && account.Platform == PlatformAnthropic {
		if err := validateClaude55Request(parsed.Body.Bytes(), validationModel); err != nil {
			s.countTokensEarlyPolicy(c, account)
			s.countTokensError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return err
		}
	}

	// Bedrock 不支持 count_tokens 端点
	if account != nil && account.IsBedrock() {
		s.countTokensEarlyPolicy(c, account)
		s.countTokensError(c, http.StatusNotFound, "not_found_error", "count_tokens endpoint is not supported for Bedrock")
		return nil
	}

	claudePolicy := DefaultClaudeCustomizationSettings()
	if isNativeCacheControlAccount(account) || (account != nil && account.Type == AccountTypeAPIKey) {
		claudePolicy = s.settingService.ResolveClaudeCustomizationForRequest(ctx, c, account)
	}
	if err := s.settingService.checkUnknownBeta(ctx, c, account, claudePolicy); err != nil {
		return err
	}
	if c != nil && s.settingService != nil && account != nil && account.Type == AccountTypeAPIKey {
		c.Set(redactUpstreamURLContextKey, claudePolicy.URLRedactionEnabled)
	}

	limitMode := requestCacheControlLimitMode(c, account, claudePolicy)
	thinkingMode := requestThinkingCacheControlMode(c, account, claudePolicy)
	if err := s.settingService.AlignmentHits().checkCacheControls(c, account, thinkingMode, limitMode, parsed); err != nil {
		return err
	}
	if account != nil && account.IsAnthropicAPIKeyPassthroughEnabled() {
		passthroughBody := parsed.Body.Bytes()
		passthroughModel := parsed.Model
		if reqModel := parsed.Model; reqModel != "" {
			if mappedModel := account.GetMappedModel(reqModel); mappedModel != reqModel {
				passthroughBody = s.replaceModelInBody(passthroughBody, mappedModel)
				passthroughModel = mappedModel
				logger.LegacyPrintf("service.gateway", "CountTokens passthrough model mapping: %s -> %s (account: %s)", reqModel, mappedModel, account.Name)
			}
		}
		return s.forwardCountTokensAnthropicAPIKeyPassthrough(ctx, c, account, passthroughBody, passthroughModel)
	}

	body := parsed.Body.Bytes()
	replaceBody := func(next []byte) error {
		if err := parsed.ReplaceBody(next); err != nil {
			return fmt.Errorf("rewrite count_tokens body: %w", err)
		}
		body = parsed.Body.Bytes()
		return nil
	}
	reqModel := parsed.Model

	// Pre-filter: strip empty text blocks to prevent upstream 400.
	if err := replaceBody(StripEmptyTextBlocks(body)); err != nil {
		return err
	}

	isClaudeCodeCT := IsClaudeCodeClient(ctx) || isClaudeCodeClient(c.GetHeader("User-Agent"), parsed.MetadataUserID)
	shouldMimicClaudeCode := account.IsOAuth() && !isClaudeCodeCT

	if shouldMimicClaudeCode {
		// 与 /v1/messages 相同：在注入 tools[-1] / messages 断点之前按客户端原始
		// 断点数决定 4 块上限的处理方式（见 planCacheControlLimit）。
		cacheControlPlan, plannedBody, marked := planCacheControlLimit(body, limitMode, thinkingMode, true)
		if marked {
			if err := replaceBody(plannedBody); err != nil {
				return err
			}
		}

		var normalizedBody []byte
		normalizedBody, reqModel = normalizeClaudeOAuthRequestBody(body, reqModel, claudeOAuthNormalizeOptions{})
		if err := replaceBody(normalizedBody); err != nil {
			return err
		}

		if err := replaceBody(s.rewriteMessageCacheControlIfEnabled(ctx, body, cacheControlPlan.preserveThinking)); err != nil {
			return err
		}
		if rw := buildToolNameRewriteFromBody(body); rw != nil {
			if err := replaceBody(applyToolNameRewriteToBody(body, rw)); err != nil {
				return err
			}
		} else {
			if err := replaceBody(applyToolsLastCacheBreakpoint(body)); err != nil {
				return err
			}
		}

		// 4 块上限的兜底：其余四条出口都在自己的转发路径上调过一次，只有这里没有。
		// 不再剥离客户端 system 断点之后，「客户端 system + 客户端 messages +
		// 上面刚注入的 tools[-1]」可以直接顶到 5 块，而上游对超限是 400。
		if err := replaceBody(cacheControlPlan.apply(body)); err != nil {
			return err
		}
	} else if limitMode == ClaudeCacheControlLimitTrim && isNativeCacheControlAccount(account) {
		if err := replaceBody(trimRealCacheControlSlots(body, thinkingMode != ClaudeAlignStrip, true)); err != nil {
			return err
		}
	}

	// 应用模型映射：
	// - APIKey 账号：使用账号级别的显式映射（如果配置），否则透传原始模型名
	// - OAuth/SetupToken 账号：使用 Anthropic 标准映射（短ID → 长ID）
	if reqModel != "" {
		mappedModel := reqModel
		mappingSource := ""
		if account.Type == AccountTypeAPIKey {
			mappedModel = account.GetMappedModel(reqModel)
			if mappedModel != reqModel {
				mappingSource = "account"
			}
		}
		if mappingSource == "" && account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
			normalized := claude.NormalizeModelID(reqModel)
			if normalized != reqModel {
				mappedModel = normalized
				mappingSource = "prefix"
			}
		}
		if mappedModel != reqModel {
			originalReqModel := reqModel
			if err := replaceBody(s.replaceModelInBody(body, mappedModel)); err != nil {
				return err
			}
			reqModel = mappedModel
			parsed.Model = mappedModel
			logger.LegacyPrintf("service.gateway", "CountTokens model mapping applied: %s -> %s (account: %s, source=%s)", originalReqModel, mappedModel, account.Name, mappingSource)
		}
	}

	// 获取凭证
	token, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		if !isNativeCacheControlAccount(account) {
			s.countTokensEarlyPolicy(c, account)
		}
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return err
	}

	// 构建上游请求
	upstreamReq, wireBody, err := s.buildCountTokensRequest(ctx, c, account, body, token, tokenType, reqModel, shouldMimicClaudeCode)
	if err != nil {
		if _, selected := c.Get(claudeCustomizationRequestContextKey); !selected {
			s.countTokensEarlyPolicy(c, account)
		}
		const message = "Failed to build request"
		var blocked *BetaBlockedError
		if !errors.As(err, &blocked) || !blocked.WriteContextPolicyJSON(c, http.StatusInternalServerError, "api_error", message) {
			s.countTokensError(c, http.StatusInternalServerError, "api_error", message)
		}
		return err
	}
	claudePolicy = cachedClaudeResponsePolicy(c)
	// 先记录首发 wire body；如果后面进入 400 retry，retry 会基于未签名的逻辑 body 重新构建。
	acceptedWireBody, err := s.alignAnthropicWireRequest(c, account, claudePolicy, upstreamReq, wireBody)
	if err != nil {
		return err
	}

	// 获取代理URL（自定义 base URL 模式下，proxy 通过 buildCustomRelayURL 作为查询参数传递）
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		if !account.IsCustomBaseURLEnabled() || account.GetCustomBaseURL() == "" {
			proxyURL = account.Proxy.URL()
		}
	}

	// 发送请求
	resp, err := s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		setOpsUpstreamError(c, 0, sanitizeUpstreamErrorMessage(err.Error()), "")
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Request failed")
		return fmt.Errorf("upstream request failed: %w", err)
	}

	// 读取响应体
	countTokensTooLarge := func(c *gin.Context) {
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream response too large")
	}
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, countTokensTooLarge)
	_ = resp.Body.Close()
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		}
		return err
	}

	// 检测 thinking block 签名错误（400）并重试一次（过滤 thinking blocks）
	if resp.StatusCode == 400 && claudePolicy.ThinkingSignatureRetryEnabled && s.shouldRectifySignatureError(ctx, c, account, respBody, body, reqModel) {
		logger.LegacyPrintf("service.gateway", "Account %d: detected thinking block signature error on count_tokens, retrying with filtered thinking blocks", account.ID)

		filteredBody := FilterThinkingBlocksForRetry(body, reqModel)
		if !bytes.Equal(filteredBody, body) {
			retryReq, retryWireBody, buildErr := s.buildCountTokensRequest(ctx, c, account, filteredBody, token, tokenType, reqModel, shouldMimicClaudeCode)
			if buildErr == nil {
				retryWireBody, err = s.alignAnthropicWireRequest(c, account, claudePolicy, retryReq, retryWireBody)
				if err != nil {
					return err
				}
				retryResp, retryErr := s.httpUpstream.DoWithTLS(retryReq, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
				if retryErr == nil {
					if retryResp.StatusCode < 400 {
						acceptedWireBody = retryWireBody
					}
					resp = retryResp
					respBody, err = ReadUpstreamResponseBody(resp.Body, s.cfg, c, countTokensTooLarge)
					_ = resp.Body.Close()
					if err != nil {
						if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
							s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
						}
						return err
					}
				}
			}
		}
	}

	if resp.StatusCode < 400 && !bytes.Equal(acceptedWireBody, body) {
		// count_tokens 成功后再同步最终 wire body，避免 retry 从已签名 body 派生。
		if err := replaceBody(acceptedWireBody); err != nil {
			return err
		}
	}

	// 处理错误响应
	if resp.StatusCode >= 400 {
		prepareOfficialResponse(c, claudePolicy, resp.Header, nil)

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, reqModel)
		}
		upstreamMsg = sanitizeUpstreamErrorMessageForContext(c, sanitizeUpstreamErrorMessage(upstreamMsg))
		upstreamDetail := ""
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = redactUpstreamURLs(truncateString(string(respBody), maxBytes))
		}
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)

		// 记录上游错误摘要便于排障（不回显请求内容）
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			logBody := redactUpstreamResponseBodyForClient(c, respBody)
			logger.LegacyPrintf("service.gateway",
				"count_tokens upstream error %d (account=%d platform=%s type=%s): %s",
				resp.StatusCode,
				account.ID,
				account.Platform,
				account.Type,
				truncateForLog(logBody, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
			)
		}

		s.writeCountTokensUpstreamError(c, resp.StatusCode)
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	// 透传成功响应
	prepareOfficialResponse(c, claudePolicy, resp.Header, nil)
	c.Data(resp.StatusCode, "application/json", respBody)
	return nil
}

// forwardCountTokensAnthropicAPIKeyPassthrough 透传 count_tokens 请求；upstreamModel 是 body 中实际发往上游的模型，
// 仅用于上游错误处理按模型隔离。
func (s *GatewayService) forwardCountTokensAnthropicAPIKeyPassthrough(ctx context.Context, c *gin.Context, account *Account, body []byte, upstreamModel string) error {
	token, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return err
	}
	if tokenType != "apikey" {
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Invalid account token type")
		return fmt.Errorf("anthropic api key passthrough requires apikey token, got: %s", tokenType)
	}

	upstreamReq, wireBody, err := s.buildCountTokensRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, token)
	if err != nil {
		s.countTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	policy := s.settingService.ResolveClaudeCustomizationForRequest(ctx, c, account)
	if _, err := s.alignAnthropicWireRequest(c, account, policy, upstreamReq, wireBody); err != nil {
		return err
	}
	resp, err := s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		setOpsUpstreamError(c, 0, sanitizeUpstreamErrorMessage(err.Error()), "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
			Passthrough:        true,
			Kind:               "request_error",
			Message:            sanitizeUpstreamErrorMessage(err.Error()),
		})
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Request failed")
		return fmt.Errorf("upstream request failed: %w", err)
	}

	countTokensTooLarge := func(c *gin.Context) {
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream response too large")
	}
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, countTokensTooLarge)
	_ = resp.Body.Close()
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		}
		return err
	}

	if resp.StatusCode >= 400 {

		prepareOfficialResponse(c, policy, resp.Header, nil)
		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
		}
		upstreamMsg = sanitizeUpstreamErrorMessageForContext(c, sanitizeUpstreamErrorMessage(upstreamMsg))

		// 中转站不支持 count_tokens 端点时（404），返回 404 让客户端 fallback 到本地估算。
		// 仅在错误消息明确指向 count_tokens endpoint 不存在时生效，避免误吞其他 404（如错误 base_url）。
		// 返回 nil 避免 handler 层记录为错误，也不设置 ops 上游错误上下文。
		if isCountTokensUnsupported404(resp.StatusCode, respBody) {
			logger.LegacyPrintf("service.gateway",
				"[count_tokens] Upstream does not support count_tokens (404), returning 404: account=%d name=%s msg=%s",
				account.ID, account.Name, truncateString(upstreamMsg, 512))
			s.countTokensError(c, http.StatusNotFound, "not_found_error", "count_tokens endpoint is not supported by upstream")
			return nil
		}

		upstreamDetail := ""
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = redactUpstreamURLs(truncateString(string(respBody), maxBytes))
		}
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
			Passthrough:        true,
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})

		s.writeCountTokensUpstreamError(c, resp.StatusCode)
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	writeAnthropicPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	prepareOfficialResponse(c, policy, resp.Header, nil)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, respBody)
	return nil
}

func (s *GatewayService) buildCountTokensRequestAnthropicAPIKeyPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, []byte, error) {
	body = stripDeferredToolCacheControl(body)
	targetURL := claudeAPICountTokensURL
	baseURL := getAnthropicGatewayBaseURL(account)
	if baseURL != "" {
		validatedURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return nil, nil, err
		}
		targetURL = validatedURL + "/v1/messages/count_tokens?beta=true"
	}
	body = sanitizeCountTokensRequestBody(body)

	// 同 buildUpstreamRequestAnthropicAPIKeyPassthrough：能力维度 sanitize。
	claudePolicy := DefaultClaudeCustomizationSettings()
	if s.settingService != nil {
		claudePolicy = s.settingService.ResolveClaudeCustomizationForRequest(ctx, c, account)
	}
	body, clientBeta := s.resolvePassthroughRequestBeta(c, account, body, claudePolicy)
	if sanitized, changed := sanitizeAnthropicBodyForBetaTokensWithFallbackPolicy(body, clientBeta, claudePolicy.FallbackPolicy, true); changed {
		body = sanitized
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if !allowedHeaders[lowerKey] {
				continue
			}
			wireKey := resolveWireCasing(key)
			for _, v := range values {
				addHeaderRaw(req.Header, wireKey, v)
			}
		}
	}

	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("cookie")
	// Ollama Cloud Anthropic 兼容端点按实际 base_url 强制 Bearer（同上方
	// targetURL 的 base 取值），其余保持 extra/default 行为。
	setAnthropicAPIKeyAuthHeader(req.Header, account, token, baseURL)

	if getHeaderRaw(req.Header, "content-type") == "" {
		setHeaderRaw(req.Header, "content-type", "application/json")
	}
	if getHeaderRaw(req.Header, "anthropic-version") == "" {
		setHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	// 账号级请求头覆写覆盖通用来源；严格协议边界仍在下方做最终过滤。
	account.ApplyHeaderOverrides(req.Header)
	// Strict remains authoritative over account-level header overrides.
	applyStrictAnthropicFallbackBetaHeader(req.Header, claudePolicy.FallbackPolicy)
	filterSonnet55ToolsetBetaHeader(req.Header, body, gjson.GetBytes(body, "model").String())

	return req, body, nil
}

// buildCountTokensRequest 构建 count_tokens 上游请求
func (s *GatewayService) buildCountTokensRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token, tokenType, modelID string, mimicClaudeCode bool) (*http.Request, []byte, error) {
	body = stripDeferredToolCacheControl(body)
	// 确定目标 URL
	targetURL := claudeAPICountTokensURL
	baseURL := ""
	if account.Type == AccountTypeAPIKey {
		baseURL = getAnthropicGatewayBaseURL(account)
		if baseURL != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, nil, err
			}
			targetURL = validatedURL + "/v1/messages/count_tokens?beta=true"
		}
	} else if account.IsCustomBaseURLEnabled() {
		customURL := account.GetCustomBaseURL()
		if customURL == "" {
			return nil, nil, fmt.Errorf("custom_base_url is enabled but not configured for account %d", account.ID)
		}
		validatedURL, err := s.validateUpstreamBaseURL(customURL)
		if err != nil {
			return nil, nil, err
		}
		targetURL = s.buildCustomRelayURL(validatedURL, "/v1/messages/count_tokens", account)
	}

	clientHeaders := http.Header{}
	if c != nil && c.Request != nil {
		clientHeaders = c.Request.Header
	}

	// OAuth 账号：应用统一指纹和重写 userID（受设置开关控制）
	// 如果启用了会话ID伪装，会在重写后替换 session 部分为固定值
	ctEnableFP, ctEnableMPT := true, false
	claudePolicy := DefaultClaudeCustomizationSettings()
	if s.settingService != nil {
		claudePolicy = s.settingService.ResolveClaudeCustomizationForRequest(ctx, c, account)
		ctEnableFP = claudePolicy.FingerprintUnification
		ctEnableMPT = claudePolicy.MetadataPassthrough
	}
	var ctFingerprint *Fingerprint
	if account.IsOAuth() && s.identityService != nil {
		fp, err := s.identityService.GetOrCreateFingerprint(ctx, account.ID, clientHeaders)
		if err == nil {
			ctFingerprint = fp
			if !ctEnableMPT {
				accountUUID := account.GetExtraString("account_uuid")
				if accountUUID != "" && fp.ClientID != "" {
					if newBody, err := s.identityService.RewriteUserIDWithMasking(ctx, body, account, accountUUID, fp.ClientID, fp.UserAgent); err == nil && len(newBody) > 0 {
						body = newBody
					}
				}
			}
		}
	}

	// Disabled fingerprint unification does not disable forced mimicry headers.
	var billingFingerprint *Fingerprint
	if ctEnableFP {
		billingFingerprint = ctFingerprint
	}
	// 一致性铁律：同一次请求内只取一次 mimic UA，billing cc_version 与出站
	// User-Agent 头共用这一个字符串（同 buildUpstreamRequest）。
	ctMimicUserAgent := claude.DefaultUserAgent()
	if billingUA := effectiveBillingUserAgent(ctMimicUserAgent, tokenType, mimicClaudeCode, billingFingerprint); billingUA != "" {
		body = syncBillingHeaderVersion(body, billingUA)
	}

	// === 计算最终 anthropic-beta header（先于 body sanitize 与 CCH 签名）===
	// 顺序约束同 buildUpstreamRequest。
	ctPolicy, err := s.requestBetaPolicy(ctx, c, account, modelID)
	if err != nil {
		return nil, nil, err
	}
	ctEffectiveDropSet := mergeDropSets(ctPolicy.filterSet)
	ctEffectiveDropSet = applyNativeAnthropicFallbackBetaPolicy(account, claudePolicy.FallbackPolicy, ctEffectiveDropSet)
	finalBetaHeader, finalBetaShouldSet := s.computeFinalCountTokensAnthropicBeta(
		tokenType, mimicClaudeCode, modelID, clientHeaders, body, ctEffectiveDropSet,
	)

	// 账号覆写整体替代策略处理后的客户端声明。
	body, finalBetaHeader = resolveAnthropicRequestBeta(c, account, body, finalBetaHeader, ctPolicy)
	finalBetaShouldSet = finalBetaShouldSet || finalBetaHeader != ""
	finalBetaHeader = filterSonnet55ToolsetBeta(finalBetaHeader, body, modelID)

	// 能力维度 body sanitize：与最终 anthropic-beta header 对称
	if sanitized, changed := sanitizeAnthropicBodyForBetaTokensWithFallbackPolicy(body, finalBetaHeader, claudePolicy.FallbackPolicy, isNativeAnthropicAPIKeyAccount(account)); changed {
		body = sanitized
	}

	body = sanitizeCountTokensRequestBody(body)

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	// 设置认证头（保持原始大小写）
	if tokenType == "oauth" {
		setHeaderRaw(req.Header, "authorization", "Bearer "+token)
	} else {
		// Ollama Cloud Anthropic 兼容端点按实际 base_url 强制 Bearer（同上方
		// targetURL 的 base 取值），其余保持 extra/default 行为。
		setAnthropicAPIKeyAuthHeader(req.Header, account, token, baseURL)
	}

	// 白名单透传 headers（恢复真实 wire casing）
	for key, values := range clientHeaders {
		lowerKey := strings.ToLower(key)
		if allowedHeaders[lowerKey] {
			wireKey := resolveWireCasing(key)
			for _, v := range values {
				addHeaderRaw(req.Header, wireKey, v)
			}
		}
	}

	// OAuth 账号：应用指纹到请求头（受设置开关控制）
	if ctEnableFP && ctFingerprint != nil {
		s.identityService.ApplyFingerprint(req, ctFingerprint)
	}

	// 确保必要的 headers 存在（保持原始大小写）
	if getHeaderRaw(req.Header, "content-type") == "" {
		setHeaderRaw(req.Header, "content-type", "application/json")
	}
	if getHeaderRaw(req.Header, "anthropic-version") == "" {
		setHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	if tokenType == "oauth" {
		applyClaudeOAuthHeaderDefaults(req)
	}

	// OAuth + mimic Claude Code：强制注入 CLI 指纹 header
	if tokenType == "oauth" && mimicClaudeCode {
		applyClaudeCodeMimicHeaders(req, false, ctMimicUserAgent)
	}

	// 同步 X-Claude-Code-Session-Id 头：取 body 中已处理的 metadata.user_id 的 session_id 覆盖
	if sessionHeader := getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"); sessionHeader != "" {
		if uid := gjson.GetBytes(body, "metadata.user_id").String(); uid != "" {
			if parsed := ParseMetadataUserID(uid); parsed != nil {
				setHeaderRaw(req.Header, "X-Claude-Code-Session-Id", parsed.SessionID)
			}
		}
	}

	// 账号级请求头覆写覆盖通用来源；严格协议边界仍在下方做最终过滤。
	account.ApplyHeaderOverrides(req.Header)
	// 写入最终 anthropic-beta header（Del 一次避免白名单透传值残留）
	deleteHeaderAllForms(req.Header, "anthropic-beta")
	if finalBetaShouldSet {
		setHeaderRaw(req.Header, "anthropic-beta", finalBetaHeader)
	}

	// Strict remains authoritative over account-level header overrides.
	applyStrictAnthropicFallbackBetaHeader(req.Header, claudePolicy.FallbackPolicy)
	filterSonnet55ToolsetBetaHeader(req.Header, body, modelID)

	if c != nil && tokenType == "oauth" {
		c.Set(claudeMimicDebugInfoKey, newClaudeMimicDebugLine(req, body, account, tokenType, mimicClaudeCode))
	}
	if s.debugClaudeMimicEnabled() {
		logClaudeMimicDebug(req, body, account, tokenType, mimicClaudeCode)
	}

	return req, body, nil
}

func sanitizeCountTokensRequestBody(body []byte) []byte {
	out := body
	for _, path := range []string{
		"temperature",
		"top_p",
		"top_k",
		"stream",
		"stop_sequences",
		"stop",
		// Anthropic's /v1/messages/count_tokens accepts request-input fields only.
		// max_tokens is a generation parameter; OAuth mimicry may inject it to
		// resemble Claude Code messages requests, so it must never reach this endpoint.
		"max_tokens",
	} {
		if gjson.GetBytes(out, path).Exists() {
			if next, ok := deleteJSONPathBytes(out, path); ok {
				out = next
			}
		}
	}
	return out
}

func (s *GatewayService) writeCountTokensUpstreamError(c *gin.Context, status int) {
	msg := "Upstream request failed"
	switch status {
	case 429:
		msg = "Rate limit exceeded"
	case 529:
		msg = "Service overloaded"
	}
	s.countTokensError(c, status, "upstream_error", msg)
}

// countTokensError 返回 count_tokens 错误响应
func (s *GatewayService) countTokensError(c *gin.Context, status int, errType, message string) {
	writeAnthropicError(c, status, errType, message)
}
