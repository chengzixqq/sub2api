package admin

import (
	"context"
	"log/slog"
	"maps"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// oauthCredentialsPersistInput is what apply-oauth-credentials and reauthorize
// save after an OAuth (re)authorization.
type oauthCredentialsPersistInput struct {
	Type        string
	Credentials map[string]any
	Extra       map[string]any
	// PreserveStatus keeps inactive accounts inactive (see
	// service.ClearAccountErrorOptions.PreserveInactive); error accounts are
	// still reactivated. False keeps the historical behavior.
	PreserveStatus bool
	// LogPrefix names the log events, e.g. "apply_oauth_credentials".
	LogPrefix string
}

// persistOAuthCredentials saves re-authorized credentials onto an existing
// account: the new OAuth values replace their counterparts while the rest of
// the stored credentials (for example model_mapping) are kept, SSO / password
// residue is dropped, extra is merged key by key, the Grok reauth flag and the
// account error are cleared and the token cache is invalidated.
//
// Only UpdateAccount errors are returned; the follow-up steps are best effort
// and only logged. in.Credentials is copied, never modified.
func (h *AccountHandler) persistOAuthCredentials(ctx context.Context, accountID int64, existing *service.Account, in oauthCredentialsPersistInput) (*service.Account, error) {
	credentials := service.MergeCredentials(existing.Credentials, maps.Clone(in.Credentials))
	credentials = service.SanitizeStoredCredentials(existing.Platform, credentials)

	updatedAccount, err := h.adminService.UpdateAccount(ctx, accountID, &service.UpdateAccountInput{
		Type:        in.Type,
		Credentials: credentials,
	})
	if err != nil {
		return nil, err
	}

	h.mergeReauthExtra(ctx, accountID, existing.Platform, in.Extra, in.LogPrefix)

	if cleared, clearErr := h.clearErrorAfterReauth(ctx, accountID, in.PreserveStatus); clearErr != nil {
		slog.Warn(in.LogPrefix+".clear_error_failed",
			"account_id", accountID,
			"err", clearErr,
		)
	} else if cleared != nil {
		updatedAccount = cleared
	}

	if h.tokenCacheInvalidator != nil && updatedAccount.IsOAuth() {
		if invalidateErr := h.tokenCacheInvalidator.InvalidateToken(ctx, updatedAccount); invalidateErr != nil {
			slog.Warn(in.LogPrefix+".invalidate_token_failed",
				"account_id", accountID,
				"err", invalidateErr,
			)
		}
	}
	return updatedAccount, nil
}

// mergeReauthExtra merges extra key by key and clears the Grok reauth flag.
// Both steps are best effort: failures are only logged.
func (h *AccountHandler) mergeReauthExtra(ctx context.Context, accountID int64, platform string, extra map[string]any, logPrefix string) {
	// 增量合并 Extra（JSONB key 级 merge，绝不覆盖 base_rpm / window_cost_limit /
	// max_sessions / quota_* / privacy_mode 等持久化键）。
	// best-effort：失败仅记日志；下方清除错误会从 DB 重新读取最新 account，
	// 因此响应里的 extra 始终以 DB 为准——这里不需要手动维护内存快照。
	if len(extra) > 0 {
		if extraErr := h.adminService.UpdateAccountExtra(ctx, accountID, extra); extraErr != nil {
			extraKeys := make([]string, 0, len(extra))
			for k := range extra {
				extraKeys = append(extraKeys, k)
			}
			slog.Error(logPrefix+".update_extra_failed",
				"account_id", accountID,
				"extra_keys", extraKeys,
				"err", extraErr,
			)
		}
	}

	// Successful re-auth clears the soft spending-limit reauth flag for Grok.
	if platform == service.PlatformGrok {
		if clearErr := h.adminService.UpdateAccountExtra(ctx, accountID, map[string]any{
			"grok_needs_reauth":        false,
			"grok_needs_reauth_reason": "",
			"grok_needs_reauth_at":     "",
		}); clearErr != nil {
			slog.Warn(logPrefix+".clear_grok_reauth_failed",
				"account_id", accountID,
				"err", clearErr,
			)
		}
	}
}

// clearErrorAfterReauth keeps the historical ClearAccountError call unless the
// caller asked to preserve an inactive status.
func (h *AccountHandler) clearErrorAfterReauth(ctx context.Context, accountID int64, preserveStatus bool) (*service.Account, error) {
	if !preserveStatus {
		return h.adminService.ClearAccountError(ctx, accountID)
	}
	result, err := h.adminService.ClearAccountErrorWithOptions(ctx, accountID, service.ClearAccountErrorOptions{PreserveInactive: true})
	if err != nil || result == nil {
		return nil, err
	}
	return result.Account, nil
}
