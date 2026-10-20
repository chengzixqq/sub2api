package admin

import (
	"context"
	"maps"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// grokCodeExchanger is the exchange half of *service.GrokOAuthService. The
// handler holds Grok through the narrower refresh port, so the exchange is
// reached through this assertion instead of new wiring.
type grokCodeExchanger interface {
	ExchangeCode(ctx context.Context, input *service.GrokExchangeCodeInput) (*service.GrokTokenInfo, error)
}

// exchangeForReauth runs the platform's code exchange and builds credentials
// and extra the same way the Web re-authorization does.
func (h *AccountHandler) exchangeForReauth(ctx context.Context, account *service.Account, req *ReauthorizeAccountRequest, proxyID *int64) (*reauthCredentials, error) {
	ctx, cancel := context.WithTimeout(ctx, reauthorizeExchangeTimeout)
	defer cancel()

	var (
		creds *reauthCredentials
		err   error
	)
	switch account.Platform {
	case service.PlatformAnthropic:
		creds, err = h.exchangeClaudeReauth(ctx, account, req, proxyID)
	case service.PlatformOpenAI:
		creds, err = h.exchangeOpenAIReauth(ctx, req, proxyID)
	case service.PlatformGemini:
		creds, err = h.exchangeGeminiReauth(ctx, account, req, proxyID)
	case service.PlatformAntigravity:
		creds, err = h.exchangeAntigravityReauth(ctx, req, proxyID)
	case service.PlatformGrok:
		creds, err = h.exchangeGrokReauth(ctx, req, proxyID)
	default:
		return nil, service.ErrReauthUnsupported.WithMetadata(map[string]string{"detail": "unsupported_platform"})
	}
	if err != nil {
		return nil, mapReauthExchangeError(account.Platform, err)
	}
	return creds, nil
}

func (h *AccountHandler) exchangeClaudeReauth(ctx context.Context, account *service.Account, req *ReauthorizeAccountRequest, proxyID *int64) (*reauthCredentials, error) {
	if h.oauthService == nil {
		return nil, service.ErrServiceUnavailable
	}
	info, err := h.oauthService.ExchangeCode(ctx, &service.ExchangeCodeInput{
		SessionID: req.SessionID,
		Code:      req.Code,
		ProxyID:   proxyID,
	})
	if err != nil {
		return nil, err
	}
	accountType := req.AuthKind
	if accountType == "" {
		accountType = account.Type
	}
	return &reauthCredentials{
		accountType: accountType,
		credentials: service.BuildClaudeAccountCredentials(info),
		extra: nonEmptyReauthExtra(map[string]string{
			"org_uuid":      info.OrgUUID,
			"account_uuid":  info.AccountUUID,
			"email_address": info.EmailAddress,
		}),
	}, nil
}

func (h *AccountHandler) exchangeOpenAIReauth(ctx context.Context, req *ReauthorizeAccountRequest, proxyID *int64) (*reauthCredentials, error) {
	if h.openaiOAuthService == nil {
		return nil, service.ErrServiceUnavailable
	}
	info, err := h.openaiOAuthService.ExchangeCode(ctx, &service.OpenAIExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     proxyID,
	})
	if err != nil {
		return nil, err
	}
	return &reauthCredentials{
		accountType: service.AccountTypeOAuth,
		credentials: h.openaiOAuthService.BuildAccountCredentials(info),
		extra:       nonEmptyReauthExtra(map[string]string{"email": info.Email, "privacy_mode": info.PrivacyMode}),
	}, nil
}

func (h *AccountHandler) exchangeGeminiReauth(ctx context.Context, account *service.Account, req *ReauthorizeAccountRequest, proxyID *int64) (*reauthCredentials, error) {
	if h.geminiOAuthService == nil {
		return nil, service.ErrServiceUnavailable
	}
	oauthType := firstNonEmpty(req.OAuthType, account.GetCredential("oauth_type"), "code_assist")
	tierID := firstNonEmpty(req.TierID, account.GetCredential("tier_id"))
	info, err := h.geminiOAuthService.ExchangeCode(ctx, &service.GeminiExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   proxyID,
		OAuthType: oauthType,
		TierID:    tierID,
	})
	if err != nil {
		return nil, err
	}
	// Drive storage metadata belongs in extra (as on creation and tier refresh),
	// not in the credentials.
	tokenInfo := *info
	extra := maps.Clone(tokenInfo.Extra)
	tokenInfo.Extra = nil
	return &reauthCredentials{
		accountType: service.AccountTypeOAuth,
		credentials: h.geminiOAuthService.BuildAccountCredentials(&tokenInfo),
		extra:       extra,
	}, nil
}

func (h *AccountHandler) exchangeAntigravityReauth(ctx context.Context, req *ReauthorizeAccountRequest, proxyID *int64) (*reauthCredentials, error) {
	if h.antigravityOAuthService == nil {
		return nil, service.ErrServiceUnavailable
	}
	info, err := h.antigravityOAuthService.ExchangeCode(ctx, &service.AntigravityExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   proxyID,
	})
	if err != nil {
		return nil, err
	}
	return antigravityReauthCredentials(h.antigravityOAuthService, info), nil
}

// antigravityReauthCredentials builds what reauthorize saves for an
// Antigravity exchange: the service's credentials, and privacy_mode in extra
// when the exchange set it.
func antigravityReauthCredentials(svc *service.AntigravityOAuthService, info *service.AntigravityTokenInfo) *reauthCredentials {
	return &reauthCredentials{
		accountType: service.AccountTypeOAuth,
		credentials: svc.BuildAccountCredentials(info),
		extra:       nonEmptyReauthExtra(map[string]string{"privacy_mode": info.PrivacyMode}),
	}
}

func (h *AccountHandler) exchangeGrokReauth(ctx context.Context, req *ReauthorizeAccountRequest, proxyID *int64) (*reauthCredentials, error) {
	exchanger, ok := h.grokOAuthService.(grokCodeExchanger)
	if !ok {
		return nil, service.ErrServiceUnavailable
	}
	info, err := exchanger.ExchangeCode(ctx, &service.GrokExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     proxyID,
	})
	if err != nil {
		return nil, err
	}
	credentials := h.grokOAuthService.BuildAccountCredentials(info)
	// Like the Web flow, never write base_url: the account keeps its own value
	// (credentials are merged) or the system default.
	delete(credentials, "base_url")
	return &reauthCredentials{
		accountType: service.AccountTypeOAuth,
		credentials: credentials,
		extra: nonEmptyReauthExtra(map[string]string{
			"email":              info.Email,
			"subscription_tier":  info.SubscriptionTier,
			"entitlement_status": info.EntitlementStatus,
		}),
	}, nil
}

// nonEmptyReauthExtra keeps the non-empty values, or returns nil.
func nonEmptyReauthExtra(values map[string]string) map[string]any {
	var extra map[string]any
	for key, value := range values {
		if value == "" {
			continue
		}
		if extra == nil {
			extra = make(map[string]any, len(values))
		}
		extra[key] = value
	}
	return extra
}
