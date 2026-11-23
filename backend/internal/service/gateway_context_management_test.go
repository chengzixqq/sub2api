//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Native Anthropic requests validate context edits before dispatch, after
// account overrides and named policy restrictions have been applied.
// Translated requests gate context_management on their final context editing
// or compaction beta to keep the wire body consistent with the header.

// ============================================================================
// anthropicBetaTokensContains
// ============================================================================

func TestAnthropicBetaTokensContains_EmptyInputs(t *testing.T) {
	require.False(t, anthropicBetaTokensContains("", "context-management-2025-06-27"))
	require.False(t, anthropicBetaTokensContains("oauth-2025-04-20", ""))
}

func TestAnthropicBetaTokensContains_SingleToken(t *testing.T) {
	require.True(t, anthropicBetaTokensContains("context-management-2025-06-27", "context-management-2025-06-27"))
}

func TestAnthropicBetaTokensContains_MultiTokenComma(t *testing.T) {
	header := "oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14"
	require.True(t, anthropicBetaTokensContains(header, "context-management-2025-06-27"))
	require.True(t, anthropicBetaTokensContains(header, "oauth-2025-04-20"))
	require.False(t, anthropicBetaTokensContains(header, "fast-mode-2026-02-01"))
}

func TestAnthropicBetaTokensContains_ToleratesWhitespace(t *testing.T) {
	header := "oauth-2025-04-20 , context-management-2025-06-27 ,  interleaved-thinking-2025-05-14"
	require.True(t, anthropicBetaTokensContains(header, "context-management-2025-06-27"))
}

func TestAnthropicBetaTokensContains_SubstringNotMatched(t *testing.T) {
	// 严格 token 比较，不应被子串误匹配
	require.False(t, anthropicBetaTokensContains("context-management-2025-06-27-rev2", "context-management-2025-06-27"),
		"必须按 token 边界匹配，不允许 prefix 子串误命中")
}

// ============================================================================
// sanitizeAnthropicBodyForBetaTokens
// ============================================================================

func TestSanitizeAnthropicBodyForBetaTokens_NoFieldNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body,
		"oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14")
	require.False(t, changed)
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.Equal(t, "clear_thinking_20251015",
		gjson.GetBytes(out, "context_management.edits.0.type").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists(),
		"header 不含 context-management beta 时必须 strip 同名字段")
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaEmpty(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[]},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_EmptyBody(t *testing.T) {
	out, changed := sanitizeAnthropicBodyForBetaTokens([]byte{}, "")
	require.False(t, changed)
	require.Empty(t, out)

	out, changed = sanitizeAnthropicBodyForBetaTokens(nil, "")
	require.False(t, changed)
	require.Empty(t, out)
}

func TestSanitizeAnthropicBodyForBetaTokens_ThinkingBlockBindingKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-1","thinking":{"type":"adaptive","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, claude.BetaThinkingBindingControls)
	require.False(t, changed)
	require.Equal(t, "drop_block",
		gjson.GetBytes(out, "thinking.block_binding.prefix_mismatch_behavior").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_ThinkingBlockBindingStrippedWhenBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-1","thinking":{"type":"adaptive","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, claude.BetaContextManagement)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "thinking.block_binding").Exists())
	require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())
	require.Equal(t, "summarized", gjson.GetBytes(out, "thinking.display").String())
}

// ★ 关键回归断言：能力维度 sanitize 解决了 "真 CC + haiku" 路径的过度删除问题。
// 真实 Claude Code CLI 2.1.87+ 客户端 header 含 context-management beta；
// 即使 model 是 haiku，sanitize 也不应剥离功能字段。
func TestSanitizeAnthropicBodyForBetaTokens_HaikuRealCCClientPreservesField(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[]}`)
	// 真 Claude Code CLI 2.1.87+ 客户端 header 含 context-management beta
	clientBeta := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27"
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, clientBeta)
	require.False(t, changed,
		"真 CC 客户端 header 含 context-management beta 时，haiku body 字段必须保留（功能不丢）")
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
}

// ============================================================================
// computeFinalAnthropicBeta — 关键路径
// ============================================================================

func newTestGatewayServiceForBeta(injectBetaForAPIKey bool) *GatewayService {
	cfg := &config.Config{}
	cfg.Gateway.InjectBetaForAPIKey = injectBetaForAPIKey
	return &GatewayService{cfg: cfg}
}

func TestComputeFinalAnthropicBeta_OAuthMimic_NonHaiku_IncludesContextManagement(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"OAuth mimic non-haiku 必须注入完整 CC mimicry beta，含 context-management-2025-06-27")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaOAuth))
	require.True(t, anthropicBetaTokensContains(final, claude.BetaClaudeCode))
	require.True(t, anthropicBetaTokensContains(final, claude.BetaThinkingBindingControls),
		"OAuth mimic 必须注入 thinking block binding 所需的 beta")
}

func TestComputeFinalAnthropicBeta_OAuthMimic_Haiku_IncludesFullClaudeCodeBetas(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	require.Equal(t, strings.Join(claude.FullClaudeCodeMimicryBetas(), ","), final)
	for _, beta := range claude.FullClaudeCodeMimicryBetas() {
		require.Truef(t, anthropicBetaTokensContains(final, beta),
			"OAuth mimic Haiku 必须包含完整 Claude Code beta 集合，缺少 %s", beta)
	}
}

func TestComputeFinalAnthropicBeta_OAuthMimic_IgnoresClientBeta(t *testing.T) {
	// mimic 路径下原代码白名单透传被跳过，client beta 应被忽略
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta")
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.False(t, strings.Contains(final, "custom-experimental-beta"),
		"mimic 路径必须忽略客户端 anthropic-beta header")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaMidConversationOutputConfig),
		"mimic 必须注入 mid-conversation output_config 控制所需的 beta")

	// 显式 dropSet 仍能移除 mimic 注入的该 beta，且不会因此放行客户端未知 beta。
	dropped, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`),
		map[string]struct{}{claude.BetaMidConversationOutputConfig: {}})
	require.True(t, ok)
	require.False(t, anthropicBetaTokensContains(dropped, claude.BetaMidConversationOutputConfig),
		"显式 dropSet 必须能移除新增的 mimic beta")
	require.False(t, strings.Contains(dropped, "custom-experimental-beta"),
		"dropSet 存在时 mimic 路径仍必须忽略客户端 anthropic-beta header")
}

func TestComputeFinalAnthropicBeta_OAuthTransparent_NonHaiku_PreservesClientContextManagement(t *testing.T) {
	// 真 CC 客户端透传：客户端 header 中的 context-management beta 必须保留
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27")
	final, ok := s.computeFinalAnthropicBeta("oauth", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement))
}

func TestComputeFinalAnthropicBeta_OAuthTransparent_Haiku_RealCCPreservesContextManagement(t *testing.T) {
	// haiku 透传 + 客户端带 context-management beta → 必须保留
	// （能力维度核心场景：避免 model-name 误删客户端透传的功能 beta）
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14")
	final, ok := s.computeFinalAnthropicBeta("oauth", false, "claude-haiku-4-5", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"真 CC + haiku + 客户端带 context-management beta → 透传必须保留")
}

func TestComputeFinalAnthropicBeta_APIKey_PassesClientBetaThroughDropSet(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "oauth-2025-04-20,custom-beta")
	final, ok := s.computeFinalAnthropicBeta("apikey", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, "oauth-2025-04-20"))
	require.True(t, anthropicBetaTokensContains(final, "custom-beta"))
}

func TestComputeFinalAnthropicBeta_APIKey_NoClientBetaInjectOff_ShouldNotSet(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalAnthropicBeta("apikey", false, "claude-sonnet-4-6", http.Header{}, []byte(`{}`), nil)
	require.False(t, ok, "API-key + 客户端未传 + InjectBetaForAPIKey 关 → 不应主动设置 anthropic-beta")
	require.Equal(t, "", final)
}

