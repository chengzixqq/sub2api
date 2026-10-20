package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	vendorOwnProxyID     = int64(11)
	vendorForeignProxyID = int64(99)
)

func claudeOAuthTestProxy(id int64) *service.Proxy {
	return &service.Proxy{
		ID:       id,
		Protocol: "http",
		Host:     fmt.Sprintf("proxy-%d.test", id),
		Port:     8080,
		Status:   service.StatusActive,
	}
}

// unscopedProxyRepo stands in for the repository the OAuth service uses: it
// resolves any proxy id, like the real GetByID, and records each lookup.
type unscopedProxyRepo struct {
	service.ProxyRepository
	mu      sync.Mutex
	lookups []int64
}

func (r *unscopedProxyRepo) GetByID(_ context.Context, id int64) (*service.Proxy, error) {
	r.mu.Lock()
	r.lookups = append(r.lookups, id)
	r.mu.Unlock()
	return claudeOAuthTestProxy(id), nil
}

func (r *unscopedProxyRepo) seen() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.lookups...)
}

// workspaceProxyAdmin answers GetProxy like the workspace-filtered admin
// service: only proxies in owned are visible.
type workspaceProxyAdmin struct {
	*stubAdminService
	owned   map[int64]bool
	mu      sync.Mutex
	lookups []int64
}

func (a *workspaceProxyAdmin) GetProxy(_ context.Context, id int64) (*service.Proxy, error) {
	a.mu.Lock()
	a.lookups = append(a.lookups, id)
	a.mu.Unlock()
	if !a.owned[id] {
		return nil, service.ErrProxyNotFound
	}
	return claudeOAuthTestProxy(id), nil
}

func (a *workspaceProxyAdmin) seen() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]int64(nil), a.lookups...)
}

// proxyRecordingClaudeClient records the proxy URL of every upstream call.
type proxyRecordingClaudeClient struct {
	mu        sync.Mutex
	proxyURLs []string
}

func (f *proxyRecordingClaudeClient) record(proxyURL string) {
	f.mu.Lock()
	f.proxyURLs = append(f.proxyURLs, proxyURL)
	f.mu.Unlock()
}

func (f *proxyRecordingClaudeClient) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.proxyURLs...)
}

func (f *proxyRecordingClaudeClient) GetOrganizationUUID(_ context.Context, _ string, proxyURL string) (string, error) {
	f.record(proxyURL)
	return "", errors.New("cookie auth stops after the organization lookup in tests")
}

func (f *proxyRecordingClaudeClient) GetAuthorizationCode(context.Context, string, string, string, string, string, string) (string, error) {
	return "", errors.New("not used")
}

func (f *proxyRecordingClaudeClient) ExchangeCodeForToken(_ context.Context, _, _, _ string, proxyURL string, _ bool) (*oauth.TokenResponse, error) {
	f.record(proxyURL)
	return &oauth.TokenResponse{AccessToken: "at-test", TokenType: "Bearer", ExpiresIn: 3600}, nil
}

func (f *proxyRecordingClaudeClient) RefreshToken(context.Context, string, string) (*oauth.TokenResponse, error) {
	return nil, errors.New("not used")
}

type claudeOAuthProxyFixture struct {
	svc    *service.OAuthService
	repo   *unscopedProxyRepo
	admin  *workspaceProxyAdmin
	client *proxyRecordingClaudeClient
	router *gin.Engine
}

// newClaudeOAuthProxyFixture wires the six Claude OAuth helper routes. A nil
// scope leaves the request context without one, as if the scope middleware
// were missing.
func newClaudeOAuthProxyFixture(t *testing.T, scope *service.Scope) *claudeOAuthProxyFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &claudeOAuthProxyFixture{
		repo:   &unscopedProxyRepo{},
		admin:  &workspaceProxyAdmin{stubAdminService: newStubAdminService(), owned: map[int64]bool{vendorOwnProxyID: true}},
		client: &proxyRecordingClaudeClient{},
	}
	f.svc = service.NewOAuthService(f.repo, f.client)
	t.Cleanup(f.svc.Stop)

	handler := NewOAuthHandler(f.svc, f.admin)
	router := gin.New()
	if scope != nil {
		router.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), *scope))
			c.Next()
		})
	}
	router.POST("/accounts/generate-auth-url", handler.GenerateAuthURL)
	router.POST("/accounts/generate-setup-token-url", handler.GenerateSetupTokenURL)
	router.POST("/accounts/exchange-code", handler.ExchangeCode)
	router.POST("/accounts/exchange-setup-token-code", handler.ExchangeSetupTokenCode)
	router.POST("/accounts/cookie-auth", handler.CookieAuth)
	router.POST("/accounts/setup-token-cookie-auth", handler.SetupTokenCookieAuth)
	f.router = router
	return f
}

// session creates an OAuth session directly on the service, outside any
// request scope, so exchange requests have something valid to consume.
func (f *claudeOAuthProxyFixture) session(t *testing.T) string {
	t.Helper()
	result, err := f.svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	return result.SessionID
}

func vendorOAuthScope(perms service.WorkspacePermissions) *service.Scope {
	scope := service.VendorScope(7, perms)
	return &scope
}

func proxyIDField(id int64) string {
	return fmt.Sprintf(`"proxy_id":%d`, id)
}

func claudeOAuthHelperBody(t *testing.T, f *claudeOAuthProxyFixture, path, proxyField string) string {
	t.Helper()
	fields := ""
	switch path {
	case "/accounts/exchange-code", "/accounts/exchange-setup-token-code":
		fields = `"session_id":"` + f.session(t) + `","code":"auth-code"`
	case "/accounts/cookie-auth", "/accounts/setup-token-cookie-auth":
		fields = `"code":"sk-ant-session"`
	}
	if proxyField != "" {
		if fields != "" {
			fields += ","
		}
		fields += proxyField
	}
	return "{" + fields + "}"
}

