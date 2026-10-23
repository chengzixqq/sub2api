//go:build unit

package service

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Warming a shared snapshot retains content-based invalidation. These sequential
// mutations exercise the cache contract; published maps must not be mutated concurrently.
func TestAccountPrepareSharedSnapshot_CopiesRetainSignatureInvalidation(t *testing.T) {
	rawMapping := map[string]any{"claude-sonnet": "sonnet-a"}
	master := &Account{
		Platform:    PlatformAnthropic,
		Credentials: map[string]any{"model_mapping": rawMapping},
	}
	master.PrepareSharedSnapshot()

	replica := *master
	require.Equal(t, map[string]string{"claude-sonnet": "sonnet-a"}, replica.GetModelMapping())

	rawMapping["claude-sonnet"] = "sonnet-b"
	require.Equal(t, "sonnet-b", replica.GetModelMapping()["claude-sonnet"],
		"warming must not hide a sequential configuration change")
	require.Equal(t, "sonnet-b", replica.GetMappedModel("claude-sonnet"))

	// 未标记为共享的账号仍按签名检出原地改写。
	plainRaw := map[string]any{"claude-sonnet": "sonnet-a"}
	plain := &Account{
		Platform:    PlatformAnthropic,
		Credentials: map[string]any{"model_mapping": plainRaw},
	}
	require.Equal(t, "sonnet-a", plain.GetModelMapping()["claude-sonnet"])
	plainRaw["claude-sonnet"] = "sonnet-b"
	require.Equal(t, "sonnet-b", plain.GetModelMapping()["claude-sonnet"])
}

func TestAccountPrepareSharedSnapshot_ReplacedCredentialsFallBackToSignaturePath(t *testing.T) {
	master := &Account{
		Platform:    PlatformAnthropic,
		Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet": "sonnet-a"}},
	}
	master.PrepareSharedSnapshot()

	replica := *master
	replacedRaw := map[string]any{"claude-sonnet": "sonnet-b"}
	replica.Credentials = map[string]any{"model_mapping": replacedRaw}
	require.Equal(t, "sonnet-b", replica.GetModelMapping()["claude-sonnet"])

	// 重建后副本回到普通路径：同一 map 的原地改写由签名检出。
	replacedRaw["claude-sonnet"] = "sonnet-c"
	require.Equal(t, "sonnet-c", replica.GetModelMapping()["claude-sonnet"])

	// 母本不受副本重建影响。
	require.Equal(t, "sonnet-a", master.GetModelMapping()["claude-sonnet"])
}

func TestAccountPrepareSharedSnapshot_MappingLengthChangeRebuilds(t *testing.T) {
	rawMapping := map[string]any{"claude-sonnet": "sonnet-a"}
	master := &Account{
		Platform:    PlatformAnthropic,
		Credentials: map[string]any{"model_mapping": rawMapping},
	}
	master.PrepareSharedSnapshot()

	replica := *master
	rawMapping["claude-opus"] = "opus-a"
	require.Equal(t, "opus-a", replica.GetModelMapping()["claude-opus"])
}

func TestAccountPrepareSharedSnapshot_NilAccountAndNoMapping(t *testing.T) {
	var nilAccount *Account
	require.NotPanics(t, nilAccount.PrepareSharedSnapshot)

	master := &Account{Platform: PlatformAnthropic}
	master.PrepareSharedSnapshot()
	replica := *master
	require.Nil(t, replica.GetModelMapping())
	require.Equal(t, "claude-sonnet", replica.GetMappedModel("claude-sonnet"))
}

func TestAccountPrepareSharedSnapshot_ConcurrentCopiesKeepConfiguration(t *testing.T) {
	master := &Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"public-model": "original"}},
	}
	master.PrepareSharedSnapshot()
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			<-start
			for range 128 {
				master.PrepareSharedSnapshot()
				copy := *master
				copy.Platform = PlatformAntigravity
				copy.PrepareSharedSnapshot()
				if copy.GetModelMapping()["gemini-3-flash"] != "gemini-3-flash" {
					t.Error("copied platform lost its implicit model mapping")
					return
				}
				copy.Credentials = map[string]any{"model_mapping": map[string]any{"public-model": "replaced"}}
				copy.PrepareSharedSnapshot()
				if copy.GetMappedModel("public-model") != "replaced" || master.GetMappedModel("public-model") != "original" {
					t.Error("copied credentials leaked into the shared master")
					return
				}
				if _, found := master.GetModelMapping()["gemini-3-flash"]; found {
					t.Error("copied platform leaked into the shared master")
					return
				}
			}
		})
	}
	close(start)
	workers.Wait()
}
