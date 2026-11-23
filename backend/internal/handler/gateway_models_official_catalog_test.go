//go:build unit

package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type catalogSettingsRepo struct {
	service.SettingRepository
	policy string
	reads  atomic.Int32
}

func (r *catalogSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	if key != service.SettingKeyClaudeCustomization {
		return "", service.ErrSettingNotFound
	}
	r.reads.Add(1)
	return r.policy, nil
}

func (*catalogSettingsRepo) ListClaudeAlignmentOverrides(context.Context, []string) ([]service.ClaudeAlignmentOverrides, error) {
	return nil, nil
}

func catalogRouter(catalog, requestID string, group service.Group, modelIDs ...string) (*gin.Engine, *catalogSettingsRepo) {
	gin.SetMode(gin.TestMode)
	repo := &catalogSettingsRepo{policy: fmt.Sprintf(`{"models_catalog":%q,"request_id_mode":%q}`, catalog, requestID)}
	group.ID = 1
	accounts := &gatewayModelsAccountRepoStub{byGroup: make(map[int64][]service.Account)}
	if len(modelIDs) != 0 {
		mapping := make(map[string]any, len(modelIDs))
		for _, id := range modelIDs {
			mapping[id] = id
		}
		platform := group.Platform
		if platform == service.PlatformComposite {
			platform = service.PlatformAnthropic
		}
		accounts.byGroup[group.ID] = []service.Account{{ID: 1, Platform: platform, Credentials: map[string]any{"model_mapping": mapping}}}
	}
	h := newGatewayModelsHandlerForTest(accounts)
	h.settingService = service.NewSettingService(repo, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{GroupID: &group.ID, Group: &group})
	})
	for _, prefix := range []string{"/v1/models", "/models"} {
		router.GET(prefix, func(c *gin.Context) {
			if c.Query("client_version") != "" {
				h.CodexModels(c)
				return
			}
			h.Models(c)
		})
		router.GET(prefix+"/:model", h.Models)
	}
	return router, repo
}

func catalogGET(router *gin.Engine, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const (
	syntheticFable   = `{"id":"claude-fable-5-2","type":"model","display_name":"claude-fable-5-2","created_at":"2024-01-01T00:00:00Z"}`
	officialFable    = `{"id":"claude-fable-5-2","capabilities":null,"created_at":"2026-11-09T00:00:00Z","deprecated_at":null,"display_name":"Claude Fable 5.2","lifecycle":"active","line":"fable","max_input_tokens":1000000,"max_tokens":128000,"retires_at":null,"type":"model"}`
	officialUnknown  = `{"id":"private","capabilities":null,"created_at":"1970-01-01T00:00:00Z","deprecated_at":null,"display_name":"private","lifecycle":"active","line":null,"max_input_tokens":null,"max_tokens":null,"retires_at":null,"type":"model"}`
	emptyCatalogPage = `{"data":[],"first_id":null,"has_more":false,"last_id":null}`
	catalogIDPattern = "^req_011C[1-9A-HJ-NP-Za-km-z]{20}$"
)

func TestOfficialCatalogSwitchMatrix(t *testing.T) {
	for _, catalog := range []string{"synthetic", "official"} {
		for _, requestID := range []string{"passthrough", "fill"} {
			t.Run(catalog+"/"+requestID, func(t *testing.T) {
				router, repo := catalogRouter(catalog, requestID, service.Group{Platform: service.PlatformAnthropic}, "claude-fable-5-2")
				for _, prefix := range []string{"/models", "/v1/models"} {
					for _, suffix := range []string{"", "/claude-fable-5-2"} {
						rec := catalogGET(router, prefix+suffix, "x-api-key", "key")
						body, contentType := syntheticFable, "application/json"
						if catalog == "official" {
							body = officialFable
						}
						if suffix == "" {
							if catalog == "official" {
								body = `{"data":[` + body + `],"first_id":"claude-fable-5-2","has_more":false,"last_id":"claude-fable-5-2"}`
							} else {
								body = `{"data":[` + body + `],"object":"list"}`
								contentType = "application/json; charset=utf-8"
							}
						}
						require.Equal(t, http.StatusOK, rec.Code)
						require.Equal(t, body, rec.Body.String())
						require.Equal(t, contentType, rec.Result().Header.Get("Content-Type"))
						if requestID == "fill" {
							require.Regexp(t, catalogIDPattern, rec.Result().Header.Get("Request-Id"))
						} else {
							require.Empty(t, rec.Result().Header.Get("Request-Id"))
						}
					}
				}
				require.EqualValues(t, 1, repo.reads.Load(), "list and detail share one settings snapshot")
			})
		}
	}
}

type catalogPageForTest struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	FirstID *string `json:"first_id"`
	HasMore bool    `json:"has_more"`
	LastID  *string `json:"last_id"`
}

