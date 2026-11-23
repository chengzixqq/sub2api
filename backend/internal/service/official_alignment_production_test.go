//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAlignmentDefaultWireBytes(t *testing.T) {
	const model = `{"model":"claude-sonnet-4-5","max_tokens":15,`
	const plain = model + `"messages":[{"role":"user","content":"hello"}]}`
	const stripped = `{"type":"thinking","thinking":"h"}`
	thinking := thinkingTestBody(thinkingTestMessage("assistant", thinkingTestBlock, redactedTestBlock))
	for _, tc := range []struct {
		name, body, want, beta string
	}{
		{"context_strip", model + `"context_management":{},` + plain[len(model):], plain, ""},
		{"thinking_strip", thinking, strings.Replace(thinking, thinkingTestBlock, stripped, 1), ""},
		{"limit_trim", cacheControlFiveClientBreakpoints, strings.Replace(cacheControlFiveClientBreakpoints, `"input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}`, `"input_schema":{"type":"object"}`, 1), ""},
		{"unknown_beta", plain, plain, "client-unknown"},
	} {
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			for _, name := range []string{"no_recorder", "recorder"} {
				t.Run(tc.name+path+"/"+name, func(t *testing.T) {
					var sent []byte
					var header http.Header
					up := &systemOneHTTPUpstream{do: func(req *http.Request) (*http.Response, error) {
						var err error
						sent, err = io.ReadAll(req.Body)
						header = req.Header.Clone()
						return cacheControlLimitResponse(http.StatusOK, cacheControlLimitOKMessage), err
					}}
					s := fallbackNativeGatewayForTest(t, up, nil)
					if name == "recorder" {
						s.settingService.alignmentHits = NewAlignmentHitRecorder(&memoryHitStore{})
						s.settingService.alignmentSnapshot(t.Context())
					}
					c := alignmentTestContext(path)
					if tc.beta != "" {
						c.Request.Header.Set("Anthropic-Beta", tc.beta)
					}
					a := fallbackNativeAccountForTest(false)
					p, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tc.body)), PlatformAnthropic)
					require.NoError(t, err)
					if path == "/v1/messages" {
						_, err = s.Forward(c.Request.Context(), c, a, p)
					} else {
						err = s.ForwardCountTokens(c.Request.Context(), c, a, p)
					}
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, c.Writer.Status())
					want := tc.want
					if path == "/v1/messages/count_tokens" {
						// Native count_tokens forwards client block overflow and thinking fields
						// and drops generation-only max_tokens.
						if tc.name == "thinking_strip" || tc.name == "limit_trim" {
							want = tc.body
						}
						want = strings.Replace(want, `"max_tokens":15,`, "", 1)
						want = strings.Replace(want, `"max_tokens":1024,`, "", 1)
					}
					require.Equal(t, want, string(sent))
					require.Equal(t, tc.beta, anthropicBetaHeader(header))
				})
			}
		}
	}
}

func TestAlignmentContextMultipleUnknownFields(t *testing.T) {
	c := alignmentTestContext("/v1/messages")
	a := fallbackNativeAccountForTest(false)
	r := NewAlignmentHitRecorder(&memoryHitStore{})
	body, changed, err := r.applyContextManagementMissingBeta(c, a, DefaultClaudeCustomizationSettings(), c.Request.Header, []byte(`{"extra":true,"context_management":{}}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, `{"extra":true}`, string(body))
	got, err := r.Summary(t.Context(), &a.ID)
	require.NoError(t, err)
	require.Zero(t, got[ClaudeFieldContextManagementMissingBeta].Last24h)
}

// Use the same keyed persistence semantics for global settings and rules as
// the provider. Count only rule reads, excluding its unrelated migrations.
type alignmentPolicyTestRepo struct {
	fallbackPolicySettingRepo
	betaReads int
	betaErr   error
}

func (r *alignmentPolicyTestRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key == SettingKeyBetaPolicySettings {
		r.betaReads++
		if r.betaErr != nil {
			return "", r.betaErr
		}
	}
	return r.fallbackPolicySettingRepo.GetValue(ctx, key)
}

func (r *alignmentPolicyTestRepo) ListClaudeAlignmentOverrides(context.Context, []string) ([]ClaudeAlignmentOverrides, error) {
	return nil, nil
}

func TestAlignmentRecorderSnapshot(t *testing.T) {
	repo := &alignmentPolicyTestRepo{}
	r := NewAlignmentHitRecorder(&memoryHitStore{})
	s := ProvideSettingService(nil, repo, nil, nil, &config.Config{}, r)
	require.Equal(t, 1, repo.betaReads, "wire must prime the rules before the first request")
	check := func(token string, policy ClaudeCustomizationSettings) error {
		c := alignmentTestContext("/v1/messages")
		c.Request.Header.Set("Anthropic-Beta", token)
		return s.checkUnknownBeta(t.Context(), c, fallbackNativeAccountForTest(false), policy)
	}
	policy := DefaultClaudeCustomizationSettings()
	policy.UnknownBetaHeader = ClaudeAlignReject
	for range 3 {
		require.ErrorIs(t, check("future", policy), ErrLocalAnthropicRejected)
	}
	require.Equal(t, 1, repo.betaReads, "reject must reuse the primed snapshot")
	rules := []BetaPolicyRule{{BetaToken: "future", Action: BetaPolicyActionPass, Scope: BetaPolicyScopeAll}}
	require.NoError(t, s.SetBetaPolicySettings(t.Context(), &BetaPolicySettings{Rules: rules}))
	snap := s.alignmentRejectCache.Load()
	require.NotNil(t, snap, "beta save must publish before returning")
	require.Equal(t, rules, snap.betaRules)
	require.NoError(t, check("future", policy))
	require.ErrorIs(t, check("unlisted", policy), ErrLocalAnthropicRejected)
	require.NoError(t, s.SetClaudeCustomizationSettings(t.Context(), policy))
	snap = s.alignmentRejectCache.Load()
	require.NotNil(t, snap, "global save must publish before returning")
	require.Equal(t, ClaudeAlignReject, snap.global.UnknownBetaHeader)
	require.Equal(t, rules, snap.betaRules)
}

func TestAlignmentUnknownBetaDefaultCounting(t *testing.T) {
	for _, tc := range []struct {
		name, platform          string
		dispatched, failedRules bool
		want                    int64
	}{
		{"dispatched", PlatformAnthropic, true, false, 0},
		{"antigravity", PlatformAntigravity, false, false, 1},
		{"rule_read_failure", PlatformAnthropic, false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &alignmentPolicyTestRepo{}
			if tc.failedRules {
				repo.betaErr = errors.New("rules offline")
			}
			r := NewAlignmentHitRecorder(&memoryHitStore{})
			s := NewSettingService(repo, nil)
			s.alignmentHits = r
			s.alignmentSnapshot(t.Context())
			a := fallbackNativeAccountForTest(false)
			a.Platform = tc.platform
			path := "/v1/messages"
			if tc.platform == PlatformAntigravity {
				path = "/antigravity/v1/messages"
			}
			c := alignmentTestContext(path)
			c.Request.Header.Set("Anthropic-Beta", "unlisted")
			if tc.dispatched {
				InstallOpsUpstreamAttemptClock(c)
				MarkOpsUpstreamAttemptStart(c.Request.Context(), a.ID)
			}
			require.NoError(t, s.checkUnknownBeta(t.Context(), c, a, DefaultClaudeCustomizationSettings()))
			got, err := r.Summary(t.Context(), &a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.want, got[ClaudeFieldUnknownBetaHeader].Last24h)
		})
	}
}
