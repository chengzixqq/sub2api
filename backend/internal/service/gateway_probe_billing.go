package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

var ErrSyntheticProbeBillingUnavailable = errors.New("synthetic probe billing unavailable")
var ErrSyntheticProbePricingUnavailable = errors.New("synthetic probe pricing unavailable")
var ErrSyntheticProbeUsagePersistence = errors.New("synthetic probe usage persistence failed")

// syntheticCostCoversUsage is intentionally stricter than the legacy billing
// path.  A zero-value CostBreakdown can mean that an interval-only card did
// not match the probe's actual token count, or that pricing resolution failed
// and was converted to a compatibility placeholder.  Neither case is safe
// for a locally synthesized success response.
func syntheticCostCoversUsage(cost *CostBreakdown, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) bool {
	if cost == nil || cost.BillingMode != string(BillingModeToken) {
		return false
	}
	if inputTokens > 0 && cost.InputCost <= 0 {
		return false
	}
	if outputTokens > 0 && cost.OutputCost <= 0 {
		return false
	}
	if cacheCreationTokens > 0 && cost.CacheCreationCost <= 0 {
		return false
	}
	if cacheReadTokens > 0 && cost.CacheReadCost <= 0 {
		return false
	}
	return true
}

// SyntheticProbeBillingReady reports whether the strict synthetic path has
// the transactional billing and usage-log dependencies it needs. A missing
// dependency must not be treated as a pricing miss: promoting such a request
// to a real upstream would only hide an installation/configuration failure.
func (s *GatewayService) SyntheticProbeBillingReady() bool {
	return s != nil && s.billingService != nil && s.usageBillingRepo != nil &&
		s.usageLogRepo != nil && s.billingCacheService != nil && s.deferredService != nil &&
		hasAtomicUsageBillingRepository(s.usageBillingRepo) &&
		(s.cfg == nil || s.cfg.RunMode != config.RunModeSimple)
}

func hasExplicitTokenPrice(p *ChannelModelPricing) bool {
	if p == nil {
		return false
	}
	if p.InputPrice != nil && p.OutputPrice != nil {
		return true
	}
	for _, interval := range p.Intervals {
		if interval.InputPrice != nil && interval.OutputPrice != nil {
			return true
		}
	}
	return false
}

// deterministicTokenPricing rejects an empty group/channel token card. The
// resolver intentionally creates a zero ModelPricing for such a card; using it
// for a synthetic response would turn an unknown model into a free request.
func deterministicTokenPricing(resolved *ResolvedPricing, model string, billing *BillingService) bool {
	if resolved == nil || resolved.Mode != BillingModeToken || billing == nil {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" || resolved.BasePricing == nil {
		return false
	}
	if resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel {
		if hasExplicitTokenPrice(resolved.channelPricing) {
			return true
		}
		// A channel card may intentionally only set tier metadata while the
		// model catalog supplies the actual standard prices.
		return billing.HasIdentifiedTokenPricing(model)
	}
	return billing.HasIdentifiedTokenPricing(model)
}

