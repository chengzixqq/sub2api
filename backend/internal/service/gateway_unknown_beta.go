package service

import (
	"context"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
)

func unknownBetaTokens(clientBeta string, policyRules []BetaPolicyRule) []string {
	if clientBeta == "" {
		return nil
	}
	var unknown []string
	var seen map[string]struct{}
	for part := range strings.SplitSeq(clientBeta, ",") {
		token := strings.TrimSpace(part)
		if token == "" || claude.IsKnownBeta(token) {
			continue
		}
		if seen == nil {
			seen = make(map[string]struct{}, len(policyRules))
			for _, rule := range policyRules {
				seen[strings.TrimSpace(rule.BetaToken)] = struct{}{}
			}
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		unknown = append(unknown, token)
	}
	return unknown
}

func formatUnknownBetaMessage(tokens []string) string {
	return "Unexpected value(s) `" + strings.Join(tokens, "`, `") + "` for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header."
}

const originalAnthropicBetaKey = "claude_original_anthropic_beta"

func (s *SettingService) checkUnknownBeta(ctx context.Context, c *gin.Context, account *Account, policy ClaudeCustomizationSettings) error {
	recorder := s.AlignmentHits()
	if (recorder == nil && policy.UnknownBetaHeader != ClaudeAlignReject) || !IsAnthropicMessagesRequest(c) || account == nil ||
		(account.Platform != PlatformAnthropic && account.Platform != PlatformAntigravity) ||
		!slices.Contains(nativeClaudeAlignmentAccountTypes, account.Type) || opsUpstreamAttemptClockFromGin(c).hasDispatched() {
		return nil
	}
	clientBeta, saved := c.Get(originalAnthropicBetaKey)
	var header string
	if saved {
		header, _ = clientBeta.(string)
	} else {
		header = anthropicBetaHeader(c.Request.Header)
	}
	if header == "" {
		return nil
	}
	// A rule in any scope recognizes the name. Leave the existing beta policy
	// in charge of its action. On a lookup failure we cannot prove it unknown.
	if s == nil || s.settingRepo == nil {
		return nil
	}
	snapshot := s.alignmentRejectCache.Load()
	if recorder != nil {
		// Wire primes the snapshot before serving and admin writes replace it.
		// Expiration schedules a refresh without holding up this request.
		s.tryRefreshAlignmentSnapshot(ctx, snapshot)
	} else {
		snapshot = s.alignmentSnapshot(ctx)
	}
	if snapshot == nil || !snapshot.betaRulesLoaded {
		return nil
	}
	if tokens := unknownBetaTokens(header, snapshot.betaRules); len(tokens) > 0 {
		recorder.Record(c, ClaudeFieldUnknownBetaHeader, account.ID, policy.UnknownBetaHeader)
		if policy.UnknownBetaHeader == ClaudeAlignReject {
			return WriteLocalAnthropicInvalidRequest(c, formatUnknownBetaMessage(tokens))
		}
	}
	return nil
}