func TestComputeFinalAnthropicBeta_APIKeyHaiku_StillUsesAPIKeyBetas(t *testing.T) {
	s := newTestGatewayServiceForBeta(true)
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled"},"messages":[]}`)
	final, ok := s.computeFinalAnthropicBeta("apikey", false, "claude-haiku-4-5", http.Header{}, body, nil)
	require.True(t, ok)
	require.Equal(t, claude.APIKeyHaikuBetaHeader, final)
	require.False(t, anthropicBetaTokensContains(final, claude.BetaOAuth))
	require.False(t, anthropicBetaTokensContains(final, claude.BetaClaudeCode))
}

// ============================================================================
// computeFinalCountTokensAnthropicBeta
// ============================================================================

func TestComputeFinalCountTokensAnthropicBeta_OAuthMimic_AlwaysIncludesContextManagement(t *testing.T) {
	// count_tokens mimic 继续注入完整 mimicry beta，并额外携带 token-counting beta。
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", true, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"count_tokens + mimic Haiku 必须保留 context-management beta")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaTokenCounting),
		"count_tokens 路径必须含 token-counting beta")
}

// 重构等价性回归：
// 原 main buildCountTokensRequest 在 count_tokens mimic 分支上不跳过白名单透传
// （与 messages mimic 不同），incomingBeta 取自客户端透传。重构后必须从 clientHeaders
// 拿同一个值并 merge，否则会丢失客户端 beta。
func TestComputeFinalCountTokensAnthropicBeta_OAuthMimic_PreservesClientBeta(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta,context-1m-2025-08-07")
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", true, "claude-haiku-4-5", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, "custom-experimental-beta"),
		"count_tokens mimic 不同于 messages mimic：原代码会保留客户端透传的 beta")
	require.True(t, anthropicBetaTokensContains(final, "context-1m-2025-08-07"),
		"客户端透传的其他 beta token 同样需要保留")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"同时 FullClaudeCodeMimicryBetas 不打折扣")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaTokenCounting),
		"同时补齐 token-counting beta")
}

// messages mimic 路径反向验证：原代码会跳过白名单透传，
// 客户端 beta 不会进入 mimic 计算。重构后 messages computeFinalAnthropicBeta
// mimic 分支依然不该使用 clientBeta。
func TestComputeFinalAnthropicBeta_OAuthMimic_IgnoresClientBetaExplicit(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta")
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.False(t, anthropicBetaTokensContains(final, "custom-experimental-beta"),
		"messages mimic 原代码跳过白名单透传 → 客户端 beta 不进入计算。"+
			"与 count_tokens mimic 是不同的设计，不能合并为同一函数。")
}

func TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_NoClientBetaInjectsDefault(t *testing.T) {
	// 真 CC 客户端透传 + 客户端未传 anthropic-beta → 用 CountTokensBetaHeader 兜底
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", false, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	require.Equal(t, claude.CountTokensBetaHeader, final)
	// CountTokensBetaHeader 不含 context-management beta
	require.False(t, anthropicBetaTokensContains(final, claude.BetaContextManagement))
}

func TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_AppendsBetaTokenCounting(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "oauth-2025-04-20,context-management-2025-06-27")
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaTokenCounting),
		"客户端未带 token-counting beta 时必须补齐")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"客户端带的 context-management beta 必须保留")
}

// ============================================================================
// normalizeClaudeOAuthRequestBody — 回归：context_management 补齐恢复原行为
// ============================================================================
//
// 重构后该函数不再按 model 名短路：thinking=enabled/adaptive 时补齐 context_management，
// 与 model 无关。strip 责任移交 sanitizeAnthropicBodyForBetaTokens（在
// buildUpstreamRequest 层按最终 beta header 执行）。

func TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingEnabled(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.Equal(t, "clear_thinking_20251015",
		gjson.GetBytes(out, "context_management.edits.0.type").String())
}

func TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","thinking":{"type":"adaptive"},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-4-7", claudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestNormalizeClaudeOAuthRequestBody_HaikuStillInjects_StripDeferredToSanitize(t *testing.T) {
	// Haiku + thinking=enabled：normalize 阶段仍按 CLI mimicry 行为补齐字段；
	// 最终是否保留仍由 beta 能力对称的 sanitize 统一决定。
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-haiku-4-5", claudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists(),
		"normalize 不再按 model 名短路；strip 责任移交 sanitize 层")
}

func TestNormalizeClaudeOAuthRequestBody_PreservesClientContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"custom_strategy"}]},"thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-4-7", claudeOAuthNormalizeOptions{})
	require.Equal(t, "custom_strategy",
		gjson.GetBytes(out, "context_management.edits.0.type").String(),
		"客户端透传的 context_management 内容必须原样保留")
}

func TestNormalizeClaudeOAuthRequestBody_NoThinking_NoInject(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})
	require.False(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestNormalizeClaudeOAuthRequestBody_HaikuShortModelStillNormalizesToDatedID(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, modelID := normalizeClaudeOAuthRequestBody(body, "claude-haiku-4-5", claudeOAuthNormalizeOptions{})
	require.Equal(t, "claude-haiku-4-5-20251001", modelID)
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(out, "model").String())
}

// issue #7919：采样锁定的模型上 temperature+top_p 共存即 400，
// 只传 top_p 时不得再补 temperature，否则网关自己制造 400。
func TestNormalizeClaudeOAuthRequestBody_SkipsTemperatureTopUpWithTopPOnLockedModels(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "claude-sonnet-5-5"} {
		body := []byte(`{"model":"` + model + `","top_p":0.99,"messages":[]}`)
		out, _ := normalizeClaudeOAuthRequestBody(body, model, claudeOAuthNormalizeOptions{})
		require.False(t, gjson.GetBytes(out, "temperature").Exists(),
			"model %s with top_p must not gain temperature", model)
		require.Equal(t, 0.99, gjson.GetBytes(out, "top_p").Float())
	}
}

func TestNormalizeClaudeOAuthRequestBody_StillTopsUpTemperatureWithoutTopP(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-5-5","messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-haiku-5-5", claudeOAuthNormalizeOptions{})
	require.Equal(t, int64(1), gjson.GetBytes(out, "temperature").Int(),
		"无 top_p 时 temperature:1 补齐保持不变")
}

