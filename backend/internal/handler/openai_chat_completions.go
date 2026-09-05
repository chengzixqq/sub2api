package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ChatCompletions handles OpenAI Chat Completions API requests.
// POST /v1/chat/completions
func (h *OpenAIGatewayHandler) ChatCompletions(c *gin.Context) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.chat_completions",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		logRequestBodyReadFailure(reqLog, c.Request, err)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	if !gjson.ValidBytes(body) {
		logRequestBodyParseFailure(reqLog, body, nil)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	if !openAICompatibleTextTargetAllowed(c, apiKey, reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}
	if cappedBody, changed, err := applyOpenAIReasoningEffortPolicyForRequest(c, apiKey, body); err != nil {
		respondOpenAIReasoningEffortPolicyError(c, err, h.errorResponse)
		return
	} else if changed {
		body = cappedBody
	}
	reqStream, ok := parseOpenAICompatibleStream(body)
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", invalidStreamFieldTypeMessage)
		return
	}
	if _, err := service.ValidateOpenAIServiceTierField(body); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if service.IsGPTImageGenerationModel(reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "This model is not supported on the Chat Completions endpoint")
		return
	}

	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	setOpsRequestContext(c, reqModel, reqStream)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(reqStream, false)))

	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, reqModel, body); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}
	if h.rejectIfCyberSessionBlocked(c, apiKey, body, reqModel, cyberBlockFormatChat) {
		return
	}

	// 解析渠道级模型映射
	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	requestPlatform := openAICompatibleRequestPlatform(c.Request.Context(), apiKey)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai_chat_completions.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	var probeLease *ProbeLease
	probeCandidateDetected := false
	probeLeaderInstalled := false
	if candidate, ok := probeCandidateForRequest(c, body, reqModel, optionalGroupID(apiKey.GroupID), requestPlatform, channelMapping.ChannelID); ok {
		probeCandidateDetected = true
		probeCtx := c.Request.Context()
		syncProbeCoalescerSettings(h.probeCoalescer, nil, probeCtx)
		probeCtx, probeRequestID := prepareProbeAdmission(c, h.probeCoalescer)
		probeLease = h.probeCoalescer.Begin(probeCtx, candidate, probeRequestID)
		if probeLease.IsExhausted() {
			h.errorResponse(c, http.StatusServiceUnavailable, "probe_unavailable", "Probe attempt budget exhausted")
			return
		}
		if probeLease.IsFollower() {
			handled, promoted, probeErr := resolveProbeFollower(c, probeLease, func(ctx context.Context, candidate ProbeCandidate, account *service.Account, leaderID string) error {
				return h.billSyntheticProbe(c, apiKey, subscription, body, candidate, account, leaderID, channelMapping)
			})
			if handled {
				if probeErr != nil {
					status, code, message := probeResolutionError(probeErr)
					h.handleStreamingAwareError(c, status, code, message, streamStarted)
				}
				return
			}
			if promoted {
				defer installProbeLeader(c, probeLease, requestIDForProbe(c))()
				probeLeaderInstalled = true
			}
		}
		if probeLease.IsLeader() && !probeLeaderInstalled {
			defer installProbeLeader(c, probeLease, requestIDForProbe(c))()
		}
	}
	markProbeAccountConcurrency(c, probeLease, probeCandidateDetected)

	sessionHash := h.gatewayService.GenerateSessionHash(c, body)
	promptCacheKey := h.gatewayService.ExtractSessionID(c, body)

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	profitVetoCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	var oauth429FailoverState service.OpenAIOAuth429FailoverState

	// 计费结算守卫：保证下面的转发循环无论从哪个 return 退出都恰好结算一次。
	// 请求体已由上面的 gjson.ValidBytes 闸门确认是合法 JSON，可安全进入估算器。
	guard := newBillingSettlementGuard(guardDeps{
		resetAttemptOutput: billingAttemptOutputReset(c),
		upstreamUsageOnly:  failureBillingUpstreamUsageOnlySnapshot(c.Request.Context(), nil),
		estimatedPromptTokens: func() int {
			return service.EstimateFailurePromptTokens(requestPlatform, body)
		},
		sink: h.openAIFailureSink(c, openAIFailureSinkParams{
			APIKey:              apiKey,
			Subscription:        subscription,
			ReqModel:            reqModel,
			ChannelUsageFields:  clientRequestedUsageFields(c, channelMapping, reqModel, ""),
			RequestPayloadBytes: body,
			Component:           "handler.openai_gateway.chat_completions",
		}),
	})
	defer guard.Flush()
	// 分组利润控制：chat completions 文本入口请求级装门并固定 pricingAt。
	ccPricingCtx, pricingAt := h.gatewayService.WithOpenAIRequestPricingContext(c.Request.Context(), apiKey.GroupID)
	c.Request = c.Request.WithContext(ccPricingCtx)

	for {
		if failoverClientGone(c) {
			return
		}
		reqLog.Debug("openai_chat_completions.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			apiKey.GroupID,
			"",
			sessionHash,
			reqModel,
			failedAccountIDs,
			service.OpenAIUpstreamTransportAny,
			service.OpenAIEndpointCapabilityChatCompletions,
			false,
			false,
			true,
			requestPlatform,
		)
		if err != nil {
			if failoverClientGone(c) {
				reqLog.Info("openai_chat_completions.account_select_aborted_client_disconnected", zap.Error(err))
				return
			}
			reqLog.Warn("openai_chat_completions.account_select_failed",
				zap.Error(openAICompatibleSelectionErrorForLog(err, requestPlatform)),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if len(failedAccountIDs) == 0 {
				cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, h.gatewayService, apiKey, reqModel, reqModel)
				cls = classifySelectionFailureError(err, cls)
				if !cls.ModelNotFound {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				}
				h.handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
				return
			} else {
				if lastFailoverErr != nil {
					h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
				} else {
					h.handleStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
				}
				return
			}
		}
		if selection == nil || selection.Account == nil {
			cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, h.gatewayService, apiKey, reqModel, reqModel)
			if !cls.ModelNotFound {
				markOpsRoutingCapacityLimited(c)
			}
			h.handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
			return
		}
		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai_chat_completions.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		_ = scheduleDecision
		setOpsSelectedAccount(c, account.ID, account.Platform)
		setProbeLeaderAccount(c, account)

		accountReleaseFunc, slotResult := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, reqStream, &streamStarted, reqLog)
		if slotResult == openAISlotAcquireProfitVetoed {
			// 利润终检否决：排除该账号重新选号；否决次数达上限则按无可用账号终止。
			if !recordOpenAIProfitVeto(failedAccountIDs, account.ID, &profitVetoCount) {
				h.handleOpenAIProfitVetoExhausted(c, streamStarted, reqLog, profitVetoCount)
				return
			}
			continue
		}
		if slotResult != openAISlotAcquireOK {
			return
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		guard.ObserveAttempt(account)
		forwardStart := time.Now()

		forwardBody := body
		if channelMapping.Mapped {
			forwardBody = h.gatewayService.ReplaceModelInBody(body, channelMapping.MappedModel)
		}
		writerSizeBeforeForward := c.Writer.Size()
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			return h.gatewayService.ForwardAsChatCompletions(c.Request.Context(), c, account, forwardBody, promptCacheKey, "")
		}()
		searchSnapshot := guard.ObserveOpenAIForwardResult(result)
		logBillingSearchAttempt(reqLog, account.ID, searchSnapshot, err)
		if result != nil {
			result.SearchCount = searchSnapshot.CumulativeSearchCount
		}
		var cyberBlockBodyChat []byte
		if service.GetOpsCyberPolicy(c) != nil {
			cyberBlockBodyChat = body
		}
		// cyber 已按上游真实 usage 记了一笔账，它就是这次请求的结算，请求级失败兜底必须让路：
		// 流式 cyber 会先置位 GatewayUpstreamDeliveredKey（token 事件处），而 cyber 返回的是裸
		// error（非 *UpstreamFailoverError），Flush 的 `ferr == nil && !outputStarted` 早退不成立，
		// 不 MarkSettled 就会在 cyber 那笔之外再按整份估算 prompt 记第二笔 = 多算。
		if h.recordCyberPolicyIfMarked(c, apiKey, account, subscription, reqModel, err != nil, cyberBlockBodyChat, clientRequestedUsageFields(c, channelMapping, reqModel, ""), service.HashUsageRequestPayload(body), cyberUsageObservation{
			SearchCount: searchSnapshot.CumulativeSearchCount,
			Result:      result,
		}) {
			guard.MarkSettled()
		}

		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if err == nil && result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		// #5148 对齐：错误返回携带的部分 result（流中断前上游已计量的 usage）照常
		// 入账；failover 错误恒定 result=nil，不会重复计费。
		submitChatUsage := func(res *service.OpenAIForwardResult) {
			if res == nil {
				return
			}
			stampOpenAIRequestedReasoningEffort(res, c)
			userAgent := c.GetHeader("User-Agent")
			clientIP := ip.GetClientIP(c)
			inboundEndpoint := GetInboundEndpoint(c)
			upstreamEndpoint := resolveOpenAIUpstreamEndpoint(c, account, res)
			quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
			sessionID := service.ExtractClientSessionID(c)
			cyberBlocked := service.GetOpsCyberPolicy(c) != nil
			if err == nil || openAIForwardResultHasActualBillableUsage(res) {
				guard.MarkSettled()
				recordUsage := func(ctx context.Context) error {
					return h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
						Result:             res,
						APIKey:             apiKey,
						User:               apiKey.User,
						Account:            account,
						Subscription:       subscription,
						InboundEndpoint:    inboundEndpoint,
						UpstreamEndpoint:   upstreamEndpoint,
						UserAgent:          userAgent,
						IPAddress:          clientIP,
						APIKeyService:      h.apiKeyService,
						QuotaPlatform:      quotaPlatform,
						SessionID:          sessionID,
						ChannelUsageFields: clientRequestedUsageFields(c, channelMapping, reqModel, res.UpstreamModel),
						PricingAt:          pricingAt,
						CyberBlocked:       cyberBlocked,
					})
				}
				if probeLease != nil && probeLease.IsLeader() {
					if recordErr := runProbeLeaderUsageTask(c.Request.Context(), probeLease, recordUsage, err == nil); recordErr != nil {
						logger.L().With(
							zap.String("component", "handler.openai_gateway.chat_completions"),
							zap.Int64("user_id", subject.UserID),
							zap.Int64("api_key_id", apiKey.ID),
							zap.Any("group_id", apiKey.GroupID),
							zap.String("model", reqModel),
							zap.Int64("account_id", account.ID),
						).Error("openai_chat_completions.record_usage_failed", zap.Error(recordErr))
					}
				} else {
					h.submitOpenAIUsageRecordTask(c.Request.Context(), res, func(ctx context.Context) {
						if recordErr := recordUsage(ctx); recordErr != nil {
							logger.L().With(
								zap.String("component", "handler.openai_gateway.chat_completions"),
								zap.Int64("user_id", subject.UserID),
								zap.Int64("api_key_id", apiKey.ID),
								zap.Any("group_id", apiKey.GroupID),
								zap.String("model", reqModel),
								zap.Int64("account_id", account.ID),
							).Error("openai_chat_completions.record_usage_failed", zap.Error(recordErr))
						}
					})
				}
			}
		}
		if err != nil {
			guard.ObserveForwardOutcome(err, forwardDeliveredStreamContent(c))
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai_chat_completions.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), false, result.FirstTokenMs)
				return
			} else {
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					if failoverClientGone(c) {
						reqLog.Info("openai_chat_completions.failover_aborted_client_disconnected",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
						)
						return
					}
					if c.Writer.Size() != writerSizeBeforeForward {
						h.gatewayService.ObserveOpenAIAccountHealthFailure(c.Request.Context(), account, err)
						h.handleFailoverExhausted(c, failoverErr, true)
						return
					}
					if failoverErr.ShouldReportAccountScheduleFailure() {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, reqModel, false, nil), false, nil, err)
					}
					if !failoverErr.ShouldRetryNextAccount() {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					// Pool mode: retry on the same account
					if failoverErr.RetryableOnSameAccount {
						retryLimit := effectiveSameAccountRetryLimit(failoverErr, account)
						if sameAccountRetryAllowed(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
							sameAccountRetryCount[account.ID]++
							retryDelay := sameAccountRetryDelayFor(failoverErr, sameAccountRetryCount[account.ID])
							reqLog.Warn("openai_chat_completions.pool_mode_same_account_retry",
								zap.Int64("account_id", account.ID),
								zap.Int("upstream_status", failoverErr.StatusCode),
								zap.Int("retry_limit", retryLimit),
								zap.Int("retry_count", sameAccountRetryCount[account.ID]),
								zap.Duration("retry_delay", retryDelay),
							)
							select {
							case <-c.Request.Context().Done():
								return
							case <-time.After(retryDelay):
							}
							continue
						}
					}
					h.gatewayService.RecordOpenAIAccountSwitch()
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = failoverErr
					if switchCount >= maxAccountSwitches {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					switchCount++
					if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount, &oauth429FailoverState) {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					reqLog.Warn("openai_chat_completions.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", maxAccountSwitches),
					)
					continue
				}
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, reqModel, false, nil), false, nil, err)
				upstreamErrorAlreadyCommunicated := openAIForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
				wroteFallback := false
				if !upstreamErrorAlreadyCommunicated {
					wroteFallback = h.ensureOpenAIStreamReadErrorResponse(c, err, streamStarted)
					if !wroteFallback {
						wroteFallback = h.ensureForwardErrorResponse(c, streamStarted)
					}
				}
				reqLog.Warn("openai_chat_completions.forward_failed",
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
					zap.Error(err),
				)
				submitChatUsage(result)
				return
			}
		}
		if result != nil {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, reqModel, false, result), true, result.FirstTokenMs)
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, reqModel, false, result), true, nil)
		}

		guard.MarkSettled()
		submitChatUsage(result)
		reqLog.Debug("openai_chat_completions.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

// resolveOpenAIUpstreamEndpoint returns the actual upstream endpoint for an
// OpenAI-compatible account. A forwarding result is authoritative because a
// single inbound route may choose raw Chat or a Responses bridge at runtime.
// The account-based derivation remains as a fallback for existing callers and
// forwarding paths that do not report their endpoint yet.
func resolveOpenAIUpstreamEndpoint(c *gin.Context, account *service.Account, result *service.OpenAIForwardResult) string {
	if result != nil {
		if endpoint := strings.TrimSpace(result.UpstreamEndpoint); endpoint != "" {
			return endpoint
		}
	}
	if endpoint := service.GetActualOpenAIUpstreamEndpoint(c); endpoint != "" {
		return endpoint
	}
	// API-Key 账号且未探测到 Responses 支持时直连 Chat Completions。该判断只
	// 对 chat 系入站成立：embeddings / images / alpha_search 等端点没有 Chat
	// Completions 形态，硬编码 chat 会把这些端点的失败错误归因成 chat
	// （usage_logs.upstream_endpoint），与其成功路径的 GetUpstreamEndpoint 口径不一致。
	if account != nil && account.Type == service.AccountTypeAPIKey &&
		!openai_compat.ShouldUseResponsesAPI(account.Extra) &&
		isChatFamilyInboundEndpoint(GetInboundEndpoint(c)) {
		return EndpointChatCompletions
	}
	return GetUpstreamEndpoint(c, account.Platform)
}
