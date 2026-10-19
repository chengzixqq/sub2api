package admin

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// scheduledTestScopePlanRepo 是内存版计划仓储，记录写操作以断言越权请求没有落库。
type scheduledTestScopePlanRepo struct {
	plans   map[int64]*service.ScheduledTestPlan
	nextID  int64
	created int
	updated int
	deleted []int64
}

func (r *scheduledTestScopePlanRepo) Create(_ context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	r.created++
	r.nextID++
	out := *plan
	out.ID = r.nextID
	r.plans[out.ID] = &out
	return &out, nil
}

func (r *scheduledTestScopePlanRepo) GetByID(_ context.Context, id int64) (*service.ScheduledTestPlan, error) {
	plan, ok := r.plans[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	out := *plan
	return &out, nil
}

func (r *scheduledTestScopePlanRepo) ListByAccountID(_ context.Context, accountID int64) ([]*service.ScheduledTestPlan, error) {
	var out []*service.ScheduledTestPlan
	for _, plan := range r.plans {
		if plan.AccountID == accountID {
			copied := *plan
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (r *scheduledTestScopePlanRepo) ListDue(context.Context, time.Time) ([]*service.ScheduledTestPlan, error) {
	return nil, nil
}

func (r *scheduledTestScopePlanRepo) Update(_ context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	r.updated++
	out := *plan
	r.plans[out.ID] = &out
	return &out, nil
}

func (r *scheduledTestScopePlanRepo) Delete(_ context.Context, id int64) error {
	r.deleted = append(r.deleted, id)
	delete(r.plans, id)
	return nil
}

func (r *scheduledTestScopePlanRepo) UpdateAfterRun(context.Context, int64, time.Time, time.Time) error {
	return nil
}

type scheduledTestScopeResultRepo struct {
	listCalls int
}

func (r *scheduledTestScopeResultRepo) Create(_ context.Context, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	return result, nil
}

func (r *scheduledTestScopeResultRepo) ListByPlanID(context.Context, int64, int) ([]*service.ScheduledTestResult, error) {
	r.listCalls++
	return []*service.ScheduledTestResult{{ID: 1, Status: "success"}}, nil
}

func (r *scheduledTestScopeResultRepo) PruneOldResults(context.Context, int64, int) error {
	return nil
}

// scheduledTestScopeAdmin 按作用域模拟 GetByIDScoped：别家工作区的账号返回 not found。
type scheduledTestScopeAdmin struct {
	*stubAdminService
	accountWorkspaces map[int64]int64
	getAccountCalls   int
}

func (s *scheduledTestScopeAdmin) GetAccount(ctx context.Context, id int64) (*service.Account, error) {
	s.getAccountCalls++
	workspaceID, ok := s.accountWorkspaces[id]
	if !ok || !service.ScopeFromContextOrDeny(ctx).OwnsWorkspaceID(workspaceID) {
		return nil, service.ErrAccountNotFound
	}
	return &service.Account{ID: id, WorkspaceID: workspaceID, Status: service.StatusActive}, nil
}

const (
	scheduledTestOwnAccountID     int64 = 21
	scheduledTestForeignAccountID int64 = 22
	scheduledTestOwnPlanID        int64 = 101
	scheduledTestForeignPlanID    int64 = 102
	scheduledTestVendorWorkspace  int64 = 11
)

type scheduledTestScopeFixture struct {
	handler *ScheduledTestHandler
	plans   *scheduledTestScopePlanRepo
	results *scheduledTestScopeResultRepo
	admin   *scheduledTestScopeAdmin
}

func newScheduledTestScopeFixture() *scheduledTestScopeFixture {
	plans := &scheduledTestScopePlanRepo{
		nextID: 200,
		plans: map[int64]*service.ScheduledTestPlan{
			scheduledTestOwnPlanID:     {ID: scheduledTestOwnPlanID, AccountID: scheduledTestOwnAccountID, ModelID: "own-model", CronExpression: "*/5 * * * *", Enabled: true, MaxResults: 50},
			scheduledTestForeignPlanID: {ID: scheduledTestForeignPlanID, AccountID: scheduledTestForeignAccountID, ModelID: "foreign-model", CronExpression: "*/5 * * * *", Enabled: true, MaxResults: 50},
		},
	}
	results := &scheduledTestScopeResultRepo{}
	admin := &scheduledTestScopeAdmin{
		stubAdminService: newStubAdminService(),
		accountWorkspaces: map[int64]int64{
			scheduledTestOwnAccountID:     scheduledTestVendorWorkspace,
			scheduledTestForeignAccountID: 12,
		},
	}
	svc := service.NewScheduledTestService(plans, results)
	return &scheduledTestScopeFixture{
		handler: NewScheduledTestHandler(svc, admin),
		plans:   plans,
		results: results,
		admin:   admin,
	}
}

func scheduledTestVendorScope() service.Scope {
	return service.VendorScope(scheduledTestVendorWorkspace, service.WorkspacePermissions{AccountManage: true, MonitorView: true})
}

func newScheduledTestScopeContext(scope *service.Scope, method, target, body, id string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if scope != nil {
		request = request.WithContext(service.WithScope(request.Context(), *scope))
	}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	return c, recorder
}

func TestScheduledTestListByAccountRejectsForeignWorkspaceAccount(t *testing.T) {
	f := newScheduledTestScopeFixture()
	scope := scheduledTestVendorScope()

	c, recorder := newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/accounts/22/scheduled-test-plans", "", "22")
	f.handler.ListByAccount(c)

	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Contains(t, recorder.Body.String(), "ACCOUNT_NOT_FOUND")
	require.NotContains(t, recorder.Body.String(), "foreign-model")
}

func TestScheduledTestListByAccountAllowsOwnWorkspaceAccount(t *testing.T) {
	f := newScheduledTestScopeFixture()
	scope := scheduledTestVendorScope()

	c, recorder := newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/accounts/21/scheduled-test-plans", "", "21")
	f.handler.ListByAccount(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "own-model")
}

func TestScheduledTestListByAccountDeniesMissingScope(t *testing.T) {
	f := newScheduledTestScopeFixture()

	c, recorder := newScheduledTestScopeContext(nil, http.MethodGet, "/admin/accounts/21/scheduled-test-plans", "", "21")
	f.handler.ListByAccount(c)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "WORKSPACE_PERMISSION_DENIED")
	require.NotContains(t, recorder.Body.String(), "own-model")
}

// 站长不受限：不做归属查询，响应与引入校验前一致（含别家工作区账号）。
func TestScheduledTestAdminScopeSkipsOwnershipLookup(t *testing.T) {
	f := newScheduledTestScopeFixture()
	scope := service.AdminScope()

	c, recorder := newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/accounts/22/scheduled-test-plans", "", "22")
	f.handler.ListByAccount(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "foreign-model")

	c, recorder = newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/scheduled-test-plans/102/results", "", "102")
	f.handler.ListResults(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	c, recorder = newScheduledTestScopeContext(&scope, http.MethodDelete, "/admin/scheduled-test-plans/999", "", "999")
	f.handler.Delete(c)
	require.Equal(t, http.StatusOK, recorder.Code, "admin delete of a missing plan keeps returning 200")

	require.Zero(t, f.admin.getAccountCalls)
}

func TestScheduledTestPlanRoutesRejectForeignWorkspacePlan(t *testing.T) {
	scope := scheduledTestVendorScope()

	t.Run("results", func(t *testing.T) {
		f := newScheduledTestScopeFixture()
		c, recorder := newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/scheduled-test-plans/102/results", "", "102")
		f.handler.ListResults(c)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Contains(t, recorder.Body.String(), "RESOURCE_NOT_FOUND")
		require.Zero(t, f.results.listCalls)
	})

	t.Run("update", func(t *testing.T) {
		f := newScheduledTestScopeFixture()
		c, recorder := newScheduledTestScopeContext(&scope, http.MethodPut, "/admin/scheduled-test-plans/102", `{"enabled":false}`, "102")
		f.handler.Update(c)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Contains(t, recorder.Body.String(), "RESOURCE_NOT_FOUND")
		require.Zero(t, f.plans.updated)
		require.True(t, f.plans.plans[scheduledTestForeignPlanID].Enabled)
	})

	t.Run("delete", func(t *testing.T) {
		f := newScheduledTestScopeFixture()
		c, recorder := newScheduledTestScopeContext(&scope, http.MethodDelete, "/admin/scheduled-test-plans/102", "", "102")
		f.handler.Delete(c)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Contains(t, recorder.Body.String(), "RESOURCE_NOT_FOUND")
		require.Empty(t, f.plans.deleted)
	})

	t.Run("missing plan looks the same as a foreign plan", func(t *testing.T) {
		f := newScheduledTestScopeFixture()
		c, recorder := newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/scheduled-test-plans/999/results", "", "999")
		f.handler.ListResults(c)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Contains(t, recorder.Body.String(), "RESOURCE_NOT_FOUND")
	})

	t.Run("create", func(t *testing.T) {
		f := newScheduledTestScopeFixture()
		c, recorder := newScheduledTestScopeContext(&scope, http.MethodPost, "/admin/scheduled-test-plans", `{"account_id":22,"cron_expression":"*/5 * * * *"}`, "")
		f.handler.Create(c)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Contains(t, recorder.Body.String(), "ACCOUNT_NOT_FOUND")
		require.Zero(t, f.plans.created)
	})
}

func TestScheduledTestPlanRoutesAllowOwnWorkspacePlan(t *testing.T) {
	scope := scheduledTestVendorScope()
	f := newScheduledTestScopeFixture()

	c, recorder := newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/scheduled-test-plans/101/results", "", "101")
	f.handler.ListResults(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	c, recorder = newScheduledTestScopeContext(&scope, http.MethodPut, "/admin/scheduled-test-plans/101", `{"enabled":false}`, "101")
	f.handler.Update(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.False(t, f.plans.plans[scheduledTestOwnPlanID].Enabled)

	c, recorder = newScheduledTestScopeContext(&scope, http.MethodPost, "/admin/scheduled-test-plans", `{"account_id":21,"cron_expression":"*/5 * * * *"}`, "")
	f.handler.Create(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, f.plans.created)

	c, recorder = newScheduledTestScopeContext(&scope, http.MethodDelete, "/admin/scheduled-test-plans/101", "", "101")
	f.handler.Delete(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []int64{scheduledTestOwnPlanID}, f.plans.deleted)
}

// 只开监控档的 vendor 可以看自己账号的计划，但不能改。
func TestScheduledTestWritesRequireAccountManage(t *testing.T) {
	scope := service.VendorScope(scheduledTestVendorWorkspace, service.WorkspacePermissions{MonitorView: true})
	f := newScheduledTestScopeFixture()

	c, recorder := newScheduledTestScopeContext(&scope, http.MethodGet, "/admin/accounts/21/scheduled-test-plans", "", "21")
	f.handler.ListByAccount(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	c, recorder = newScheduledTestScopeContext(&scope, http.MethodDelete, "/admin/scheduled-test-plans/101", "", "101")
	f.handler.Delete(c)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Empty(t, f.plans.deleted)

	c, recorder = newScheduledTestScopeContext(&scope, http.MethodPost, "/admin/scheduled-test-plans", `{"account_id":21,"cron_expression":"*/5 * * * *"}`, "")
	f.handler.Create(c)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Zero(t, f.plans.created)
}
