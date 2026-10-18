package service

import (
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestMergeAccountMapPatch(t *testing.T) {
	base := map[string]any{
		"api_key":            "sk-old",
		"base_url":           "https://old.example.com",
		"custom_error_codes": []any{529},
		"model_mapping":      map[string]any{"a": "b"},
	}

	got := mergeAccountMapPatch(base, map[string]any{
		"base_url":           "https://new.example.com",
		"custom_error_codes": nil,
		"model_mapping":      map[string]any{"c": "d"},
	})

	require.Equal(t, map[string]any{
		"api_key":       "sk-old",
		"base_url":      "https://new.example.com",
		"model_mapping": map[string]any{"c": "d"},
	}, got)
	// The base map is left untouched.
	require.Equal(t, "https://old.example.com", base["base_url"])
	require.Contains(t, base, "custom_error_codes")
}

func TestMergeAccountMapPatch_NilBase(t *testing.T) {
	got := mergeAccountMapPatch(nil, map[string]any{"pool_mode": true, "gone": nil})
	require.Equal(t, map[string]any{"pool_mode": true}, got)
}

func TestValidateAccountCredentialsPatch(t *testing.T) {
	require.NoError(t, validateAccountCredentialsPatch(map[string]any{"api_key": "sk-new", "base_url": nil}))

	err := validateAccountCredentialsPatch(map[string]any{"api_key": nil})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "CREDENTIAL_KEY_NOT_DELETABLE", infraerrors.Reason(err))

	err = validateAccountCredentialsPatch(map[string]any{" ": "x"})
	require.Equal(t, "INVALID_PATCH", infraerrors.Reason(err))
}

func TestParseAccountConfigVersionTag(t *testing.T) {
	for raw, want := range map[string]int64{`"cv-12"`: 12, `W/"cv-3"`: 3, `cv-0`: 0, `7`: 7, ` "cv-9" `: 9} {
		got, err := ParseAccountConfigVersionTag(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"", `"abc"`, `"cv-"`, `"cv--1"`, `*`, `"cv-1", "cv-2"`} {
		_, err := ParseAccountConfigVersionTag(raw)
		require.Error(t, err, raw)
	}
	require.Equal(t, `"cv-12"`, AccountConfigVersionTag(12))
}

func TestApplyAccountMapDiff(t *testing.T) {
	base := map[string]any{"pool_mode": true, "custom_error_codes": []any{529.0}, "model_rate_limits": map[string]any{}}
	desired := map[string]any{"pool_mode": false, "model_rate_limits": map[string]any{}}
	// Meanwhile the gateway recorded a model rate limit and a token refresh happened.
	current := map[string]any{
		"pool_mode":          true,
		"custom_error_codes": []any{529.0},
		"model_rate_limits":  map[string]any{"claude": map[string]any{"rate_limit_reset_at": "x"}},
		"privacy_mode":       "set",
	}

	got := ApplyAccountMapDiff(current, base, desired)

	require.Equal(t, map[string]any{
		"pool_mode":         false,
		"model_rate_limits": map[string]any{"claude": map[string]any{"rate_limit_reset_at": "x"}},
		"privacy_mode":      "set",
	}, got)
}