func TestApplyClaudeCodeOAuthMimicryToBody_HaikuRewritesSystem(t *testing.T) {
	account := &Account{ID: 405, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"model":"claude-haiku-4-5","system":"Pi project instructions","messages":[{"role":"user","content":"hello"}]}`)
	svc := &GatewayService{cfg: &config.Config{}}

	out := svc.applyClaudeCodeOAuthMimicryToBody(
		context.Background(), nil, account, body, "Pi project instructions", "claude-haiku-4-5",
	)

	system := gjson.GetBytes(out, "system").Array()
	require.Len(t, system, 3)
	require.Contains(t, system[0].Get("text").String(), "x-anthropic-billing-header:")
	require.Equal(t, claudeCodeSystemPrompt, system[1].Get("text").String())
	require.Contains(t, gjson.GetBytes(out, "messages.0.content.0.text").String(), "Pi project instructions")
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(out, "model").String())
}

func TestApplyClaudeCodeOAuthMimicryToBody_FableOmitsRefusedExpansion(t *testing.T) {
	account := &Account{ID: 406, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"model":"claude-fable-5","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	svc := &GatewayService{cfg: &config.Config{}}

	out := svc.applyClaudeCodeOAuthMimicryToBody(
		context.Background(), nil, account, body, "Project instructions", "claude-fable-5",
	)

	system := gjson.GetBytes(out, "system").Array()
	require.Len(t, system, 2)
	require.Contains(t, system[0].Get("text").String(), "x-anthropic-billing-header:")
	require.Equal(t, claudeCodeSystemPrompt, system[1].Get("text").String())
	require.NotContains(t, string(out), claudeCodeSystemPromptExpansion)
	require.Contains(t, gjson.GetBytes(out, "messages.0.content.0.text").String(), "Project instructions")
	require.Equal(t, "Understood. I will follow these instructions.", gjson.GetBytes(out, "messages.1.content.0.text").String())
	require.Equal(t, "hello", gjson.GetBytes(out, "messages.2.content").String())
}

// ============================================================================
// passthrough 集成测试：buildUpstreamRequest-
// AnthropicAPIKeyPassthrough 与 buildCountTokensRequestAnthropicAPIKeyPassthrough
// 路径上 sanitize 是否生效。
// ============================================================================

// passthrough 集成测试不设 base_url，避开 validateUpstreamBaseURL 对 cfg.Security 的依赖。
// targetURL 会走默认 claudeAPIURL，sanitize 逻辑与 baseURL 是否存在无关。
func newAnthropicAPIKeyPassthroughAccountForBetaTest() *Account {
	return &Account{
		ID:       501,
		Name:     "anthropic-apikey-passthrough-ctxmgmt-test",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "upstream-key",
		},
		Extra:       map[string]any{"anthropic_passthrough": true},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func readUpstreamBodyForTest(t *testing.T, req *http.Request) []byte {
	t.Helper()
	require.NotNil(t, req.Body)
	b, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	return b
}

func TestNativeContextManagementUpstreamRequest(t *testing.T) {
	const compact = "compact-2026-01-12"
	const clear = "context-management-2025-06-27"
	const both = compact + "," + clear
	const compactEdit = `{"type":"compact_20260112","trigger":{"type":"input_tokens","value":50000}}`
	const toolEdit = `{"type":"clear_tool_uses_20250919"}`
	const thinkingEdit = `{"type":"clear_thinking_20251015","keep":"all"}`
	for _, tc := range []struct {
		name, edit, clientBeta, override, wantBeta string
		ruleToken, ruleAction                      string
		strict, unknownFilter, stripped, blocked   bool
	}{
		{name: "missing", edit: toolEdit},
		{name: "unrelated", edit: thinkingEdit, clientBeta: claude.BetaInterleavedThinking, wantBeta: claude.BetaInterleavedThinking},
		{name: "compact", edit: compactEdit, clientBeta: compact, wantBeta: compact},
		{name: "clear tools", edit: toolEdit, clientBeta: clear, wantBeta: clear},
		{name: "clear thinking", edit: thinkingEdit, clientBeta: clear, wantBeta: clear},
		{name: "both", edit: compactEdit, clientBeta: both, wantBeta: both},
		{name: "strict", edit: compactEdit, clientBeta: both, wantBeta: both, strict: true},
		{name: "unknown filter", edit: compactEdit, clientBeta: both, wantBeta: both, unknownFilter: true},
		{name: "explicit filter", edit: toolEdit, clientBeta: clear, ruleToken: clear, ruleAction: BetaPolicyActionFilter, stripped: true},
		{name: "filter compact", edit: compactEdit, clientBeta: compact, ruleToken: compact, ruleAction: BetaPolicyActionFilter, stripped: true},
		{name: "filter one beta", edit: compactEdit, clientBeta: both, ruleToken: clear, ruleAction: BetaPolicyActionFilter, wantBeta: compact},
		{name: "filter including override", edit: toolEdit, clientBeta: clear, override: clear, ruleToken: clear, ruleAction: BetaPolicyActionFilter, wantBeta: clear},
		{name: "block override", edit: compactEdit, override: compact, ruleToken: compact, ruleAction: BetaPolicyActionBlock, wantBeta: compact},
		{name: "allow override with other named filter", edit: compactEdit, clientBeta: both, override: compact, ruleToken: clear, ruleAction: BetaPolicyActionFilter, strict: true, wantBeta: compact},
		// The builder leaves the field for the missing-beta check when the client did not declare the token.
		{name: "filter without beta", edit: toolEdit, ruleToken: clear, ruleAction: BetaPolicyActionFilter},
		{name: "block without beta", edit: compactEdit, ruleToken: compact, ruleAction: BetaPolicyActionBlock},
		{name: "block second line", edit: toolEdit, clientBeta: compact + "\n" + clear, ruleToken: clear, ruleAction: BetaPolicyActionBlock, blocked: true},
		{name: "two header lines", edit: compactEdit, clientBeta: compact + "\n" + clear, wantBeta: both},
		{name: "override removes context", edit: toolEdit, clientBeta: clear, override: claude.BetaInterleavedThinking, wantBeta: claude.BetaInterleavedThinking, stripped: true},
		{name: "override", edit: compactEdit, clientBeta: both, override: "account-beta," + compact, wantBeta: "account-beta," + compact, strict: true},
	} {
		for _, passthrough := range []bool{false, true} {
			for _, countTokens := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/passthrough=%t/count_tokens=%t", tc.name, passthrough, countTokens), func(t *testing.T) {
					account := newAnthropicAPIKeyPassthroughAccountForBetaTest()
					account.Credentials["base_url"] = "https://relay.example"
					if !passthrough {
						delete(account.Extra, "anthropic_passthrough")
					}
					mode, unknown := ClaudeBetaCapabilityAware, ClaudeUnknownBetaNativeOnly
					if tc.unknownFilter {
						unknown = ClaudeUnknownBetaFilter
					}
					if tc.strict {
						mode, unknown = ClaudeBetaOfficialStrict, ClaudeUnknownBetaFilter
					}
					account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{
						"fallback_policy":     ClaudeFallbackNativePassthrough,
						"beta_policy_mode":    mode,
						"unknown_beta_action": unknown,
					}
					if tc.override != "" {
						account.Credentials[credKeyHeaderOverrideEnabled] = true
						account.Credentials[credKeyHeaderOverrides] = map[string]any{"anthropic-beta": tc.override}
					}
					repo := &fallbackPolicySettingRepo{}
					svc := &GatewayService{cfg: &config.Config{}, settingService: NewSettingService(repo, &config.Config{})}
					if tc.ruleToken != "" {
						rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{
							Scope: BetaPolicyScopeAPIKey, BetaToken: tc.ruleToken, Action: tc.ruleAction,
						}}})
						require.NoError(t, err)
						require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					if tc.clientBeta != "" {
						for _, line := range strings.Split(tc.clientBeta, "\n") {
							c.Request.Header.Add("Anthropic-Beta", line)
						}
					}
					const model = "claude-opus-4-6"
					body := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[` + tc.edit + `]}}`)
					var req *http.Request
					var err error
					switch {
					case passthrough && countTokens:
						req, _, err = svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(t.Context(), c, account, body, "key")
					case passthrough:
						req, _, err = svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(t.Context(), c, account, body, "key")
					case countTokens:
						req, _, err = svc.buildCountTokensRequest(t.Context(), c, account, body, "key", "apikey", model, false)
					default:
						req, _, err = svc.buildUpstreamRequest(t.Context(), c, account, body, "key", "apikey", model, false, false)
					}
					wantBeta, stripped, blocked := tc.wantBeta, tc.stripped, tc.blocked
					if passthrough {
						wantBeta = strings.ReplaceAll(tc.clientBeta, "\n", ",")
						if tc.override != "" {
							wantBeta = tc.override
						}
						stripped = tc.override != "" && !hasContextManagementBeta(wantBeta)
						blocked = false
					} else if tc.name == "strict" || tc.name == "unknown filter" || tc.name == "block second line" || tc.name == "two header lines" {
						wantBeta, blocked = compact, false
					}
					if blocked {
						var blocked *BetaBlockedError
						require.ErrorAs(t, err, &blocked)
						return
					}
					require.NoError(t, err)
					require.Equal(t, wantBeta, strings.Join(req.Header["anthropic-beta"], ","))
					wireBody := readUpstreamBodyForTest(t, req)
					if stripped {
						require.JSONEq(t, `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`, string(wireBody))
					} else {
						require.Equal(t, body, wireBody)
					}
				})
			}
		}
	}
}