// CanBillSyntheticProbe is a fail-closed pricing admission check.  A local
// probe response must never be returned for a model that the normal billing
// path cannot identify deterministically; such requests stay on the real
// upstream path instead of silently becoming free.
func (s *GatewayService) CanBillSyntheticProbe(ctx context.Context, apiKey *APIKey, model string) bool {
	if !s.SyntheticProbeBillingReady() || apiKey == nil {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if resolved := s.resolveChannelPricing(ctx, model, apiKey); resolved != nil {
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
		if resolved := s.resolveChannelPricing(ctx, candidate, apiKey); resolved != nil {
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

// SyntheticProbeUsageInput describes one user-visible probe request whose
// response may have been synthesized by probe coalescing. It intentionally
// carries the same identity and pricing inputs as the normal gateway billing
// path, so followers are charged independently and idempotently.
//
// Account is a snapshot supplied by the coordinator. This method never
// acquires an account slot or performs upstream I/O; followers may reuse the
// leader's snapshot solely for billing dimensions and audit attribution.
type SyntheticProbeUsageInput struct {
	APIKey       *APIKey
	User         *User
	Account      *Account
	Subscription *UserSubscription

	RequestID           string
	Model               string
	UpstreamModel       string
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	Stream              bool
	Duration            time.Duration
	FirstTokenMs        *int

	PricingAt          time.Time
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string
	IPAddress          string
	SessionID          string
	RequestPayloadHash string
	APIKeyService      APIKeyQuotaUpdater
	QuotaPlatform      string

	// ProbeCoalesced is true for a follower response. ProbeLeaderRequestID
	// identifies the request that performed the real upstream probe.
	ProbeCoalesced       bool
	ProbeLeaderRequestID string
	// ProviderCostRecorded must be true for a leader and false for followers.
	// User billing is performed in both cases.
	ProviderCostRecorded bool

	// ChannelUsageFields must match the normal gateway mapping snapshot. A
	// follower is billed against the same effective model/channel as the
	// leader, while retaining the client's requested model in the audit row.
	ChannelUsageFields
}

// RecordSyntheticProbeUsage records and charges one synthetic probe request.
// It reuses recordUsageCore, including pricing, subscription/balance/API-key
// quota updates, and usage-billing idempotency keyed by RequestID + API key.
// The method deliberately does not select an account or consume an account
// concurrency slot; the coordinator owns those concerns.
func (s *GatewayService) RecordSyntheticProbeUsage(ctx context.Context, in *SyntheticProbeUsageInput) error {
	if s == nil {
		return errors.New("gateway service is nil")
	}
	if in == nil {
		return errors.New("synthetic probe usage input is nil")
	}
	if in.APIKey == nil || in.User == nil || in.Account == nil {
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
	if requestID == "" {
		return errors.New("synthetic probe request id is required")
	}
	if model == "" {
		return errors.New("synthetic probe model is required")
	}
	billingModel := model
	if strings.TrimSpace(in.ChannelMappedModel) != "" {
		billingModel = strings.TrimSpace(in.ChannelMappedModel)
	}
	if !s.CanBillSyntheticProbe(ctx, in.APIKey, billingModel) {
		return ErrSyntheticProbePricingUnavailable
	}

	result := &ForwardResult{
		RequestID:     requestID,
		Model:         model,
		UpstreamModel: strings.TrimSpace(in.UpstreamModel),
		Stream:        in.Stream,
		Duration:      in.Duration,
		FirstTokenMs:  in.FirstTokenMs,
		Usage: ClaudeUsage{
			InputTokens:              max(in.InputTokens, 0),
			OutputTokens:             max(in.OutputTokens, 0),
			CacheCreationInputTokens: max(in.CacheCreationTokens, 0),
			CacheReadInputTokens:     max(in.CacheReadTokens, 0),
		},
	}

	return s.recordUsageCore(ctx, &recordUsageCoreInput{
		Result:                     result,
		APIKey:                     in.APIKey,
		User:                       in.User,
		Account:                    in.Account,
		Subscription:               in.Subscription,
		PricingAt:                  in.PricingAt,
		InboundEndpoint:            in.InboundEndpoint,
		UpstreamEndpoint:           in.UpstreamEndpoint,
		UserAgent:                  in.UserAgent,
		IPAddress:                  in.IPAddress,
		SessionID:                  in.SessionID,
		RequestPayloadHash:         in.RequestPayloadHash,
		APIKeyService:              in.APIKeyService,
		QuotaPlatform:              in.QuotaPlatform,
		ProbeCoalesced:             in.ProbeCoalesced,
		ProbeLeaderRequestID:       strings.TrimSpace(in.ProbeLeaderRequestID),
		ProviderCostRecorded:       in.ProviderCostRecorded,
		RequireUsageLogPersistence: true,
		ChannelUsageFields:         in.ChannelUsageFields,
	}, &recordUsageOpts{})
}
