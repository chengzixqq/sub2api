package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func init() { gin.SetMode(gin.TestMode) }

// ──────────────────────────────────────────────────────────
// NormalizeInboundEndpoint
// ──────────────────────────────────────────────────────────

func TestNormalizeInboundEndpoint(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Direct canonical paths.
		{"/v1/messages", EndpointMessages},
		{"/v1/chat/completions", EndpointChatCompletions},
		{"/v1/embeddings", EndpointEmbeddings},
		{"/v1/alpha/search", EndpointAlphaSearch},
		{"/v1/responses", EndpointResponses},
		{"/v1/responses/input_tokens", EndpointResponsesInputTokens},
		{"/v1/responses/compact", EndpointResponsesCompact},
		{"/v1/responses/compact/detail", EndpointResponsesCompact},
		{"/v1/images/generations", EndpointImagesGenerations},
		{"/v1/images/edits", EndpointImagesEdits},
		{"/v1/images/tasks/imgtask_123", EndpointImageTasks},
		{"/v1/videos/generations", EndpointVideosGenerations},
		{"/v1/videos/req_123", EndpointVideos},
		{"/v1beta/models", EndpointGeminiModels},

		// Prefixed paths (antigravity, openai) — root Responses.
		{"/antigravity/v1/messages", EndpointMessages},
		{"/openai/v1/responses", EndpointResponses},
		{"/openai/v1/images/generations", EndpointImagesGenerations},
		{"/openai/v1/images/edits", EndpointImagesEdits},
		{"/antigravity/v1beta/models/gemini:generateContent", EndpointGeminiModels},

		// Prefixed paths — "/responses/compact" is its OWN distinct
		// inbound endpoint, not folded into the root Responses endpoint.
		{"/openai/v1/responses/compact", EndpointResponsesCompact},
		{"/openai/v1/responses/compact/detail", EndpointResponsesCompact},

		// Bare top-level alias route "/responses" — root vs. compact.
		{"/responses", EndpointResponses},
		{"/responses/input_tokens", EndpointResponsesInputTokens},
		{"/responses/compact", EndpointResponsesCompact},
		{"/responses/compact/detail", EndpointResponsesCompact},
		{"/alpha/search", EndpointAlphaSearch},
		{"/images/tasks/imgtask_123", EndpointImageTasks},

		// Bare Codex direct alias route — root vs. compact.
		{"/backend-api/codex/responses", EndpointResponses},
		{"/backend-api/codex/responses/input_tokens", EndpointResponsesInputTokens},
		{"/backend-api/codex/responses/compact", EndpointResponsesCompact},
		{"/backend-api/codex/responses/compact/detail", EndpointResponsesCompact},
		{"/backend-api/codex/alpha/search", EndpointAlphaSearch},

		// Must NOT generalize to arbitrary paths merely ending in
		// "/responses" (or "/responses/compact") that are unrelated to
		// the two known bare alias roots, unless they already carry a
		// supported "/v1/responses..." prefix form.
		{"/foo/responses", "/foo/responses"},
		{"/foo/responses/compact", "/foo/responses/compact"},

		// Unknown path is returned as-is.
		{"/v1/embeddings", "/v1/embeddings"},
		{"", ""},
		{"  /v1/messages  ", EndpointMessages},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, NormalizeInboundEndpoint(tt.path))
		})
	}
}

// ──────────────────────────────────────────────────────────
// DeriveUpstreamEndpoint
// ──────────────────────────────────────────────────────────

func TestDeriveUpstreamEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		inbound  string
		rawPath  string
		platform string
		want     string
	}{
		// Anthropic.
		{"anthropic messages", EndpointMessages, "/v1/messages", service.PlatformAnthropic, EndpointMessages},

		// Gemini.
		{"gemini models", EndpointGeminiModels, "/v1beta/models/gemini:gen", service.PlatformGemini, EndpointGeminiModels},

		// OpenAI — root Responses.
		{"openai responses root", EndpointResponses, "/v1/responses", service.PlatformOpenAI, EndpointResponses},
		{"openai responses input tokens", EndpointResponsesInputTokens, "/v1/responses/input_tokens", service.PlatformOpenAI, EndpointResponsesInputTokens},

		// OpenAI — compact, raw path carries the derivable "/compact"
		// (or nested) suffix, which must be preserved on the upstream
		// endpoint.
		{"openai responses compact", EndpointResponsesCompact, "/openai/v1/responses/compact", service.PlatformOpenAI, "/v1/responses/compact"},
		{"openai responses nested", EndpointResponsesCompact, "/openai/v1/responses/compact/detail", service.PlatformOpenAI, "/v1/responses/compact/detail"},
		{"openai bare responses compact", EndpointResponsesCompact, "/responses/compact", service.PlatformOpenAI, "/v1/responses/compact"},
		{"openai bare responses compact detail", EndpointResponsesCompact, "/responses/compact/detail", service.PlatformOpenAI, "/v1/responses/compact/detail"},
		{"openai codex direct responses compact", EndpointResponsesCompact, "/backend-api/codex/responses/compact", service.PlatformOpenAI, "/v1/responses/compact"},
		{"openai codex direct responses compact detail", EndpointResponsesCompact, "/backend-api/codex/responses/compact/detail", service.PlatformOpenAI, "/v1/responses/compact/detail"},

		// OpenAI — bare root alias routes normalize to root Responses.
		{"openai bare responses", EndpointResponses, "/responses", service.PlatformOpenAI, EndpointResponses},
		{"openai codex direct responses", EndpointResponses, "/backend-api/codex/responses", service.PlatformOpenAI, EndpointResponses},

		// OpenAI — inbound is already the canonical compact endpoint but
		// the raw path carries no derivable "/responses..." suffix (e.g.
		// it was already normalized upstream). Must not silently fall
		// back to the root Responses endpoint.
		{"openai responses compact inbound only, unrelated raw path", EndpointResponsesCompact, "/v1/messages", service.PlatformOpenAI, EndpointResponsesCompact},

		{"openai from messages", EndpointMessages, "/v1/messages", service.PlatformOpenAI, EndpointResponses},
		{"openai from completions", EndpointChatCompletions, "/v1/chat/completions", service.PlatformOpenAI, EndpointResponses},
		{"openai embeddings", EndpointEmbeddings, "/v1/embeddings", service.PlatformOpenAI, EndpointEmbeddings},
		{"openai alpha search", EndpointAlphaSearch, "/backend-api/codex/alpha/search", service.PlatformOpenAI, EndpointAlphaSearch},
		{"openai image generations", EndpointImagesGenerations, "/v1/images/generations", service.PlatformOpenAI, EndpointImagesGenerations},
		{"openai image edits", EndpointImagesEdits, "/openai/v1/images/edits", service.PlatformOpenAI, EndpointImagesEdits},
		{"grok chat defaults to responses without runtime result", EndpointChatCompletions, "/v1/chat/completions", service.PlatformGrok, EndpointResponses},
		{"grok responses", EndpointResponses, "/v1/responses", service.PlatformGrok, EndpointResponses},
		{"grok video generations", EndpointVideosGenerations, "/v1/videos/generations", service.PlatformGrok, EndpointVideosGenerations},
		{"grok video status", EndpointVideos, "/videos/req_123", service.PlatformGrok, EndpointVideos},

		// Antigravity — uses inbound to pick Claude vs Gemini upstream.
		{"antigravity claude", EndpointMessages, "/antigravity/v1/messages", service.PlatformAntigravity, EndpointMessages},
		{"antigravity gemini", EndpointGeminiModels, "/antigravity/v1beta/models", service.PlatformAntigravity, EndpointGeminiModels},

		// Unknown platform — passthrough.
		{"unknown platform", "/v1/embeddings", "/v1/embeddings", "unknown", "/v1/embeddings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, DeriveUpstreamEndpoint(tt.inbound, tt.rawPath, tt.platform))
		})
	}
}