func TestNativeContextManagementUpstream4xx(t *testing.T) {
	const validation = `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"},"request_id":"req_test"}`
	const privateError = `{"type":"error","error":{"message":"relay key fragment TEST_ONLY, billing@relay.example"},"request_id":"private"}`
	body := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]}}`)
	for _, passthrough := range []bool{false, true} {
		for _, tc := range []struct {
			status                        int
			upstreamBody, countMessage    string
			messagesFailover              bool
			messagesStatus                int
			messagesType, messagesMessage string
		}{
			{status: 400, upstreamBody: validation, countMessage: "Upstream request failed"},
			{status: 422, upstreamBody: validation, countMessage: "Upstream request failed", messagesStatus: 502, messagesType: "api_error", messagesMessage: "Upstream request failed"},
			{status: 401, upstreamBody: privateError, countMessage: "Upstream request failed", messagesFailover: true},
			{status: 402, upstreamBody: privateError, countMessage: "Upstream request failed", messagesFailover: true},
			{status: 403, upstreamBody: privateError, countMessage: "Upstream request failed", messagesFailover: true},
			{status: 404, upstreamBody: privateError, countMessage: "Upstream request failed", messagesStatus: 502, messagesType: "api_error", messagesMessage: "Upstream request failed"},
			{status: 413, upstreamBody: privateError, countMessage: "Upstream request failed", messagesStatus: 413, messagesType: "request_too_large", messagesMessage: "Request body is too large. Reduce the request size and try again."},
			{status: 429, upstreamBody: privateError, countMessage: "Rate limit exceeded", messagesFailover: true},
		} {
			for _, count := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/passthrough=%t/count=%t", tc.status, passthrough, count), func(t *testing.T) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					path := "/v1/messages"
					if count {
						path += "/count_tokens"
					}
					c.Request = httptest.NewRequest(http.MethodPost, path, nil)
					c.Request.Header.Set("anthropic-beta", claude.BetaCompact)
					account := newAnthropicAPIKeyPassthroughAccountForBetaTest()
					account.Extra["anthropic_passthrough"] = passthrough
					upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
						StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.upstreamBody)),
					}}
					svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)}
					parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-6"}
					var err error
					if count {
						err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
					} else {
						_, err = svc.Forward(t.Context(), c, account, parsed)
					}
					require.Error(t, err)
					require.Equal(t, claude.BetaCompact, getHeaderRaw(upstream.lastReq.Header, "anthropic-beta"))
					require.Equal(t, body, upstream.lastBody)
					if !count && tc.messagesFailover {
						var failover *UpstreamFailoverError
						require.ErrorAs(t, err, &failover)
						require.Equal(t, tc.status, failover.StatusCode)
						require.False(t, c.Writer.Written())
						return
					}
					require.False(t, IsOpsRequestScopedError(c))
					if !count && tc.status == http.StatusBadRequest {
						require.Equal(t, tc.status, rec.Code)
						require.Equal(t, tc.upstreamBody, rec.Body.String())

						return
					}
					status, typ, message := tc.messagesStatus, tc.messagesType, tc.messagesMessage
					if count {
						status, typ, message = tc.status, claude.ErrorType(tc.status), tc.countMessage
						require.Equal(t, "upstream_error", c.GetString(OpsAnthropicErrorTypeKey))
					}
					require.Equal(t, status, rec.Code)
					require.JSONEq(t, fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`, typ, message), rec.Body.String())
				})
			}
		}
	}
}

func TestNativeContextManagementBeforeDispatch(t *testing.T) {
	const model = "claude-opus-4-6"
	for _, pass := range []bool{false, true} {
		for _, count := range []bool{false, true} {
			for _, tc := range []struct {
				name, beta, override, extra   string
				stream, reject, strip, filter bool
				rejectMode                    bool
			}{
				{name: "StripDefault", strip: true},
				{name: "StripDefaultStream", stream: true, strip: true},
				{name: "PassthroughAccountConfig", rejectMode: true, reject: true},
				{name: "RejectGoldenStream", stream: true, rejectMode: true, reject: true},
				{name: "RejectDefersWhenOtherUnknownField", rejectMode: true, extra: `,"bogus_field_xyz":false`},
				{name: "CompactOnlyUnaffected", beta: claude.BetaCompact, rejectMode: true},
				{name: "compact", beta: claude.BetaCompact},
				{name: "editing", beta: claude.BetaContextManagement},
				{name: "both", beta: claude.BetaCompact + "," + claude.BetaContextManagement},
				{name: "override removes beta", beta: claude.BetaCompact, override: "other-beta", strip: true},
				{name: "filter removes beta", beta: claude.BetaCompact, strip: true, filter: true},
			} {
				t.Run(fmt.Sprintf("%s/passthrough=%t/count=%t", tc.name, pass, count), func(t *testing.T) {
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						b, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						if gjson.GetBytes(b, "context_management").Exists() == (tc.strip && !(tc.filter && pass)) {
							t.Errorf("unexpected outbound body %s", b)
						}
						if tc.extra != "" && !gjson.GetBytes(b, "bogus_field_xyz").Exists() {
							t.Errorf("missing second unknown field in %s", b)
						}
						if tc.stream && !count {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":3}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						if count {
							fmt.Fprint(w, `{"input_tokens":3}`)
							return
						}
						fmt.Fprint(w, `{"type":"message","id":"msg_1","model":"claude-opus-4-6","content":[],"usage":{"input_tokens":3,"output_tokens":0}}`)
					}))
					defer server.Close()
					account := newAnthropicAPIKeyPassthroughAccountForBetaTest()
					account.ID = 7
					account.Credentials["base_url"] = server.URL
					account.Extra["anthropic_passthrough"] = pass
					overrides := map[string]any{
						"fallback_policy": ClaudeFallbackNativePassthrough, "beta_policy_mode": ClaudeBetaCapabilityAware, "unknown_beta_action": ClaudeUnknownBetaNativeOnly,
					}
					if tc.rejectMode {
						overrides["context_management_missing_beta"] = ClaudeAlignReject
					}
					account.Extra[AccountClaudeCustomizationExtraKey] = overrides
					account.Credentials["temp_unschedulable_enabled"] = true
					account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{"error_code": float64(400), "keywords": []any{"context_management"}, "duration_minutes": float64(10), "scope": "account"}}
					if tc.override != "" {
						account.Credentials[credKeyHeaderOverrideEnabled] = true
						account.Credentials[credKeyHeaderOverrides] = map[string]any{"anthropic-beta": tc.override}
					}
					svc := newSystemOneTestService(&systemOneHTTPUpstream{do: server.Client().Do})
					settings := &fallbackPolicySettingRepo{}
					if tc.filter {
						rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: BetaPolicyScopeAPIKey, BetaToken: claude.BetaCompact, Action: BetaPolicyActionFilter}}})
						require.NoError(t, err)
						require.NoError(t, settings.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
					}
					svc.settingService = NewSettingService(settings, svc.cfg)
					repo := &rateLimitAccountRepoStub{}
					svc.rateLimitService = &RateLimitService{accountRepo: repo}
					path := "/v1/messages"
					if count {
						path += "/count_tokens"
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, path, nil)
					c.Request.Header.Set("Anthropic-Beta", tc.beta)
					body := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[]}%s}`, model, tc.stream, tc.extra)
					parsed := &ParsedRequest{Model: model, Stream: tc.stream, Body: NewRequestBodyRef([]byte(body))}
					var err error
					var result *ForwardResult
					if count {
						err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
					} else {
						result, err = svc.Forward(t.Context(), c, account, parsed)
					}
					if tc.reject {
						require.ErrorIs(t, err, ErrLocalAnthropicRejected)
						require.Nil(t, result)
						var failover *UpstreamFailoverError
						require.NotErrorAs(t, err, &failover)
						require.Zero(t, calls.Load())
						require.Zero(t, repo.tempCalls)
						require.Zero(t, repo.rateLimitedCalls)
						require.Zero(t, repo.setErrorCalls)
						require.True(t, IsResponseCommitted(c))
						require.True(t, IsOpsRequestScopedError(c))
						require.Equal(t, 400, rec.Code)
						require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
						id := rec.Header().Get("request-id")
						require.NotEmpty(t, id)
						require.Equal(t, `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"},"request_id":"`+id+`"}`, rec.Body.String())
					} else {
						require.NoError(t, err)
						require.EqualValues(t, 1, calls.Load())
						require.Equal(t, 200, rec.Code)
					}
				})
			}
		}
	}
}

