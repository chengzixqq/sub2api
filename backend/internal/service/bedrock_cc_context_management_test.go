//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const bedrockCCContextKey = "bedrock_cc_context_management"
const bedrockCCContextBody = `{"model":"claude-opus-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"context_management":{ "edits": [{"type":"clear_thinking_20251015","keep":"all"}] }}`

func bedrockCCContextGateway(t *testing.T, mode string, enabled bool) (*GatewayService, *claudeCustomizationRepoStub) {
	t.Helper()
	repo := &claudeCustomizationRepoStub{value: fmt.Sprintf(`{"preset":"custom","%s":%q}`, bedrockCCContextKey, mode)}
	channels := &ChannelService{}
	channels.cache.Store(&channelCache{loadedAt: time.Now(), channelByGroupID: map[int64]*Channel{
		1: {Status: StatusActive, FeaturesConfig: map[string]any{featureKeyBedrockCCCompat: enabled}},
	}})
	settings := NewSettingService(repo, nil)
	settings.alignmentSnapshot(t.Context())
	return &GatewayService{channelService: channels, settingService: settings}, repo
}

func bedrockCCContextRequest(beta string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if beta != "" {
		c.Request.Header.Set("Anthropic-Beta", beta)
	}
	return c
}

func TestBedrockCCContextManagementPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode, beta, bodyBeta string
		keep                 bool
	}{
		{"strip", "context-management-2025-06-27", "", false},
		{"passthrough", "context-management-2025-06-27", "", true},
		{"passthrough", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27", "", true},
		{"strip", "", "", false},
		{"passthrough", "", "", false},
		{"passthrough", "context-management-2025-06-27-other", "", false},
		{"strip", "compact-2026-01-12", "", true},
		{"passthrough", "compact-2026-01-12", "", true},
		{"strip", "", "context-management-2025-06-27", false},
		{"passthrough", "", "context-management-2025-06-27", false},
		{"passthrough", "unknown-beta", "context-management-2025-06-27", false},
		{"strip", "", "compact-2026-01-12", false},
	} {
		t.Run(fmt.Sprintf("%s/header=%s/body=%s", tc.mode, tc.beta, tc.bodyBeta), func(t *testing.T) {
			svc, _ := bedrockCCContextGateway(t, tc.mode, true)
			c := bedrockCCContextRequest(tc.beta)
			body := []byte(bedrockCCContextBody)
			if tc.bodyBeta != "" {
				body = []byte(fmt.Sprintf(`{"anthropic_beta":[%q],%s`, tc.bodyBeta, body[1:]))
			}
			groupID := int64(1)
			out := svc.ApplyBedrockCCCompat(c, body, "claude-opus-4-6", &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformAnthropic}, &groupID)
			require.Equal(t, tc.keep, gjson.GetBytes(out, "context_management").Exists())
			if tc.keep {
				require.Equal(t, gjson.GetBytes(body, "context_management").Raw, gjson.GetBytes(out, "context_management").Raw)
			}
		})
	}
}

func TestBedrockCCContextManagementAccountOverrides(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken, AccountTypeBedrock, "service_account", "upstream"} {
		for _, tc := range []struct {
			global, override string
			keep             bool
		}{
			{"passthrough", "strip", false}, {"strip", "passthrough", true},
		} {
			t.Run(accountType+"/"+tc.global+"/"+tc.override, func(t *testing.T) {
				svc, _ := bedrockCCContextGateway(t, tc.global, true)
				groupID := int64(1)
				c := bedrockCCContextRequest("context-management-2025-06-27")
				// Prime the cache for another selected account, as in failover.
				svc.settingService.ResolveClaudeCustomizationForRequest(t.Context(), c, &Account{ID: 99})
				account := &Account{ID: 2, Type: accountType, Platform: PlatformOpenAI, Extra: map[string]any{
					AccountClaudeCustomizationExtraKey: map[string]any{bedrockCCContextKey: tc.override},
				}}
				out := svc.ApplyBedrockCCCompat(c, []byte(bedrockCCContextBody), "claude-opus-4-6", account, &groupID)
				require.Equal(t, tc.keep, gjson.GetBytes(out, "context_management").Exists())
				require.Empty(t, IgnoredClaudeOverrides(accountType, map[string]any{bedrockCCContextKey: tc.override}))
			})
		}
	}
}

