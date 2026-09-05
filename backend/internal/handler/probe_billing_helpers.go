package handler

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func probeAccountSnapshot(account *service.Account) *service.Account {
	if account == nil {
		return nil
	}
	copy := *account
	return &copy
}

func probeChannelUsageFields(mapping service.ChannelMappingResult, candidate ProbeCandidate) service.ChannelUsageFields {
	upstreamModel := candidate.Model
	if mapping.Mapped && strings.TrimSpace(mapping.MappedModel) != "" {
		upstreamModel = strings.TrimSpace(mapping.MappedModel)
	}
	return mapping.ToUsageFields(candidate.Model, upstreamModel)
}

func (h *GatewayHandler) billSyntheticProbe(c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription, body []byte, candidate ProbeCandidate, account *service.Account, leaderRequestID string, mapping service.ChannelMappingResult) error {
	if h == nil || h.gatewayService == nil || c == nil || apiKey == nil || apiKey.User == nil {
		return service.ErrSyntheticProbeBillingUnavailable
	}
	ctx := c.Request.Context()
	channelFields := probeChannelUsageFields(mapping, candidate)
	billingModel := candidate.Model
	if strings.TrimSpace(channelFields.ChannelMappedModel) != "" {
		billingModel = channelFields.ChannelMappedModel
	}
	if !h.gatewayService.CanBillSyntheticProbe(ctx, apiKey, billingModel) {
		if h.gatewayService.SyntheticProbeBillingReady() {
			return service.ErrSyntheticProbePricingUnavailable
		}
		return service.ErrSyntheticProbeBillingUnavailable
	}
	requestID := requestIDForProbe(c)
	return h.gatewayService.RecordSyntheticProbeUsage(ctx, &service.SyntheticProbeUsageInput{
		APIKey:               apiKey,
		User:                 apiKey.User,
		Account:              probeAccountSnapshot(account),
		Subscription:         subscription,
		RequestID:            requestID,
		Model:                candidate.Model,
		UpstreamModel:        channelFields.ChannelMappedModel,
		InputTokens:          candidate.InputTokens,
		OutputTokens:         candidate.OutputTokens,
		PricingAt:            time.Now(),
		InboundEndpoint:      c.Request.URL.Path,
		UpstreamEndpoint:     c.Request.URL.Path,
		UserAgent:            c.GetHeader("User-Agent"),
		IPAddress:            ip.GetClientIP(c),
		RequestPayloadHash:   service.HashUsageRequestPayload(body),
		APIKeyService:        h.apiKeyService,
		QuotaPlatform:        service.QuotaPlatform(ctx, apiKey),
		ProbeCoalesced:       true,
		ProbeLeaderRequestID: strings.TrimSpace(leaderRequestID),
		ProviderCostRecorded: false,
		ChannelUsageFields:   channelFields,
	})
}

func (h *OpenAIGatewayHandler) billSyntheticProbe(c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription, body []byte, candidate ProbeCandidate, account *service.Account, leaderRequestID string, mapping service.ChannelMappingResult) error {
	if h == nil || h.gatewayService == nil || c == nil || apiKey == nil || apiKey.User == nil {
		return service.ErrSyntheticProbeBillingUnavailable
	}
	ctx := c.Request.Context()
	channelFields := probeChannelUsageFields(mapping, candidate)
	billingModel := candidate.Model
	if strings.TrimSpace(channelFields.ChannelMappedModel) != "" {
		billingModel = channelFields.ChannelMappedModel
	}
	if h.gatewayService == nil || !h.gatewayService.CanBillSyntheticProbe(ctx, apiKey, billingModel) {
		if h.gatewayService != nil && h.gatewayService.SyntheticProbeBillingReady() {
			return service.ErrSyntheticProbePricingUnavailable
		}
		return service.ErrSyntheticProbeBillingUnavailable
	}
	return h.gatewayService.RecordSyntheticProbeUsage(ctx, &service.SyntheticProbeUsageInput{
		APIKey:               apiKey,
		User:                 apiKey.User,
		Account:              probeAccountSnapshot(account),
		Subscription:         subscription,
		RequestID:            requestIDForProbe(c),
		Model:                candidate.Model,
		UpstreamModel:        channelFields.ChannelMappedModel,
		InputTokens:          candidate.InputTokens,
		OutputTokens:         candidate.OutputTokens,
		PricingAt:            time.Now(),
		InboundEndpoint:      c.Request.URL.Path,
		UpstreamEndpoint:     c.Request.URL.Path,
		UserAgent:            c.GetHeader("User-Agent"),
		IPAddress:            ip.GetClientIP(c),
		RequestPayloadHash:   service.HashUsageRequestPayload(body),
		APIKeyService:        h.apiKeyService,
		QuotaPlatform:        service.QuotaPlatform(ctx, apiKey),
		ProbeCoalesced:       true,
		ProbeLeaderRequestID: strings.TrimSpace(leaderRequestID),
		ProviderCostRecorded: false,
		ChannelUsageFields:   channelFields,
	})
}