func TestContextManagementUnknownTopLevelFieldsInWireOrder(t *testing.T) {
	body := []byte(`{"first":[],"model":"claude-opus-4-6","fallbacks":"default","context_management":null,"messages":[{"nested":1}],"fallback_credit_token":"token","last":false}`)
	require.Equal(t, []string{"first", "fallbacks", "context_management", "fallback_credit_token", "last"}, claude.UnknownTopLevelRequestFields(body, ""))
	require.Equal(t, []string{"first", "last"}, claude.UnknownTopLevelRequestFields(body, " "+claude.BetaServerSideFallback+", "+claude.BetaCompact))
	require.Equal(t, []string{"first", "fallbacks", "context_management", "last"}, claude.UnknownTopLevelRequestFields(body, claude.BetaFallbackCredit))
}

type queueAlignmentSettings struct {
	claudeCustomizationRepoStub
	accounts []*Account
	reads    int
	listErr  error
}

func (r *queueAlignmentSettings) ListClaudeAlignmentOverrides(context.Context, []string) ([]ClaudeAlignmentOverrides, error) {
	r.reads++
	var result []ClaudeAlignmentOverrides
	for _, account := range r.accounts {
		fields, _ := account.Extra[AccountClaudeCustomizationExtraKey].(map[string]any)
		result = append(result, ClaudeAlignmentOverrides{AccountID: account.ID, AccountType: account.Type, Platform: account.Platform, Fields: fields, GroupIDs: account.GroupIDs, Schedulable: account.Schedulable, DispatchScope: account.DispatchScope, Mixed: account.IsMixedSchedulingEnabled()})
	}
	return result, r.listErr
}

type queueAlignmentAccounts struct {
	accountDispatchTestRepo
	calls   int
	listErr error
}

func TestAnyAlignmentReject_SharesSnapshotAndRefreshesOverrides(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		settings := &queueAlignmentSettings{}
		s := NewSettingService(settings, nil)
		require.False(t, s.AnyAlignmentReject(t.Context(), "context_management_missing_beta"))
		settings.accounts = []*Account{{ID: 8, Type: AccountTypeOAuth, Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{"thinking_cache_control": " REJECT "}}}}
		time.Sleep(30 * time.Second)
		require.False(t, s.AnyAlignmentReject(t.Context(), "thinking_cache_control"))
		synctest.Wait()
		require.True(t, s.AnyAlignmentReject(t.Context(), "thinking_cache_control"))
		require.False(t, s.AnyAlignmentReject(t.Context(), "context_management_missing_beta"))
		require.False(t, s.AnyAlignmentReject(t.Context(), "unknown_key"))
		require.Equal(t, 2, settings.reads)
		settings.accounts = nil
		time.Sleep(30 * time.Second)
		require.True(t, s.AnyAlignmentReject(t.Context(), "thinking_cache_control"))
		synctest.Wait()
		require.False(t, s.AnyAlignmentReject(t.Context(), "thinking_cache_control"))
	})
}

func TestAlignmentSnapshotFiltersOverridesByAccountType(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			fields := map[string]any{ClaudeFieldThinkingCacheControl: ClaudeAlignReject, ClaudeFieldCacheControlLimitMode: ClaudeAlignReject, ClaudeFieldContextManagementMissingBeta: ClaudeAlignReject}
			repo := &queueAlignmentSettings{accounts: []*Account{{ID: 7, Type: accountType, Extra: map[string]any{AccountClaudeCustomizationExtraKey: fields}}}}
			s := NewSettingService(repo, nil)
			require.True(t, s.AnyAlignmentReject(t.Context(), ClaudeFieldThinkingCacheControl))
			require.True(t, s.AnyAlignmentReject(t.Context(), ClaudeFieldCacheControlLimitMode))
			require.Equal(t, accountType == AccountTypeAPIKey, s.AnyAlignmentReject(t.Context(), ClaudeFieldContextManagementMissingBeta))
		})
	}
}

func (r *queueAlignmentAccounts) ListByGroup(context.Context, int64) ([]Account, error) {
	r.calls++
	return nil, r.listErr
}

func (r *queueAlignmentAccounts) HasGroupAlignmentCandidate(_ context.Context, accountTypes []string, ids []int64, groupID *int64, _ bool, platform string, mixedOnly bool) (bool, error) {
	r.calls++
	if r.listErr != nil {
		return false, r.listErr
	}
	for _, account := range r.accounts {
		if (account.Platform == "" || account.Platform == platform) && (!mixedOnly || account.IsMixedSchedulingEnabled()) && slices.Contains(ids, account.ID) && slices.Contains(accountTypes, account.Type) && accountDispatchHasGroup(account, *groupID) {
			return true, nil
		}
	}
	return false, nil
}

func (r *queueAlignmentAccounts) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	r.calls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.accountDispatchTestRepo.GetByIDs(ctx, ids)
}

