package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetClaudeCustomization returns the global Claude compatibility policy.
func (h *SettingHandler) GetClaudeCustomization(c *gin.Context) {
	cfg, err := h.settingService.GetClaudeCustomizationSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"global":     cfg,
		"defaults":   service.DefaultClaudeCustomizationSettings(),
		"precedence": []string{"account", "global", "defaults"},
	})
}

type updateClaudeCustomizationRequest struct {
	service.ClaudeCustomizationSettings
}

// UpdateClaudeCustomization replaces the global policy. The preset controls
// official/magic defaults; custom preserves the explicitly supplied fields.
func (h *SettingHandler) UpdateClaudeCustomization(c *gin.Context) {
	var req updateClaudeCustomizationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid customization settings: "+err.Error())
		return
	}
	if err := h.settingService.SetClaudeCustomizationSettings(c.Request.Context(), req.ClaudeCustomizationSettings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	cfg, err := h.settingService.GetClaudeCustomizationSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"global": cfg})
}

type applyClaudeCustomizationPresetRequest struct {
	Preset string `json:"preset" binding:"required"`
}

// ApplyClaudeCustomizationPreset applies one of the built-in policy presets.
func (h *SettingHandler) ApplyClaudeCustomizationPreset(c *gin.Context) {
	var req applyClaudeCustomizationPresetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid preset: "+err.Error())
		return
	}
	cfg, err := h.settingService.GetClaudeCustomizationSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	cfg.Preset = req.Preset
	if err := h.settingService.SetClaudeCustomizationSettings(c.Request.Context(), cfg); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// SetClaudeCustomizationSettings materializes built-in presets on its value
	// receiver before persisting. Reload it so the response reflects the exact
	// values now active instead of the pre-existing fields from the old preset.
	persisted, err := h.settingService.GetClaudeCustomizationSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"global": persisted})
}

// ResetClaudeCustomization restores the recommended magic preset.
func (h *SettingHandler) ResetClaudeCustomization(c *gin.Context) {
	cfg := service.DefaultClaudeCustomizationSettings()
	if err := h.settingService.SetClaudeCustomizationSettings(c.Request.Context(), cfg); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"global": cfg})
}
