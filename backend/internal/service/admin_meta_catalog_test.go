//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBuildAdminMetaCatalogEmbedsPlatformCatalog(t *testing.T) {
	catalog := BuildAdminMetaCatalog(AdminScope())
	require.Equal(t, BuildPlatformCatalog(), catalog.PlatformCatalog)

	raw, err := json.Marshal(catalog)
	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded))
	for _, key := range []string{
		"platform_catalog", "account_types", "platform_auth", "account_status_filters",
		"request_types", "error_phases", "user_roles", "api_protocols",
	} {
		require.Contains(t, decoded, key)
	}
	var embedded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(decoded["platform_catalog"], &embedded))
	require.Contains(t, embedded, "platforms")
	require.Contains(t, embedded, "composite_precedence")
}

func TestAdminMetaCatalogPlatformAuthCoversConcretePlatforms(t *testing.T) {
	catalog := BuildAdminMetaCatalog(AdminScope())
	allowed := make(map[string]struct{}, len(catalog.AccountTypes))
	for _, accountType := range catalog.AccountTypes {
		allowed[accountType] = struct{}{}
	}
	require.Len(t, catalog.PlatformAuth, len(domain.ConcretePlatformIDs()))
	for _, platform := range domain.ConcretePlatformIDs() {
		types, ok := catalog.PlatformAuth[platform]
		require.Truef(t, ok, "platform %s has no auth list", platform)
		require.NotEmptyf(t, types, "platform %s has an empty auth list", platform)
		for _, accountType := range types {
			require.Containsf(t, allowed, accountType, "platform %s lists unknown type %s", platform, accountType)
		}
	}
	require.NotContains(t, catalog.PlatformAuth, PlatformComposite)

	require.Equal(t, []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey, AccountTypeBedrock, AccountTypeServiceAccount},
		catalog.PlatformAuth[PlatformAnthropic])
	require.Equal(t, []string{AccountTypeOAuth, AccountTypeAPIKey}, catalog.PlatformAuth[PlatformOpenAI])
	require.Equal(t, []string{AccountTypeOAuth, AccountTypeAPIKey, AccountTypeServiceAccount}, catalog.PlatformAuth[PlatformGemini])
	// Web creates the Antigravity "upstream" option as an apikey account.
	require.Equal(t, []string{AccountTypeOAuth, AccountTypeAPIKey}, catalog.PlatformAuth[PlatformAntigravity])
	require.Equal(t, []string{AccountTypeOAuth, AccountTypeAPIKey}, catalog.PlatformAuth[PlatformGrok])
	require.Equal(t, []string{AccountTypeAPIKey}, catalog.PlatformAuth[PlatformTypeSafe])
	require.Equal(t, []string{AccountTypeAPIKey}, catalog.PlatformAuth[PlatformKimi])
	require.Equal(t, []string{AccountTypeAPIKey}, catalog.PlatformAuth[PlatformCline])
}

func TestAdminMetaCatalogVendorPlatformAuthTrimmed(t *testing.T) {
	manager := BuildAdminMetaCatalog(VendorScope(7, WorkspacePermissions{AccountManage: true}))
	require.NotContains(t, manager.PlatformAuth, PlatformGrok, "vendors may not create Grok accounts")
	require.Contains(t, manager.PlatformAuth, PlatformAnthropic)
	require.Len(t, manager.PlatformAuth, len(domain.ConcretePlatformIDs())-1)

	monitor := BuildAdminMetaCatalog(VendorScope(7, WorkspacePermissions{MonitorView: true}))
	missing := BuildAdminMetaCatalog(Scope{})
	for _, catalog := range []AdminMetaCatalog{monitor, missing} {
		require.NotNil(t, catalog.PlatformAuth)
		require.Empty(t, catalog.PlatformAuth)
		raw, err := json.Marshal(catalog)
		require.NoError(t, err)
		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.JSONEq(t, `{}`, string(decoded["platform_auth"]))
	}

	for _, catalog := range []AdminMetaCatalog{manager, monitor, missing} {
		require.Equal(t, BuildPlatformCatalog(), catalog.PlatformCatalog, "display names stay available to every caller")
		require.Equal(t, BuildAdminMetaCatalog(AdminScope()).AccountTypes, catalog.AccountTypes)
	}
}

func TestAdminMetaCatalogEnumsMatchConstants(t *testing.T) {
	catalog := BuildAdminMetaCatalog(AdminScope())

	require.Equal(t, []string{
		AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey,
		AccountTypeUpstream, AccountTypeBedrock, AccountTypeServiceAccount,
	}, catalog.AccountTypes)

	require.Equal(t, []string{"sync", "stream", "ws_v2", "cyber", "live"}, catalog.RequestTypes)
	for _, name := range catalog.RequestTypes {
		parsed, err := ParseUsageRequestType(name)
		require.NoError(t, err)
		require.NotEqual(t, RequestTypeUnknown, parsed)
		require.Equal(t, name, parsed.String())
	}

	require.Equal(t, []string{RoleAdmin, RoleUser, RoleVendor}, catalog.UserRoles)

	require.Equal(t, []string{"request", "auth", "account_auth", "routing", "upstream", "network", "internal"}, catalog.ErrorPhases)
	// The user-facing error categories map back to exactly the listed phases.
	mapped := make([]string, 0, len(catalog.ErrorPhases))
	seen := map[string]struct{}{}
	for _, category := range []string{"auth", "service_unavailable", "upstream", "internal", "cyber"} {
		phases, _ := CategoryToFilter(category)
		for _, phase := range phases {
			if _, ok := seen[phase]; !ok {
				seen[phase] = struct{}{}
				mapped = append(mapped, phase)
			}
		}
	}
	require.ElementsMatch(t, catalog.ErrorPhases, mapped)

	require.Equal(t, []string{
		StatusActive, AccountListStatusInactive, StatusError,
		AccountListStatusRateLimited, AccountListStatusTempUnschedulable, AccountListStatusUnschedulable,
		AccountListStatusOverloaded, AccountListStatusNeedsAttention, AccountListStatusSchedulableNow,
	}, catalog.AccountStatusFilters)

	require.Equal(t, []string{APIProtocolAdaptive, APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses},
		catalog.APIProtocols)
}

func TestAdminMetaCatalogJSONDeterministicAndCopies(t *testing.T) {
	first, err := json.Marshal(BuildAdminMetaCatalog(AdminScope()))
	require.NoError(t, err)
	second, err := json.Marshal(BuildAdminMetaCatalog(AdminScope()))
	require.NoError(t, err)
	require.Equal(t, first, second, "the ETag relies on byte-stable JSON")

	mutated := BuildAdminMetaCatalog(AdminScope())
	mutated.AccountTypes[0] = "mutated"
	mutated.AccountStatusFilters[0] = "mutated"
	mutated.RequestTypes[0] = "mutated"
	mutated.ErrorPhases[0] = "mutated"
	mutated.UserRoles[0] = "mutated"
	mutated.APIProtocols[0] = "mutated"
	mutated.PlatformAuth[PlatformAnthropic][0] = "mutated"
	delete(mutated.PlatformAuth, PlatformOpenAI)

	after, err := json.Marshal(BuildAdminMetaCatalog(AdminScope()))
	require.NoError(t, err)
	require.Equal(t, first, after, "callers must not be able to change the shared tables")
}
