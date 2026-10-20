package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

const (
	reauthTestAccessToken  = "at-SECRET-reauth"
	reauthTestRefreshToken = "rt-SECRET-reauth"
)

// reauthAdmin keeps accounts in memory so persisted credentials and status
// show up in the response, and records how errors were cleared. Account and
// proxy lookups are workspace scoped like the real AdminService: a vendor
// cannot see another workspace's account, or a proxy listed in
// proxyWorkspaces under another workspace.
type reauthAdmin struct {
	*stubAdminService
	lock                  sync.Mutex
	store                 map[int64]*service.Account
	clearCalls            int
	clearWithOptionsCalls int
	lastClearOptions      service.ClearAccountErrorOptions
	extraUpdates          []map[string]any
	proxyLookups          []int64
	proxyWorkspaces       map[int64]int64
	// afterGetAccount runs once, after the next GetAccount has read its snapshot.
	afterGetAccount func()
	// beforeUpdateAccount runs inside every UpdateAccount call.
	beforeUpdateAccount func()
}

func newReauthAdmin(accounts ...*service.Account) *reauthAdmin {
	admin := &reauthAdmin{stubAdminService: newStubAdminService(), store: map[int64]*service.Account{}}
	for _, account := range accounts {
		admin.store[account.ID] = account
	}
	return admin
}

func cloneReauthAccount(account *service.Account) *service.Account {
	out := *account
	out.Credentials = maps.Clone(account.Credentials)
	out.Extra = maps.Clone(account.Extra)
	return &out
}

func (a *reauthAdmin) GetAccount(ctx context.Context, id int64) (*service.Account, error) {
	a.lock.Lock()
	var snapshot *service.Account
	if account, ok := a.store[id]; ok && service.ScopeFromContextOrDeny(ctx).OwnsWorkspaceID(account.WorkspaceID) {
		snapshot = cloneReauthAccount(account)
	}
	hook := a.afterGetAccount
	a.afterGetAccount = nil
	a.lock.Unlock()
	if hook != nil {
		hook()
	}
	if snapshot == nil {
		return nil, service.ErrAccountNotFound
	}
	return snapshot, nil
}

func (a *reauthAdmin) UpdateAccount(ctx context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	a.lock.Lock()
	defer a.lock.Unlock()
	if a.beforeUpdateAccount != nil {
		a.beforeUpdateAccount()
	}
	if _, err := a.stubAdminService.UpdateAccount(ctx, id, input); err != nil {
		return nil, err
	}
	account := a.store[id]
	account.Credentials = maps.Clone(input.Credentials)
	if input.Type != "" {
		account.Type = input.Type
	}
	return cloneReauthAccount(account), nil
}

func (a *reauthAdmin) UpdateAccountExtra(ctx context.Context, id int64, updates map[string]any) error {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.extraUpdates = append(a.extraUpdates, maps.Clone(updates))
	account := a.store[id]
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	maps.Copy(account.Extra, updates)
	return a.stubAdminService.UpdateAccountExtra(ctx, id, updates)
}

func (a *reauthAdmin) ClearAccountError(_ context.Context, id int64) (*service.Account, error) {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.clearCalls++
	account := a.store[id]
	account.Status = service.StatusActive
	return cloneReauthAccount(account), nil
}

func (a *reauthAdmin) ClearAccountErrorWithOptions(_ context.Context, id int64, opts service.ClearAccountErrorOptions) (*service.ClearAccountErrorResult, error) {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.clearWithOptionsCalls++
	a.lastClearOptions = opts
	account := a.store[id]
	skipped := opts.PreserveInactive && account.Status != service.StatusActive && account.Status != service.StatusError
	if !skipped {
		account.Status = service.StatusActive
	}
	return &service.ClearAccountErrorResult{Account: cloneReauthAccount(account), SkippedInactive: skipped}, nil
}

func (a *reauthAdmin) GetProxy(ctx context.Context, id int64) (*service.Proxy, error) {
	a.lock.Lock()
	a.proxyLookups = append(a.proxyLookups, id)
	workspaceID, listed := a.proxyWorkspaces[id]
	a.lock.Unlock()
	if listed && !service.ScopeFromContextOrDeny(ctx).OwnsWorkspaceID(workspaceID) {
		return nil, service.ErrProxyNotFound
	}
	return a.stubAdminService.GetProxy(ctx, id)
}

type fakeClaudeOAuthClient struct {
	mu            sync.Mutex
	exchangeCalls int
	err           error
	started       chan struct{}
	release       chan struct{}
}

func (f *fakeClaudeOAuthClient) GetOrganizationUUID(context.Context, string, string) (string, error) {
	return "", errors.New("not used")
}

func (f *fakeClaudeOAuthClient) GetAuthorizationCode(context.Context, string, string, string, string, string, string) (string, error) {
	return "", errors.New("not used")
}

func (f *fakeClaudeOAuthClient) ExchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
	f.mu.Lock()
	f.exchangeCalls++
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
		<-f.release
	}
	if f.err != nil {
		return nil, f.err
	}
	return &oauth.TokenResponse{
		AccessToken:  reauthTestAccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    28800,
		RefreshToken: reauthTestRefreshToken,
		Scope:        "user:inference",
		Organization: &oauth.OrgInfo{UUID: "org-1"},
		Account:      &oauth.AccountInfo{UUID: "acct-1", EmailAddress: "owner@example.com"},
	}, nil
}

