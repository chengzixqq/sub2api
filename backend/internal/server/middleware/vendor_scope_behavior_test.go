package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// fakeWorkspaceRepo 是 service.WorkspaceRepository 的测试替身。
//
// 只实现鉴权链路真正会走到的方法，其余返回 not found —— 若某条被意外调用，
// 测试会以「未授权」失败而不是静默通过。
type fakeWorkspaceRepo struct {
	byUser map[int64]*service.Workspace
	err    error
}

func (f *fakeWorkspaceRepo) GetByUserID(_ context.Context, userID int64) (*service.Workspace, error) {
	if f.err != nil {
		return nil, f.err
	}
	ws, ok := f.byUser[userID]
	if !ok {
		return nil, domain.ErrWorkspaceMemberNotFound
	}
	return ws, nil
}

func (f *fakeWorkspaceRepo) GetByID(_ context.Context, _ int64) (*service.Workspace, error) {
	return nil, domain.ErrWorkspaceNotFound
}

func (f *fakeWorkspaceRepo) List(_ context.Context) ([]*service.Workspace, error) {
	return nil, nil
}

func (f *fakeWorkspaceRepo) ListGrantsByWorkspace(_ context.Context, _ int64) ([]*service.WorkspaceGroupGrant, error) {
	return nil, nil
}

func (f *fakeWorkspaceRepo) GetGrant(_ context.Context, _, _ int64) (*service.WorkspaceGroupGrant, error) {
	return nil, domain.ErrWorkspaceGrantNotFound
}

func (f *fakeWorkspaceRepo) CountGrantsByGroup(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// newVendorScopeTestRouter 挂载中间件并在终端处理器里回写解析结果，
// 便于断言「放行后拿到的作用域」而不仅是状态码。
func newVendorScopeTestRouter(
	t *testing.T,
	svc *service.WorkspaceService,
	routePath string,
	onPass func(c *gin.Context),
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET(routePath, NewVendorScopeMiddleware(svc), func(c *gin.Context) {
		if onPass != nil {
			onPass(c)
		}
		c.Status(http.StatusOK)
	})
	return r
}

// withRole 在中间件之前注入角色与主体，模拟管理端鉴权已通过。
func withRole(role string, userID int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(string(ContextKeyUserRole), role)
		c.Set(string(ContextKeyUser), AuthSubject{UserID: userID})
		c.Next()
	}
}

// TestVendorScopeAdminIsUnrestricted 锁定 admin 行为与引入工作区之前一致。
//
// 这是最重要的回归锚点：站长请求不得被作用域机制改变，
// 否则等于用一个新功能打断了原有的全站管理能力。
func TestVendorScopeAdminIsUnrestricted(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceRepo{}, nil)

	var got VendorScope
	var ok bool
	r := gin.New()
	r.GET("/api/v1/admin/settings",
		withRole(service.RoleAdmin, 1),
		NewVendorScopeMiddleware(svc),
		func(c *gin.Context) {
			got, ok = GetVendorScopeFromContext(c)
			c.Status(http.StatusOK)
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("admin 请求应放行，得到 %d", w.Code)
	}
	if !ok {
		t.Fatal("admin 请求未写入作用域")
	}
	if !got.Unrestricted {
		t.Error("admin 作用域必须是 Unrestricted，否则全站管理能力被收窄")
	}
}

// TestVendorScopeRejectsUnknownRole 锁定未知角色一律拒绝。
//
// 默认拒绝是本机制的安全底线：新增角色若忘记登记，
// 必须是访问不了后台，而不是拿到不受限作用域。
func TestVendorScopeRejectsUnknownRole(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceRepo{}, nil)

	r := gin.New()
	r.GET("/api/v1/admin/accounts",
		withRole("some_future_role", 7),
		NewVendorScopeMiddleware(svc),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("未知角色应 403，得到 %d", w.Code)
	}
}

// TestVendorScopeRejectsMissingRole 锁定角色缺失时拒绝而非放行。
//
// 覆盖中间件挂载顺序写错（挂在鉴权之前）的场景。
func TestVendorScopeRejectsMissingRole(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceRepo{}, nil)
	r := newVendorScopeTestRouter(t, svc, "/api/v1/admin/accounts", nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("角色缺失应 401，得到 %d", w.Code)
	}
}

// TestVendorScopeRejectsWhenServiceMissing 锁定依赖未装配时拒绝 vendor。
//
// 宁可 vendor 用不了后台，也不能在拿不到工作区的情况下放行 ——
// 那会让请求以零值作用域继续，语义上等同不受限。
func TestVendorScopeRejectsWhenServiceMissing(t *testing.T) {
	r := gin.New()
	r.GET("/api/v1/admin/accounts",
		withRole(service.RoleVendor, 9),
		NewVendorScopeMiddleware(nil),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("工作区服务缺失时应 403，得到 %d", w.Code)
	}
}

// newVendorRepo 构造一个绑定了工作区的 vendor 仓储替身。
//
// status 直接用 domain 常量传入，避免测试自行拼字符串而与实现漂移。
func newVendorRepo(userID, workspaceID int64, status string, perms service.WorkspacePermissions) *fakeWorkspaceRepo {
	return &fakeWorkspaceRepo{
		byUser: map[int64]*service.Workspace{
			userID: {
				ID:          workspaceID,
				Name:        "ws",
				Status:      status,
				Permissions: perms,
			},
		},
	}
}