func TestShouldUseAntigravityCompat(t *testing.T) {
	tests := []struct {
		name    string
		account *service.Account
		want    bool
	}{
		{"oauth", &service.Account{Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth}, true},
		{"setup token", &service.Account{Platform: service.PlatformAntigravity, Type: service.AccountTypeSetupToken}, false},
		{"upstream", &service.Account{Platform: service.PlatformAntigravity, Type: service.AccountTypeUpstream}, false},
		{"api key", &service.Account{Platform: service.PlatformAntigravity, Type: service.AccountTypeAPIKey}, false},
		{"anthropic oauth", &service.Account{Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth}, false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldUseAntigravityCompat(tt.account))
		})
	}
}

func TestGetUpstreamEndpointPrefersRuntimeOverride(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointChatCompletions, nil)
	c.Set(ctxKeyInboundEndpoint, EndpointChatCompletions)

	setActualUpstreamEndpoint(c, EndpointAntigravityGenerateContent)
	require.Equal(t, EndpointAntigravityGenerateContent, GetUpstreamEndpoint(c, service.PlatformAntigravity))

	setActualUpstreamEndpoint(c, "")
	require.Equal(t, EndpointMessages, GetUpstreamEndpoint(c, service.PlatformAntigravity))
}

func TestGetUpstreamEndpointUsesOpenAIRuntimeOverride(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
	c.Set(ctxKeyInboundEndpoint, EndpointResponses)

	service.SetActualOpenAIUpstreamEndpoint(c, EndpointChatCompletions)
	require.Equal(t, EndpointChatCompletions, GetUpstreamEndpoint(c, service.PlatformOpenAI))
}

func TestResolveOpenAIUpstreamEndpointPrefersForwardResult(t *testing.T) {
	tests := []struct {
		name            string
		account         *service.Account
		result          *service.OpenAIForwardResult
		runtimeEndpoint string
		want            string
	}{
		{
			name:            "grok raw chat result overrides stale context",
			account:         &service.Account{Platform: service.PlatformGrok, Type: service.AccountTypeOAuth},
			result:          &service.OpenAIForwardResult{UpstreamEndpoint: EndpointChatCompletions},
			runtimeEndpoint: EndpointResponses,
			want:            EndpointChatCompletions,
		},
		{
			name:    "grok chat bridged to responses",
			account: &service.Account{Platform: service.PlatformGrok, Type: service.AccountTypeOAuth},
			result:  &service.OpenAIForwardResult{UpstreamEndpoint: EndpointResponses},
			want:    EndpointResponses,
		},
		{
			name:    "grok empty result keeps responses default",
			account: &service.Account{Platform: service.PlatformGrok, Type: service.AccountTypeOAuth},
			result:  &service.OpenAIForwardResult{},
			want:    EndpointResponses,
		},
		{
			name:            "grok raw error uses runtime endpoint",
			account:         &service.Account{Platform: service.PlatformGrok, Type: service.AccountTypeOAuth},
			runtimeEndpoint: EndpointChatCompletions,
			want:            EndpointChatCompletions,
		},
		{
			name:    "openai behavior remains responses",
			account: &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth},
			result:  &service.OpenAIForwardResult{},
			want:    EndpointResponses,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, EndpointChatCompletions, nil)
			c.Set(ctxKeyInboundEndpoint, EndpointChatCompletions)
			service.SetActualOpenAIUpstreamEndpoint(c, tt.runtimeEndpoint)
			require.Equal(t, tt.want, resolveOpenAIUpstreamEndpoint(c, tt.account, tt.result))
		})
	}
}