func (f *fakeClaudeOAuthClient) RefreshToken(context.Context, string, string) (*oauth.TokenResponse, error) {
	return nil, errors.New("not used")
}

func (f *fakeClaudeOAuthClient) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchangeCalls
}

type fakeGrokReauthService struct {
	exchangeCalls int
	lastInput     service.GrokExchangeCodeInput
	err           error
}

func (f *fakeGrokReauthService) RefreshAccountToken(context.Context, *service.Account) (*service.GrokTokenInfo, error) {
	return nil, errors.New("not used")
}

func (f *fakeGrokReauthService) BuildAccountCredentials(tokenInfo *service.GrokTokenInfo) map[string]any {
	return (&service.GrokOAuthService{}).BuildAccountCredentials(tokenInfo)
}

func (f *fakeGrokReauthService) ExchangeCode(_ context.Context, input *service.GrokExchangeCodeInput) (*service.GrokTokenInfo, error) {
	f.exchangeCalls++
	f.lastInput = *input
	if f.err != nil {
		return nil, f.err
	}
	return &service.GrokTokenInfo{
		AccessToken:       reauthTestAccessToken,
		RefreshToken:      reauthTestRefreshToken,
		TokenType:         "Bearer",
		ExpiresIn:         21600,
		ExpiresAt:         time.Now().Add(6 * time.Hour).Unix(),
		Email:             "grok@example.com",
		SubscriptionTier:  "SuperGrok",
		EntitlementStatus: "active",
	}, nil
}

type countingTokenInvalidator struct {
	mu    sync.Mutex
	calls int
}

func (c *countingTokenInvalidator) InvalidateToken(context.Context, *service.Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return nil
}

type reauthDeps struct {
	claude      *service.OAuthService
	openai      *service.OpenAIOAuthService
	gemini      *service.GeminiOAuthService
	antigravity *service.AntigravityOAuthService
	grok        service.GrokOAuthTokenService
	invalidator *countingTokenInvalidator
}

func newReauthHandler(admin service.AdminService, deps reauthDeps) *AccountHandler {
	var invalidator service.TokenCacheInvalidator
	if deps.invalidator != nil {
		invalidator = deps.invalidator
	}
	return NewAccountHandler(admin, deps.claude, deps.openai, deps.gemini, deps.antigravity, deps.grok,
		nil, nil, nil, nil, nil, nil, nil, invalidator)
}

func newReauthRouter(handler *AccountHandler, scope service.Scope) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), scope))
		c.Next()
	})
	router.POST("/accounts/:id/reauthorize", handler.Reauthorize)
	router.POST("/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)
	return router
}

type reauthEnvelope struct {
	Code     int               `json:"code"`
	Reason   string            `json:"reason"`
	Message  string            `json:"message"`
	Metadata map[string]string `json:"metadata"`
	Data     map[string]any    `json:"data"`
}

func postReauth(t *testing.T, router *gin.Engine, path, body string) (*httptest.ResponseRecorder, reauthEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var envelope reauthEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return rec, envelope
}

func newClaudeReauthService(t *testing.T, client *fakeClaudeOAuthClient) *service.OAuthService {
	t.Helper()
	svc := service.NewOAuthService(nil, client)
	t.Cleanup(svc.Stop)
	return svc
}

func newClaudeReauthSession(t *testing.T, svc *service.OAuthService, setupToken bool) string {
	t.Helper()
	var (
		result *service.GenerateAuthURLResult
		err    error
	)
	if setupToken {
		result, err = svc.GenerateSetupTokenURL(context.Background(), nil)
	} else {
		result, err = svc.GenerateAuthURL(context.Background(), nil)
	}
	require.NoError(t, err)
	return result.SessionID
}

func claudeReauthAccount(id int64, status string) *service.Account {
	return &service.Account{
		ID:       id,
		Name:     "claude",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Status:   status,
		Credentials: map[string]any{
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
			"model_mapping": map[string]any{"claude-x": "claude-y"},
			"password":      "must-not-survive",
		},
	}
}

func reauthBody(sessionID string, extra string) string {
	body := `{"session_id":"` + sessionID + `","code":"auth-code"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func TestReauthorizeClaudePersistsCredentialsWithoutLeakingTokens(t *testing.T) {
	client := &fakeClaudeOAuthClient{}
	claude := newClaudeReauthService(t, client)
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError))
	invalidator := &countingTokenInvalidator{}
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude, invalidator: invalidator}), service.AdminScope())

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), ""))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, client.calls())
	input := admin.lastUpdateAccountInput
	require.Equal(t, service.AccountTypeOAuth, input.Type)
	require.Equal(t, reauthTestAccessToken, input.Credentials["access_token"])
	require.Equal(t, reauthTestRefreshToken, input.Credentials["refresh_token"])
	require.IsType(t, "", input.Credentials["expires_at"])
	require.Equal(t, map[string]any{"claude-x": "claude-y"}, input.Credentials["model_mapping"])
	require.NotContains(t, input.Credentials, "password")
	require.Len(t, admin.extraUpdates, 1)
	require.Equal(t, map[string]any{"org_uuid": "org-1", "account_uuid": "acct-1", "email_address": "owner@example.com"}, admin.extraUpdates[0])
	require.Equal(t, 1, admin.clearCalls)
	require.Zero(t, admin.clearWithOptionsCalls)
	require.Equal(t, 1, invalidator.calls)
	require.NotContains(t, rec.Body.String(), reauthTestAccessToken)
	require.NotContains(t, rec.Body.String(), reauthTestRefreshToken)
	require.Equal(t, service.StatusActive, envelope.Data["status"])
	status, ok := envelope.Data["credentials_status"].(map[string]any)
	require.True(t, ok, rec.Body.String())
	require.Equal(t, true, status["has_access_token"])
}

func TestReauthorizeClaudeSetupTokenAuthKindSetsAccountType(t *testing.T) {
	claude := newClaudeReauthService(t, &fakeClaudeOAuthClient{})
	oauthAccount := claudeReauthAccount(1, service.StatusActive)
	setupAccount := claudeReauthAccount(2, service.StatusActive)
	setupAccount.Type = service.AccountTypeSetupToken
	admin := newReauthAdmin(oauthAccount, setupAccount)
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())

	rec, _ := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, true), `"auth_kind":"setup-token"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, service.AccountTypeSetupToken, admin.lastUpdateAccountInput.Type)

	rec, _ = postReauth(t, router, "/accounts/2/reauthorize", reauthBody(newClaudeReauthSession(t, claude, true), ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, service.AccountTypeSetupToken, admin.lastUpdateAccountInput.Type, "auth_kind defaults to the account's type")

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), `"auth_kind":"cookie"`))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, envelope.Reason)
}

