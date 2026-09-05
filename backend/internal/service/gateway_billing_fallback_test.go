package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecideFailureBilling(t *testing.T) {
	tests := []struct {
		name         string
		in           FailureBillingInput
		wantBillable bool
		wantIn       int
		wantOut      int
		wantProv     BillingProvenance
		wantReason   string
	}{
		{
			name: "credential failure never reached the model",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 401, Stage: GatewayFailureStageAccountAuth},
				EstimatedPromptTokens: 900,
			},
			wantBillable: false, wantReason: "credential_failure",
		},
		{
			name: "request rejected before inference",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 400, Scope: GatewayFailureScopeRequest},
				EstimatedPromptTokens: 900,
			},
			wantBillable: false, wantReason: "request_rejected",
		},
		{
			name: "rate limited before any output",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 429, Scope: GatewayFailureScopeAccount},
				EstimatedPromptTokens: 900,
			},
			wantBillable: false, wantReason: "rate_limited_before_inference",
		},
		{
			name: "rate limited after output started is billable",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 429, Scope: GatewayFailureScopeAccount},
				EstimatedPromptTokens: 900, OutputStarted: true, ObservedResponseEvents: 3,
			},
			wantBillable: true, wantIn: 900, wantOut: 3,
			wantProv: BillingProvenanceFailedEstimated, wantReason: "interrupted_stream",
		},
		{
			name: "upstream usage is trusted verbatim",
			in: FailureBillingInput{
				Usage:                 ClaudeUsage{InputTokens: 1200, OutputTokens: 340},
				Err:                   &UpstreamFailoverError{StatusCode: 500, Stage: GatewayFailureStageInference},
				EstimatedPromptTokens: 900, OutputStarted: true,
			},
			wantBillable: true, wantIn: 1200, wantOut: 340,
			wantProv: BillingProvenanceFailedUpstream, wantReason: "upstream_usage",
		},
		{
			name: "upstream usage wins over pre-output rate limit heuristic",
			in: FailureBillingInput{
				Usage: ClaudeUsage{InputTokens: 12, OutputTokens: 1},
				Err:   &UpstreamFailoverError{StatusCode: 429, Scope: GatewayFailureScopeAccount},
			},
			wantBillable: true, wantIn: 12, wantOut: 1,
			wantProv: BillingProvenanceFailedUpstream, wantReason: "upstream_usage",
		},
		{
			// new-api conservativeInterruptedStreamUsage 的关键行为:
			// 已向下游输出过内容时 completion 下限为 1,绝不为 0。
			name: "interrupted stream with zero observed events floors completion at one",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 500, Stage: GatewayFailureStageInference},
				EstimatedPromptTokens: 900, OutputStarted: true, ObservedResponseEvents: 0,
			},
			wantBillable: true, wantIn: 900, wantOut: 1,
			wantProv: BillingProvenanceFailedEstimated, wantReason: "interrupted_stream",
		},
		{
			// 零投递 + 零上游 usage = 上游没产出任何 token,不会向我方计费,
			// 因此绝不能凭估算的 prompt 造出一笔下游扣费。
			name: "no output and no upstream usage is not billable",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 504, Stage: GatewayFailureStageInference},
				EstimatedPromptTokens: 900,
			},
			wantBillable: false, wantReason: "no_output_no_usage",
		},
		{
			// 客户实测缺陷:上游空流(502 empty stream response)零投递、零 usage,
			// Scope 为零值故三条豁免全落空,旧实现在此按整个估算 prompt 扣费。
			// 这类错误还会 failover 重试,一次客户请求可被重复扣多笔。
			name: "empty upstream stream response is not billable",
			in: FailureBillingInput{
				Err: &UpstreamFailoverError{
					StatusCode:             502,
					ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
					RetryableOnSameAccount: true,
				},
				EstimatedPromptTokens: 1500,
			},
			wantBillable: false, wantReason: "no_output_no_usage",
		},
		{
			name: "client disconnect mid stream is billable",
			in: FailureBillingInput{
				ClientDisconnect: true, OutputStarted: true, ObservedResponseEvents: 7,
				EstimatedPromptTokens: 450,
			},
			wantBillable: true, wantIn: 450, wantOut: 7,
			wantProv: BillingProvenanceFailedEstimated, wantReason: "interrupted_stream",
		},
		{
			// 零投递路径统一不计费,不再靠「写一行 cost=0 的用量行」兜底:
			// 估算值非零时那条路径就是实打实的凭空扣费。
			name: "unparseable request with no output is not billable",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 504, Stage: GatewayFailureStageInference},
				EstimatedPromptTokens: 0,
			},
			wantBillable: false, wantReason: "no_output_no_usage",
		},
		{
			name: "negative estimate is clamped to zero",
			in: FailureBillingInput{
				Err:                   &UpstreamFailoverError{StatusCode: 504, Stage: GatewayFailureStageInference},
				EstimatedPromptTokens: -5, OutputStarted: true,
			},
			wantBillable: true, wantIn: 0, wantOut: 1,
			wantProv: BillingProvenanceFailedEstimated, wantReason: "interrupted_stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideFailureBilling(tt.in)
			assert.Equal(t, tt.wantBillable, got.Billable)
			assert.Equal(t, tt.wantReason, got.Reason)
			if !tt.wantBillable {
				return
			}
			assert.Equal(t, tt.wantIn, got.Usage.InputTokens)
			assert.Equal(t, tt.wantOut, got.Usage.OutputTokens)
			assert.Equal(t, tt.wantProv, got.Provenance)
		})
	}
}

