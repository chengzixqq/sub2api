package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

var ErrAdvisorPricingUnavailable = errors.New("advisor_pricing_unavailable")

// ResolveTokenOnly resolves an independently priced model. A per-request card
// belongs to the outer request and cannot be reinterpreted as a token card.
// Catalog fallback is allowed only for an identified model, never a family guess.
func (r *ModelPricingResolver) ResolveTokenOnly(ctx context.Context, input PricingInput) (*ResolvedPricing, error) {
	if r == nil || r.billingService == nil || strings.TrimSpace(input.Model) == "" {
		return nil, ErrAdvisorPricingUnavailable
	}
	var config *ChannelModelPricing
	source := PricingSourceLiteLLM
	if card := matchGroupModelPricing(input.Group, input.Model); card != nil && (card.BillingMode == "" || card.BillingMode == BillingModeToken) {
		copy := card.Clone()
		copy.Intervals = nil
		config, source = &copy, PricingSourceGroup
	} else if input.GroupID != nil {
		if card := r.lookupChannelPricingNormalized(ctx, *input.GroupID, input.Model); card != nil && (card.BillingMode == "" || card.BillingMode == BillingModeToken) {
			config, source = card, PricingSourceChannel
		}
	}
	var base *ModelPricing
	normalized := strings.ToLower(strings.TrimSpace(input.Model))
	if r.billingService.pricingService != nil {
		if p := r.billingService.pricingService.GetIdentifiedModelPricing(normalized); p != nil && !p.TokenPricingAbsent {
			base, _ = r.billingService.GetModelPricing(normalized)
		}
	}
	if base == nil {
		base = r.billingService.fallbackPrices[normalized]
		if base != nil && config == nil {
			source = PricingSourceFallback
		}
	}
	if base == nil && (config == nil || config.InputPrice == nil || config.OutputPrice == nil) {
		return nil, fmt.Errorf("%w: %s", ErrAdvisorPricingUnavailable, input.Model)
	}
	resolved := &ResolvedPricing{Mode: BillingModeToken, BasePricing: base, Source: source,
		longContextPricingEnabled: input.Group == nil || input.Group.LongContextPricingEnabled,
		SupportsCacheBreakdown:    base != nil && base.SupportsCacheBreakdown}
	if config != nil {
		resolved.channelPricing = config
		r.applyTokenOverrides(config, resolved)
	}
	return resolved, nil
}

func advisorPricingCoversUsage(r *ModelPricingResolver, resolved *ResolvedPricing, model string, tokens UsageTokens, preflight bool) bool {
	if resolved == nil || resolved.BasePricing == nil || r == nil || r.billingService == nil {
		return false
	}
	// Without a catalog, absence is not an explicit free rate. A declared advisor
	// can create or read caches, so preflight requires those rates as well.
	if !r.billingService.HasIdentifiedTokenPricing(model) {
		card := resolved.channelPricing
		if card == nil || card.InputPrice == nil || card.OutputPrice == nil {
			return false
		}
		if (preflight || tokens.CacheCreationTokens > 0) && card.CacheWritePrice == nil {
			return false
		}
		if (preflight || tokens.CacheReadTokens > 0) && card.CacheReadPrice == nil {
			return false
		}
	}
	contextTokens := tokens.InputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
	if contextTokens < 0 {
		return false
	}
	pricing := r.GetIntervalPricing(resolved, contextTokens)
	if pricing == nil {
		return false
	}
	for _, price := range []float64{pricing.InputPricePerToken, pricing.OutputPricePerToken, pricing.CacheCreationPricePerToken,
		pricing.CacheReadPricePerToken, pricing.CacheCreation5mPrice, pricing.CacheCreation1hPrice} {
		if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
			return false
		}
	}
	if price := pricing.CacheCreation12hPricePerToken; price != nil && (math.IsNaN(*price) || math.IsInf(*price, 0) || *price < 0) {
		return false
	}
	return true
}

func (s *GatewayService) advisorResolver() *ModelPricingResolver {
	if s == nil {
		return nil
	}
	if s.resolver != nil {
		return s.resolver
	}
	return NewModelPricingResolver(s.channelService, s.billingService)
}

// ValidateAdvisorPricing is called before any upstream request. Response model
// changes are handled separately during settlement, without retry or debt.
func (s *GatewayService) ValidateAdvisorPricing(ctx context.Context, key *APIKey, body []byte) error {
	var validationErr error
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if !strings.HasPrefix(tool.Get("type").String(), "advisor_") {
			return true
		}
		input := PricingInput{Model: strings.TrimSpace(tool.Get("model").String())}
		if key != nil {
			input.GroupID, input.Group = key.GroupID, key.Group
		}
		resolver := s.advisorResolver()
		resolved, err := resolver.ResolveTokenOnly(ctx, input)
		if err != nil || !advisorPricingCoversUsage(resolver, resolved, input.Model, UsageTokens{}, true) {
			validationErr = fmt.Errorf("%w: %s", ErrAdvisorPricingUnavailable, input.Model)
			return false
		}
		return true
	})
	return validationErr
}

func (s *GatewayService) validateAdvisorPricingForForward(ctx context.Context, c *gin.Context, body []byte) error {
	var key *APIKey
	if c != nil {
		if value, ok := c.Get("api_key"); ok {
			key, _ = value.(*APIKey)
		}
	}
	if err := s.ValidateAdvisorPricing(ctx, key, body); err != nil {
		if c != nil && !c.Writer.Written() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"type": "error", "error": gin.H{"type": "advisor_pricing_unavailable", "message": "Advisor model pricing is unavailable"}})
		}
		return err
	}
	return nil
}