func TestReauthorizePreserveStatusKeepsInactiveAccount(t *testing.T) {
	claude := newClaudeReauthService(t, &fakeClaudeOAuthClient{})
	admin := newReauthAdmin(claudeReauthAccount(1, "inactive"), claudeReauthAccount(2, "inactive"))
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), `"preserve_status":true`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, admin.clearWithOptionsCalls)
	require.Equal(t, service.ClearAccountErrorOptions{PreserveInactive: true}, admin.lastClearOptions)
	require.Zero(t, admin.clearCalls)
	require.Equal(t, "inactive", envelope.Data["status"])

	rec, envelope = postReauth(t, router, "/accounts/2/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, admin.clearCalls)
	require.Equal(t, 1, admin.clearWithOptionsCalls)
	require.Equal(t, service.StatusActive, envelope.Data["status"])
}

func TestApplyOAuthCredentialsPreserveStatusIsOptional(t *testing.T) {
	admin := newReauthAdmin(claudeReauthAccount(1, "inactive"), claudeReauthAccount(2, "inactive"))
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{}), service.AdminScope())
	body := `{"type":"oauth","credentials":{"access_token":"new-token"}`

	rec, envelope := postReauth(t, router, "/accounts/1/apply-oauth-credentials", body+`,"preserve_status":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, admin.clearWithOptionsCalls)
	require.Equal(t, service.ClearAccountErrorOptions{PreserveInactive: true}, admin.lastClearOptions)
	require.Zero(t, admin.clearCalls)
	require.Equal(t, "inactive", envelope.Data["status"])
	require.Equal(t, map[string]any{"claude-x": "claude-y"}, admin.lastUpdateAccountInput.Credentials["model_mapping"])

	rec, envelope = postReauth(t, router, "/accounts/2/apply-oauth-credentials", body+`}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, admin.clearCalls)
	require.Equal(t, service.StatusActive, envelope.Data["status"])
}

func TestReauthorizeRejectsNonOAuthBeforeExchange(t *testing.T) {
	client := &fakeClaudeOAuthClient{}
	claude := newClaudeReauthService(t, client)
	apiKey := &service.Account{ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
	admin := newReauthAdmin(apiKey)
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), ""))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "NOT_OAUTH", envelope.Reason)
	require.Zero(t, client.calls())
	require.Zero(t, admin.updateAccountCalls)
}

func TestReauthorizeRejectsUnsupportedTargets(t *testing.T) {
	parentID := int64(99)
	cases := []struct {
		name    string
		account *service.Account
		extra   string
		detail  string
	}{
		{"shadow", &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ParentAccountID: &parentID}, "", "shadow_account"},
		{"codex pat", &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Credentials: map[string]any{"auth_mode": service.OpenAIAuthModePersonalAccessToken}}, "", "codex_personal_access_token"},
		{"unsupported platform", &service.Account{ID: 1, Platform: service.PlatformKiro, Type: service.AccountTypeOAuth}, "", "unsupported_platform"},
		{"setup token outside anthropic", &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}, `"auth_kind":"setup-token"`, "auth_kind_not_supported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grok := &fakeGrokReauthService{}
			tc.account.Status = service.StatusActive
			admin := newReauthAdmin(tc.account)
			router := newReauthRouter(newReauthHandler(admin, reauthDeps{grok: grok}), service.AdminScope())

			rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody("sess-unsupported", tc.extra))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, "REAUTH_UNSUPPORTED", envelope.Reason)
			require.Equal(t, tc.detail, envelope.Metadata["detail"])
			require.Zero(t, admin.updateAccountCalls)
			require.Zero(t, grok.exchangeCalls)
		})
	}
}

