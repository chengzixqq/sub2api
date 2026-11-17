//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestAccountSharedSnapshotCurrent_TracksImmutableCacheKey(t *testing.T) {
	var absent *Account
	require.False(t, absent.SharedSnapshotCurrent())
	account := &Account{Platform: PlatformAnthropic, Credentials: map[string]any{
		"model_mapping": map[string]any{"request-model": "first-model"},
	}}
	account.PrepareSharedSnapshot()
	require.True(t, account.SharedSnapshotCurrent())
	copy := *account
	copy.Credentials = map[string]any{"model_mapping": map[string]any{"request-model": "second-model"}}
	require.False(t, copy.SharedSnapshotCurrent())
	copy.PrepareSharedSnapshot()
	require.True(t, copy.SharedSnapshotCurrent())
	require.Equal(t, "first-model", account.GetMappedModel("request-model"))
	xai.SetRuntimeModelMappingOptions(xai.RuntimeModelMappingOptions())
	require.False(t, account.SharedSnapshotCurrent(), "a runtime model catalog change must invalidate the old cache key")
	account.PrepareSharedSnapshot()
	require.True(t, account.SharedSnapshotCurrent())
	account.Credentials["model_mapping"].(map[string]any)["request-model"] = "changed-model"
	require.False(t, account.SharedSnapshotCurrent(), "sequential same-size mutations remain detectable")
}
