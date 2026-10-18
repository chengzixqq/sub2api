package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAccountFromService_StructuredErrorFields(t *testing.T) {
	errorAt := time.Date(2026, 10, 18, 8, 30, 0, 0, time.UTC)
	src := &service.Account{
		ID:           7,
		Name:         "broken",
		Platform:     "anthropic",
		Type:         "apikey",
		Status:       service.StatusError,
		ErrorMessage: "Authentication failed (401): invalid x-api-key",
		ErrorAt:      &errorAt,
	}

	got := AccountFromService(src)
	require.NotNil(t, got)
	require.Equal(t, "Authentication failed (401): invalid x-api-key", got.ErrorMessage)
	require.Equal(t, &errorAt, got.ErrorAt)
	require.Equal(t, service.AccountErrorKindAuth, got.ErrorKind)
	require.NotNil(t, got.ErrorStatusCode)
	require.Equal(t, 401, *got.ErrorStatusCode)
	require.Equal(t, "Authentication failed (401): invalid x-api-key", got.ErrorSummary)

	item := AccountListItemFromAccount(got)
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "auth", decoded["error_kind"])
	require.EqualValues(t, 401, decoded["error_status_code"])
	require.Equal(t, "2026-10-18T08:30:00Z", decoded["error_at"])
	require.Equal(t, "Authentication failed (401): invalid x-api-key", decoded["error_summary"])
}

func TestAccountFromService_NoErrorOmitsStructuredFields(t *testing.T) {
	got := AccountFromService(&service.Account{ID: 8, Name: "ok", Platform: "anthropic", Type: "apikey", Status: service.StatusActive})
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Nil(t, decoded["error_at"])
	require.NotContains(t, decoded, "error_kind")
	require.NotContains(t, decoded, "error_status_code")
	require.NotContains(t, decoded, "error_summary")
	require.Contains(t, decoded, "error_message")
}