func TestReauthorizePassesThroughSessionReason(t *testing.T) {
	claude := newClaudeReauthService(t, &fakeClaudeOAuthClient{})
	openai := service.NewOpenAIOAuthService(nil, nil)
	t.Cleanup(openai.Stop)
	gemini := service.NewGeminiOAuthService(nil, nil, nil, nil, &config.Config{})
	t.Cleanup(gemini.Stop)
	antigravity := service.NewAntigravityOAuthService(nil)
	t.Cleanup(antigravity.Stop)
	cases := []struct {
		platform string
		reason   string
	}{
		{service.PlatformAnthropic, "CLAUDE_OAUTH_SESSION_NOT_FOUND"},
		{service.PlatformOpenAI, "OPENAI_OAUTH_SESSION_NOT_FOUND"},
		{service.PlatformGemini, "GEMINI_OAUTH_SESSION_NOT_FOUND"},
		{service.PlatformAntigravity, "ANTIGRAVITY_OAUTH_SESSION_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			admin := newReauthAdmin(&service.Account{ID: 1, Platform: tc.platform, Type: service.AccountTypeOAuth, Status: service.StatusError})
			handler := newReauthHandler(admin, reauthDeps{claude: claude, openai: openai, gemini: gemini, antigravity: antigravity})
			router := newReauthRouter(handler, service.AdminScope())

			rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody("missing-session", `"state":"some-state"`))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, tc.reason, envelope.Reason)
			require.Zero(t, admin.updateAccountCalls)
		})
	}
}

func TestReauthorizeUnclassifiedExchangeErrorMapsTo502(t *testing.T) {
	client := &fakeClaudeOAuthClient{err: errors.New("request failed: dial tcp 10.0.0.1:443: connect: connection refused")}
	claude := newClaudeReauthService(t, client)
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError))
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), ""))

	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Equal(t, "OAUTH_EXCHANGE_FAILED", envelope.Reason)
	require.Equal(t, service.PlatformAnthropic, envelope.Metadata["platform"])
	require.Contains(t, envelope.Message, "connection refused")
	require.Zero(t, admin.updateAccountCalls)
}

// failingOpenAIOAuthClient fails every code exchange with err.
type failingOpenAIOAuthClient struct{ err error }

func (f failingOpenAIOAuthClient) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	return nil, f.err
}

func (f failingOpenAIOAuthClient) RefreshToken(context.Context, string, string) (*openai.TokenResponse, error) {
	return nil, errors.New("not used")
}

func (f failingOpenAIOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	return nil, errors.New("not used")
}

func newOpenAIReauthSession(t *testing.T, svc *service.OpenAIOAuthService) (sessionID, state string) {
	t.Helper()
	result, err := svc.GenerateAuthURL(context.Background(), nil, "", "")
	require.NoError(t, err)
	parsed, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	return result.SessionID, parsed.Query().Get("state")
}

// OpenAI and Grok report upstream and network failures with their own 502
// reasons; reauthorize folds them into OAUTH_EXCHANGE_FAILED like the other
// platforms and keeps the platform reason in metadata.cause_reason.
func TestReauthorizeOpenAIUpstreamFailuresMapToExchangeFailed(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"upstream_5xx", infraerrors.New(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_EXCHANGE_FAILED", "token exchange failed: status 503, body: upstream unavailable")},
		{"network", infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_REQUEST_FAILED", "request failed: %v", context.DeadlineExceeded)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := service.NewOpenAIOAuthService(nil, failingOpenAIOAuthClient{err: tc.err})
			t.Cleanup(svc.Stop)
			admin := newReauthAdmin(&service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusError})
			router := newReauthRouter(newReauthHandler(admin, reauthDeps{openai: svc}), service.AdminScope())
			sessionID, state := newOpenAIReauthSession(t, svc)

			rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(sessionID, `"state":"`+state+`"`))

			require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
			require.Equal(t, "OAUTH_EXCHANGE_FAILED", envelope.Reason)
			require.Equal(t, service.PlatformOpenAI, envelope.Metadata["platform"])
			require.Equal(t, infraerrors.Reason(tc.err), envelope.Metadata["cause_reason"])
			require.Contains(t, envelope.Message, infraerrors.Message(tc.err))
			require.NotContains(t, envelope.Message, "reason=", "the message carries the cause text, not the debug string")
			require.Zero(t, admin.updateAccountCalls)
		})
	}
}

func TestReauthorizeGrokExchangeErrorMapping(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		status      int
		reason      string
		causeReason string
	}{
		{"upstream_5xx", infraerrors.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_EXCHANGE_FAILED", "token exchange failed: status 500, body: oops"), http.StatusBadGateway, "OAUTH_EXCHANGE_FAILED", "GROK_OAUTH_TOKEN_EXCHANGE_FAILED"},
		{"network", infraerrors.New(http.StatusBadGateway, "GROK_OAUTH_REQUEST_FAILED", "request failed: connection reset by peer"), http.StatusBadGateway, "OAUTH_EXCHANGE_FAILED", "GROK_OAUTH_REQUEST_FAILED"},
		{"code_rejected_passes_through", service.NewOAuthCodeRejectedError(service.GrokOAuthCodeRejectedReason, "token exchange failed: status 400", http.StatusBadRequest), http.StatusBadRequest, service.GrokOAuthCodeRejectedReason, ""},
		{"entitlement_denied_passes_through", infraerrors.New(http.StatusForbidden, "GROK_OAUTH_ENTITLEMENT_DENIED", "token exchange failed: status 403"), http.StatusForbidden, "GROK_OAUTH_ENTITLEMENT_DENIED", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grok := &fakeGrokReauthService{err: tc.err}
			admin := newReauthAdmin(newGrokReauthAccount(""))
			router := newReauthRouter(newReauthHandler(admin, reauthDeps{grok: grok}), service.AdminScope())

			rec, envelope := postReauth(t, router, "/accounts/5/reauthorize", reauthBody("grok-sess", `"state":"s"`))

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, tc.reason, envelope.Reason)
			require.Equal(t, tc.causeReason, envelope.Metadata["cause_reason"])
			if tc.causeReason != "" {
				require.Equal(t, service.PlatformGrok, envelope.Metadata["platform"])
				require.Contains(t, envelope.Message, infraerrors.Message(tc.err))
			}
			require.Zero(t, admin.updateAccountCalls)
		})
	}
}