func checkCatalogPage(t *testing.T, router *gin.Engine, query string, more bool, ids ...string) catalogPageForTest {
	t.Helper()
	rec := catalogGET(router, "/v1/models?"+query, "anthropic-version", "2023-06-01", "Authorization", "Bearer key")
	require.Equal(t, http.StatusOK, rec.Code)
	var page catalogPageForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	if len(ids) == 0 {
		require.Equal(t, emptyCatalogPage, rec.Body.String())
		return page
	}
	require.Len(t, page.Data, len(ids))
	for i, id := range ids {
		require.Equal(t, id, page.Data[i].ID)
	}
	require.Equal(t, &ids[0], page.FirstID)
	require.Equal(t, &ids[len(ids)-1], page.LastID)
	require.Equal(t, more, page.HasMore)
	return page
}

func TestOfficialCatalogPaginationAndLifecycle(t *testing.T) {
	const fable, mythos, alias, snapshot, retired = "claude-fable-5-2", "claude-mythos-5-2", "claude-sonnet-4-5", "claude-sonnet-4-5-20250929", "claude-opus-4-1-20250805"
	router, _ := catalogRouter("official", "passthrough", service.Group{Platform: service.PlatformAnthropic}, snapshot, retired, "private", mythos, alias, fable)
	first := checkCatalogPage(t, router, "limit=2", true, fable, mythos)
	middle := checkCatalogPage(t, router, "limit=2&after_id="+*first.LastID, true, alias, snapshot)
	last := checkCatalogPage(t, router, "limit=2&after_id="+*middle.LastID+"&before_id="+fable, false, "private")
	checkCatalogPage(t, router, "limit=2&before_id="+*last.FirstID, true, alias, snapshot)
	checkCatalogPage(t, router, "limit=2&before_id="+*middle.FirstID, false, fable, mythos)
	for _, query := range []string{"after_id=private", "before_id=" + fable, "before_id=missing", "after_id=missing&before_id=private", "after_id=" + retired} {
		checkCatalogPage(t, router, query, false)
	}
	for _, query := range []string{"lifecycle=retired", "lifecycle[]=retired"} {
		checkCatalogPage(t, router, query, false, retired)
	}
	for _, query := range []string{"lifecycle=active,retired", "lifecycle=active&lifecycle=retired", "lifecycle[]=active&lifecycle=retired,junk"} {
		checkCatalogPage(t, router, query, false, fable, mythos, retired, "private")
	}
	checkCatalogPage(t, router, "lifecycle=invalid", false, fable, mythos, alias, snapshot, "private")
	rec := catalogGET(router, "/models/private", "anthropic-version", "")
	require.Equal(t, officialUnknown, rec.Body.String())
	rec = catalogGET(router, "/models/"+retired+"?lifecycle=active&limit=0", "x-api-key", "key")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"lifecycle":"retired"`)
}

func TestOfficialCatalogLimits(t *testing.T) {
	ids := make([]string, 1002)
	for i := range ids {
		ids[i] = fmt.Sprintf("private-%04d", i)
	}
	router, _ := catalogRouter("official", "passthrough", service.Group{Platform: service.PlatformAnthropic}, ids...)
	for _, tc := range []struct {
		query string
		count int
	}{
		{"", 20}, {"limit=abc", 20}, {"limit=0", 1}, {"limit=-1", 1}, {"limit=1001", 1000},
		{"limit=999999999999999999999999", 1000}, {"limit=-999999999999999999999999", 1},
	} {
		t.Run(tc.query, func(t *testing.T) { checkCatalogPage(t, router, tc.query, true, ids[:tc.count]...) })
	}
}

func TestOfficialCatalogNotFound(t *testing.T) {
	for _, mode := range []string{"passthrough", "fill"} {
		router, _ := catalogRouter("official", mode, service.Group{Platform: service.PlatformAnthropic}, "private")
		for _, prefix := range []string{"/models/", "/v1/models/"} {
			for _, id := range []string{"claude-nope-1", "claude-opus-4-6"} {
				rec := catalogGET(router, prefix+id, "x-api-key", "key")
				requestID := rec.Result().Header.Get("Request-Id")
				require.Equal(t, http.StatusNotFound, rec.Code)
				require.Regexp(t, catalogIDPattern, requestID)
				require.Equal(t, "application/json", rec.Result().Header.Get("Content-Type"))
				require.Equal(t, fmt.Sprintf(`{"type":"error","error":{"type":"not_found_error","message":"model: %s"},"request_id":"%s"}`, id, requestID), rec.Body.String())
			}
		}
	}
}

func TestOfficialCatalogGeminiGroupFormat(t *testing.T) {
	for _, mapped := range []bool{true, false} {
		t.Run(fmt.Sprintf("mapped=%t", mapped), func(t *testing.T) {
			var ids []string
			if mapped {
				ids = []string{"claude-fable-5-2"}
			}
			group := service.Group{Platform: service.PlatformGemini}
			synthetic, _ := catalogRouter("synthetic", "passthrough", group, ids...)
			official, _ := catalogRouter("official", "passthrough", group, ids...)
			for _, path := range []string{"/v1/models", "/models"} {
				base := catalogGET(synthetic, path, "x-api-key", "key")
				got := catalogGET(official, path, "x-api-key", "key")
				require.Equal(t, http.StatusOK, base.Code)
				require.Equal(t, base.Code, got.Code)
				if mapped {
					require.Equal(t, `{"data":[`+syntheticFable+`],"object":"list"}`, base.Body.String())
					require.Equal(t, `{"data":[`+officialFable+`],"first_id":"claude-fable-5-2","has_more":false,"last_id":"claude-fable-5-2"}`, got.Body.String())
					require.Equal(t, "application/json", got.Result().Header.Get("Content-Type"))
				} else {
					// Without a mapping or allowlist, Gemini keeps its native list.
					require.Equal(t, base.Result().Header, got.Result().Header)
					require.Equal(t, base.Body.Bytes(), got.Body.Bytes())
				}
			}
		})
	}
}

func TestOfficialCatalogCompatibility(t *testing.T) {
	// Captured before the official catalogue was added and verified unchanged after it.
	const (
		goldenMapped             = "66610ac1d9e1b3420bad500469f054deb557ef1d101a78c7b631b54fe9bda095"
		goldenComposite          = "66610ac1d9e1b3420bad500469f054deb557ef1d101a78c7b631b54fe9bda095"
		goldenAllowlist          = "2005760ab896a3db86fb77ca881f8f83724938e37aaef5e32068a8a323cba1de"
		goldenCompositeAllowlist = "2005760ab896a3db86fb77ca881f8f83724938e37aaef5e32068a8a323cba1de"
		goldenDefault            = "af2e3070df78822eb0dd8d3ee2593706588cbb8f5062da2a2d275a82afdebfe7"
		goldenTypeSafe           = "8e47882284ade9928e65eede246dd39e046751bfbade71c2845c89a3df0c6cd3"
		goldenOpenAIMapped       = "c54b0aba86980f0e3405a7a09a1783936110943f2d51f52dffe5307ff6c81794"
		goldenOpenAIDefault      = "8a5ce6344f21cda45151c4e6f1cb3efeed4879ba35155455be53325d9c12dc81"
		goldenGrok               = "41f059c20fd5a4705f53e419026b2ad6ba87dfe30e8d1e46e312d73743c47f91"
		goldenGeminiDefault      = "66351917a5e512e0d11c59ba55ac7a00331331ef47da296ae7f986336544e69e"
	)
	for _, tc := range []struct {
		name, platform, model          string
		mapped, allowlist, claudeShape bool
		goldenSHA256                   string
	}{
		{"mapped", "anthropic", "private", true, false, true, goldenMapped},
		{"composite", "composite", "private", true, false, true, goldenComposite},
		{"allowlist", "anthropic", "private", true, true, true, goldenAllowlist},
		{"composite-allowlist", "composite", "private", true, true, true, goldenCompositeAllowlist},
		{"default", "anthropic", "claude-fable-5-2", false, false, true, goldenDefault},
		{"typesafe", service.PlatformTypeSafe, "jev-latest", false, false, true, goldenTypeSafe},
		{"openai-mapped", "openai", "gpt-private", true, false, false, goldenOpenAIMapped},
		{"openai-default", "openai", "gpt-5.4", false, false, false, goldenOpenAIDefault},
		{"grok", "grok", "grok-private", true, false, false, goldenGrok},
		{"gemini-default", "gemini", "gemini-2.5-pro", false, false, false, goldenGeminiDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := service.Group{Platform: tc.platform}
			if tc.allowlist {
				group.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{tc.model}}
			}
			var ids []string
			if tc.mapped {
				ids = []string{tc.model, "not-allowlisted"}
			}
			synthetic, _ := catalogRouter("synthetic", "passthrough", group, ids...)
			official, _ := catalogRouter("official", "fill", group, ids...)
			digest := sha256.New()
			for _, prefix := range []string{"/models", "/v1/models"} {
				for _, suffix := range []string{"?limit=1&lifecycle=retired", "/" + tc.model, "/missing", "?client_version=0.147.0"} {
					for _, headers := range [][]string{{"x-api-key", "key"}, {"Authorization", "Bearer key", "x-api-key", "key"}} {
						path := prefix + suffix
						base := catalogGET(synthetic, path, headers...)
						fmt.Fprintf(digest, "%s\n%d\n%v\n%s\n", path, base.Code, base.Result().Header, base.Body.Bytes())
						if tc.claudeShape && headers[0] == "x-api-key" && suffix != "?client_version=0.147.0" {
							if suffix == "/"+tc.model {
								got := catalogGET(official, path, headers...)
								require.Equal(t, 200, got.Code)
								assert.Contains(t, got.Body.String(), `"capabilities":null`)
							}
							continue
						}
						got := catalogGET(official, path, headers...)
						require.Equal(t, base.Code, got.Code, path)
						require.Equal(t, base.Result().Header, got.Result().Header, path)
						require.Equal(t, base.Body.Bytes(), got.Body.Bytes(), path)
					}
				}
			}
			require.Equal(t, tc.goldenSHA256, fmt.Sprintf("%x", digest.Sum(nil)), "BASE-compatible response SHA-256")
			t.Logf("BASE-compatible responses %s %x", tc.name, digest.Sum(nil))
		})
	}
}

func TestHandlerLocalErrorGlobalFill(t *testing.T) {
	repo := &catalogSettingsRepo{policy: `{"request_id_mode":"fill"}`}
	handler := &GatewayHandler{settingService: service.NewSettingService(repo, nil)}
	handler.settingService.AnyAlignmentReject(t.Context(), "unknown_beta_header")
	for _, status := range []int{400, 403, 503} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		if status == 400 {
			handler.errorResponse(c, status, "invalid_request_error", "Request body is empty")
		} else {
			handler.handleLocalStreamingAwareError(c, status, "api_error", "Service unavailable", false)
		}
		id := w.Header().Get("Request-Id")
		require.Regexp(t, catalogIDPattern, id)
		require.Contains(t, w.Body.String(), `"request_id":"`+id+`"`)
	}
	require.EqualValues(t, 1, repo.reads.Load())
}