// TestResolveOpenAIUpstreamEndpointKeepsNonChatInboundForAPIKeyAccounts 固化
// openAIFailureSink 与各端点成功路径的 usage_logs.upstream_endpoint 归因口径一致。
//
// 成功路径（embeddings / images / alpha_search / grok_media）用
// GetUpstreamEndpoint(c, account.Platform)，对这些 inbound 端点
// DeriveUpstreamEndpoint 原样返回 inbound。失败路径的 openAIFailureSink 用
// resolveOpenAIUpstreamEndpoint(c, account, nil)：前两个分支都取不到值时，
// 「API-Key 账号且不走 Responses」这一分支曾无条件返回 /v1/chat/completions，
// 把一次 embeddings 失败错误归因成 chat（不影响金额，只影响归因）。
//
// 该分支的本意是「chat 入站请求在这类账号上直连 Chat Completions 而非 Responses 桥」，
// 所以它只应作用于 chat 系入站；非 chat 入站必须退回 GetUpstreamEndpoint。
func TestResolveOpenAIUpstreamEndpointKeepsNonChatInboundForAPIKeyAccounts(t *testing.T) {
	// API-Key 账号 + 显式标记不支持 Responses ⇒ 命中 chat 直连分支。
	apiKeyChatOnly := &service.Account{
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Extra:    map[string]any{openai_compat.ExtraKeyResponsesSupported: false},
	}

	tests := []struct {
		name    string
		inbound string
		want    string
		// matchesSuccessPath 为真时额外断言与成功路径 GetUpstreamEndpoint 完全一致。
		// chat 入站刻意不参与该断言：GetUpstreamEndpoint 对 chat 入站返回
		// /v1/responses（OpenAI 默认走 Responses），而这类账号直连 Chat Completions
		// 正是本分支存在的理由，二者本就应该不同。
		matchesSuccessPath bool
	}{
		{"embeddings 保持 embeddings", EndpointEmbeddings, EndpointEmbeddings, true},
		{"images generations 保持自身", EndpointImagesGenerations, EndpointImagesGenerations, true},
		{"alpha search 保持自身", EndpointAlphaSearch, EndpointAlphaSearch, true},
		// chat 入站仍须走直连分支，证明修法没有削弱该分支本来的作用。
		{"chat 入站仍直连 chat", EndpointChatCompletions, EndpointChatCompletions, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.inbound, nil)
			c.Set(ctxKeyInboundEndpoint, tt.inbound)

			got := resolveOpenAIUpstreamEndpoint(c, apiKeyChatOnly, nil)
			require.Equal(t, tt.want, got)
			if tt.matchesSuccessPath {
				require.Equal(t, GetUpstreamEndpoint(c, apiKeyChatOnly.Platform), got,
					"失败路径与成功路径的 upstream_endpoint 口径必须一致")
			}
		})
	}
}

// ──────────────────────────────────────────────────────────
// responsesSubpathSuffix
// ──────────────────────────────────────────────────────────

func TestResponsesSubpathSuffix(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"/v1/responses", ""},
		{"/v1/responses/", ""},
		{"/v1/responses/compact", "/compact"},
		{"/openai/v1/responses/compact/detail", "/compact/detail"},
		{"/responses", ""},
		{"/responses/compact", "/compact"},
		{"/responses/compact/detail", "/compact/detail"},
		{"/backend-api/codex/responses", ""},
		{"/backend-api/codex/responses/compact", "/compact"},
		{"/backend-api/codex/responses/compact/detail", "/compact/detail"},
		{"/v1/messages", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			require.Equal(t, tt.want, responsesSubpathSuffix(tt.raw))
		})
	}
}

// ──────────────────────────────────────────────────────────
// InboundEndpointMiddleware + context helpers
// ──────────────────────────────────────────────────────────

