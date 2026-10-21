package admin

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The Web sends these writes without an Idempotency-Key. They must keep working once an operator switches
// idempotency.observe_only off, while clients that do send a key (the App) still get replays.

// enforceIdempotencyKeys installs a coordinator with observe-only mode off: endpoints that require a key reject
// keyless requests with IDEMPOTENCY_KEY_REQUIRED.
func enforceIdempotencyKeys(t *testing.T) {
	t.Helper()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
}

func TestExecuteAdminOptionalIdempotentJSON_RunsWithoutKeyWhenKeysAreEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	enforceIdempotencyKeys(t)

	var executed atomic.Int32
	router := gin.New()
	router.POST("/optional", func(c *gin.Context) {
		executeAdminOptionalIdempotentJSON(c, "admin.test.optional", map[string]any{"a": 1}, time.Minute, func(context.Context) (any, error) {
			executed.Add(1)
			return gin.H{"ok": true}, nil
		})
	})

	for i := 0; i < 2; i++ {
		rec := bulkActionHandlerRequest(router, "/optional", `{"a":1}`, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
	}
	require.Equal(t, int32(2), executed.Load(), "keyless callers run every time")
}

func TestExecuteAdminOptionalIdempotentJSON_FailCloseOnStoreUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(storeUnavailableRepoStub{}, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	var executed atomic.Int32
	router := gin.New()
	router.POST("/optional", func(c *gin.Context) {
		executeAdminOptionalIdempotentJSON(c, "admin.test.optional", map[string]any{"a": 1}, time.Minute, func(context.Context) (any, error) {
			executed.Add(1)
			return gin.H{"ok": true}, nil
		})
	})

	before := service.GetIdempotencyMetricsSnapshot().StoreUnavailableTotal
	rec := bulkActionHandlerRequest(router, "/optional", `{"a":1}`, "optional-store-down")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "IDEMPOTENCY_STORE_UNAVAILABLE")
	require.Zero(t, executed.Load(), "a keyed write must not run when the store cannot record it")
	// The coordinator records the failed claim; the handler records its fail-close decision like the required variant.
	require.GreaterOrEqual(t, service.GetIdempotencyMetricsSnapshot().StoreUnavailableTotal-before, uint64(2))
}

func TestExecuteAdminOptionalIdempotentJSON_BusinessUnavailableIsNotCountedAsStoreOutage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	router := gin.New()
	router.POST("/optional", func(c *gin.Context) {
		executeAdminOptionalIdempotentJSON(c, "admin.test.optional", map[string]any{"a": 1}, time.Minute, func(context.Context) (any, error) {
			return nil, infraerrors.New(http.StatusServiceUnavailable, "TEST_FEATURE_DISABLED", "feature disabled")
		})
	})

	before := service.GetIdempotencyMetricsSnapshot().StoreUnavailableTotal
	rec := bulkActionHandlerRequest(router, "/optional", `{"a":1}`, "business-503")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "TEST_FEATURE_DISABLED")
	require.Equal(t, before, service.GetIdempotencyMetricsSnapshot().StoreUnavailableTotal,
		"a business 503 is not an idempotency store outage")
}

func TestProxyHandlerCreate_OptionalIdempotencyKeyWhenKeysAreEnforced(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	enforceIdempotencyKeys(t)
	path := "/api/v1/admin/proxies"
	body := `{"name":"proxy-h08","protocol":"http","host":"127.0.0.1","port":8080}`

	noKey := bulkActionHandlerRequest(router, path, body, "")
	require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
	require.Len(t, adminSvc.createdProxies, 1)

	first := bulkActionHandlerRequest(router, path, body, "proxy-create-retry")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	replay := bulkActionHandlerRequest(router, path, body, "proxy-create-retry")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), replay.Body.String())
	require.Len(t, adminSvc.createdProxies, 2, "the keyed retry must not create a second proxy")
}

func newAccountCreateTestRouter(admin service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts", h.Create)
	router.POST("/api/v1/admin/accounts/batch", h.BatchCreate)
	router.POST("/api/v1/admin/accounts/data", h.ImportData)
	router.POST("/api/v1/admin/accounts/import/codex-session", h.ImportCodexSession)
	return router
}

