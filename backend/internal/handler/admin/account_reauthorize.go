package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const (
	// reauthorizeExchangeTimeout bounds the upstream exchange, which keeps
	// running after the client disconnects (the code can be used only once).
	reauthorizeExchangeTimeout = 2 * time.Minute
	// reauthorizePersistTimeout bounds saving the exchanged credentials.
	reauthorizePersistTimeout = 30 * time.Second
	// reauthorizeErrorMessageMax caps the upstream text echoed in
	// OAUTH_EXCHANGE_FAILED messages.
	reauthorizeErrorMessageMax = 512
)

// ReauthorizeAccountRequest is the body of POST /api/v1/admin/accounts/:id/reauthorize.
//
// session_id / code / state / redirect_uri come from the platform's usual
// auth-url flow. proxy_id defaults to the account's proxy and is ignored for
// vendors. auth_kind (oauth | setup-token, Anthropic only) defaults to the
// account's type; oauth_type and tier_id only matter for Gemini (tier_id
// defaults to the stored one).
type ReauthorizeAccountRequest struct {
	SessionID      string `json:"session_id" binding:"required"`
	Code           string `json:"code" binding:"required"`
	State          string `json:"state"`
	RedirectURI    string `json:"redirect_uri"`
	ProxyID        *int64 `json:"proxy_id"`
	AuthKind       string `json:"auth_kind" binding:"omitempty,oneof=oauth setup-token"`
	OAuthType      string `json:"oauth_type" binding:"omitempty,oneof=code_assist google_one ai_studio"`
	TierID         string `json:"tier_id"`
	PreserveStatus bool   `json:"preserve_status"`
}

// reauthCoordinator makes reauthorize retries idempotent: concurrent requests
// for the same session and account share one exchange, and the stash lets a
// failed save be retried without a new authorization code. A nil coordinator
// (handlers built without NewAccountHandler) simply runs every request.
type reauthCoordinator struct {
	stash  *reauthCredentialStash
	flight singleflight.Group
}

func newReauthCoordinator() *reauthCoordinator {
	return &reauthCoordinator{stash: newReauthCredentialStash(reauthStashTTL, reauthStashMaxEntries)}
}

func (r *reauthCoordinator) credentialStash() *reauthCredentialStash {
	if r == nil {
		return nil
	}
	return r.stash
}

func (r *reauthCoordinator) do(key string, fn func() (*service.Account, error)) (*service.Account, error) {
	if r == nil {
		return fn()
	}
	value, err, _ := r.flight.Do(key, func() (any, error) { return fn() })
	account, _ := value.(*service.Account)
	return account, err
}

// reauthCredentials is what one platform exchange produced for the account.
type reauthCredentials struct {
	accountType string
	credentials map[string]any
	extra       map[string]any
}

var reauthSupportedPlatforms = map[string]bool{
	service.PlatformAnthropic:   true,
	service.PlatformOpenAI:      true,
	service.PlatformGemini:      true,
	service.PlatformAntigravity: true,
	service.PlatformGrok:        true,
}

