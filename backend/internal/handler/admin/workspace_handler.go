package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// WorkspaceHandler 处理工作区管理端点。
//
// 除 GetMine 外，全部端点只对站长开放 —— 由路由白名单保证，
// 而非在 handler 内判角色。
//
// 工作区侧没有调价端点：结算倍率挂在账号上（走 /admin/accounts/:id），
// 这里只暴露站长设定的可调区间。
type WorkspaceHandler struct {
	svc *service.WorkspaceAdminService
}

func NewWorkspaceHandler(svc *service.WorkspaceAdminService) *WorkspaceHandler {
	return &WorkspaceHandler{svc: svc}
}

// workspacePermissionsPayload 是权限档的传输结构。
//
// 五个开关整档提交：站长每次看到并提交的是完整状态，
// 省略某档不表示「保留原值」，而是关闭它。
type workspacePermissionsPayload struct {
	AccountManage bool `json:"account_manage"`
	GroupOps      bool `json:"group_ops"`
	GroupBilling  bool `json:"group_billing"`
	ProxyManage   bool `json:"proxy_manage"`
	MonitorView   bool `json:"monitor_view"`
}

func (p workspacePermissionsPayload) toService() service.WorkspacePermissions {
	return service.WorkspacePermissions{
		AccountManage: p.AccountManage,
		GroupOps:      p.GroupOps,
		GroupBilling:  p.GroupBilling,
		ProxyManage:   p.ProxyManage,
		MonitorView:   p.MonitorView,
	}
}

func permissionsPayloadFrom(p service.WorkspacePermissions) workspacePermissionsPayload {
	return workspacePermissionsPayload{
		AccountManage: p.AccountManage,
		GroupOps:      p.GroupOps,
		GroupBilling:  p.GroupBilling,
		ProxyManage:   p.ProxyManage,
		MonitorView:   p.MonitorView,
	}
}

type workspaceResponse struct {
	ID          int64                       `json:"id"`
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Status      string                      `json:"status"`
	Permissions workspacePermissionsPayload `json:"permissions"`
	// SettlementRateMin/Max 是供应商自设账号倍率的可调区间。
	//
	// null 表示该端不设限，与「0」不同：供应商侧要据此区分「站长没设下限」
	// 和「站长把下限设成了 0」，前者输入框不显示边界提示，后者显示 0。
	// 两端都为 null 时供应商完全不可自调（见 domain 的校验）。
	SettlementRateMin *float64 `json:"settlement_rate_min"`
	SettlementRateMax *float64 `json:"settlement_rate_max"`
	CreatedAt         int64    `json:"created_at"`
	UpdatedAt         int64    `json:"updated_at"`
}

func workspaceResponseFrom(ws *service.Workspace) workspaceResponse {
	return workspaceResponse{
		ID:                ws.ID,
		Name:              ws.Name,
		Description:       ws.Description,
		Status:            ws.Status,
		Permissions:       permissionsPayloadFrom(ws.Permissions),
		SettlementRateMin: ws.SettlementRateMin,
		SettlementRateMax: ws.SettlementRateMax,
		CreatedAt:         ws.CreatedAt.Unix(),
		UpdatedAt:         ws.UpdatedAt.Unix(),
	}
}

// List 返回全部工作区。
// GET /api/v1/admin/workspaces
func (h *WorkspaceHandler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]workspaceResponse, 0, len(items))
	for i := range items {
		out = append(out, workspaceResponseFrom(items[i]))
	}
	response.Success(c, out)
}

// Get 返回单个工作区。
// GET /api/v1/admin/workspaces/:id
func (h *WorkspaceHandler) Get(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	ws, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, workspaceResponseFrom(ws))
}

type createWorkspaceRequest struct {
	Name        string                      `json:"name" binding:"required"`
	Description string                      `json:"description"`
	Permissions workspacePermissionsPayload `json:"permissions"`
	// 省略即两端都不设限，供应商不可自调倍率 —— 新建时的安全默认。
	SettlementRateMin *float64 `json:"settlement_rate_min"`
	SettlementRateMax *float64 `json:"settlement_rate_max"`
}

