package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// MonitorModelCatalogue uses the same group catalogue and pinned manifest as
// model discovery; observations are never a source of public model names.
func (h *GatewayHandler) MonitorModelCatalogue(ctx context.Context, group *service.Group) ([]string, error) {
	if h == nil || group == nil {
		return nil, errors.New("model catalogue unavailable")
	}
	if group.Platform == service.PlatformOpenAI && group.CodexModelsManifestConfig.Enabled {
		if h.openAIGatewayService == nil {
			return nil, errors.New("pinned model catalogue unavailable")
		}
		manifest, _, err := h.openAIGatewayService.FetchPinnedOpenAIModelsList(ctx, group, h.maxAccountSwitches, "")
		if err != nil {
			return nil, err
		}
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if manifest == nil || json.Unmarshal(manifest.Body, &list) != nil {
			return nil, errors.New("invalid pinned model catalogue")
		}
		models := make([]string, 0, len(list.Data))
		for _, model := range list.Data {
			if model.ID != "" {
				models = append(models, model.ID)
			}
		}
		return group.ModelAllowlist.FilterForListing(models), nil
	}
	if h.gatewayService == nil {
		return nil, errors.New("model catalogue unavailable")
	}
	var models []string
	if group.Platform == service.PlatformComposite {
		models = h.compositeAvailableModels(ctx, &group.ID)
	} else {
		models = h.gatewayService.GetAvailableModels(ctx, &group.ID, group.Platform)
	}
	if group.ModelAllowlistEnabled() {
		return group.ModelAllowlist.FilterForListing(modelListingSource(group.Platform, models, defaultModelIDsForPlatform(group.Platform))), nil
	}
	if len(models) == 0 {
		models = defaultModelIDsForPlatform(group.Platform)
	}
	return models, nil
}

func (h *GatewayHandler) validateMonitorProbeTarget(ctx context.Context, group *service.Group, model string) error {
	models, err := h.MonitorModelCatalogue(ctx, group)
	if err != nil {
		return err
	}
	for _, id := range models {
		if id == model {
			return nil
		}
	}
	return service.ErrChannelMonitorProbeInvalid
}