var claudeOAuthHelperPaths = []string{
	"/accounts/generate-auth-url",
	"/accounts/generate-setup-token-url",
	"/accounts/exchange-code",
	"/accounts/exchange-setup-token-code",
	"/accounts/cookie-auth",
	"/accounts/setup-token-cookie-auth",
}

func TestClaudeOAuthVendorRejectsProxyOutsideWorkspace(t *testing.T) {
	for _, path := range claudeOAuthHelperPaths {
		t.Run(path, func(t *testing.T) {
			f := newClaudeOAuthProxyFixture(t, vendorOAuthScope(service.WorkspacePermissions{AccountManage: true}))

			rec, envelope := postReauth(t, f.router, path, claudeOAuthHelperBody(t, f, path, proxyIDField(vendorForeignProxyID)))

			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Equal(t, "PROXY_NOT_FOUND", envelope.Reason)
			require.Equal(t, []int64{vendorForeignProxyID}, f.admin.seen())
			require.Empty(t, f.repo.seen(), "the unscoped lookup must not run for a rejected proxy")
			require.Empty(t, f.client.seen(), "no upstream call may go out")
		})
	}
}

func TestClaudeOAuthVendorUsesOwnWorkspaceProxy(t *testing.T) {
	ownProxyURL := claudeOAuthTestProxy(vendorOwnProxyID).URL()

	t.Run("generate then exchange through the session proxy", func(t *testing.T) {
		f := newClaudeOAuthProxyFixture(t, vendorOAuthScope(service.WorkspacePermissions{AccountManage: true}))

		rec, envelope := postReauth(t, f.router, "/accounts/generate-auth-url", "{"+proxyIDField(vendorOwnProxyID)+"}")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		sessionID, _ := envelope.Data["session_id"].(string)
		require.NotEmpty(t, sessionID)

		rec, _ = postReauth(t, f.router, "/accounts/exchange-code", `{"session_id":"`+sessionID+`","code":"auth-code"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, []int64{vendorOwnProxyID}, f.admin.seen())
		require.Equal(t, []string{ownProxyURL}, f.client.seen())
	})

	for _, path := range claudeOAuthHelperPaths[2:] {
		t.Run(path, func(t *testing.T) {
			f := newClaudeOAuthProxyFixture(t, vendorOAuthScope(service.WorkspacePermissions{AccountManage: true}))

			rec, _ := postReauth(t, f.router, path, claudeOAuthHelperBody(t, f, path, proxyIDField(vendorOwnProxyID)))

			require.NotEqual(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Equal(t, []int64{vendorOwnProxyID}, f.admin.seen())
			require.Equal(t, []string{ownProxyURL}, f.client.seen())
		})
	}
}

func TestClaudeOAuthVendorWithoutProxyIDSkipsLookup(t *testing.T) {
	for _, proxyField := range []string{"", `"proxy_id":null`, `"proxy_id":0`} {
		for _, path := range claudeOAuthHelperPaths {
			t.Run(path+" "+proxyField, func(t *testing.T) {
				f := newClaudeOAuthProxyFixture(t, vendorOAuthScope(service.WorkspacePermissions{AccountManage: true}))

				rec, _ := postReauth(t, f.router, path, claudeOAuthHelperBody(t, f, path, proxyField))

				require.NotEqual(t, http.StatusNotFound, rec.Code, rec.Body.String())
				require.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
				require.Empty(t, f.admin.seen())
				require.Empty(t, f.repo.seen(), "no proxy means a direct connection, as for the station owner")
			})
		}
	}
}

func TestClaudeOAuthStationOwnerProxyUnchanged(t *testing.T) {
	foreignProxyURL := claudeOAuthTestProxy(vendorForeignProxyID).URL()
	for _, path := range claudeOAuthHelperPaths[2:] {
		t.Run(path, func(t *testing.T) {
			adminScope := service.AdminScope()
			f := newClaudeOAuthProxyFixture(t, &adminScope)

			rec, _ := postReauth(t, f.router, path, claudeOAuthHelperBody(t, f, path, proxyIDField(vendorForeignProxyID)))

			require.NotEqual(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Empty(t, f.admin.seen(), "the station owner may use any proxy")
			require.Equal(t, []int64{vendorForeignProxyID}, f.repo.seen())
			require.Equal(t, []string{foreignProxyURL}, f.client.seen())
		})
	}
}

func TestClaudeOAuthVendorNeedsAccountManage(t *testing.T) {
	cases := []struct {
		name  string
		scope *service.Scope
	}{
		{"monitor only", vendorOAuthScope(service.WorkspacePermissions{MonitorView: true})},
		{"scope missing", nil},
	}
	for _, tc := range cases {
		for _, path := range claudeOAuthHelperPaths {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				f := newClaudeOAuthProxyFixture(t, tc.scope)

				rec, envelope := postReauth(t, f.router, path, claudeOAuthHelperBody(t, f, path, proxyIDField(vendorOwnProxyID)))

				require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				require.Equal(t, "WORKSPACE_PERMISSION_DENIED", envelope.Reason)
				require.Empty(t, f.repo.seen())
				require.Empty(t, f.client.seen())
			})
		}
	}
}

func TestConstrainVendorOAuthProxyFailsClosedWithoutLookup(t *testing.T) {
	requested := vendorOwnProxyID
	ctx := service.WithScope(context.Background(), *vendorOAuthScope(service.WorkspacePermissions{AccountManage: true}))

	resolved, err := constrainVendorOAuthProxy(ctx, nil, &requested)

	require.Nil(t, resolved)
	require.ErrorIs(t, err, service.ErrServiceUnavailable)
}