func TestAccountHandlerCreate_OptionalIdempotencyKeyWhenKeysAreEnforced(t *testing.T) {
	admin := &createAccountCapturingAdmin{stubAdminService: newStubAdminService()}
	router := newAccountCreateTestRouter(admin)
	enforceIdempotencyKeys(t)
	path := "/api/v1/admin/accounts"
	body := `{"name":"account-h08","platform":"anthropic","type":"apikey","credentials":{"api_key":"sk"}}`

	noKey := bulkActionHandlerRequest(router, path, body, "")
	require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
	require.Len(t, admin.inputs, 1)
	require.Empty(t, noKey.Header().Get("X-Idempotency-Expires-At"), "nothing is stored without a key")

	first := bulkActionHandlerRequest(router, path, body, "account-create-retry")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))
	require.NotEmpty(t, first.Header().Get("X-Idempotency-Expires-At"))

	replay := bulkActionHandlerRequest(router, path, body, "account-create-retry")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.NotEmpty(t, replay.Header().Get("X-Idempotency-Expires-At"))
	require.Len(t, admin.inputs, 2, "the keyed retry must not create a second account")

	var firstEnvelope, replayEnvelope struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstEnvelope))
	require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &replayEnvelope))
	require.NotZero(t, firstEnvelope.Data.ID)
	require.Equal(t, firstEnvelope.Data.ID, replayEnvelope.Data.ID)
}