// Create 新建工作区。
// POST /api/v1/admin/workspaces
func (h *WorkspaceHandler) Create(c *gin.Context) {
	var req createWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	ws, err := h.svc.Create(c.Request.Context(), service.CreateWorkspaceInput{
		Name:              req.Name,
		Description:       req.Description,
		Permissions:       req.Permissions.toService(),
		SettlementRateMin: req.SettlementRateMin,
		SettlementRateMax: req.SettlementRateMax,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, workspaceResponseFrom(ws))
}

// updateWorkspaceRequest 全部字段可选。
//
// 指针语义：未提交的字段不写库，避免站长改个名字就把权限档清空。
type updateWorkspaceRequest struct {
	Name        *string                      `json:"name"`
	Description *string                      `json:"description"`
	Status      *string                      `json:"status" binding:"omitempty,oneof=active disabled"`
	Permissions *workspacePermissionsPayload `json:"permissions"`
	// 结算区间是三态：字段缺省=不改，提 0=清掉该端边界，提正数=设为该值。
	// 这与 service 层 normalizeRateBound 的约定一致；用 0 而非 null 表达
	// 「清除」，是因为 JSON null 与字段缺省在 *float64 上都解成 nil，分不开。
	SettlementRateMin *float64 `json:"settlement_rate_min"`
	SettlementRateMax *float64 `json:"settlement_rate_max"`
}

// Update 修改工作区。
// PUT /api/v1/admin/workspaces/:id
func (h *WorkspaceHandler) Update(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	var req updateWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	input := service.WorkspaceUpdateInput{
		Name:              req.Name,
		Description:       req.Description,
		Status:            req.Status,
		SettlementRateMin: req.SettlementRateMin,
		SettlementRateMax: req.SettlementRateMax,
	}
	if req.Permissions != nil {
		perms := req.Permissions.toService()
		input.Permissions = &perms
	}

	ws, err := h.svc.Update(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, workspaceResponseFrom(ws))
}

// Delete 软删除工作区。
// DELETE /api/v1/admin/workspaces/:id
func (h *WorkspaceHandler) Delete(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// memberResponse 是成员绑定的传输结构。
//
// email/username/role 在用户已被删除时为空串 —— 绑定行仍返回，
// 让站长看得到并能解绑这条脏数据。
type memberResponse struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	WorkspaceID int64  `json:"workspace_id"`
	Email       string `json:"email"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	CreatedAt   int64  `json:"created_at"`
	// RolePromoted 仅在绑定响应里出现：true 表示该用户原为普通用户，
	// 本次已顺带提升为 vendor。列表响应中恒为 false，故用 omitempty。
	RolePromoted bool `json:"role_promoted,omitempty"`
}

// ListMembers 返回工作区成员。
// GET /api/v1/admin/workspaces/:id/members
func (h *WorkspaceHandler) ListMembers(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	members, err := h.svc.ListMemberDetails(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]memberResponse, 0, len(members))
	for i := range members {
		out = append(out, memberResponse{
			ID:          members[i].ID,
			UserID:      members[i].UserID,
			WorkspaceID: members[i].WorkspaceID,
			Email:       members[i].Email,
			Username:    members[i].Username,
			Role:        members[i].Role,
			CreatedAt:   members[i].CreatedAt.Unix(),
		})
	}
	response.Success(c, out)
}

type addMemberRequest struct {
	UserID int64 `json:"user_id" binding:"required,min=1"`
}

// AddMember 绑定用户到工作区。
// POST /api/v1/admin/workspaces/:id/members
func (h *WorkspaceHandler) AddMember(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	var req addMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.svc.AddMember(c.Request.Context(), id, req.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 前端收到后直接重载列表，这里不再单独查一次用户信息。
	// role_promoted 必须回传：这次绑定可能顺带改了用户的全局角色，
	// 站长有权在当场就知道，而不是事后从审计日志里翻出来。
	response.Created(c, memberResponse{
		ID:           result.Member.ID,
		UserID:       result.Member.UserID,
		WorkspaceID:  result.Member.WorkspaceID,
		CreatedAt:    result.Member.CreatedAt.Unix(),
		RolePromoted: result.RolePromoted,
	})
}

// RemoveMember 解除绑定。
// DELETE /api/v1/admin/workspaces/:id/members/:user_id
func (h *WorkspaceHandler) RemoveMember(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	userID, ok := parsePositiveIDParam(c, "user_id")
	if !ok {
		return
	}
	if err := h.svc.RemoveMember(c.Request.Context(), id, userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// grantResponse 只描述「可见性 + 新号优先级」。
//
// 不含倍率：结算倍率就是账号自身的 rate_multiplier，可调区间挂在
// 工作区上，供应商在账号管理里改。
type grantResponse struct {
	ID           int64 `json:"id"`
	WorkspaceID  int64 `json:"workspace_id"`
	GroupID      int64 `json:"group_id"`
	BasePriority int   `json:"base_priority"`
	Enabled      bool  `json:"enabled"`
}

func grantResponseFrom(g *service.WorkspaceGroupGrant) grantResponse {
	return grantResponse{
		ID:           g.ID,
		WorkspaceID:  g.WorkspaceID,
		GroupID:      g.GroupID,
		BasePriority: g.BasePriority,
		Enabled:      g.Enabled,
	}
}

// ListGrants 返回工作区的分组授权。
// GET /api/v1/admin/workspaces/:id/grants
func (h *WorkspaceHandler) ListGrants(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	grants, err := h.svc.ListGrants(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]grantResponse, 0, len(grants))
	for i := range grants {
		out = append(out, grantResponseFrom(grants[i]))
	}
	response.Success(c, out)
}

type upsertGrantRequest struct {
	GroupID      int64 `json:"group_id" binding:"required,min=1"`
	BasePriority int   `json:"base_priority"`
	Enabled      *bool `json:"enabled"`
}

// UpsertGrant 新增或修改分组授权。
// PUT /api/v1/admin/workspaces/:id/grants
func (h *WorkspaceHandler) UpsertGrant(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	var req upsertGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// Enabled 缺省为 true：站长新增一条授权的意图就是要它生效。
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	grant, err := h.svc.UpsertGrant(c.Request.Context(), service.WorkspaceGrantInput{
		WorkspaceID:  id,
		GroupID:      req.GroupID,
		BasePriority: req.BasePriority,
		Enabled:      enabled,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, grantResponseFrom(grant))
}

// DeleteGrant 收回分组授权。
// DELETE /api/v1/admin/workspaces/:id/grants/:group_id
func (h *WorkspaceHandler) DeleteGrant(c *gin.Context) {
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	groupID, ok := parsePositiveIDParam(c, "group_id")
	if !ok {
		return
	}
	if err := h.svc.DeleteGrant(c.Request.Context(), id, groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// mineResponse 是 vendor 自读的载荷。
//
// 只暴露供应商该知道的：自己的工作区、权限档、结算倍率可调区间与已授权分组，
// 不含成员列表（那是站长视角的运营信息）。
//
// 结算倍率本身挂在账号上，不在这里 —— 供应商从这里拿到的是区间边界，
// 用来在账号管理页约束输入；具体某个账号的倍率随账号一起返回。
type mineResponse struct {
	Workspace workspaceResponse `json:"workspace"`
	Grants    []grantResponse   `json:"grants"`
}

// GetMine 返回当前 vendor 自己的工作区与授权。
// GET /api/v1/admin/workspaces/me
//
// 工作区取自上下文作用域，不接受任何入参 —— 供应商无法读他人工作区。
func (h *WorkspaceHandler) GetMine(c *gin.Context) {
	scope, exists := middleware.GetVendorScopeFromContext(c)
	if !exists || scope.Unrestricted || scope.WorkspaceID <= 0 {
		// 站长没有「自己的工作区」，用空载荷表达而非报错，
		// 让前端可以用同一个请求判断当前身份是否属于某工作区。
		response.Success(c, gin.H{"workspace": nil, "grants": []grantResponse{}})
		return
	}

	ws, err := h.svc.Get(c.Request.Context(), scope.WorkspaceID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	grants, err := h.svc.ListGrants(c.Request.Context(), scope.WorkspaceID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := mineResponse{
		Workspace: workspaceResponseFrom(ws),
		Grants:    make([]grantResponse, 0, len(grants)),
	}
	for i := range grants {
		out.Grants = append(out.Grants, grantResponseFrom(grants[i]))
	}
	response.Success(c, out)
}

// 供应商调整结算倍率不再走本文件的专用端点。
//
// 倍率就是账号自身的 rate_multiplier，改它走 /admin/accounts/:id —— 供应商
// 本来就有账号管理权，一处入口即可，可调区间由工作区的
// settlement_rate_min/max 在 service 层夹住。

// ID 路径参数统一复用 group_handler.go 的 parsePositiveIDParam：
// 工作区、成员、分组 ID 都必须为正。
