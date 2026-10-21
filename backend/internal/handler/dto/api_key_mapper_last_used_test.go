package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyFromService_MapsLastUsedAt(t *testing.T) {
	lastUsed := time.Now().UTC().Truncate(time.Second)
	lastUsedIP := "203.0.113.10"
	src := &service.APIKey{
		ID:                 1,
		UserID:             2,
		Key:                "sk-map-last-used",
		Name:               "Mapper",
		Status:             service.StatusActive,
		LastUsedAt:         &lastUsed,
		LastUsedIP:         &lastUsedIP,
		CurrentConcurrency: 3,
	}

	out := APIKeyFromService(src)
	require.NotNil(t, out)
	require.NotNil(t, out.LastUsedAt)
	require.WithinDuration(t, lastUsed, *out.LastUsedAt, time.Second)
	require.NotNil(t, out.LastUsedIP)
	require.Equal(t, lastUsedIP, *out.LastUsedIP)
	require.Equal(t, 3, out.CurrentConcurrency)
}

func TestAPIKeyFromService_MapsNilLastUsedAt(t *testing.T) {
	src := &service.APIKey{
		ID:     1,
		UserID: 2,
		Key:    "sk-map-last-used-nil",
		Name:   "MapperNil",
		Status: service.StatusActive,
	}

	out := APIKeyFromService(src)
	require.NotNil(t, out)
	require.Nil(t, out.LastUsedAt)
	require.Nil(t, out.LastUsedIP)
}

func TestMaskAPIKey(t *testing.T) {
	require.Equal(t, "sk-…3f9a", MaskAPIKey("sk-abcdefghijklmnop3f9a"))
	require.Equal(t, "", MaskAPIKey("__deleted__12__1700000000"))
	require.Equal(t, "", MaskAPIKey(""))
	require.Equal(t, "…", MaskAPIKey("abcdef"), "keys too short to keep a prefix and suffix reveal nothing")

	out := APIKeyFromService(&service.APIKey{ID: 1, Key: "sk-abcdefghijklmnop3f9a"})
	require.Equal(t, "sk-…3f9a", out.KeyMasked)
	require.Equal(t, "sk-abcdefghijklmnop3f9a", out.Key)

	out.HideFullKey()
	require.Empty(t, out.Key)
	require.Equal(t, "sk-…3f9a", out.KeyMasked)
}

func TestAPIKeyFromService_EffectiveStatus(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	out := APIKeyFromService(&service.APIKey{ID: 1, Status: service.StatusAPIKeyInactive})
	require.Equal(t, service.StatusAPIKeyInactive, out.EffectiveStatus)

	out = APIKeyFromService(&service.APIKey{ID: 2, Status: service.StatusAPIKeyDisabled})
	require.Equal(t, service.StatusAPIKeyDisabled, out.Status, "the stored value is passed through unchanged")
	require.Equal(t, service.StatusAPIKeyInactive, out.EffectiveStatus)

	out = APIKeyFromService(&service.APIKey{ID: 3, Status: service.StatusActive, ExpiresAt: &past})
	require.Equal(t, service.StatusActive, out.Status)
	require.Equal(t, service.StatusAPIKeyExpired, out.EffectiveStatus)

	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"status":"active"`)
	require.Contains(t, string(raw), `"effective_status":"expired"`)
}
