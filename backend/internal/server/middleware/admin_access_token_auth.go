package middleware

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ContextKeyAdminAccessTokenID 管理访问令牌（sat_）认证时写入的令牌 ID。
const ContextKeyAdminAccessTokenID = "admin_access_token_id"

// adminAccessTokenSelfRevokePath 只读令牌也允许撤销自己（App 退出登录时）。
const adminAccessTokenSelfRevokePath = "/api/v1/admin/access-tokens/:id"

type adminAccessTokenAuthenticator interface {
	Authenticate(ctx context.Context, plain, clientIP string) (*service.AdminAccessToken, error)
}

// validateAdminAccessToken 验证 sat_ 管理访问令牌：主体是令牌所属的管理员（不是第一个管理员），
// 只读令牌拒绝写方法。
func validateAdminAccessToken(c *gin.Context, plain string, tokens adminAccessTokenAuthenticator, userService *service.UserService) bool {
	if tokens == nil {
		AbortWithError(c, http.StatusUnauthorized, "INVALID_ADMIN_KEY", "Invalid admin API key")
		return false
	}
	token, err := tokens.Authenticate(c.Request.Context(), plain, SecurityClientIP(c))
	if err != nil {
		abortWithServiceError(c, err)
		return false
	}

	user, err := userService.GetByID(c.Request.Context(), token.UserID)
	if err != nil {
		if errors.Is(err, service.ErrUserNotFound) {
			AbortWithError(c, http.StatusUnauthorized, "INVALID_ADMIN_KEY", "Invalid admin API key")
		} else {
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load user")
		}
		return false
	}
	if !user.IsActive() {
		AbortWithError(c, http.StatusUnauthorized, "USER_INACTIVE", "User account is not active")
		return false
	}
	if user.Role != service.RoleAdmin {
		AbortWithError(c, http.StatusForbidden, "FORBIDDEN", "Admin access required")
		return false
	}
	if token.ReadOnly() && isWriteMethod(c.Request.Method) && !isAdminAccessTokenSelfRevoke(c, token.ID) {
		AbortWithError(c, http.StatusForbidden, "ACCESS_TOKEN_READ_ONLY", "This admin access token is read-only")
		return false
	}

	c.Set(string(ContextKeyUser), AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})
	c.Set(string(ContextKeyUserRole), user.Role)
	c.Set(ContextKeyAuthEmail, user.Email)
	c.Set("auth_method", service.AuditAuthMethodAdminAccessToken)
	c.Set(ContextKeyAdminAccessTokenID, token.ID)
	return true
}

func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isAdminAccessTokenSelfRevoke(c *gin.Context, tokenID int64) bool {
	return c.Request.Method == http.MethodDelete &&
		c.FullPath() == adminAccessTokenSelfRevokePath &&
		c.Param("id") == strconv.FormatInt(tokenID, 10)
}

// abortWithServiceError 把服务层的 ApplicationError 写成中间件错误格式（code 与 reason 都是字符串）。
func abortWithServiceError(c *gin.Context, err error) {
	reason := infraerrors.Reason(err)
	if reason == "" {
		AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error")
		return
	}
	AbortWithError(c, infraerrors.Code(err), reason, infraerrors.Message(err))
}