// TestVendorScopeResolvesWorkspaceAndPerms 锁定 vendor 放行后拿到正确的
// 工作区与权限档，且不是 Unrestricted。
//
// 这条覆盖隔离的正向路径：作用域必须带上 WorkspaceID，
// 否则 service 层收窄无从下手。
func TestVendorScopeResolvesWorkspaceAndPerms(t *testing.T) {
	repo := newVendorRepo(42, 7, domain.WorkspaceStatusActive, service.WorkspacePermissions{AccountManage: true})
	svc := service.NewWorkspaceService(repo, nil)

	var got VendorScope
	var ok bool
	r := gin.New()
	r.GET("/api/v1/admin/accounts",
		withRole(service.RoleVendor, 42),
		NewVendorScopeMiddleware(svc),
		func(c *gin.Context) {
			got, ok = GetVendorScopeFromContext(c)
			c.Status(http.StatusOK)
		})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("已授权 vendor 请求应放行，得到 %d", w.Code)
	}
	if !ok {
		t.Fatal("vendor 请求未写入作用域")
	}
	if got.Unrestricted {
		t.Error("vendor 作用域不得为 Unrestricted —— 那等于全站可见")
	}
	if got.WorkspaceID != 7 {
		t.Errorf("WorkspaceID = %d, 期望 7", got.WorkspaceID)
	}
	if !got.Perms.AccountManage {
		t.Error("已开启的 AccountManage 权限档未传递到作用域")
	}
}

// TestVendorScopePermsDefaultDeny 锁定未开启权限档时路由即被拒绝。
//
// 权限判定发生在中间件（vendorAllowedRoutes 的 permit），不留给 handler：
// 权限结构体零值即全 false，未显式开权限的工作区连白名单路由也进不去。
func TestVendorScopePermsDefaultDeny(t *testing.T) {
	repo := newVendorRepo(42, 7, domain.WorkspaceStatusActive, service.WorkspacePermissions{})
	svc := service.NewWorkspaceService(repo, nil)

	r := gin.New()
	r.GET("/api/v1/admin/accounts",
		withRole(service.RoleVendor, 42),
		NewVendorScopeMiddleware(svc),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("未开权限档的工作区访问受权限保护的路由应 403，得到 %d", w.Code)
	}
}

// TestVendorScopeScopeAllowsIsDefaultDeny 锁定作用域自身的权限判定为默认拒绝。
//
// 与上一条互补：那条验证中间件的路由拦截，这条验证 Allows 这个判定原语，
// 供 service/handler 层复用时同样不会“兜底放开”。
func TestVendorScopeScopeAllowsIsDefaultDeny(t *testing.T) {
	scope := VendorScope{WorkspaceID: 7, Perms: service.WorkspacePermissions{}}
	if scope.Allows(func(p service.WorkspacePermissions) bool { return p.AccountManage }) {
		t.Error("未开启的权限档必须判定为拒绝")
	}
}

// TestVendorScopeAdminAllowsEveryPerm 锁定 admin 恒具备全部权限档。
func TestVendorScopeAdminAllowsEveryPerm(t *testing.T) {
	scope := AdminScope()
	if !scope.Allows(func(p service.WorkspacePermissions) bool { return p.AccountManage }) {
		t.Error("admin 应恒具备任意权限档")
	}
}

// TestVendorScopeRejectsDisabledWorkspace 锁定停用工作区立即失去后台访问。
//
// 站长停用工作区是运营上的“断闸”手段，必须当场生效（受缓存 TTL 限制）。
func TestVendorScopeRejectsDisabledWorkspace(t *testing.T) {
	repo := newVendorRepo(42, 7, domain.WorkspaceStatusDisabled, service.WorkspacePermissions{AccountManage: true})
	svc := service.NewWorkspaceService(repo, nil)

	r := gin.New()
	r.GET("/api/v1/admin/accounts",
		withRole(service.RoleVendor, 42),
		NewVendorScopeMiddleware(svc),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("停用工作区应 403，得到 %d", w.Code)
	}
}

// TestVendorScopeRejectsUnboundVendor 锁定未绑定工作区的 vendor 被拒绝。
//
// 这属于数据不一致（角色是 vendor 却没有成员记录），
// 不能回落成不受限或空作用域放行。
func TestVendorScopeRejectsUnboundVendor(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceRepo{}, nil)

	r := gin.New()
	r.GET("/api/v1/admin/accounts",
		withRole(service.RoleVendor, 42),
		NewVendorScopeMiddleware(svc),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("未绑定工作区的 vendor 应 403，得到 %d", w.Code)
	}
}

// TestVendorScopeRejectsRouteOutsideWhitelist 锁定白名单外路由对 vendor 关闭。
//
// 与静态清单测试互补：那些校验两份清单一致，这条校验清单真的被执行。
func TestVendorScopeRejectsRouteOutsideWhitelist(t *testing.T) {
	repo := newVendorRepo(42, 7, domain.WorkspaceStatusActive, service.WorkspacePermissions{AccountManage: true})
	svc := service.NewWorkspaceService(repo, nil)

	r := gin.New()
	r.GET("/api/v1/admin/settings",
		withRole(service.RoleVendor, 42),
		NewVendorScopeMiddleware(svc),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("白名单外路由对 vendor 应 403，得到 %d", w.Code)
	}
}