func TestBedrockCCContextManagementScopeAndMissingBetaIndependence(t *testing.T) {
	for _, mode := range []string{"strip", "passthrough"} {
		svc, _ := bedrockCCContextGateway(t, mode, false)
		account := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformAnthropic}
		groupID := int64(1)
		for _, beta := range []string{"", "context-management-2025-06-27"} {
			c := bedrockCCContextRequest(beta)
			require.Equal(t, bedrockCCContextBody, string(svc.ApplyBedrockCCCompat(c, []byte(bedrockCCContextBody), "claude-opus-4-6", account, &groupID)))
			require.Equal(t, beta, c.GetHeader("Anthropic-Beta"))
			for _, missingBeta := range []string{"strip", "reject"} {
				policy := decodePolicy(t, map[string]any{bedrockCCContextKey: mode, "context_management_missing_beta": missingBeta})
				body, _, err := (*AlignmentHitRecorder)(nil).applyContextManagementMissingBeta(c, account, policy, c.Request.Header, []byte(bedrockCCContextBody))
				require.Equal(t, beta == "" && missingBeta == "reject", err != nil)
				if err == nil {
					require.Equal(t, beta != "", gjson.GetBytes(body, "context_management").Exists())
				}
			}
		}
		// Native Bedrock Invoke already preserves the context beta and is not controlled by this switch.
		invoke, err := PrepareBedrockRequestBody([]byte(bedrockCCContextBody), "us.anthropic.claude-opus-4-6-v1", "context-management-2025-06-27")
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(invoke, "context_management").Exists())
		svc, repo := bedrockCCContextGateway(t, mode, true)
		for _, missingBeta := range []string{"strip", "reject"} {
			repo.value = fmt.Sprintf(`{"preset":"custom","%s":%q,"context_management_missing_beta":%q}`, bedrockCCContextKey, mode, missingBeta)
			for _, beta := range []string{"", "context-management-2025-06-27"} {
				out := svc.ApplyBedrockCCCompat(bedrockCCContextRequest(beta), []byte(bedrockCCContextBody), "claude-opus-4-6", account, &groupID)
				require.Equal(t, mode == "passthrough" && beta != "", gjson.GetBytes(out, "context_management").Exists())
			}
		}
	}
}

type bedrockCCSettingsReadCounter struct {
	claudeCustomizationRepoStub
	reads int
}

func (r *bedrockCCSettingsReadCounter) GetValue(ctx context.Context, key string) (string, error) {
	r.reads++
	return r.claudeCustomizationRepoStub.GetValue(ctx, key)
}

func TestBedrockCCContextManagementReusesRequestPolicy(t *testing.T) {
	for _, mode := range []string{"strip", "passthrough"} {
		for _, primed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/primed=%t", mode, primed), func(t *testing.T) {
				svc, _ := bedrockCCContextGateway(t, mode, true)
				repo := &bedrockCCSettingsReadCounter{claudeCustomizationRepoStub: claudeCustomizationRepoStub{value: fmt.Sprintf(`{"preset":"custom","%s":%q}`, bedrockCCContextKey, mode)}}
				svc.settingService = NewSettingService(repo, nil)
				svc.settingService.alignmentSnapshot(t.Context())
				repo.reads = 0
				c := bedrockCCContextRequest("context-management-2025-06-27")
				account := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformAnthropic}
				if primed {
					svc.settingService.ResolveClaudeCustomizationForRequest(t.Context(), c, account)
					// The request must continue using the already resolved policy.
					repo.value = `{"preset":"custom","bedrock_cc_context_management":"strip"}`
				}
				groupID := int64(1)
				out := svc.ApplyBedrockCCCompat(c, []byte(bedrockCCContextBody), "claude-opus-4-6", account, &groupID)
				svc.settingService.ResolveClaudeCustomizationForRequest(t.Context(), c, account)
				require.Equal(t, 1, repo.reads, "CC compatibility and forwarding must share the one policy read")
				require.Equal(t, mode == "passthrough", gjson.GetBytes(out, "context_management").Exists())
			})
		}
	}
}

