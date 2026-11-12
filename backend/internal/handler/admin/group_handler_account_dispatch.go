package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *GroupHandler) accountDispatchAdminService(c *gin.Context) (service.AccountDispatchAdminService, bool) {
	adminService, ok := h.adminService.(service.AccountDispatchAdminService)
	if !ok {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "account dispatch policy unavailable"})
	}
	return adminService, ok
}

type putAccountDispatchPolicyRequest struct {
	Mode     string                         `json:"mode" binding:"required"`
	Accounts []service.AccountDispatchGrant `json:"accounts"`
}

func accountDispatchExpectedVersion(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
	version, err := strconv.ParseInt(raw, 10, 64)
	if raw == "" || err != nil || version < 0 {
		response.BadRequest(c, "If-Match must be a nonnegative integer version")
		return 0, false
	}
	return version, true
}

func (h *GroupHandler) ListAccountDispatchPolicies(c *gin.Context) {
	adminService, ok := h.accountDispatchAdminService(c)
	if !ok {
		return
	}
	groupID, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	entries, err := adminService.ListAccountDispatchPolicies(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"entries": entries})
}

func (h *GroupHandler) PutAccountDispatchPolicy(c *gin.Context) {
	adminService, ok := h.accountDispatchAdminService(c)
	if !ok {
		return
	}
	groupID, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	userID, ok := parsePositiveIDParam(c, "user_id")
	if !ok {
		return
	}
	version, ok := accountDispatchExpectedVersion(c)
	if !ok {
		return
	}
	var req putAccountDispatchPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	policy, err := adminService.PutAccountDispatchPolicy(c.Request.Context(), &service.AccountDispatchPolicy{
		UserID: userID, GroupID: groupID, Mode: req.Mode, Accounts: req.Accounts,
	}, version)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, policy)
}

func (h *GroupHandler) DeleteAccountDispatchPolicy(c *gin.Context) {
	adminService, ok := h.accountDispatchAdminService(c)
	if !ok {
		return
	}
	groupID, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	userID, ok := parsePositiveIDParam(c, "user_id")
	if !ok {
		return
	}
	version, ok := accountDispatchExpectedVersion(c)
	if !ok {
		return
	}
	if err := adminService.DeleteAccountDispatchPolicy(c.Request.Context(), userID, groupID, version); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Account dispatch policy restored to default"})
}
