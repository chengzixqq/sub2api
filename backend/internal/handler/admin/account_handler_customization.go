package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type updateAccountClaudeCustomizationRequest struct {
	Overrides map[string]any `json:"overrides"`
}

// GetClaudeCustomization returns effective policy and sparse account overrides.
func (h *AccountHandler) GetClaudeCustomization(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	effective, overrides, err := h.resolveAccountClaudeCustomization(c, account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.ClaudeCustomizationResponse{
		Global:    effectiveGlobalClaudeCustomization(c, h),
		Overrides: overrides,
		Effective: effective,
	})
}

// UpdateClaudeCustomization updates only sparse account-level overrides. A
// null field restores inheritance from the global policy.
func (h *AccountHandler) UpdateClaudeCustomization(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req updateAccountClaudeCustomizationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid customization overrides: "+err.Error())
		return
	}
	overrides, err := service.NormalizeClaudeCustomizationOverrides(req.Overrides)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.adminService.UpdateAccountExtra(c.Request.Context(), accountID, map[string]any{
		service.AccountClaudeCustomizationExtraKey: overrides,
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	effective, persisted, err := h.resolveAccountClaudeCustomization(c, account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.ClaudeCustomizationResponse{
		Global:    effectiveGlobalClaudeCustomization(c, h),
		Overrides: persisted,
		Effective: effective,
	})
}

func (h *AccountHandler) resolveAccountClaudeCustomization(c *gin.Context, account *service.Account) (service.ClaudeCustomizationSettings, map[string]any, error) {
	if h != nil && h.settingService != nil {
		return h.settingService.ResolveClaudeCustomization(c.Request.Context(), account)
	}
	return service.DefaultClaudeCustomizationSettings(), nil, nil
}

func effectiveGlobalClaudeCustomization(c *gin.Context, h *AccountHandler) service.ClaudeCustomizationSettings {
	if h != nil && h.settingService != nil {
		if cfg, err := h.settingService.GetClaudeCustomizationSettings(c.Request.Context()); err == nil {
			return cfg
		}
	}
	return service.DefaultClaudeCustomizationSettings()
}
