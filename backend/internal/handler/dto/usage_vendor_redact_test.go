package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMaskEmail(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"alice@example.com": "a***@example.com",
		"a@example.com":     "a***@example.com",
		"  bob@x.io ":       "b***@x.io",
		"no-at-sign":        "***",
		"@example.com":      "***@example.com",
		"":                  "",
	}
	for in, want := range cases {
		require.Equal(t, want, MaskEmail(in), "MaskEmail(%q)", in)
	}
}

// A vendor workspace must never see an end user's API key plaintext, balance or profile through usage logs.
func TestRedactAdminUsageLogForVendor_StripsKeyAndUserProfile(t *testing.T) {
	t.Parallel()

	log := UsageLogFromServiceAdmin(&service.UsageLog{
		ID:             42,
		UserID:         7,
		APIKeyID:       9,
		SubscriptionID: ptrInt64(3),
		User: &service.User{
			ID: 7, Email: "alice@example.com", Username: "alice", Balance: 123.45, TotalRecharged: 500,
			BalanceNotifyEnabled: true, BalanceNotifyThreshold: ptrFloat64(5),
		},
		APIKey:       &service.APIKey{ID: 9, UserID: 7, Key: "sk-secret-plaintext", Name: "脚本", Quota: 50},
		Subscription: &service.UserSubscription{ID: 3, UserID: 7},
	})

	RedactAdminUsageLogForVendor(log)

	require.NotNil(t, log.User)
	require.Equal(t, int64(7), log.User.ID)
	require.Equal(t, "a***@example.com", log.User.Email)
	require.NotNil(t, log.APIKey)
	require.Equal(t, int64(9), log.APIKey.ID)
	require.Equal(t, "脚本", log.APIKey.Name)
	require.Nil(t, log.Subscription, "subscriptions are station-owner data")
	require.Equal(t, int64(3), *log.SubscriptionID, "the id alone stays so rows can still be told apart")

	raw, err := json.Marshal(log)
	require.NoError(t, err)
	body := string(raw)
	for _, leaked := range []string{"sk-secret-plaintext", "alice@example.com", "123.45", "\"username\":\"alice\""} {
		require.NotContains(t, body, leaked)
	}
}

// The end user's IP address and User-Agent identify a person; vendors run upstream accounts and do not need them.
func TestRedactAdminUsageLogForVendor_StripsClientIPAndUserAgent(t *testing.T) {
	t.Parallel()

	ip, ua := "203.0.113.24", "claude-cli/1.0 (macOS)"
	log := UsageLogFromServiceAdmin(&service.UsageLog{ID: 5, IPAddress: &ip, UserAgent: &ua})
	require.NotNil(t, log.IPAddress)
	require.NotNil(t, log.UserAgent)

	RedactAdminUsageLogForVendor(log)

	require.Nil(t, log.IPAddress)
	require.Nil(t, log.UsageLog.IPAddress)
	require.Nil(t, log.UserAgent)
	raw, err := json.Marshal(log)
	require.NoError(t, err)
	require.NotContains(t, string(raw), ip)
	require.NotContains(t, string(raw), ua)
}

func TestRedactAdminUsageLogForVendor_NilSafe(t *testing.T) {
	t.Parallel()

	RedactAdminUsageLogForVendor(nil)
	log := UsageLogFromServiceAdmin(&service.UsageLog{ID: 1})
	RedactAdminUsageLogForVendor(log)
	require.Nil(t, log.User)
	require.Nil(t, log.APIKey)
}

func ptrInt64(v int64) *int64       { return &v }
func ptrFloat64(v float64) *float64 { return &v }
