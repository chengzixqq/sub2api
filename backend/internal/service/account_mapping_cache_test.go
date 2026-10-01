//go:build unit

package service

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountMappingCachesConcurrentReadsAndCopies(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping":              map[string]any{"public-model": "upstream-model"},
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"X-Application": "gateway"},
		},
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			<-start
			for range 256 {
				if got := account.GetMappedModel("public-model"); got != "upstream-model" {
					t.Errorf("shared account mapping = %q", got)
					return
				}
				if got := account.GetHeaderOverrides()["x-application"]; got != "gateway" {
					t.Errorf("shared account header override = %q", got)
					return
				}
				// Plugin metadata and OAuth refresh copy shared Account snapshots.
				// Lazy cache publication must not race with these plain value copies.
				copy := *account
				if got := copy.GetMappedModel("public-model"); got != "upstream-model" {
					t.Errorf("copied account mapping = %q", got)
					return
				}
				if got := copy.GetHeaderOverrides()["x-application"]; got != "gateway" {
					t.Errorf("copied account header override = %q", got)
					return
				}
			}
		})
	}
	close(start)
	workers.Wait()
}

func TestAccountMappingCachesCopiedAccountKeepsOwnConfiguration(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping":              map[string]any{"public-model": "upstream-original"},
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"X-Application": "original"},
		},
	}
	require.Equal(t, "upstream-original", account.GetMappedModel("public-model"))
	require.Equal(t, "original", account.GetHeaderOverrides()["x-application"])
	copy := *account
	copy.Credentials = map[string]any{
		"model_mapping":              map[string]any{"public-model": "upstream-copy"},
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides:       map[string]any{"X-Application": "copy"},
	}
	require.Equal(t, "upstream-copy", copy.GetMappedModel("public-model"))
	require.Equal(t, "copy", copy.GetHeaderOverrides()["x-application"])
	require.Equal(t, "upstream-original", account.GetMappedModel("public-model"))
	require.Equal(t, "original", account.GetHeaderOverrides()["x-application"])

	// Reusing raw credentials with a different platform must not reuse a
	// mapping resolved using the original platform's implicit model aliases.
	copy.Credentials = account.Credentials
	copy.Platform = PlatformAntigravity
	require.Equal(t, "gemini-3-flash", copy.GetModelMapping()["gemini-3-flash"])
	require.NotContains(t, account.GetModelMapping(), "gemini-3-flash")
}

func TestAccountHeaderMappingCacheInvalidation(t *testing.T) {
	rawHeaders := map[string]any{"X-Application": "old"}
	account := &Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       rawHeaders,
		},
	}
	require.Equal(t, "old", account.GetHeaderOverrides()["x-application"])
	rawHeaders["X-Application"] = "new"
	require.Equal(t, "new", account.GetHeaderOverrides()["x-application"])
	account.Credentials[credKeyHeaderOverrideEnabled] = false
	require.Nil(t, account.GetHeaderOverrides())
	account.Credentials[credKeyHeaderOverrideEnabled] = true
	require.Equal(t, "new", account.GetHeaderOverrides()["x-application"])
	account.Type = AccountTypeOAuth
	require.Nil(t, account.GetHeaderOverrides())
}

func TestAccountModelMappingCacheKeepsGoogleOneDefaultsSeparate(t *testing.T) {
	account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	require.Nil(t, account.GetModelMapping())
	account.Credentials["oauth_type"] = "google_one"
	require.Equal(t, "gemini-2.5-pro", account.GetModelMapping()["gemini-2.5-pro"])
	account.Type = AccountTypeAPIKey
	require.Nil(t, account.GetModelMapping())
}
