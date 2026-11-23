package claude

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOfficialModelInfoCatalog(t *testing.T) {
	require.Len(t, officialCatalog, 18)
	for _, tc := range []struct {
		id, name, release, line            string
		input, output                      int
		lifecycle, deprecatedAt, retiresAt string
	}{
		{"claude-fable-5-2", "Claude Fable 5.2", "2026-11-09T00:00:00Z", "fable", 1000000, 128000, "active", "", ""},
		{"claude-fable-5-1", "Claude Fable 5.1", "2026-09-01T00:00:00Z", "fable", 1000000, 128000, "active", "", ""},
		{"claude-fable-5", "Claude Fable 5", "2026-06-09T00:00:00Z", "fable", 1000000, 128000, "active", "", ""},
		{"claude-mythos-5-2", "Claude Mythos 5.2", "2026-11-09T00:00:00Z", "mythos", 1000000, 128000, "active", "", ""},
		{"claude-opus-5-5", "Claude Opus 5.5", "2026-09-22T00:00:00Z", "opus", 1000000, 128000, "active", "", ""},
		{"claude-opus-5", "Claude Opus 5", "2026-07-24T00:00:00Z", "opus", 1000000, 128000, "active", "", ""},
		{"claude-opus-4-8", "Claude Opus 4.8", "2026-05-28T00:00:00Z", "opus", 1000000, 128000, "active", "", ""},
		{"claude-opus-4-7", "Claude Opus 4.7", "2026-04-16T00:00:00Z", "opus", 1000000, 128000, "active", "", ""},
		{"claude-opus-4-6", "Claude Opus 4.6", "2026-02-05T00:00:00Z", "opus", 1000000, 128000, "active", "", ""},
		{"claude-opus-4-5-20251101", "Claude Opus 4.5", "2025-11-24T00:00:00Z", "opus", 200000, 64000, "active", "", ""},
		{"claude-opus-4-1-20250805", "Claude Opus 4.1", "2025-08-05T00:00:00Z", "opus", 200000, 32000, "retired", "2026-06-05T00:00:00Z", "2026-08-05T00:00:00Z"},
		{"claude-opus-4-20250514", "Claude Opus 4", "2025-05-22T00:00:00Z", "opus", 200000, 32000, "retired", "2026-04-14T00:00:00Z", "2026-06-15T00:00:00Z"},
		{"claude-sonnet-5-5", "Claude Sonnet 5.5", "2026-09-28T00:00:00Z", "sonnet", 1000000, 128000, "active", "", ""},
		{"claude-sonnet-5", "Claude Sonnet 5", "2026-06-30T00:00:00Z", "sonnet", 1000000, 128000, "active", "", ""},
		{"claude-sonnet-4-6", "Claude Sonnet 4.6", "2026-02-17T00:00:00Z", "sonnet", 1000000, 128000, "active", "", ""},
		{"claude-sonnet-4-5-20250929", "Claude Sonnet 4.5", "2025-09-29T00:00:00Z", "sonnet", 200000, 64000, "deprecated", "2026-09-30T00:00:00Z", "2026-11-30T00:00:00Z"},
		{"claude-haiku-5-5", "Claude Haiku 5.5", "2026-10-07T00:00:00Z", "haiku", 1000000, 128000, "active", "", ""},
		{"claude-haiku-4-5-20251001", "Claude Haiku 4.5", "2025-10-15T00:00:00Z", "haiku", 200000, 64000, "active", "", ""},
	} {
		t.Run(tc.id, func(t *testing.T) {
			info, found := OfficialModelInfo(tc.id)
			require.True(t, found)
			require.Equal(t, tc.id, info.ID)
			require.Equal(t, "model", info.Type)
			require.Nil(t, info.Capabilities)
			require.Equal(t, tc.name, info.DisplayName)
			require.Equal(t, tc.release, info.CreatedAt)
			_, err := time.Parse(time.RFC3339, info.CreatedAt)
			require.NoError(t, err)
			require.Equal(t, &tc.line, info.Line)
			require.Equal(t, &tc.input, info.MaxInputTokens)
			require.Equal(t, &tc.output, info.MaxTokens)
			require.Equal(t, tc.lifecycle, info.Lifecycle)
			if tc.lifecycle == "active" {
				require.Nil(t, info.DeprecatedAt)
				require.Nil(t, info.RetiresAt)
			} else {
				require.Equal(t, &tc.deprecatedAt, info.DeprecatedAt)
				require.Equal(t, &tc.retiresAt, info.RetiresAt)
			}
		})
	}
}

func TestOfficialModelInfoAliases(t *testing.T) {
	require.Len(t, catalogAliases, 3)
	for alias, snapshot := range map[string]string{
		"claude-opus-4-5":   "claude-opus-4-5-20251101",
		"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
		"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
	} {
		model, found := OfficialModelInfo(alias)
		require.True(t, found)
		want, found := OfficialModelInfo(snapshot)
		require.True(t, found)
		want.ID = alias
		require.Equal(t, want, model)
	}
	_, found := OfficialModelInfo("claude-opus-4")
	require.False(t, found)
}
