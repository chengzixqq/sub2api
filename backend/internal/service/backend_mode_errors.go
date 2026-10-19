package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

// 后端模式拒绝非管理员的错误。
var (
	ErrBackendModeAdminOnly           = infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
	ErrBackendModeSelfServiceDisabled = infraerrors.Forbidden("BACKEND_MODE_SELF_SERVICE_DISABLED", "Backend mode is active. User self-service is disabled.")
)
