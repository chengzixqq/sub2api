package service

// GatewayUpstreamDeliveredKey 是 gin.Context 里标记「本请求上游已向客户端投递过真实推理内容」的 key。
// service 层在观测到上游第一帧时 c.Set(GatewayUpstreamDeliveredKey, true);handler 层据此判定是否计费。
//
// 之所以要显式传标记而不是让 handler 自己看 writer 字节数：sub2api 自己在流式读循环挂死时也会写
// SSE 帧(keepalive ping、stream_timeout/stream_read_error 等 sendErrorEvent 错误帧),handler 层区分
// 不了这些自造帧和上游真实投递的帧,只有 service 层的读循环自己知道这个事实。
const GatewayUpstreamDeliveredKey = "gateway.upstream_delivered"

// BillingProvenance 标记用量行的计费来源与成败,写入 usage_logs.billing_provenance。
// 空值表示成功且用量来自上游,与历史行一致。
type BillingProvenance string

const (
	// BillingProvenanceEstimated 成功请求,但上游未给 usage 帧,用量为本地估算。
	BillingProvenanceEstimated BillingProvenance = "estimated"
	// BillingProvenanceFailedUpstream 失败请求,但拿到了上游真实 usage。
	BillingProvenanceFailedUpstream BillingProvenance = "failed_upstream"
	// BillingProvenanceFailedEstimated 失败请求,用量为本地估算。
	// 只用于「已向下游投递过内容、但缺终止 usage 帧」的中断流;零投递请求不计费,
	// 不会产生这个来源的行。
	BillingProvenanceFailedEstimated BillingProvenance = "failed_estimated"
)

// FailureBillingInput 是失败计费决策的全部输入。刻意只收标量与既有值类型,
// 不收 gin.Context / ent client,使决策可独立单测。
type FailureBillingInput struct {
	RequestID              string
	BillingModel           string
	UpstreamModel          string
	Usage                  ClaudeUsage
	SearchCount            int
	ImageCount             int
	ImageSize              string
	ImageInputSize         string
	ImageOutputSize        string
	ImageOutputSizes       []string
	ImageSizeSource        string
	ImageSizeBreakdown     map[string]int
	Err                    *UpstreamFailoverError
	EstimatedPromptTokens  int
	OutputStarted          bool
	ObservedResponseEvents int
	ClientDisconnect       bool
	// UpstreamUsageOnly disables local estimated settlement. When enabled,
	// failure billing is limited to explicit upstream token/image usage or
	// another explicit billable upstream action such as a completed search.
	UpstreamUsageOnly bool
}

// FailureBillingDecision 是决策结果。Billable 为 false 时其余字段无意义。
type FailureBillingDecision struct {
	Billable           bool
	RequestID          string
	BillingModel       string
	UpstreamModel      string
	Usage              ClaudeUsage
	SearchCount        int
	ImageCount         int
	ImageSize          string
	ImageInputSize     string
	ImageOutputSize    string
	ImageOutputSizes   []string
	ImageSizeSource    string
	ImageSizeBreakdown map[string]int
	Provenance         BillingProvenance
	Reason             string
}