func TestReauthorizePersistFailureRetriesWithSameSessionWithoutReexchange(t *testing.T) {
	client := &fakeClaudeOAuthClient{}
	claude := newClaudeReauthService(t, client)
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError))
	admin.updateAccountErr = errors.New("db down")
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())
	body := reauthBody(newClaudeReauthSession(t, claude, false), "")

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", body)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.Equal(t, "REAUTH_PERSIST_FAILED", envelope.Reason)
	require.Equal(t, "300", envelope.Metadata["retry_within_seconds"])
	require.NotContains(t, rec.Body.String(), reauthTestAccessToken)
	require.Equal(t, 1, client.calls())

	admin.updateAccountErr = nil
	rec, _ = postReauth(t, router, "/accounts/1/reauthorize", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, client.calls(), "the stashed credentials are saved without a new exchange")
	require.Equal(t, 2, admin.updateAccountCalls)
	require.Equal(t, reauthTestAccessToken, admin.lastUpdateAccountInput.Credentials["access_token"])

	rec, envelope = postReauth(t, router, "/accounts/1/reauthorize", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, client.calls())
	require.Equal(t, 2, admin.updateAccountCalls, "a replay after success returns the account without writing")
	require.Equal(t, float64(1), envelope.Data["id"])
}

func TestReauthorizeStashIsBoundToAccount(t *testing.T) {
	client := &fakeClaudeOAuthClient{}
	claude := newClaudeReauthService(t, client)
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError), claudeReauthAccount(2, service.StatusError))
	admin.updateAccountErr = errors.New("db down")
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())
	body := reauthBody(newClaudeReauthSession(t, claude, false), "")

	rec, _ := postReauth(t, router, "/accounts/1/reauthorize", body)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	admin.updateAccountErr = nil
	rec, envelope := postReauth(t, router, "/accounts/2/reauthorize", body)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "CLAUDE_OAUTH_SESSION_NOT_FOUND", envelope.Reason)
	require.Equal(t, 1, admin.updateAccountCalls, "account 2 is never written")
	require.Equal(t, "old-access", admin.store[2].Credentials["access_token"])
}

func TestReauthorizeConcurrentRetryWaitsForInflightExchange(t *testing.T) {
	client := &fakeClaudeOAuthClient{started: make(chan struct{}, 2), release: make(chan struct{})}
	claude := newClaudeReauthService(t, client)
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError))
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())
	body := reauthBody(newClaudeReauthSession(t, claude, false), "")

	codes := make(chan int, 2)
	send := func() {
		req := httptest.NewRequest(http.MethodPost, "/accounts/1/reauthorize", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		codes <- rec.Code
	}
	go send()
	<-client.started
	go send()
	time.Sleep(100 * time.Millisecond)
	close(client.release)

	require.Equal(t, http.StatusOK, <-codes)
	require.Equal(t, http.StatusOK, <-codes)
	require.Equal(t, 1, client.calls())
	require.Equal(t, 1, admin.updateAccountCalls)
}

// A retry can read the account just before the first request saves and reach
// the flight just after it finished; the replay must answer with the saved
// account, not with the snapshot taken before the save.
func TestReauthorizeReplayAfterSaveReturnsSavedAccount(t *testing.T) {
	client := &fakeClaudeOAuthClient{}
	claude := newClaudeReauthService(t, client)
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError))
	handler := newReauthHandler(admin, reauthDeps{claude: claude})
	router := newReauthRouter(handler, service.AdminScope())
	sessionID := newClaudeReauthSession(t, claude, false)
	admin.afterGetAccount = func() {
		admin.lock.Lock()
		admin.store[1].Status = service.StatusActive
		admin.store[1].Credentials["access_token"] = reauthTestAccessToken
		admin.lock.Unlock()
		handler.reauth.credentialStash().markDone(reauthStashKey(service.PlatformAnthropic, sessionID, 1))
	}

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(sessionID, ""))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, service.StatusActive, envelope.Data["status"], "the replay reads the account again")
	require.Zero(t, client.calls())
	require.Zero(t, admin.updateAccountCalls)
}

// retry_within_seconds counts from the failed save, not from the exchange, and
// a replay works for the full window after the successful save.
func TestReauthorizeStashWindowsCountFromFailureAndSave(t *testing.T) {
	client := &fakeClaudeOAuthClient{}
	claude := newClaudeReauthService(t, client)
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError))
	handler := newReauthHandler(admin, reauthDeps{claude: claude})
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	handler.reauth.credentialStash().now = func() time.Time { return now }
	router := newReauthRouter(handler, service.AdminScope())
	body := reauthBody(newClaudeReauthSession(t, claude, false), "")

	// The exchange stashes at T0; the save then runs into its 30 second timeout.
	admin.updateAccountErr = errors.New("db down")
	admin.beforeUpdateAccount = func() { now = now.Add(reauthorizePersistTimeout) }
	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", body)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.Equal(t, "300", envelope.Metadata["retry_within_seconds"])

	// Retry 4m50s after the 500 (5m20s after the exchange): saved from the stash.
	admin.updateAccountErr = nil
	admin.beforeUpdateAccount = nil
	now = now.Add(4*time.Minute + 50*time.Second)
	rec, _ = postReauth(t, router, "/accounts/1/reauthorize", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, client.calls(), "no new exchange within the advertised window")
	require.Equal(t, 2, admin.updateAccountCalls)

	// Replay 4m50s after the save: still answered from the done marker.
	now = now.Add(4*time.Minute + 50*time.Second)
	rec, envelope = postReauth(t, router, "/accounts/1/reauthorize", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, client.calls())
	require.Equal(t, 2, admin.updateAccountCalls)
	require.Equal(t, service.StatusActive, envelope.Data["status"])
}