func TestQueuePing_AlignmentDefaultAndReadFailures(t *testing.T) {
	group := int64(5)
	body := []byte(`{"context_management":{}}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request = c.Request.WithContext(accountDispatchTestContext(21))
	settings := &queueAlignmentSettings{}
	accounts := &queueAlignmentAccounts{}
	groups := &mockGroupRepoForGateway{groups: map[int64]*Group{group: {ID: group, Platform: PlatformAnthropic}}}
	s := &GatewayService{settingService: NewSettingService(settings, nil), accountRepo: accounts, groupRepo: groups}
	check := func() bool { return s.AlignmentMayRejectBeforeDispatch(c, body, c.Request.Header, &group, nil) }
	for range 3 {
		require.False(t, check())
	}
	require.Zero(t, accounts.calls)
	require.Zero(t, accounts.userID, "default requests must skip dispatch-policy reads")
	require.Equal(t, 1, settings.reads, "reuse the override snapshot")

	settings.getErr = errors.New("global settings unavailable")
	s.settingService = NewSettingService(settings, nil)
	require.False(t, check())
	account := &Account{ID: 8, GroupIDs: []int64{group}, Status: StatusActive, Schedulable: true, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{"context_management_missing_beta": ClaudeAlignReject}}}
	settings.accounts, accounts.accounts = []*Account{account}, []*Account{account}
	s.settingService = NewSettingService(settings, nil)
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(`{"context_management":{},"fallbacks":"default"}`), c.Request.Header, &group, nil))
	require.Zero(t, accounts.userID, "shape without matching reject candidates must skip dispatch-policy reads")
	require.True(t, check(), "account reject survives a global settings read failure")
	accounts.listErr = errors.New("account lookup failed")
	require.False(t, check())
	s.accountRepo = nil
	require.False(t, check())
	s.accountRepo, s.settingService = accounts, nil
	require.False(t, check())
	settings.getErr = nil
	settings.value = `{"context_management_missing_beta":"reject"}`
	settings.listErr = errors.New("override list unavailable")
	s.settingService = NewSettingService(settings, nil)
	require.True(t, check(), "an unavailable override list must preserve a known global reject")
	settings.getErr = errors.New("global settings unavailable")
	s.settingService = NewSettingService(settings, nil)
	require.False(t, check(), "when both reads fail the default policy permits pings")
}

func TestQueuePing_AlignmentDefaultSkipsCacheControlMatch(t *testing.T) {
	original := alignmentQueueConditions
	t.Cleanup(func() { alignmentQueueConditions = original })
	calls := 0
	alignmentQueueConditions = []alignmentQueueCondition{{key: ClaudeFieldCacheControlLimitMode, match: func(body []byte, header http.Header, rules []BetaPolicyRule) func(ClaudeCustomizationSettings) bool {
		calls++
		return cacheControlLimitQueueCondition(body, header, rules)
	}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	settings := &queueAlignmentSettings{}
	s := &GatewayService{settingService: NewSettingService(settings, nil), accountRepo: &queueAlignmentAccounts{}}
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(cacheControlFiveClientBreakpoints), c.Request.Header, nil, nil))
	require.Zero(t, calls)
	settings.value = `{"cache_control_limit_mode":"reject"}`
	s.settingService = NewSettingService(settings, nil)
	s.AlignmentMayRejectBeforeDispatch(c, []byte(cacheControlFiveClientBreakpoints), c.Request.Header, nil, nil)
	require.Equal(t, 1, calls)
	calls = 0
	settings.value = ""
	settings.accounts = []*Account{{ID: 8, Type: AccountTypeOAuth, Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{ClaudeFieldCacheControlLimitMode: ClaudeAlignReject}}}}
	s.settingService = NewSettingService(settings, nil)
	s.AlignmentMayRejectBeforeDispatch(c, []byte(cacheControlFiveClientBreakpoints), c.Request.Header, nil, nil)
	require.Equal(t, 1, calls, "only an account rejects")
}

func TestQueuePing_ContextManagementGlobalAndFallbackGroup(t *testing.T) {
	const body = `{"context_management":{},"messages":[]}`
	group, fallback := int64(1), int64(2)
	account := &Account{ID: 7, GroupIDs: []int64{fallback}, Status: StatusActive, Schedulable: true, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{
		AccountClaudeCustomizationExtraKey: map[string]any{"context_management_missing_beta": ClaudeAlignReject},
	}}
	settings := &queueAlignmentSettings{accounts: []*Account{account}}
	groups := &mockGroupRepoForGateway{groups: map[int64]*Group{
		group: {ID: group, Platform: PlatformAnthropic}, fallback: {ID: fallback, Platform: PlatformAnthropic},
	}}
	s := GatewayService{settingService: NewSettingService(settings, &config.Config{}), accountRepo: &queueAlignmentAccounts{accountDispatchTestRepo: accountDispatchTestRepo{accounts: []*Account{account}}}, groupRepo: groups}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	header := c.Request.Header
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(body), header, &group, nil))
	require.True(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(body), header, &group, &fallback))
	intermediate := int64(3)
	groups.groups[intermediate] = &Group{ID: intermediate, Platform: PlatformAnthropic, ClaudeCodeOnly: true, FallbackGroupID: &fallback}
	require.True(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(body), header, &group, &intermediate))
	c.Request = c.Request.WithContext(SetClaudeCodeClient(c.Request.Context(), true))
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(body), header, &intermediate, nil))
	global := DefaultClaudeCustomizationSettings()
	global.ContextManagementMissingBeta = ClaudeAlignReject
	require.NoError(t, s.settingService.SetClaudeCustomizationSettings(t.Context(), global))
	require.True(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(body), header, &group, nil))
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(`{"bogus":1,"context_management":null}`), header, &group, nil))
	header.Add("Anthropic-Beta", "other")
	header.Add("Anthropic-Beta", claude.BetaCompact)
	require.False(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(body), header, &group, &fallback))
}

func TestQueuePing_AlignmentLookupWarningsRateLimited(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	synctest.Test(t, func(t *testing.T) {
		group := int64(5)
		readErr := errors.New("lookup unavailable")
		settings := &queueAlignmentSettings{listErr: readErr, accounts: []*Account{{ID: 8, Type: AccountTypeAPIKey,
			Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{"context_management_missing_beta": ClaudeAlignReject}},
		}}}
		s := &GatewayService{
			settingService: NewSettingService(settings, nil),
			accountRepo:    &queueAlignmentAccounts{listErr: readErr},
			groupRepo:      &mockGroupRepoForGateway{groups: map[int64]*Group{group: {ID: group, Platform: PlatformAnthropic}}},
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		check := func() {
			require.False(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(`{"context_management":{}}`), c.Request.Header, &group, nil))
		}
		for want := 1; want <= 2; want++ {
			settings.listErr = readErr
			check()
			time.Sleep(time.Second)
			check()
			synctest.Wait()
			settings.listErr = nil
			time.Sleep(time.Second)
			check()
			synctest.Wait()
			check()
			require.Equal(t, want, strings.Count(logs.String(), "alignment override lookup failed"))
			require.Equal(t, want, strings.Count(logs.String(), "alignment candidate lookup failed"))
			s.groupRepo = nil
			for range 2 {
				require.True(t, s.AlignmentMayRejectBeforeDispatch(c, []byte(`{"context_management":{}}`), c.Request.Header, &group, nil))
			}
			require.Equal(t, want, strings.Count(logs.String(), "alignment group lookup failed"))
			s.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{group: {ID: group, Platform: PlatformAnthropic}}}
			time.Sleep(time.Minute)
		}
	})
}

func TestQueuePing_AlignmentCompositeTarget(t *testing.T) {
	group := int64(1)
	account := &Account{ID: 7, Type: AccountTypeAPIKey, GroupIDs: []int64{group}, Extra: map[string]any{
		AccountClaudeCustomizationExtraKey: map[string]any{"context_management_missing_beta": ClaudeAlignReject},
	}}
	s := &GatewayService{
		settingService: NewSettingService(&queueAlignmentSettings{accounts: []*Account{account}}, nil),
		accountRepo:    &queueAlignmentAccounts{accountDispatchTestRepo: accountDispatchTestRepo{accounts: []*Account{account}}},
		groupRepo:      &mockGroupRepoForGateway{groups: map[int64]*Group{group: {ID: group, Platform: PlatformComposite}}},
	}
	check := func(platform string) bool {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(WithResolvedTargetPlatform(t.Context(), platform))
		return s.AlignmentMayRejectBeforeDispatch(c, []byte(`{"context_management":{}}`), c.Request.Header, &group, nil)
	}
	require.True(t, check(PlatformAnthropic))
	require.False(t, check(PlatformOpenAI))
	account.GroupIDs = []int64{2}
	require.False(t, check(PlatformAnthropic), "resolved targets use the composite group's own account pool")
	require.True(t, check(""), "an unresolved target defers pings when the snapshot has a reject")
	s.groupRepo = nil
	require.True(t, check(""), "missing group metadata also leaves the status writable")
}

func TestQueuePing_AlignmentConditionsAreIndependent(t *testing.T) {
	group, fallback := int64(1), int64(2)
	account := &Account{ID: 7, Type: AccountTypeAPIKey, GroupIDs: []int64{group}, Extra: map[string]any{
		AccountClaudeCustomizationExtraKey: map[string]any{ClaudeFieldCacheControlLimitMode: ClaudeAlignReject},
	}}
	settings := &queueAlignmentSettings{accounts: []*Account{account}}
	s := GatewayService{settingService: NewSettingService(settings, nil), accountRepo: &queueAlignmentAccounts{accountDispatchTestRepo: accountDispatchTestRepo{accounts: []*Account{account}}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	check := func(body string, invalidFallback *int64) bool {
		return s.AlignmentMayRejectBeforeDispatch(c, []byte(body), c.Request.Header, &group, invalidFallback)
	}
	composite := &Group{ID: group, Platform: PlatformComposite, Hydrated: true, Status: StatusActive}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, composite))
	const contextBody = `{"context_management":{}}`
	require.False(t, check(contextBody, nil), "a cache override alone cannot suppress context pings in an unresolved composite group")
	settings.accounts = nil
	s.settingService = NewSettingService(settings, nil)
	require.False(t, check(cacheControlFiveClientBreakpoints, nil), "default policy, unresolved composite and nil group repo")
	account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldContextManagementMissingBeta: ClaudeAlignReject}
	settings.accounts = []*Account{account}
	s.settingService = NewSettingService(settings, nil)
	require.False(t, check(`{"context_management":{},"fallbacks":"default"}`, nil), "no matching policy even though a context reject override exists")
	require.False(t, check(cacheControlFiveClientBreakpoints, nil))

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	groups := &mockGroupRepoForGateway{groups: map[int64]*Group{
		group:    {ID: group, Platform: PlatformAnthropic, ClaudeCodeOnly: true, FallbackGroupID: &fallback},
		fallback: {ID: fallback, Platform: PlatformAnthropic},
	}}
	s.groupRepo = groups
	require.False(t, check(contextBody, nil), "a non-CC client cannot select the rejecting account in the CC-only group")
	missing := int64(3)
	require.False(t, check(contextBody, &missing), "a deleted invalid-request fallback is ineligible")
	account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldCacheControlLimitMode: ClaudeAlignReject}
	s.settingService = NewSettingService(settings, nil)
	require.False(t, check(cacheControlFiveClientBreakpoints, nil))
	account.GroupIDs = []int64{fallback}
	require.True(t, check(cacheControlFiveClientBreakpoints, nil))
	require.False(t, check(contextBody, nil))
	require.False(t, check(strings.Replace(cacheControlFiveClientBreakpoints, "{", `{"unknown":null,`, 1), nil))
}

func TestCompatibleNativeMessagesContextManagement(t *testing.T) {
	const errBody = `{"type":"error","request_id":"req_relay","error":{"type":"invalid_request_error","message":"Request validation failed"}}`
	body := []byte(`{"model":"k3","messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]}}`)
	for _, beta := range []string{"", claude.BetaCompact} {
		t.Run("beta="+beta, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if beta != "" {
				c.Request.Header.Set("Anthropic-Beta", beta)
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(errBody)),
			}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			_, err := svc.ForwardAsAnthropic(t.Context(), c, nativeAnthropicTestAccount(), body, "", "")
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.JSONEq(t, `{"type":"error","error":{"type":"invalid_request_error","message":"Request validation failed"}}`, rec.Body.String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "context_management").Exists())
			require.Equal(t, beta, getHeaderRaw(upstream.lastReq.Header, "anthropic-beta"))
		})
	}
}