func TestInboundEndpointMiddleware(t *testing.T) {
	router := gin.New()
	router.Use(InboundEndpointMiddleware())

	var captured string
	router.POST("/v1/messages", func(c *gin.Context) {
		captured = GetInboundEndpoint(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, EndpointMessages, captured)
}

func TestGetInboundEndpoint_FallbackWithoutMiddleware(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1/messages", nil)

	// Middleware did not run — fallback to normalizing c.Request.URL.Path.
	got := GetInboundEndpoint(c)
	require.Equal(t, EndpointMessages, got)
}

// TestInboundEndpointMiddleware_WildcardRoutes verifies that, when a
// gateway route is registered with a Gin wildcard pattern (e.g.
// "/v1/responses/*subpath"), InboundEndpointMiddleware normalizes based
// on the concrete request path (c.Request.URL.Path) rather than the
// route pattern (c.FullPath()). Using c.FullPath() here would collapse
// every request under the wildcard — including "/v1/responses/compact"
// — down to the literal pattern string, which never matches the
// "compact" alias detection and would incorrectly normalize to the root
// Responses endpoint.
func TestInboundEndpointMiddleware_WildcardRoutes(t *testing.T) {
	tests := []struct {
		name        string
		routePath   string
		requestPath string
		want        string
	}{
		{
			name:        "v1 responses wildcard route, compact request",
			routePath:   "/v1/responses/*subpath",
			requestPath: "/v1/responses/compact",
			want:        EndpointResponsesCompact,
		},
		{
			name:        "bare responses wildcard route, compact request",
			routePath:   "/responses/*subpath",
			requestPath: "/responses/compact",
			want:        EndpointResponsesCompact,
		},
		{
			name:        "codex direct wildcard route, compact request",
			routePath:   "/backend-api/codex/responses/*subpath",
			requestPath: "/backend-api/codex/responses/compact",
			want:        EndpointResponsesCompact,
		},
		{
			name:        "v1 responses wildcard route, non-compact subpath request",
			routePath:   "/v1/responses/*subpath",
			requestPath: "/v1/responses/foo",
			want:        EndpointResponses,
		},
		{
			name:        "bare responses wildcard route, non-compact subpath request",
			routePath:   "/responses/*subpath",
			requestPath: "/responses/foo",
			want:        EndpointResponses,
		},
		{
			name:        "codex direct wildcard route, non-compact subpath request",
			routePath:   "/backend-api/codex/responses/*subpath",
			requestPath: "/backend-api/codex/responses/foo",
			want:        EndpointResponses,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(InboundEndpointMiddleware())

			var captured string
			router.POST(tt.routePath, func(c *gin.Context) {
				captured = GetInboundEndpoint(c)
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, tt.requestPath, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, tt.want, captured)
		})
	}
}

// TestInboundEndpointMiddleware_GeminiWildcardRoute verifies that a Gemini
// wildcard route (e.g. "/v1beta/models/*modelAction", used to capture the
// ":generateContent"-style action suffix embedded in the path) is normalized
// to EndpointGeminiModels via InboundEndpointMiddleware, using the same real
// Gin routing path as TestInboundEndpointMiddleware_WildcardRoutes above.
func TestInboundEndpointMiddleware_GeminiWildcardRoute(t *testing.T) {
	router := gin.New()
	router.Use(InboundEndpointMiddleware())

	var captured string
	router.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
		captured = GetInboundEndpoint(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, EndpointGeminiModels, captured)
}

// TestGetInboundEndpoint_FallbackWildcardRouteWithoutMiddleware verifies
// that when InboundEndpointMiddleware did NOT run (so no value is stored
// in gin.Context), the GetInboundEndpoint fallback path still prefers
// c.Request.URL.Path over c.FullPath(). This guards against the fallback
// regressing to prefer c.FullPath() again, which would misnormalize
// concrete requests matched by a wildcard route pattern (e.g.
// "/v1/responses/*subpath" matching "/v1/responses/compact") down to
// the root Responses endpoint.
func TestGetInboundEndpoint_FallbackWildcardRouteWithoutMiddleware(t *testing.T) {
	router := gin.New()
	// Deliberately do NOT register InboundEndpointMiddleware.

	var captured string
	router.POST("/v1/responses/*subpath", func(c *gin.Context) {
		// Sanity check: FullPath returns the route pattern, not the
		// concrete request path, when a wildcard route matches.
		require.Equal(t, "/v1/responses/*subpath", c.FullPath())
		captured = GetInboundEndpoint(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, EndpointResponsesCompact, captured)
}

func TestGetUpstreamEndpoint_FullFlow(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses/compact", nil)

	// Simulate middleware.
	c.Set(ctxKeyInboundEndpoint, NormalizeInboundEndpoint(c.Request.URL.Path))

	got := GetUpstreamEndpoint(c, service.PlatformOpenAI)
	require.Equal(t, "/v1/responses/compact", got)
}