func TestBedrockCCContextManagementNeverAutoEnables(t *testing.T) {
	for _, preset := range []string{"official", "magic", "custom"} {
		for _, raw := range []string{fmt.Sprintf(`{"preset":%q}`, preset), fmt.Sprintf(`{"preset":%q,"%s":null}`, preset, bedrockCCContextKey)} {
			svc := NewSettingService(&claudeCustomizationRepoStub{value: raw}, nil)
			otherFieldUpdate := legacyPolicy("custom")
			otherFieldUpdate["metadata_passthrough"] = false
			for _, update := range []map[string]any{legacyPolicy(preset), otherFieldUpdate} {
				cfg, err := svc.GetClaudeCustomizationSettings(t.Context())
				require.NoError(t, err)
				require.Equal(t, "strip", policyMap(t, cfg)[bedrockCCContextKey])
				require.NoError(t, applyClaudePreset(&cfg, preset))
				require.Equal(t, "strip", policyMap(t, cfg)[bedrockCCContextKey])
				require.NoError(t, svc.SetClaudeCustomizationSettings(t.Context(), decodePolicy(t, update)))
				effective, _, err := svc.ResolveClaudeCustomization(t.Context(), &Account{Type: AccountTypeAPIKey, Extra: map[string]any{
					AccountClaudeCustomizationExtraKey: map[string]any{"request_id_mode": "fill"},
				}})
				require.NoError(t, err)
				require.Equal(t, "strip", policyMap(t, effective)[bedrockCCContextKey])
			}
		}
	}
}

func TestBedrockCCForwardWireBeta(t *testing.T) {
	const cm = "context-management-2025-06-27"
	for _, typ := range []string{AccountTypeAPIKey, AccountTypeBedrock, AccountTypeOAuth} {
		t.Run(typ, func(t *testing.T) {
			gateway, _ := bedrockCCContextGateway(t, "passthrough", true)
			upstream := &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
			svc := fallbackNativeGatewayForTest(t, upstream, nil)
			svc.settingService, svc.channelService = gateway.settingService, gateway.channelService
			acc := fallbackNativeAccountForTest(false)
			acc.Type = typ
			acc.Credentials["access_token"] = "test"
			if typ == AccountTypeBedrock {
				acc = newBedrockErrorTestAccount()
			}
			c := bedrockCCContextRequest(cm)
			c.Request.Header.Set("User-Agent", "claude-cli/2.1.328 (external, cli)")
			gid := int64(1)
			body := svc.ApplyBedrockCCCompat(c, []byte(bedrockCCContextBody), "claude-opus-4-6", acc, &gid)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			_, err = svc.Forward(t.Context(), c, acc, parsed)
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.True(t, gjson.GetBytes(upstream.lastBody, "context_management").Exists(), "outbound %s", upstream.lastBody)
			if typ == AccountTypeBedrock {
				require.Contains(t, gjson.GetBytes(upstream.lastBody, "anthropic_beta").Raw, cm)
			} else {
				require.True(t, containsBetaToken(getHeaderRaw(upstream.lastReq.Header, "anthropic-beta"), cm))
			}
		})
	}
}

func TestBedrockCCBodyOnlyCompactWire(t *testing.T) {
	svc, _ := bedrockCCContextGateway(t, "strip", true)
	repo := &bedrockCCSettingsReadCounter{}
	svc.settingService = NewSettingService(repo, nil)
	gid := int64(1)
	acc := fallbackNativeAccountForTest(false)
	body := []byte(`{"anthropic_beta":["compact-2026-01-12"],` + bedrockCCContextBody[1:])
	out := svc.ApplyBedrockCCCompat(bedrockCCContextRequest(""), body, "claude-opus-4-6", acc, &gid)
	t.Logf("wire %s reads=%d", out, repo.reads)
	require.Equal(t, `{"anthropic_beta":["compact-2026-01-12"],"model":"claude-opus-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"anthropic_version":"bedrock-2023-05-31"}`, string(out))
	require.Zero(t, repo.reads)
}

func TestBedrockCCEarlyReadWithExpiredSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, _ := bedrockCCContextGateway(t, "passthrough", true)
		repo := &bedrockCCSettingsReadCounter{claudeCustomizationRepoStub: claudeCustomizationRepoStub{value: `{"preset":"custom","bedrock_cc_context_management":"passthrough"}`}}
		svc.settingService = NewSettingService(repo, nil)
		svc.settingService.alignmentSnapshot(t.Context())
		svc.settingService.alignmentRejectCache.Load().expiresAt = time.Now().Add(-time.Second)
		repo.reads = 0
		account := fallbackNativeAccountForTest(false)
		gid := int64(1)
		body := svc.ApplyBedrockCCCompat(bedrockCCContextRequest("context-management-2025-06-27"), []byte(bedrockCCContextBody), "claude-opus-4-6", account, &gid)
		synctest.Wait()
		require.True(t, gjson.GetBytes(body, "context_management").Exists())
		require.Zero(t, repo.reads)
	})
}