// DecideFailureBilling 判定一次失败/中断的转发是否应当计费、按什么用量计费。
//
// 语义逐条对齐 new-api:
//   - 上游给了真实 usage 就照用,不估算;
//   - 已向下游投递过内容但没有终止 usage 帧时,completion 下限为 1(new-api
//     conservativeInterruptedStreamUsage),防止一次已部分投递的响应变成免费请求;
//   - 从未投递过内容且没有上游 usage 时不计费——上游零产出不会向我方收费,
//     下游也不能凭估算的 prompt 凭空扣费。
//
// 后两条不可合并:合并成「一律按估算计费」会让空返回凭空产生扣费;合并成
// 「一律不计费」会让已部分投递的流中断重新变免费。
//
// UpstreamUsageOnly 开启时保留上述真实 usage 与 search/image 等明确上游动作，
// 但跳过所有 failed_estimated 兜底，不因已投递内容或客户端断开而凭本地估算扣费。
func DecideFailureBilling(in FailureBillingInput) FailureBillingDecision {
	searchCount := in.SearchCount
	if searchCount < 0 {
		searchCount = 0
	}

	// Real upstream usage is authoritative even when the terminal response is an
	// error. It must win over estimates and status-based no-charge heuristics.
	imageCount := in.ImageCount
	if imageCount < 0 {
		imageCount = 0
	}
	baseDecision := failureBillingDecisionBase(in, searchCount, imageCount)
	if in.Usage.InputTokens > 0 || in.Usage.OutputTokens > 0 ||
		in.Usage.CacheReadInputTokens > 0 || in.Usage.CacheCreationInputTokens > 0 ||
		in.Usage.CacheCreation5mTokens > 0 || in.Usage.CacheCreation1hTokens > 0 ||
		in.Usage.ImageInputTokens > 0 || in.Usage.ImageOutputTokens > 0 || imageCount > 0 {
		baseDecision.Billable = true
		baseDecision.Usage = in.Usage
		baseDecision.Provenance = BillingProvenanceFailedUpstream
		baseDecision.Reason = "upstream_usage"
		return baseDecision
	}

	// A completed upstream search is a real billable action even when the
	// terminal response contains no token usage. If content was delivered (or
	// the client disconnected mid-stream), keep the existing conservative token
	// estimate and add the real search surcharge to the same settlement row.
	if searchCount > 0 && (in.OutputStarted || in.ClientDisconnect) {
		if in.UpstreamUsageOnly {
			// The search itself is an explicit upstream billable action, but the
			// token portion would be locally estimated. Keep the action for the
			// settlement row and let the dedicated search branch below preserve
			// its real upstream provenance.
			baseDecision.Billable = true
			baseDecision.Provenance = BillingProvenanceFailedUpstream
			baseDecision.Reason = "upstream_search"
			return baseDecision
		}
		promptTokens := in.EstimatedPromptTokens
		if promptTokens < 0 {
			promptTokens = 0
		}
		completionTokens := in.ObservedResponseEvents
		if completionTokens < 1 {
			completionTokens = 1
		}
		baseDecision.Billable = true
		baseDecision.Usage = ClaudeUsage{InputTokens: promptTokens, OutputTokens: completionTokens}
		baseDecision.Provenance = BillingProvenanceFailedEstimated
		baseDecision.Reason = "interrupted_stream_with_upstream_search"
		return baseDecision
	}

	if searchCount > 0 {
		baseDecision.Billable = true
		baseDecision.Provenance = BillingProvenanceFailedUpstream
		baseDecision.Reason = "upstream_search"
		return baseDecision
	}

	// 不计费边界:请求从未进入推理,上游不会计费。
	if in.Err != nil {
		switch {
		case in.Err.IsCredentialFailure():
			return FailureBillingDecision{Reason: "credential_failure"}
		case in.Err.Scope == GatewayFailureScopeRequest:
			return FailureBillingDecision{Reason: "request_rejected"}
		case in.Err.StatusCode == 429 && !in.OutputStarted:
			return FailureBillingDecision{Reason: "rate_limited_before_inference"}
		}
	}

	// 零投递 + 零上游 usage = 上游没产出任何 token,不会向我方计费,下游也不能扣。
	// 上游空流(502 "empty stream response")正落在这里:它的 Scope 是零值、
	// Stage 不是 AccountAuth、状态码不是 429,上面三条豁免全部不命中,若继续往下
	// 走估算分支就会凭请求体估算的 prompt 造出一笔纯凭空扣费;这类错误还会 failover
	// 重试其他账号,一次客户请求能被重复扣多笔。
	//
	// 这条判据不能与下面的中断流兜底合并:new-api 的 conservativeInterruptedStreamUsage
	// 前提是「已经向下游投递过内容」,估算只用于补它缺失的终止 usage 帧,而不是
	// 给零产出请求造费用。ClientDisconnect 同样算已产出侧(客户端先断,上游已在推理)。
	if !in.OutputStarted && !in.ClientDisconnect {
		return FailureBillingDecision{Reason: "no_output_no_usage"}
	}
	if in.UpstreamUsageOnly {
		return FailureBillingDecision{Reason: "estimated_disabled"}
	}

	promptTokens := in.EstimatedPromptTokens
	if promptTokens < 0 {
		promptTokens = 0
	}

	// 到这里必然是「投递过内容但没拿到终止 usage 帧」的中断流:completion 下限为 1,
	// 防止一次已部分投递的响应变成免费请求(new-api conservativeInterruptedStreamUsage)。
	completionTokens := in.ObservedResponseEvents
	if completionTokens < 1 {
		completionTokens = 1
	}

	baseDecision.Billable = true
	baseDecision.Usage = ClaudeUsage{InputTokens: promptTokens, OutputTokens: completionTokens}
	baseDecision.Provenance = BillingProvenanceFailedEstimated
	baseDecision.Reason = "interrupted_stream"
	return baseDecision
}

func failureBillingDecisionBase(in FailureBillingInput, searchCount, imageCount int) FailureBillingDecision {
	return FailureBillingDecision{
		RequestID:          in.RequestID,
		BillingModel:       in.BillingModel,
		UpstreamModel:      in.UpstreamModel,
		SearchCount:        searchCount,
		ImageCount:         imageCount,
		ImageSize:          in.ImageSize,
		ImageInputSize:     in.ImageInputSize,
		ImageOutputSize:    in.ImageOutputSize,
		ImageOutputSizes:   append([]string(nil), in.ImageOutputSizes...),
		ImageSizeSource:    in.ImageSizeSource,
		ImageSizeBreakdown: cloneFailureImageSizeBreakdown(in.ImageSizeBreakdown),
	}
}

func cloneFailureImageSizeBreakdown(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for size, count := range in {
		out[size] = count
	}
	return out
}