func TestAntigravityContextManagementFinalBeta(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-6","messages":[],"context_management":{"edits":[{"type":"compact_20260112"}]}}`)
	for _, beta := range []string{"", "compact-2026-01-12"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Request.Header.Set("Anthropic-Beta", beta)
		account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{"base_url": "https://relay.example", "api_key": "key"}}
		upstream := &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
		svc := &AntigravityGatewayService{httpUpstream: upstream}
		_, err := svc.ForwardUpstream(t.Context(), c, account, body)
		require.NoError(t, err)
		require.Equal(t, beta, upstream.lastReq.Header.Get("anthropic-beta"))
		require.Equal(t, beta != "", gjson.GetBytes(upstream.lastBody, "context_management").Exists())
	}
}

func TestRebuiltContextManagementBetaHeaders(t *testing.T) {
	const compact = "compact-2026-01-12"
	const model = "claude-opus-4-6"
	body := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"compact_20260112"}]}}`)
	c := newVertexBetaTestContext(t, compact)
	svc := &GatewayService{cfg: &config.Config{}}
	vertex, _, err := svc.buildUpstreamRequest(t.Context(), c, newVertexServiceAccount(402), body, "token", "service_account", model, false, false)
	require.NoError(t, err)
	require.Empty(t, getHeaderRaw(vertex.Header, "anthropic-beta"))
	require.False(t, gjson.GetBytes(readUpstreamBodyForTest(t, vertex), "context_management").Exists())

	oauth := &Account{ID: 401, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	setBetaPolicyFilterSet(c, oauth, model, map[string]struct{}{claude.BetaContextManagement: {}})
	// Messages mimic rebuilds its header. Count tokens mimic also includes client betas.
	messages, _, err := svc.buildUpstreamRequest(t.Context(), c, oauth, body, "token", "oauth", model, false, true)
	require.NoError(t, err)
	require.NotContains(t, getHeaderRaw(messages.Header, "anthropic-beta"), compact)
	require.NotContains(t, getHeaderRaw(messages.Header, "anthropic-beta"), claude.BetaContextManagement)
	require.False(t, gjson.GetBytes(readUpstreamBodyForTest(t, messages), "context_management").Exists())
	count, _, err := svc.buildCountTokensRequest(t.Context(), c, oauth, body, "token", "oauth", model, true)
	require.NoError(t, err)
	require.Contains(t, getHeaderRaw(count.Header, "anthropic-beta"), compact)
	require.NotContains(t, getHeaderRaw(count.Header, "anthropic-beta"), claude.BetaContextManagement)
	require.Equal(t, gjson.GetBytes(body, "context_management").Raw, gjson.GetBytes(readUpstreamBodyForTest(t, count), "context_management").Raw)
}

func TestBedrockCCCompatContextManagement(t *testing.T) {
	groupID := int64(1)
	channels := &ChannelService{}
	channels.cache.Store(&channelCache{
		loadedAt: time.Now(),
		channelByGroupID: map[int64]*Channel{
			groupID: {Status: StatusActive, FeaturesConfig: map[string]any{featureKeyBedrockCCCompat: true}},
		},
	})
	svc := &GatewayService{channelService: channels}
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}
	body := []byte(`{"model":"claude-opus-4-6","messages":[],"context_management":{"edits":[{"type":"compact_20260112"}]}}`)
	for _, beta := range []string{"compact-2026-01-12", "context-management-2025-06-27", "unknown-beta"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Request.Header.Set("Anthropic-Beta", beta)
		out := svc.ApplyBedrockCCCompat(c, body, "claude-opus-4-6", account, &groupID)
		if beta == "unknown-beta" {
			require.Empty(t, c.GetHeader("Anthropic-Beta"))
			require.False(t, gjson.GetBytes(out, "context_management").Exists())
		} else {
			require.Equal(t, beta, c.GetHeader("Anthropic-Beta"))
			require.Equal(t, beta == "compact-2026-01-12", gjson.GetBytes(out, "context_management").Exists())
		}
	}
}

func TestTranslatedContextManagementUsesFinalCompactBeta(t *testing.T) {
	const compact = "compact-2026-01-12"
	body := []byte(`{"model":"claude-opus-4-6","messages":[],"context_management":{"edits":[{"type":"compact_20260112"}]}}`)
	for _, beta := range []string{"", compact, compact + "-other"} {
		want := beta == compact
		out, _ := sanitizeAnthropicBodyForBetaTokens(body, beta)
		require.Equal(t, want, gjson.GetBytes(out, "context_management").Exists())
		out, _ = sanitizeAnthropicBodyForBetaTokensWithFallbackPolicy(body, beta, ClaudeFallbackNativePassthrough, false)
		require.Equal(t, want, gjson.GetBytes(out, "context_management").Exists())
		bedrock, err := PrepareBedrockRequestBody(body, "us.anthropic.claude-opus-4-6-v1", beta)
		require.NoError(t, err)
		require.Equal(t, want, gjson.GetBytes(bedrock, "context_management").Exists())
		if want {
			require.Equal(t, []string{compact}, bedrockAnthropicBetaNames(bedrock))
		} else {
			require.Empty(t, bedrockAnthropicBetaNames(bedrock))
		}
	}
}

// ============================================================================
// 集成测试：buildUpstreamRequest
// 全路径验证上游 outgoing body 与 anthropic-beta header 严格对称。
// 这个测试能挡住未来某人忘调 sanitize / 将 sanitize 挪到 CCH 之后 等 regression。
// ============================================================================

func TestBuildUpstreamRequest_OAuthMimicHaiku_PreservesContextManagementEndToEnd(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 401, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive,
		Schedulable: true,
	}
	// Haiku + mimic CC 使用完整 beta，其中包含 context-management；body 必须对称保留。
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-haiku-4-5", false, true, // mimicClaudeCode=true
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"OAuth mimic + Haiku 端到端：outgoing body 必须保留 context_management")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"对称约束：outgoing anthropic-beta header 必须包含 context-management beta")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaClaudeCode),
		"Haiku mimic 必须携带 claude-code beta")
}

func TestBuildUpstreamRequest_APIKeyHaiku_RemainsUnmimicked(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{
		ID: 404, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","system":"API-key client system","thinking":{"type":"enabled"},"messages":[]}`)
	svc := newTestGatewayServiceForBeta(true)
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"sk-ant-xxx", "apikey", "claude-haiku-4-5", false, false,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.Equal(t, "API-key client system", gjson.GetBytes(outBody, "system").String())
	require.Equal(t, claude.APIKeyHaikuBetaHeader, getHeaderRaw(req.Header, "anthropic-beta"))
	require.False(t, anthropicBetaTokensContains(getHeaderRaw(req.Header, "anthropic-beta"), claude.BetaOAuth))
	require.NotContains(t, string(outBody), "x-anthropic-billing-header:")
}

