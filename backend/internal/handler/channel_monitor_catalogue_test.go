package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMonitorModelCatalogue_UsesGroupMappingAndAllowlist(t *testing.T) {
	g := &service.Group{ID: 81, Platform: service.PlatformOpenAI, ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"visible"}}}
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
		81: {{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"visible": "secret-upstream", "hidden": "secret"}}}},
	}})
	models, err := h.MonitorModelCatalogue(context.Background(), g)
	require.NoError(t, err)
	require.Equal(t, []string{"visible"}, models)
}

func TestMonitorModelCatalogue_PinnedDoesNotFallback(t *testing.T) {
	g := &service.Group{ID: 81, Platform: service.PlatformOpenAI, CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{Enabled: true}}
	_, err := (&GatewayHandler{}).MonitorModelCatalogue(context.Background(), g)
	require.Error(t, err)
}