func TestReauthorizeDoesNotLogTokens(t *testing.T) {
	var buf bytes.Buffer
	oldSlog, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&buf)
	t.Cleanup(func() {
		slog.SetDefault(oldSlog)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})

	claude := newClaudeReauthService(t, &fakeClaudeOAuthClient{})
	admin := newReauthAdmin(claudeReauthAccount(1, service.StatusError), claudeReauthAccount(2, service.StatusError))
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), service.AdminScope())

	rec, _ := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), ""))
	require.Equal(t, http.StatusOK, rec.Code)
	admin.updateAccountErr = errors.New("db down: access_token=" + reauthTestAccessToken)
	rec, _ = postReauth(t, router, "/accounts/2/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), ""))
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	require.NotEmpty(t, buf.String(), "the persist failure is logged")
	require.NotContains(t, buf.String(), reauthTestAccessToken)
	require.NotContains(t, buf.String(), reauthTestRefreshToken)
	require.NotContains(t, rec.Body.String(), reauthTestAccessToken)
}

func newGrokReauthAccount(baseURL string) *service.Account {
	proxyID := int64(9)
	creds := map[string]any{"access_token": "old-access", "refresh_token": "old-refresh"}
	if baseURL != "" {
		creds["base_url"] = baseURL
	}
	return &service.Account{
		ID:          5,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusError,
		ProxyID:     &proxyID,
		WorkspaceID: 7,
		Credentials: creds,
	}
}

func TestReauthorizeVendorUsesAccountProxy(t *testing.T) {
	cases := []struct {
		name      string
		scope     service.Scope
		extra     string
		wantCode  int
		wantProxy int64
	}{
		{"vendor ignores proxy_id", service.VendorScope(7, service.WorkspacePermissions{AccountManage: true}), `"state":"s","proxy_id":42`, http.StatusOK, 9},
		{"owner proxy_id wins", service.AdminScope(), `"state":"s","proxy_id":42`, http.StatusOK, 42},
		{"owner defaults to account proxy", service.AdminScope(), `"state":"s"`, http.StatusOK, 9},
		{"vendor without account manage", service.VendorScope(7, service.WorkspacePermissions{MonitorView: true}), `"state":"s"`, http.StatusForbidden, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grok := &fakeGrokReauthService{}
			admin := newReauthAdmin(newGrokReauthAccount(""))
			router := newReauthRouter(newReauthHandler(admin, reauthDeps{grok: grok}), tc.scope)

			rec, _ := postReauth(t, router, "/accounts/5/reauthorize", reauthBody("grok-sess", tc.extra))

			require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())
			if tc.wantCode != http.StatusOK {
				require.Zero(t, grok.exchangeCalls)
				return
			}
			require.Equal(t, 1, grok.exchangeCalls)
			require.NotNil(t, grok.lastInput.ProxyID)
			require.Equal(t, tc.wantProxy, *grok.lastInput.ProxyID)
			if tc.scope.IsVendor() {
				require.Equal(t, []int64{9}, admin.proxyLookups, "vendor proxy is checked against the workspace")
			}
		})
	}
}

// A vendor's account bound to a proxy of another workspace stops before any
// exchange; the same account with its own workspace's proxy goes through, so
// the rejection comes from the workspace check.
func TestReauthorizeVendorAccountProxyOutsideWorkspaceStopsBeforeExchange(t *testing.T) {
	vendor := service.VendorScope(7, service.WorkspacePermissions{AccountManage: true})
	cases := []struct {
		name           string
		proxyWorkspace int64
		wantCode       int
		wantReason     string
		wantExchanges  int
	}{
		{"foreign proxy", 8, http.StatusNotFound, "PROXY_NOT_FOUND", 0},
		{"own proxy", 7, http.StatusOK, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grok := &fakeGrokReauthService{}
			admin := newReauthAdmin(newGrokReauthAccount(""))
			admin.proxyWorkspaces = map[int64]int64{9: tc.proxyWorkspace}
			router := newReauthRouter(newReauthHandler(admin, reauthDeps{grok: grok}), vendor)

			rec, envelope := postReauth(t, router, "/accounts/5/reauthorize", reauthBody("grok-sess", `"state":"s","proxy_id":42`))

			require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())
			require.Equal(t, tc.wantReason, envelope.Reason)
			require.Equal(t, tc.wantExchanges, grok.exchangeCalls)
			require.Equal(t, []int64{9}, admin.proxyLookups, "only the account's own proxy is looked up")
			if tc.wantCode != http.StatusOK {
				require.Zero(t, admin.updateAccountCalls)
				require.Empty(t, admin.extraUpdates)
				require.Equal(t, "old-access", admin.store[5].Credentials["access_token"])
			}
		})
	}
}

