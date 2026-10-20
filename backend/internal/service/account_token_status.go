package service

import (
	"strings"
	"time"
)

// CredentialKeyTokenRefreshedAt is the non-sensitive credentials key holding the
// RFC3339 (UTC, second precision) time the access token was last replaced by a
// refresh or an authorization. It is visible through the redacted credentials.
const CredentialKeyTokenRefreshedAt = "_token_refreshed_at"

// AccountTokenStatus is a uniform view of an OAuth / setup-token access token's
// lifetime. All fields are derived from stored credentials only; nothing in it
// changes with the passage of time, so list ETags stay stable.
type AccountTokenStatus struct {
	// ExpiresAt is credentials.expires_at normalized to UTC; stored values may be
	// Unix seconds (string or number) or RFC3339.
	ExpiresAt *time.Time `json:"expires_at"`
	// LifetimeSeconds is the upstream-issued access token lifetime
	// (credentials.expires_in); nil when unknown or not positive.
	LifetimeSeconds *int64 `json:"lifetime_seconds"`
	// RefreshedAt is the last successful refresh / authorization. Accounts that
	// predate _token_refreshed_at fall back to _token_version (Unix millis).
	RefreshedAt *time.Time `json:"refreshed_at"`
	// AutoRefresh reports whether a refresh token exists for background refresh.
	// OpenAI personal access tokens are never refreshed.
	AutoRefresh bool `json:"auto_refresh"`
}

// AccountTokenStatusFromAccount derives the token status of an OAuth or
// setup-token account. Other account types and spark shadow accounts (which
// hold no credentials of their own) return nil.
func AccountTokenStatusFromAccount(a *Account) *AccountTokenStatus {
	if a == nil || !a.IsOAuth() || a.IsCredentialShadow() {
		return nil
	}
	status := &AccountTokenStatus{
		RefreshedAt: accountTokenRefreshedAt(a),
		AutoRefresh: strings.TrimSpace(a.GetCredential("refresh_token")) != "" && !a.IsOpenAIPersonalAccessToken(),
	}
	if expiresAt := a.GetCredentialAsTime("expires_at"); expiresAt != nil {
		status.ExpiresAt = normalizeTokenStatusTime(*expiresAt)
	}
	if lifetime := a.GetCredentialAsInt64("expires_in"); lifetime > 0 {
		status.LifetimeSeconds = &lifetime
	}
	return status
}

func accountTokenRefreshedAt(a *Account) *time.Time {
	if raw := strings.TrimSpace(a.GetCredential(CredentialKeyTokenRefreshedAt)); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			if normalized := normalizeTokenStatusTime(parsed); normalized != nil {
				return normalized
			}
		}
	}
	if version := a.GetCredentialAsInt64("_token_version"); version > 0 {
		return normalizeTokenStatusTime(time.UnixMilli(version))
	}
	return nil
}

// normalizeTokenStatusTime converts t to UTC at second precision. Times whose
// year falls outside 1..9999 (e.g. a millisecond value stored where Unix
// seconds are expected) cannot be encoded by time.Time.MarshalJSON and would
// break the whole account response, so they are reported as unknown (nil).
func normalizeTokenStatusTime(t time.Time) *time.Time {
	normalized := t.UTC().Truncate(time.Second)
	if year := normalized.Year(); year < 1 || year > 9999 {
		return nil
	}
	return &normalized
}

func formatTokenRefreshedAt(now time.Time) string {
	return now.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// stampTokenRefreshedAt records now as the access token refresh time.
func stampTokenRefreshedAt(creds map[string]any, now time.Time) {
	if creds == nil {
		return
	}
	creds[CredentialKeyTokenRefreshedAt] = formatTokenRefreshedAt(now)
}

// stampTokenRefreshedAtOnNewAccessToken records now when an OAuth or
// setup-token account received an access token different from the previous one
// (manual refresh, apply / reauthorize, admin token replacement). Edits that do
// not carry a new access token leave the stored value untouched.
func stampTokenRefreshedAtOnNewAccessToken(accountType, previousAccessToken string, creds map[string]any, now time.Time) {
	if creds == nil || (accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken) {
		return
	}
	accessToken, _ := creds["access_token"].(string)
	if strings.TrimSpace(accessToken) == "" || accessToken == previousAccessToken {
		return
	}
	stampTokenRefreshedAt(creds, now)
}

// stampTokenRefreshedAtOnCreate stamps a newly created OAuth / setup-token
// account that carries an access token, unless the input (e.g. a data import)
// already records a valid refresh time.
func stampTokenRefreshedAtOnCreate(accountType string, creds map[string]any, now time.Time) {
	if raw, ok := creds[CredentialKeyTokenRefreshedAt].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil && normalizeTokenStatusTime(parsed) != nil {
			return
		}
	}
	stampTokenRefreshedAtOnNewAccessToken(accountType, "", creds, now)
}
