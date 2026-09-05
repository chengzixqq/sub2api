package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// SyntheticProbeBillingReady mirrors the generic gateway's strict dependency
// check. OpenAI synthetic responses must never fall back to the legacy billing
// path, which can intentionally swallow repository errors.
func (s *OpenAIGatewayService) SyntheticProbeBillingReady() bool {
	return s != nil && s.billingService != nil && s.usageBillingRepo != nil &&
		s.usageLogRepo != nil && s.billingCacheService != nil && s.deferredService != nil &&
		hasAtomicUsageBillingRepository(s.usageBillingRepo) &&
		(s.cfg == nil || s.cfg.RunMode != config.RunModeSimple)
}

// CanBillSyntheticProbe is the OpenAI pricing admission check used before a
// follower receives a locally generated response.  Unknown CN/provider model
// names fail closed and are sent through the ordinary upstream path.
func (s *OpenAIGatewayService) CanBillSyntheticProbe(ctx context.Context, apiKey *APIKey, model string) bool {
	if !s.SyntheticProbeBillingReady() || apiKey == nil {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, model, apiKey); resolved != nil {
		if resolved.Mode != BillingModeToken {
			return false
		}
		if deterministicTokenPricing(resolved, model, s.billingService) {
			return true
		}
		return false
	}
	if s.billingService.HasIdentifiedTokenPricing(model) {
		return true
	}
	for _, candidate := range usageBillingModelCandidates(model) {
		if resolved := s.resolveOpenAIChannelPricing(ctx, candidate, apiKey); resolved != nil {
			if resolved.Mode != BillingModeToken {
				return false
			}
			if deterministicTokenPricing(resolved, candidate, s.billingService) {
				return true
			}
			continue
		}
		if s.billingService.HasIdentifiedTokenPricing(candidate) {
			return true
		}
	}
	return false
}

// RecordSyntheticProbeUsage is the OpenAI equivalent of the Anthropic/generic
// probe billing entry point.  It deliberately reuses RecordUsage so pricing,
// subscription consumption, API-key quota and request-id idempotency stay on
// the existing path.
func (s *OpenAIGatewayService) RecordSyntheticProbeUsage(ctx context.Context, in *SyntheticProbeUsageInput) error {
	if s == nil {
		return errors.New("openai gateway service is nil")
	}
	if in == nil || in.APIKey == nil || in.User == nil || in.Account == nil {
		return errors.New("synthetic probe usage requires api key, user, and account snapshot")
	}
	if s.usageLogRepo == nil {
		return ErrSyntheticProbeBillingUnavailable
	}
	if !hasAtomicUsageBillingRepository(s.usageBillingRepo) || s.billingCacheService == nil || s.deferredService == nil || (s.cfg != nil && s.cfg.RunMode == config.RunModeSimple) {
		return ErrSyntheticProbeBillingUnavailable
	}
	requestID := strings.TrimSpace(in.RequestID)
	model := strings.TrimSpace(in.Model)
	if requestID == "" || model == "" {
		return errors.New("synthetic probe request id and model are required")
	}
	billingModel := model
	if strings.TrimSpace(in.ChannelMappedModel) != "" {
		billingModel = strings.TrimSpace(in.ChannelMappedModel)
	}
	if !s.CanBillSyntheticProbe(ctx, in.APIKey, billingModel) {
		return ErrSyntheticProbePricingUnavailable
	}
	result := &OpenAIForwardResult{
		RequestID:     requestID,
		Model:         model,
		UpstreamModel: strings.TrimSpace(in.UpstreamModel),
		Stream:        in.Stream,
		Duration:      in.Duration,
		FirstTokenMs:  in.FirstTokenMs,
		Usage: OpenAIUsage{
			InputTokens:              max(in.InputTokens, 0),
			OutputTokens:             max(in.OutputTokens, 0),
			CacheCreationInputTokens: max(in.CacheCreationTokens, 0),
			CacheReadInputTokens:     max(in.CacheReadTokens, 0),
		},
	}
	return s.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:                     result,
		APIKey:                     in.APIKey,
		User:                       in.User,
		Account:                    in.Account,
		Subscription:               in.Subscription,
		InboundEndpoint:            in.InboundEndpoint,
		UpstreamEndpoint:           in.UpstreamEndpoint,
		UserAgent:                  in.UserAgent,
		IPAddress:                  in.IPAddress,
		SessionID:                  in.SessionID,
		RequestPayloadHash:         in.RequestPayloadHash,
		APIKeyService:              in.APIKeyService,
		QuotaPlatform:              in.QuotaPlatform,
		PricingAt:                  in.PricingAt,
		ProbeCoalesced:             in.ProbeCoalesced,
		ProbeLeaderRequestID:       strings.TrimSpace(in.ProbeLeaderRequestID),
		ProviderCostRecorded:       in.ProviderCostRecorded,
		RequireUsageLogPersistence: true,
		ChannelUsageFields:         in.ChannelUsageFields,
	})
}