// Another workspace's account is not found for a vendor: no proxy lookup, no
// exchange and no write.
func TestReauthorizeVendorForeignAccountNotFound(t *testing.T) {
	vendor := service.VendorScope(7, service.WorkspacePermissions{AccountManage: true})

	t.Run("grok", func(t *testing.T) {
		account := newGrokReauthAccount("")
		account.WorkspaceID = 8
		grok := &fakeGrokReauthService{}
		admin := newReauthAdmin(account)
		router := newReauthRouter(newReauthHandler(admin, reauthDeps{grok: grok}), vendor)

		rec, envelope := postReauth(t, router, "/accounts/5/reauthorize", reauthBody("grok-sess", `"state":"s"`))

		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		require.Equal(t, "ACCOUNT_NOT_FOUND", envelope.Reason)
		require.Zero(t, grok.exchangeCalls)
		require.Empty(t, admin.proxyLookups)
		require.Zero(t, admin.updateAccountCalls)
		require.Empty(t, admin.extraUpdates)
	})

	t.Run("claude without proxy", func(t *testing.T) {
		client := &fakeClaudeOAuthClient{}
		claude := newClaudeReauthService(t, client)
		account := claudeReauthAccount(1, service.StatusError)
		account.WorkspaceID = 8
		admin := newReauthAdmin(account)
		router := newReauthRouter(newReauthHandler(admin, reauthDeps{claude: claude}), vendor)

		rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(newClaudeReauthSession(t, claude, false), ""))

		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		require.Equal(t, "ACCOUNT_NOT_FOUND", envelope.Reason)
		require.Zero(t, client.calls())
		require.Zero(t, admin.updateAccountCalls)
		require.Equal(t, service.StatusError, admin.store[1].Status)
	})
}

