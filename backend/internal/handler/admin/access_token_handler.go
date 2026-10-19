package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// AccessTokenHandler 管理访问令牌（sat_）：按设备签发、可命名、可只读、会过期、可单独吊销。
type AccessTokenHandler struct {
	tokens *service.AdminAccessTokenService
}

func NewAccessTokenHandler(tokens *service.AdminAccessTokenService) *AccessTokenHandler {
	return &AccessTokenHandler{tokens: tokens}
}

type createAccessTokenRequest struct {
	Name          string `json:"name"`
	Scope         string `json:"scope"`
	ExpiresInDays int    `json:"expires_in_days"`
}

type accessTokenView struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Last4      string     `json:"last4"`
	Scope      string     `json:"scope"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip"`
	CreatedAt  time.Time  `json:"created_at"`
	// Current 表示本次请求使用的就是这个令牌。
	Current bool `json:"current"`
}

type createdAccessTokenView struct {
	accessTokenView
	// Token 令牌原文，只在创建时返回这一次。
	Token string `json:"token"`
}

func toAccessTokenView(t *service.AdminAccessToken, currentID int64) accessTokenView {
	return accessTokenView{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, Last4: t.Last4, Scope: t.Scope,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, LastUsedIP: t.LastUsedIP, CreatedAt: t.CreatedAt,
		Current: currentID > 0 && t.ID == currentID,
	}
}

// Create 签发一个管理访问令牌（只允许登录会话调用，路由要求 step-up）
// POST /api/v1/admin/access-tokens
func (h *AccessTokenHandler) Create(c *gin.Context) {
	if service.IsMachineCredential(c.GetString("auth_method")) {
		response.ErrorFrom(c, service.ErrAdminAccessTokenNeedSession)
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if role, _ := middleware.GetUserRoleFromContext(c); role != service.RoleAdmin {
		response.Forbidden(c, "Admin access required")
		return
	}
	var req createAccessTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	created, err := h.tokens.Create(c.Request.Context(), service.CreateAdminAccessTokenInput{
		UserID: subject.UserID, Name: req.Name, Scope: req.Scope, ExpiresInDays: req.ExpiresInDays,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, createdAccessTokenView{accessTokenView: toAccessTokenView(created.Record, 0), Token: created.Token})
}

// List 列出当前管理员未吊销、未过期的访问令牌（不含原文）
// GET /api/v1/admin/access-tokens
func (h *AccessTokenHandler) List(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	tokens, err := h.tokens.List(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	currentID := c.GetInt64(middleware.ContextKeyAdminAccessTokenID)
	items := make([]accessTokenView, 0, len(tokens))
	for _, t := range tokens {
		items = append(items, toAccessTokenView(t, currentID))
	}
	response.Success(c, gin.H{"items": items})
}

// Revoke 立即吊销当前管理员的一个访问令牌；不存在或属于他人返回 404 ACCESS_TOKEN_NOT_FOUND
// DELETE /api/v1/admin/access-tokens/:id
func (h *AccessTokenHandler) Revoke(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, service.ErrAdminAccessTokenNotFound)
		return
	}
	if err := h.tokens.Revoke(c.Request.Context(), subject.UserID, id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"message": "Access token revoked",
		"current": id == c.GetInt64(middleware.ContextKeyAdminAccessTokenID),
	})
}