func TestAccountHandlerBatchCreate_RunsWithoutKeyWhenKeysAreEnforced(t *testing.T) {
	admin := &createAccountCapturingAdmin{stubAdminService: newStubAdminService()}
	router := newAccountCreateTestRouter(admin)
	enforceIdempotencyKeys(t)

	rec := bulkActionHandlerRequest(router, "/api/v1/admin/accounts/batch",
		`{"accounts":[{"name":"batch-h08","platform":"anthropic","type":"apikey","credentials":{"api_key":"sk"}}]}`, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope struct {
		Data struct {
			Success int `json:"success"`
			Failed  int `json:"failed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, 1, envelope.Data.Success)
	require.Zero(t, envelope.Data.Failed)
	require.Len(t, admin.inputs, 1)
}

func TestImportData_RunsWithoutKeyWhenKeysAreEnforced(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	enforceIdempotencyKeys(t)
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"type":    dataType,
			"version": dataVersion,
			"proxies": []map[string]any{},
			"accounts": []map[string]any{{
				"name":        "import-h08",
				"platform":    service.PlatformOpenAI,
				"type":        service.AccountTypeOAuth,
				"credentials": map[string]any{"token": "x"},
				"concurrency": 3,
				"priority":    50,
			}},
		},
		"skip_default_group_bind": true,
	})
	require.NoError(t, err)

	rec := bulkActionHandlerRequest(router, "/api/v1/admin/accounts/data", string(body), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope struct {
		Data DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, 1, envelope.Data.AccountCreated)
	require.Len(t, adminSvc.createdAccounts, 1)
}

func TestImportCodexSession_RunsWithoutKeyWhenKeysAreEnforced(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	router := newAccountCreateTestRouter(svc)
	enforceIdempotencyKeys(t)
	content, err := json.Marshal(map[string]any{
		"access_token": buildCodexAccessToken(t, "workspace-h08", "user-h08", time.Now().Add(time.Hour)),
	})
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"content": string(content), "skip_default_group_bind": true})
	require.NoError(t, err)

	rec := bulkActionHandlerRequest(router, "/api/v1/admin/accounts/import/codex-session", string(body), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope struct {
		Data CodexSessionImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, 1, envelope.Data.Created, rec.Body.String())
	require.Len(t, svc.createdAccounts, 1)
}

func TestUsageHandlerCreateCleanupTask_OptionalIdempotencyKeyWhenKeysAreEnforced(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &config.Config{UsageCleanup: config.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	router := setupCleanupRouter(service.NewUsageCleanupService(repo, nil, nil, cfg), 99)
	enforceIdempotencyKeys(t)
	path := "/api/v1/admin/usage/cleanup-tasks"
	body := `{"start_date":"2024-01-01","end_date":"2024-01-02","timezone":"UTC"}`
	createdTasks := func() int {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.created)
	}

	noKey := bulkActionHandlerRequest(router, path, body, "")
	require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
	require.Equal(t, 1, createdTasks())

	first := bulkActionHandlerRequest(router, path, body, "cleanup-create-retry")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	replay := bulkActionHandlerRequest(router, path, body, "cleanup-create-retry")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 2, createdTasks(), "the keyed retry must not create a second cleanup task")
}

func TestSubscriptionExtend_OptionalIdempotencyKeyWhenKeysAreEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expiresAt := time.Now().AddDate(0, 0, 30)
	repo := &bulkActionHandlerSubscriptionRepo{sub: &service.UserSubscription{
		ID: 1, UserID: 1, GroupID: 10, Status: service.SubscriptionStatusActive, ExpiresAt: expiresAt,
	}}
	svc := service.NewSubscriptionService(nil, repo, nil, nil, nil)
	t.Cleanup(svc.Stop)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.AdminScope()))
		c.Next()
	})
	router.POST("/api/v1/admin/subscriptions/:id/extend", NewSubscriptionHandler(svc).Extend)
	enforceIdempotencyKeys(t)
	path := "/api/v1/admin/subscriptions/1/extend"

	noKey := bulkActionHandlerRequest(router, path, `{"days":7}`, "")
	require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
	require.Equal(t, 1, repo.extendCalls)

	first := bulkActionHandlerRequest(router, path, `{"days":7}`, "extend-retry")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	replay := bulkActionHandlerRequest(router, path, `{"days":7}`, "extend-retry")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 2, repo.extendCalls, "the keyed retry must not extend again")
	require.Equal(t, expiresAt.AddDate(0, 0, 7).AddDate(0, 0, 7), repo.sub.ExpiresAt)
}

// TestWebCalledWriteHandlersUseOptionalIdempotency keeps the Web-called writes (which send no Idempotency-Key) on
// optional-key helpers, so switching observe_only off cannot break them again. System restart and redeem-code
// generation are covered here instead of by route tests (restart would really schedule a restart).
func TestWebCalledWriteHandlersUseOptionalIdempotency(t *testing.T) {
	targets := map[string][]string{
		"proxy_handler.go":        {"ProxyHandler.Create"},
		"account_handler.go":      {"AccountHandler.Create", "AccountHandler.BatchCreate"},
		"account_data.go":         {"AccountHandler.ImportData"},
		"account_codex_import.go": {"AccountHandler.ImportCodexSession"},
		"usage_handler.go":        {"UsageHandler.CreateCleanupTask"},
		"subscription_handler.go": {"SubscriptionHandler.Extend"},
		"system_handler.go":       {"SystemHandler.PerformUpdate", "SystemHandler.Rollback", "SystemHandler.RestartService"},
		"redeem_handler.go":       {"RedeemHandler.Generate"},
	}

	found := 0
	fset := token.NewFileSet()
	for file, methods := range targets {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		wanted := make(map[string]bool, len(methods))
		for _, method := range methods {
			wanted[method] = true
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			name := receiverTypeName(fn) + "." + fn.Name.Name
			if !wanted[name] {
				continue
			}
			found++
			usesOptional := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case strings.HasPrefix(ident.Name, "executeAdminOptional"):
					usesOptional = true
				case strings.HasPrefix(ident.Name, "executeAdminIdempotent"), ident.Name == "executeAdminMoneyWriteJSON":
					t.Errorf("%s calls %s, which rejects the Web's keyless request once observe_only is off", name, ident.Name)
				}
				return true
			})
			require.True(t, usesOptional, "%s must use an executeAdminOptional* helper", name)
		}
	}
	require.Equal(t, 11, found, "every guarded handler method must still exist")
}

func receiverTypeName(fn *ast.FuncDecl) string {
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}