func TestDecideFailureBillingPreservesExtendedUpstreamUsage(t *testing.T) {
	usage := ClaudeUsage{
		CacheCreation5mTokens: 4,
		CacheCreation1hTokens: 5,
		ImageInputTokens:      6,
		ImageOutputTokens:     7,
	}

	got := DecideFailureBilling(FailureBillingInput{
		Usage: usage,
		Err:   &UpstreamFailoverError{StatusCode: 500, Stage: GatewayFailureStageInference},
	})

	assert.True(t, got.Billable)
	assert.Equal(t, BillingProvenanceFailedUpstream, got.Provenance)
	assert.Equal(t, usage, got.Usage)
}

func TestDecideFailureBillingPreservesPartialImageAndSearch(t *testing.T) {
	got := DecideFailureBilling(FailureBillingInput{
		RequestID:          "upstream-req-7",
		BillingModel:       "gpt-image-2",
		UpstreamModel:      "gpt-image-2",
		Usage:              ClaudeUsage{InputTokens: 12},
		SearchCount:        7,
		ImageCount:         2,
		ImageSize:          "2K",
		ImageInputSize:     "1024x1024",
		ImageOutputSize:    "2048x2048",
		ImageOutputSizes:   []string{"2048x2048", "2048x2048"},
		ImageSizeSource:    "output",
		ImageSizeBreakdown: map[string]int{"2K": 2},
		Err:                &UpstreamFailoverError{StatusCode: 502, Stage: GatewayFailureStageInference},
	})

	require.True(t, got.Billable)
	require.Equal(t, BillingProvenanceFailedUpstream, got.Provenance)
	require.Equal(t, "upstream-req-7", got.RequestID)
	require.Equal(t, "gpt-image-2", got.BillingModel)
	require.Equal(t, 7, got.SearchCount)
	require.Equal(t, 2, got.ImageCount)
	require.Equal(t, "2K", got.ImageSize)
	require.Equal(t, []string{"2048x2048", "2048x2048"}, got.ImageOutputSizes)
	require.Equal(t, map[string]int{"2K": 2}, got.ImageSizeBreakdown)
}

