package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// SessionItem 会话列表项（一个会话对应一个 refresh token 家族，id 即 access token 的 sid）。
type SessionItem struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"device_name"`
	ClientType string    `json:"client_type"`
	UserAgent  string    `json:"user_agent"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

// SessionListResponse 会话列表响应。
type SessionListResponse struct {
	Items []SessionItem `json:"items"`
}

// RevokeSessionResponse 撤销单个会话响应；current 为 true 表示撤销的是本机会话，客户端应退出登录。
type RevokeSessionResponse struct {
	Message string `json:"message"`
	Current bool   `json:"current"`
}

// ListSessions 列出当前用户的登录会话（按最后活跃倒序，最多 50 条）
// GET /api/v1/auth/sessions
func (h *AuthHandler) ListSessions(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	sessions, err := h.authService.ListUserSessions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	currentID := c.GetString(middleware2.ContextKeySessionID)
	items := make([]SessionItem, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, SessionItem{
			ID:         s.ID,
			DeviceName: s.DeviceName,
			ClientType: s.ClientType,
			UserAgent:  s.UserAgent,
			IP:         s.IP,
			CreatedAt:  s.CreatedAt,
			LastSeenAt: s.LastSeenAt,
			ExpiresAt:  s.ExpiresAt,
			Current:    currentID != "" && s.ID == currentID,
		})
	}
	response.Success(c, SessionListResponse{Items: items})
}

// RevokeSession 撤销当前用户的一个会话：其 refresh token 删除，已签发的 access token 立即失效。
// 不存在或属于他人的 id 都返回 404 SESSION_NOT_FOUND。
// DELETE /api/v1/auth/sessions/:id
func (h *AuthHandler) RevokeSession(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	sessionID := c.Param("id")
	if err := h.authService.RevokeUserSession(c.Request.Context(), subject.UserID, sessionID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, RevokeSessionResponse{
		Message: "Session revoked",
		Current: sessionID == c.GetString(middleware2.ContextKeySessionID),
	})
}
