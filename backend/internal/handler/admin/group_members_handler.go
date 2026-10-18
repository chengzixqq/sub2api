package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// BatchUpdateMembersRequest adds and removes accounts of one group.
type BatchUpdateMembersRequest struct {
	Add                     []service.GroupMemberAdd `json:"add"`
	Remove                  []int64                  `json:"remove"`
	ConfirmMixedChannelRisk bool                     `json:"confirm_mixed_channel_risk"`
}

// BatchUpdateMembers changes group membership account by account; failures are
// reported per account and do not block the others. Replays are no-ops.
// POST /api/v1/admin/groups/:id/accounts/batch
func (h *GroupHandler) BatchUpdateMembers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var req BatchUpdateMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.adminService.UpdateGroupMembers(c.Request.Context(), groupID, service.UpdateGroupMembersInput{
		Add: req.Add, Remove: req.Remove, SkipMixedChannelCheck: req.ConfirmMixedChannelRisk,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