// Reauthorize exchanges an OAuth authorization code and saves the new
// credentials onto an existing account in one request; tokens never leave the
// server. The response is the account, like apply-oauth-credentials.
// POST /api/v1/admin/accounts/:id/reauthorize
func (h *AccountHandler) Reauthorize(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req ReauthorizeAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	ctx := c.Request.Context()
	existing, proxyID, err := h.prepareReauthorize(ctx, accountID, &req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// The authorization code works once, so the exchange and the save finish
	// even when the client goes away; a retry with the same session_id then
	// gets the result instead of a used code.
	opCtx := context.WithoutCancel(ctx)
	key := reauthStashKey(existing.Platform, req.SessionID, accountID)
	account, err := h.reauth.do(key, func() (*service.Account, error) {
		return h.reauthorizeOnce(opCtx, key, accountID, existing, &req, proxyID)
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.buildAccountResponseWithRuntime(ctx, account))
}

// prepareReauthorize checks permission, ownership and eligibility before any
// upstream call, and resolves the proxy for the exchange.
func (h *AccountHandler) prepareReauthorize(ctx context.Context, accountID int64, req *ReauthorizeAccountRequest) (*service.Account, *int64, error) {
	scope := service.ScopeFromContextOrDeny(ctx)
	if err := scope.RequireAccountManage(); err != nil {
		return nil, nil, err
	}
	// GetAccount is workspace scoped: another workspace's account is not found.
	existing, err := h.adminService.GetAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if existing == nil {
		return nil, nil, service.ErrAccountNotFound
	}
	if !existing.IsOAuth() {
		return nil, nil, service.ErrAccountNotOAuth
	}
	if detail := reauthUnsupportedDetail(existing, req.AuthKind); detail != "" {
		return nil, nil, service.ErrReauthUnsupported.WithMetadata(map[string]string{"detail": detail})
	}
	proxyID, err := h.resolveReauthProxy(ctx, scope, existing, req.ProxyID)
	if err != nil {
		return nil, nil, err
	}
	return existing, proxyID, nil
}

// reauthUnsupportedDetail names why an OAuth account cannot use this flow, or
// returns "" when it can.
func reauthUnsupportedDetail(account *service.Account, authKind string) string {
	switch {
	case account.IsCredentialShadow():
		// Shadow accounts share the parent's credentials.
		return "shadow_account"
	case account.IsOpenAIPersonalAccessToken():
		// Merging OAuth tokens would leave the PAT auth_mode behind.
		return "codex_personal_access_token"
	case !reauthSupportedPlatforms[account.Platform]:
		return "unsupported_platform"
	case authKind == service.AccountTypeSetupToken && account.Platform != service.PlatformAnthropic:
		return "auth_kind_not_supported"
	}
	return ""
}

// resolveReauthProxy picks the exchange proxy. Vendors always use the
// account's own proxy, checked against their workspace, because the OAuth
// services look proxies up without a workspace filter.
func (h *AccountHandler) resolveReauthProxy(ctx context.Context, scope service.Scope, account *service.Account, requested *int64) (*int64, error) {
	if !scope.IsVendor() {
		if requested != nil {
			return requested, nil
		}
		return account.ProxyID, nil
	}
	if account.ProxyID != nil {
		if _, err := h.adminService.GetProxy(ctx, *account.ProxyID); err != nil {
			return nil, err
		}
	}
	return account.ProxyID, nil
}

// reauthorizeOnce exchanges the code (unless a previous attempt already did)
// and saves the credentials. A replay after a successful save reads the
// account again without writing.
func (h *AccountHandler) reauthorizeOnce(ctx context.Context, key string, accountID int64, existing *service.Account, req *ReauthorizeAccountRequest, proxyID *int64) (*service.Account, error) {
	stash := h.reauth.credentialStash()
	entry, found := stash.lookup(key)
	if found && entry.done {
		// existing may predate the save: a retry can read the account just
		// before the first request saved and get here just after it finished.
		return h.reloadReauthorizedAccount(ctx, accountID)
	}
	creds := reauthCredentials{accountType: entry.accountType, credentials: entry.credentials, extra: entry.extra}
	if !found {
		exchanged, err := h.exchangeForReauth(ctx, existing, req, proxyID)
		if err != nil {
			slog.Warn("reauthorize.exchange_failed",
				"account_id", accountID,
				"platform", existing.Platform,
				"reason", infraerrors.Reason(err),
			)
			return nil, err
		}
		creds = *exchanged
		stash.put(key, reauthStashEntry{accountType: creds.accountType, credentials: creds.credentials, extra: creds.extra})
	}

	persistCtx, cancel := context.WithTimeout(ctx, reauthorizePersistTimeout)
	defer cancel()
	account, err := h.persistOAuthCredentials(persistCtx, accountID, existing, oauthCredentialsPersistInput{
		Type:           creds.accountType,
		Credentials:    creds.credentials,
		Extra:          creds.extra,
		PreserveStatus: req.PreserveStatus,
		LogPrefix:      "reauthorize",
	})
	if err != nil {
		slog.Error("reauthorize.persist_failed",
			"account_id", accountID,
			"platform", existing.Platform,
			"reason", infraerrors.Reason(err),
			"err", logredact.RedactText(err.Error()),
		)
		// Restart the stash window so retry_within_seconds counts from this
		// response rather than from the exchange (the save may take up to
		// reauthorizePersistTimeout).
		stash.put(key, reauthStashEntry{accountType: creds.accountType, credentials: creds.credentials, extra: creds.extra})
		return nil, reauthPersistFailed(err)
	}
	stash.markDone(key)
	return account, nil
}

// reloadReauthorizedAccount answers a replay with the account as stored now.
func (h *AccountHandler) reloadReauthorizedAccount(ctx context.Context, accountID int64) (*service.Account, error) {
	account, err := h.adminService.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, service.ErrAccountNotFound
	}
	return account, nil
}

func reauthPersistFailed(cause error) error {
	metadata := map[string]string{"retry_within_seconds": strconv.Itoa(int(reauthStashTTL / time.Second))}
	if reason := infraerrors.Reason(cause); reason != "" {
		metadata["cause_reason"] = reason
	}
	return service.ErrReauthPersistFailed.WithMetadata(metadata).WithCause(cause)
}

// mapReauthExchangeError turns upstream and network failures into 502
// OAUTH_EXCHANGE_FAILED with a redacted cause in the message, and keeps the
// other errors that carry a stable reason (session expired, state mismatch,
// code rejected, entitlement denied, ...). OpenAI and Grok report upstream and
// network failures as 502 with their own reasons (*_TOKEN_EXCHANGE_FAILED,
// *_REQUEST_FAILED, ...); those are folded in as well so every platform answers
// alike, with the platform reason kept in metadata.cause_reason.
func mapReauthExchangeError(platform string, err error) error {
	reason := infraerrors.Reason(err)
	if reason != "" && infraerrors.Code(err) != http.StatusBadGateway {
		return err
	}
	metadata := map[string]string{"platform": platform}
	detail := err.Error()
	if reason != "" {
		metadata["cause_reason"] = reason
		detail = infraerrors.Message(err)
	}
	appErr := service.ErrOAuthExchangeFailed.WithMetadata(metadata).WithCause(err)
	appErr.Message += ": " + truncateRunes(logredact.RedactText(detail), reauthorizeErrorMessageMax)
	return appErr
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}
