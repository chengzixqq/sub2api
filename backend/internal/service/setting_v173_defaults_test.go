package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestV173MissingSettingsUseSafeGrokAndChannelMonitorDefaults(t *testing.T) {
	settings := (&SettingService{cfg: &config.Config{}}).parseSettings(map[string]string{})

	if settings.GrokCrossClientModelMapEnabled {
		t.Fatal("missing Grok cross-client mapping setting must default to disabled")
	}
	if settings.ChannelMonitorMode != ChannelMonitorModeV1 {
		t.Fatalf("missing channel monitor mode must default to v1, got %q", settings.ChannelMonitorMode)
	}
}

func TestV173ExplicitGrokCrossClientMappingValueIsPreserved(t *testing.T) {
	service := &SettingService{cfg: &config.Config{}}
	if !service.parseSettings(map[string]string{
		SettingKeyGrokCrossClientModelMapEnabled: "true",
	}).GrokCrossClientModelMapEnabled {
		t.Fatal("explicit true must remain enabled")
	}
	if service.parseSettings(map[string]string{
		SettingKeyGrokCrossClientModelMapEnabled: "false",
	}).GrokCrossClientModelMapEnabled {
		t.Fatal("explicit false must remain disabled")
	}
}