func TestBuildUpstreamRequest_OAuthMimicNonHaiku_PreservesContextManagementEndToEnd(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 402, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive,
		Schedulable: true,
	}
	// sonnet + mimic CC → final beta = FullClaudeCodeMimicryBetas（含 context-management）→
	// body 保留。
	body := []byte(`{"model":"claude-sonnet-4-6","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-sonnet-4-6", false, true,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"OAuth mimic + non-haiku：outgoing body 必须保留 context_management。")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"对称约束：outgoing anthropic-beta header 同时含 context-management beta")
}

func TestBuildUpstreamRequest_OAuthTransparentHaikuWithRealCCBeta_PreservesField(t *testing.T) {
	// 端到端验证：真 CC 客户端 + haiku + 客户端 header 带 context-management beta
	// → final beta 透传 → 不应该过度删除 body 字段
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta",
		"claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27")

	account := &Account{ID: 403, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-haiku-4-5", false, false, // mimicClaudeCode=false（真 CC）
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"真 CC 透传路径：客户端 header 中的 context-management beta 必须保留")
	require.True(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"回归保护：真 CC + haiku + 客户端带 beta token 时，clear_thinking_20251015 功能不能静默失效")
}

// count_tokens 主路径 E2E 集成测试
func TestBuildCountTokensRequest_OAuthMimicHaiku_PreservesContextManagementEndToEnd(t *testing.T) {
	// count_tokens 继续注入 BetaContextManagement 和 BetaTokenCounting；
	// sanitize 看到最终 beta header 含 context-management beta 后保留字段。
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	account := &Account{ID: 411, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildCountTokensRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-haiku-4-5", true, // mimicClaudeCode=true
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"count_tokens mimic 始终注入 context-management beta")
	require.True(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"对称约束：final beta 含 token 时 body 字段保留")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaTokenCounting),
		"count_tokens 路径必须含 token-counting beta")
}

func TestBuildCountTokensRequest_OAuthMimic_DropsInjectedMaxTokens(t *testing.T) {
	// OAuth mimicry injects max_tokens=128000 for normal messages requests. It is
	// invalid for Anthropic's count_tokens endpoint and must be stripped on wire.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	account := &Account{ID: 413, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive, Schedulable: true,
	}
	normalized, _ := normalizeClaudeOAuthRequestBody(
		[]byte(`{"model":"claude-sonnet-4-5","messages":[]}`),
		"claude-sonnet-4-5", claudeOAuthNormalizeOptions{},
	)
	require.Equal(t, int64(128000), gjson.GetBytes(normalized, "max_tokens").Int(),
		"precondition: OAuth mimicry injects the Claude Code default")

	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildCountTokensRequest(
		context.Background(), c, account, normalized,
		"oauth-tok", "oauth", "claude-sonnet-4-5", true,
	)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "max_tokens").Exists(),
		"count_tokens wire body must not contain max_tokens")
}

func TestBuildCountTokensRequest_APIKeyHaiku_PreservesContextManagementEndToEnd(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	account := &Account{ID: 412, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildCountTokensRequest(
		context.Background(), c, account, body,
		"sk-ant-xxx", "apikey", "claude-haiku-4-5", false,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.Equal(t, body, outBody)
}

func TestBuildCountTokensRequest_StripsCacheControlOnlyFromLiteralDeferredTools(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[],"tools":[{"name":"deferred","custom":{"defer_loading":true},"cache_control":{"type":"ephemeral"}},{"name":"ordinary","custom":{"defer_loading":false},"cache_control":{"type":"ephemeral"}},{"name":"string","custom":{"defer_loading":"true"},"cache_control":{"type":"ephemeral"}},{"name":"number","custom":{"defer_loading":1},"cache_control":{"type":"ephemeral"}},{"name":"object","custom":{"defer_loading":{}},"cache_control":{"type":"ephemeral"}}]}`)

	tests := []struct {
		name      string
		account   *Account
		token     string
		tokenType string
	}{
		{
			name:      "generic API key",
			account:   &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			token:     "sk-ant-test",
			tokenType: "apikey",
		},
		{
			name:      "recognized Claude Code OAuth without mimicry",
			account:   &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			token:     "oauth-token",
			tokenType: "oauth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
			svc := &GatewayService{cfg: &config.Config{}}

			req, wireBody, err := svc.buildCountTokensRequest(
				context.Background(), c, tt.account, body,
				tt.token, tt.tokenType, "claude-haiku-4-5", false,
			)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(wireBody, "tools.0.cache_control").Exists())
			for idx := 1; idx < 5; idx++ {
				require.Equal(t, "ephemeral", gjson.GetBytes(wireBody, fmt.Sprintf("tools.%d.cache_control.type", idx)).String())
			}
			require.JSONEq(t, string(wireBody), string(readUpstreamBodyForTest(t, req)))
		})
	}
}

// count_tokens passthrough preserve 测试
func TestBuildCountTokensRequestAnthropicAPIKeyPassthrough_PreservesContextManagementWhenClientHeaderHasBeta(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20,context-management-2025-06-27,token-counting-2024-11-01")

	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "context_management").Exists(),
		"count_tokens passthrough + 客户端带 context-management beta → 字段保留")
}

func TestBuildUpstreamRequest_APIKeyHaikuWithContextManagement_PreservesField(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	account := &Account{ID: 404, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"sk-ant-xxx", "apikey", "claude-haiku-4-5", false, false,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.Equal(t, body, outBody)
}

type projectedQueueSettings struct {
	*queueAlignmentSettings
	groups map[int64]*Group
}

func (p *projectedQueueSettings) ClaudeAlignmentRouting(context.Context) (*ClaudeAlignmentRouting, error) {
	return &ClaudeAlignmentRouting{Groups: p.groups}, nil
}

func TestQueueHeadersSnapshotGroups(t *testing.T) {
	ids := []int64{5, 6, 8}
	groups := &mockGroupRepoForGateway{groups: map[int64]*Group{}}
	for _, id := range ids {
		groups.groups[id] = &Group{ID: id, Platform: PlatformAnthropic}
	}
	a := fallbackNativeAccountForTest(true)
	a.GroupIDs = []int64{8}
	a.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{ClaudeFieldRequestIDMode: "fill"}
	repo := &queueAlignmentAccounts{accountDispatchTestRepo: accountDispatchTestRepo{accounts: []*Account{a}}}
	settings := &projectedQueueSettings{&queueAlignmentSettings{accounts: []*Account{a}}, groups.groups}
	svc := &GatewayService{settingService: NewSettingService(settings, nil), groupRepo: groups, accountRepo: repo}
	svc.settingService.alignmentSnapshot(t.Context())
	c := alignmentTestContext("/v1/messages")
	c.Request = c.Request.WithContext(accountDispatchTestContext(15))
	for range 3 {
		require.False(t, svc.DeferClaudeQueuePings(c, nil, nil, &ids[0], &ids[1]))
		require.True(t, svc.DeferClaudeQueuePings(c, nil, nil, &ids[2], nil))
	}
	require.Zero(t, groups.getByIDCalls+groups.getByIDLiteCalls)
	require.Equal(t, 1, settings.reads)
	require.Zero(t, repo.calls)
	require.Zero(t, repo.userID)
}

func TestProjectedQueueMixedPoolDispatch(t *testing.T) {
	group := int64(5)
	a := ClaudeAlignmentOverrides{AccountID: 7, Platform: PlatformAntigravity, Schedulable: true, Mixed: true, GroupIDs: []int64{group}}
	pools := []alignmentPool{{groupID: &group, platform: PlatformAntigravity, mixedOnly: true}}
	for _, tc := range []struct {
		name, origin, scope, mode string
		want                      bool
	}{
		{"shared mixed pool", "", "shared", "append", true},
		{"replace excludes membership", "", "shared", "replace", false},
		{"authorized only", "", "authorized_only", "append", false},
		{"member grant", "member", "authorized_only", "replace", true},
		{"extra grant cannot enter mixed pool", "extra", "authorized_only", "replace", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.DispatchScope = tc.scope
			routing := &ClaudeAlignmentRouting{accounts: map[int64]ClaudeAlignmentOverrides{7: a}}
			dispatch := &accountDispatchSelection{requestGroupID: group, policy: &AccountDispatchPolicy{Mode: tc.mode}, grants: map[int64]string{7: tc.origin}}
			require.Equal(t, tc.want, routing.containsCandidate(dispatch, []int64{7}, pools, false))
		})
	}
}
