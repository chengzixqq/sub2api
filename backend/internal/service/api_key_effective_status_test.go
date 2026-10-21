//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// effectiveStatusFixtures lists the key states the derived status must tell apart (the gateway parity test in
// middleware/api_key_auth_effective_status_test.go replays the same states through the auth middleware).
func effectiveStatusFixtures(now time.Time) []struct {
	name string
	key  APIKey
	want string
} {
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	exact := now
	return []struct {
		name string
		key  APIKey
		want string
	}{
		{"active", APIKey{Status: StatusAPIKeyActive}, StatusAPIKeyActive},
		{"inactive", APIKey{Status: StatusAPIKeyInactive}, StatusAPIKeyInactive},
		{"legacy disabled reads as inactive", APIKey{Status: StatusAPIKeyDisabled}, StatusAPIKeyInactive},
		{"unknown status reads as inactive", APIKey{Status: "banned"}, StatusAPIKeyInactive},
		{"inactive wins over expiry and quota", APIKey{Status: StatusAPIKeyInactive, ExpiresAt: &past, Quota: 10, QuotaUsed: 10}, StatusAPIKeyInactive},
		{"stored quota_exhausted with room left", APIKey{Status: StatusAPIKeyQuotaExhausted, Quota: 10, QuotaUsed: 1}, StatusAPIKeyQuotaExhausted},
		{"stored quota_exhausted wins over expiry", APIKey{Status: StatusAPIKeyQuotaExhausted, ExpiresAt: &past}, StatusAPIKeyQuotaExhausted},
		{"stored expired with a future expiry", APIKey{Status: StatusAPIKeyExpired, ExpiresAt: &future}, StatusAPIKeyExpired},
		{"stored expired wins over quota", APIKey{Status: StatusAPIKeyExpired, Quota: 10, QuotaUsed: 10}, StatusAPIKeyExpired},
		{"active past expiry", APIKey{Status: StatusAPIKeyActive, ExpiresAt: &past}, StatusAPIKeyExpired},
		{"active expiring exactly now is still active", APIKey{Status: StatusAPIKeyActive, ExpiresAt: &exact}, StatusAPIKeyActive},
		{"active with a future expiry", APIKey{Status: StatusAPIKeyActive, ExpiresAt: &future}, StatusAPIKeyActive},
		{"active quota used up", APIKey{Status: StatusAPIKeyActive, Quota: 10, QuotaUsed: 10}, StatusAPIKeyQuotaExhausted},
		{"active unlimited quota", APIKey{Status: StatusAPIKeyActive, Quota: 0, QuotaUsed: 100}, StatusAPIKeyActive},
		{"active expired and quota used up reads as expired (auth checks expiry first)", APIKey{Status: StatusAPIKeyActive, ExpiresAt: &past, Quota: 10, QuotaUsed: 10}, StatusAPIKeyExpired},
	}
}

func TestAPIKeyEffectiveStatusAt(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range effectiveStatusFixtures(now) {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			require.Equal(t, tc.want, key.EffectiveStatusAt(now))
		})
	}
}

func TestAPIKeyEffectiveStatus_UsesCurrentTime(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	require.Equal(t, StatusAPIKeyExpired, (&APIKey{Status: StatusAPIKeyActive, ExpiresAt: &past}).EffectiveStatus())
	require.Equal(t, StatusAPIKeyActive, (&APIKey{Status: StatusAPIKeyActive, ExpiresAt: &future}).EffectiveStatus())
}
