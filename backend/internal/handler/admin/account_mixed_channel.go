package admin

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// mixedChannelWarningError 是混合渠道响应里为旧客户端保留的 error 字段值。
const mixedChannelWarningError = "mixed_channel_warning"

// mixedChannelDetails 构造混合渠道风险的 details：group_id 保持数字，
// check-mixed-channel 的 200 与各写接口的 409 共用同一形状。
func mixedChannelDetails(e *service.MixedChannelError) gin.H {
	return gin.H{
		"group_id":         e.GroupID,
		"group_name":       e.GroupName,
		"current_platform": e.CurrentPlatform,
		"other_platform":   e.OtherPlatform,
	}
}

// respondMixedChannelConflict 在 err 是混合渠道风险时写出 409 并返回 true。
//
// 只增不改：保留旧的 error / message，新增 code、reason、metadata（全字符串）
// 与 details（数字 group_id）。App 据 reason=MIXED_CHANNEL_WARNING 弹确认框，
// 确认后带 confirm_mixed_channel_risk=true 重发。
func respondMixedChannelConflict(c *gin.Context, err error) bool {
	var mixedErr *service.MixedChannelError
	if !errors.As(err, &mixedErr) {
		return false
	}
	appErr := mixedErr.ConflictError()
	c.JSON(int(appErr.Code), gin.H{
		"code":     int(appErr.Code),
		"reason":   appErr.Reason,
		"error":    mixedChannelWarningError,
		"message":  appErr.Message,
		"metadata": appErr.Metadata,
		"details":  mixedChannelDetails(mixedErr),
	})
	return true
}