func TestDecideFailureBillingSearch(t *testing.T) {
	t.Run("search only uses real upstream provenance", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			RequestID:     "search-request-1",
			BillingModel:  "grok-4",
			UpstreamModel: "grok-4-latest",
			SearchCount:   3,
		})
		require.True(t, got.Billable)
		assert.Equal(t, "search-request-1", got.RequestID)
		assert.Equal(t, "grok-4", got.BillingModel)
		assert.Equal(t, "grok-4-latest", got.UpstreamModel)
		assert.Equal(t, 3, got.SearchCount)
		assert.Equal(t, BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, "upstream_search", got.Reason)
		assert.Zero(t, got.Usage.InputTokens)
		assert.Zero(t, got.Usage.OutputTokens)
	})

	t.Run("real usage and search share one decision", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			Usage:       ClaudeUsage{InputTokens: 9, OutputTokens: 2},
			SearchCount: 4,
		})
		require.True(t, got.Billable)
		assert.Equal(t, 4, got.SearchCount)
		assert.Equal(t, BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, "upstream_usage", got.Reason)
	})

	t.Run("estimated interrupted tokens keep estimated provenance", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			SearchCount:            2,
			OutputStarted:          true,
			EstimatedPromptTokens:  13,
			ObservedResponseEvents: 5,
		})
		require.True(t, got.Billable)
		assert.Equal(t, 2, got.SearchCount)
		assert.Equal(t, 13, got.Usage.InputTokens)
		assert.Equal(t, 5, got.Usage.OutputTokens)
		assert.Equal(t, BillingProvenanceFailedEstimated, got.Provenance)
		assert.Equal(t, "interrupted_stream_with_upstream_search", got.Reason)
	})

	t.Run("negative search count remains non billable", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{SearchCount: -1})
		assert.False(t, got.Billable)
		assert.Equal(t, "no_output_no_usage", got.Reason)
	})
}

func TestDecideFailureBillingUpstreamUsageOnly(t *testing.T) {
	t.Run("explicit usage remains billable", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			UpstreamUsageOnly: true,
			Usage:             ClaudeUsage{InputTokens: 21, OutputTokens: 5},
		})
		require.True(t, got.Billable)
		assert.Equal(t, ClaudeUsage{InputTokens: 21, OutputTokens: 5}, got.Usage)
		assert.Equal(t, BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, "upstream_usage", got.Reason)
	})

	t.Run("search-only action remains billable without estimated tokens", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			UpstreamUsageOnly:      true,
			SearchCount:            2,
			OutputStarted:          true,
			EstimatedPromptTokens:  900,
			ObservedResponseEvents: 4,
		})
		require.True(t, got.Billable)
		assert.Equal(t, 2, got.SearchCount)
		assert.Zero(t, got.Usage.InputTokens)
		assert.Zero(t, got.Usage.OutputTokens)
		assert.Equal(t, BillingProvenanceFailedUpstream, got.Provenance)
		assert.Equal(t, "upstream_search", got.Reason)
	})

	t.Run("output without usage is not estimated", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			UpstreamUsageOnly:      true,
			OutputStarted:          true,
			EstimatedPromptTokens:  900,
			ObservedResponseEvents: 4,
		})
		assert.False(t, got.Billable)
		assert.Equal(t, "estimated_disabled", got.Reason)
	})

	t.Run("client disconnect without usage is not estimated", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			UpstreamUsageOnly:      true,
			ClientDisconnect:       true,
			EstimatedPromptTokens:  900,
			ObservedResponseEvents: 4,
		})
		assert.False(t, got.Billable)
		assert.Equal(t, "estimated_disabled", got.Reason)
	})

	t.Run("disabled policy preserves estimated interruption billing", func(t *testing.T) {
		got := DecideFailureBilling(FailureBillingInput{
			UpstreamUsageOnly:      false,
			OutputStarted:          true,
			EstimatedPromptTokens:  900,
			ObservedResponseEvents: 4,
		})
		require.True(t, got.Billable)
		assert.Equal(t, 900, got.Usage.InputTokens)
		assert.Equal(t, 4, got.Usage.OutputTokens)
		assert.Equal(t, BillingProvenanceFailedEstimated, got.Provenance)
		assert.Equal(t, "interrupted_stream", got.Reason)
	})
}
