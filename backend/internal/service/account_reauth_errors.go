package service

import (
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Stable reasons of POST /api/v1/admin/accounts/:id/reauthorize
// (docs/API_ERROR_CODES.md). The App branches on them.
var (
	// ErrAccountNotOAuth: the account does not use OAuth / setup-token credentials.
	ErrAccountNotOAuth = infraerrors.BadRequest("NOT_OAUTH", "account is not an OAuth account")
	// ErrReauthUnsupported: an OAuth account this flow cannot reauthorize;
	// metadata.detail tells which rule rejected it.
	ErrReauthUnsupported = infraerrors.BadRequest("REAUTH_UNSUPPORTED", "account cannot be reauthorized with this flow")
	// ErrOAuthExchangeFailed: the code exchange failed upstream or on the
	// network on any platform; metadata.platform names the platform and
	// metadata.cause_reason keeps the platform's own 502 reason when it had one.
	ErrOAuthExchangeFailed = infraerrors.New(http.StatusBadGateway, "OAUTH_EXCHANGE_FAILED", "oauth code exchange failed")
	// ErrReauthPersistFailed: the exchange succeeded but saving failed. The new
	// credentials are kept for a while; retry with the same session_id.
	ErrReauthPersistFailed = infraerrors.InternalServer("REAUTH_PERSIST_FAILED", "authorization succeeded but saving credentials failed; retry with the same session_id within 5 minutes")
)