func TestReauthorizeGrokKeepsBaseURLAndClearsReauthFlag(t *testing.T) {
	grok := &fakeGrokReauthService{}
	admin := newReauthAdmin(newGrokReauthAccount("https://grok.custom.example/v1"))
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{grok: grok}), service.AdminScope())

	rec, _ := postReauth(t, router, "/accounts/5/reauthorize", reauthBody("grok-sess", `"state":"s"`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	creds := admin.lastUpdateAccountInput.Credentials
	require.Equal(t, "https://grok.custom.example/v1", creds["base_url"])
	require.Equal(t, reauthTestAccessToken, creds["access_token"])
	require.Contains(t, admin.extraUpdates, map[string]any{"email": "grok@example.com", "subscription_tier": "SuperGrok", "entitlement_status": "active"})
	require.Contains(t, admin.extraUpdates, map[string]any{"grok_needs_reauth": false, "grok_needs_reauth_reason": "", "grok_needs_reauth_at": ""})

	admin = newReauthAdmin(newGrokReauthAccount(""))
	router = newReauthRouter(newReauthHandler(admin, reauthDeps{grok: grok}), service.AdminScope())
	rec, _ = postReauth(t, router, "/accounts/5/reauthorize", reauthBody("grok-sess-2", `"state":"s"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, admin.lastUpdateAccountInput.Credentials, "base_url", "like the Web flow, base_url is left to the system default")
}

// succeedingOpenAIOAuthClient answers every code exchange with resp.
type succeedingOpenAIOAuthClient struct{ resp *openai.TokenResponse }

func (f succeedingOpenAIOAuthClient) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	resp := *f.resp
	return &resp, nil
}

func (f succeedingOpenAIOAuthClient) RefreshToken(context.Context, string, string) (*openai.TokenResponse, error) {
	return nil, errors.New("not used")
}

func (f succeedingOpenAIOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	return nil, errors.New("not used")
}

// unsignedTestJWT builds a JWT the OpenAI service can decode (it does not
// verify signatures).
func unsignedTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestReauthorizeOpenAISavesCredentialsAndExtraLikeWeb(t *testing.T) {
	idToken := unsignedTestJWT(t, map[string]any{
		"email": "openai-owner@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-openai",
			"chatgpt_plan_type":  "plus",
		},
	})
	svc := service.NewOpenAIOAuthService(nil, succeedingOpenAIOAuthClient{resp: &openai.TokenResponse{
		AccessToken:  reauthTestAccessToken,
		RefreshToken: reauthTestRefreshToken,
		IDToken:      idToken,
		TokenType:    "Bearer",
		ExpiresIn:    864000,
	}})
	t.Cleanup(svc.Stop)
	// The privacy call fails offline, which still yields a privacy_mode value.
	svc.SetPrivacyClientFactory(func(string) (*req.Client, error) { return nil, errors.New("offline in tests") })
	account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusError,
		Credentials: map[string]any{"access_token": "old-access", "refresh_token": "old-refresh", "model_mapping": map[string]any{"gpt-x": "gpt-y"}}}
	admin := newReauthAdmin(account)
	router := newReauthRouter(newReauthHandler(admin, reauthDeps{openai: svc}), service.AdminScope())
	sessionID, state := newOpenAIReauthSession(t, svc)

	rec, envelope := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(sessionID, `"state":"`+state+`"`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	input := admin.lastUpdateAccountInput
	require.Equal(t, service.AccountTypeOAuth, input.Type)
	require.Equal(t, reauthTestAccessToken, input.Credentials["access_token"])
	require.Equal(t, reauthTestRefreshToken, input.Credentials["refresh_token"])
	require.Equal(t, "openai-owner@example.com", input.Credentials["email"])
	require.Equal(t, "acct-openai", input.Credentials["chatgpt_account_id"])
	require.Equal(t, "plus", input.Credentials["plan_type"])
	require.Equal(t, "864000", input.Credentials["expires_in"])
	expiresAt, ok := input.Credentials["expires_at"].(string)
	require.True(t, ok)
	_, err := time.Parse(time.RFC3339, expiresAt)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"gpt-x": "gpt-y"}, input.Credentials["model_mapping"])
	require.Equal(t, []map[string]any{{"email": "openai-owner@example.com", "privacy_mode": service.PrivacyModeFailed}}, admin.extraUpdates)
	require.Equal(t, service.StatusActive, envelope.Data["status"])
	require.NotContains(t, rec.Body.String(), reauthTestAccessToken)
	require.NotContains(t, rec.Body.String(), reauthTestRefreshToken)
}

type fakeGeminiReauthOAuthClient struct{}

func (fakeGeminiReauthOAuthClient) ExchangeCode(context.Context, string, string, string, string, string) (*geminicli.TokenResponse, error) {
	return &geminicli.TokenResponse{AccessToken: reauthTestAccessToken, RefreshToken: reauthTestRefreshToken, TokenType: "Bearer", ExpiresIn: 3600, Scope: "drive.readonly"}, nil
}

func (fakeGeminiReauthOAuthClient) RefreshToken(context.Context, string, string, string) (*geminicli.TokenResponse, error) {
	return nil, errors.New("not used")
}

type fakeGeminiDriveClient struct {
	info *geminicli.DriveStorageInfo
	err  error
}

func (f fakeGeminiDriveClient) GetStorageQuota(context.Context, string, string) (*geminicli.DriveStorageInfo, error) {
	return f.info, f.err
}

// Gemini (google_one): the Drive storage info goes to extra, never into the
// credentials, and without a detected tier the stored tier_id is kept.
func TestReauthorizeGeminiDriveInfoGoesToExtraAndTierDefaultsToStored(t *testing.T) {
	const driveLimit = int64(3) << 40 // 3 TiB
	cases := []struct {
		name       string
		drive      fakeGeminiDriveClient
		wantTierID string
		wantExtra  bool
	}{
		{"drive tier detected", fakeGeminiDriveClient{info: &geminicli.DriveStorageInfo{Limit: driveLimit, Usage: 1024}}, service.GeminiTierGoogleAIPro, true},
		{"drive unavailable keeps stored tier", fakeGeminiDriveClient{err: errors.New("status 403")}, service.GeminiTierGoogleAIUltra, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gemini := service.NewGeminiOAuthService(nil, fakeGeminiReauthOAuthClient{}, nil, tc.drive, &config.Config{})
			t.Cleanup(gemini.Stop)
			session, err := gemini.GenerateAuthURL(context.Background(), nil, "", "proj-1", "google_one", "")
			require.NoError(t, err)
			account := &service.Account{ID: 1, Platform: service.PlatformGemini, Type: service.AccountTypeOAuth, Status: service.StatusError,
				Credentials: map[string]any{"access_token": "old-access", "oauth_type": "google_one", "tier_id": service.GeminiTierGoogleAIUltra, "project_id": "proj-1"}}
			admin := newReauthAdmin(account)
			router := newReauthRouter(newReauthHandler(admin, reauthDeps{gemini: gemini}), service.AdminScope())

			rec, _ := postReauth(t, router, "/accounts/1/reauthorize", reauthBody(session.SessionID, `"state":"`+session.State+`"`))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			creds := admin.lastUpdateAccountInput.Credentials
			require.Equal(t, reauthTestAccessToken, creds["access_token"])
			require.Equal(t, reauthTestRefreshToken, creds["refresh_token"])
			require.Equal(t, "google_one", creds["oauth_type"])
			require.Equal(t, "proj-1", creds["project_id"])
			require.Equal(t, tc.wantTierID, creds["tier_id"])
			for _, key := range []string{"drive_storage_limit", "drive_storage_usage", "drive_tier_updated_at"} {
				require.NotContains(t, creds, key, "Drive info stays out of the credentials")
			}
			if !tc.wantExtra {
				require.Empty(t, admin.extraUpdates)
				return
			}
			require.Len(t, admin.extraUpdates, 1)
			extra := admin.extraUpdates[0]
			require.Equal(t, driveLimit, extra["drive_storage_limit"])
			require.Equal(t, int64(1024), extra["drive_storage_usage"])
			require.NotEmpty(t, extra["drive_tier_updated_at"])
		})
	}
}

// Antigravity's exchange always calls Google, so the handler's mapping from
// the token info is checked directly.
func TestAntigravityReauthCredentialsWritePrivacyModeToExtra(t *testing.T) {
	svc := &service.AntigravityOAuthService{}
	info := &service.AntigravityTokenInfo{
		AccessToken:  reauthTestAccessToken,
		RefreshToken: reauthTestRefreshToken,
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		TokenType:    "Bearer",
		Email:        "ag@example.com",
		ProjectID:    "ag-project",
		PlanType:     "pro",
		PrivacyMode:  "privacy_set",
	}

	creds := antigravityReauthCredentials(svc, info)
	require.Equal(t, service.AccountTypeOAuth, creds.accountType)
	require.Equal(t, svc.BuildAccountCredentials(info), creds.credentials)
	require.Equal(t, "ag@example.com", creds.credentials["email"])
	require.Equal(t, "ag-project", creds.credentials["project_id"])
	require.NotContains(t, creds.credentials, "privacy_mode")
	require.Equal(t, map[string]any{"privacy_mode": "privacy_set"}, creds.extra)

	info.PrivacyMode = ""
	require.Nil(t, antigravityReauthCredentials(svc, info).extra, "no privacy_mode, no extra update")
}
