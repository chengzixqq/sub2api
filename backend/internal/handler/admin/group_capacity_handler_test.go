package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// capacityAccountRepoStub 实现容量批量取数的两个方法，按请求的分组返回行并记录调用。
type capacityAccountRepoStub struct {
	service.AccountRepository
	rows              []service.GroupAccountCapacityRow
	unscopedCalls     int
	scopedCalls       int
	scopedWorkspaceID int64
	requested         []int64
}

func (s *capacityAccountRepoStub) ListSchedulableCapacityByGroupIDs(_ context.Context, groupIDs []int64) ([]service.GroupAccountCapacityRow, error) {
	s.unscopedCalls++
	return s.rowsFor(groupIDs), nil
}

func (s *capacityAccountRepoStub) ListSchedulableCapacityByGroupIDsScoped(_ context.Context, groupIDs []int64, workspaceID int64) ([]service.GroupAccountCapacityRow, error) {
	s.scopedCalls++
	s.scopedWorkspaceID = workspaceID
	return s.rowsFor(groupIDs), nil
}

func (s *capacityAccountRepoStub) rowsFor(groupIDs []int64) []service.GroupAccountCapacityRow {
	s.requested = append([]int64(nil), groupIDs...)
	wanted := make(map[int64]bool, len(groupIDs))
	for _, id := range groupIDs {
		wanted[id] = true
	}
	out := make([]service.GroupAccountCapacityRow, 0, len(s.rows))
	for _, row := range s.rows {
		if wanted[row.GroupID] {
			out = append(out, row)
		}
	}
	return out
}

func (s *capacityAccountRepoStub) calls() int { return s.unscopedCalls + s.scopedCalls }

// capacityGroupRepoStub 实现启用分组列表与批量存在性读取。
type capacityGroupRepoStub struct {
	service.GroupRepository
	active      []int64
	existing    map[int64]bool
	listCalls   int
	existsCalls int
}

func (s *capacityGroupRepoStub) ListActiveIDs(context.Context) ([]int64, error) {
	s.listCalls++
	return append([]int64(nil), s.active...), nil
}

func (s *capacityGroupRepoStub) ExistsByIDs(_ context.Context, ids []int64) (map[int64]bool, error) {
	s.existsCalls++
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = s.existing[id]
	}
	return out, nil
}

// scopedCapacityAdminStub 模拟 vendor 的分组授权集。
type scopedCapacityAdminStub struct {
	*stubAdminService
	granted map[int64]bool
}

func (s *scopedCapacityAdminStub) FilterGroupIDsByScope(_ context.Context, ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if s.granted[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

type capacityFixture struct {
	router    *gin.Engine
	accounts  *capacityAccountRepoStub
	groups    *capacityGroupRepoStub
	expectAll []service.GroupCapacitySummary
}

// newCapacityFixture：启用分组 10、20、30（按 sort_order 排为 10、20、30），40 存在但停用。
func newCapacityFixture(t *testing.T, admin service.AdminService, scope *service.Scope) capacityFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	accounts := &capacityAccountRepoStub{rows: []service.GroupAccountCapacityRow{
		{GroupID: 10, AccountID: 1, Concurrency: 2, Extra: map[string]any{"max_sessions": 3}},
		{GroupID: 20, AccountID: 2, Concurrency: 4},
		{GroupID: 20, AccountID: 3, Concurrency: 1, Extra: map[string]any{"base_rpm": 9}},
		{GroupID: 30, AccountID: 4, Concurrency: 6},
	}}
	groups := &capacityGroupRepoStub{
		active:   []int64{10, 20, 30},
		existing: map[int64]bool{10: true, 20: true, 30: true, 40: true},
	}
	capacity := service.NewGroupCapacityService(accounts, groups, nil, nil, nil)
	h := NewGroupHandlerWithConfig(admin, nil, capacity, nil, nil)
	r := gin.New()
	if scope != nil {
		r.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), *scope))
			c.Next()
		})
	}
	r.GET("/groups/capacity-summary", h.GetCapacitySummary)
	return capacityFixture{
		router:   r,
		accounts: accounts,
		groups:   groups,
		expectAll: []service.GroupCapacitySummary{
			{GroupID: 10, ConcurrencyMax: 2, SessionsMax: 3},
			{GroupID: 20, ConcurrencyMax: 5, RPMMax: 9},
			{GroupID: 30, ConcurrencyMax: 6},
		},
	}
}

func (f capacityFixture) get(t *testing.T, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/groups/capacity-summary"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decodeCapacityRows(t *testing.T, rec *httptest.ResponseRecorder) []service.GroupCapacitySummary {
	t.Helper()
	var body struct {
		Code int                            `json:"code"`
		Data []service.GroupCapacitySummary `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.Zero(t, body.Code)
	return body.Data
}

type capacityErrorBody struct {
	Code     int               `json:"code"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
}

func decodeCapacityError(t *testing.T, rec *httptest.ResponseRecorder) capacityErrorBody {
	t.Helper()
	var body capacityErrorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

func TestGetCapacitySummaryWithoutParamsReturnsAllActiveGroups(t *testing.T) {
	f := newCapacityFixture(t, newStubAdminService(), nil)

	rec := f.get(t, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// 不带参数时响应与改前逐字节一致：信封 + 全部启用分组。
	want, err := json.Marshal(response.Response{Code: 0, Message: "success", Data: f.expectAll})
	require.NoError(t, err)
	require.JSONEq(t, string(want), rec.Body.String())
	require.Equal(t, string(want), strings.TrimSpace(rec.Body.String()))
	require.Zero(t, f.groups.existsCalls)

	for _, q := range []string{"group_id=", "group_ids=", "group_ids=,", "group_id=&group_ids=%20,%20"} {
		again := f.get(t, q)
		require.Equal(t, http.StatusOK, again.Code, q)
		require.Equal(t, rec.Body.String(), again.Body.String(), q)
	}
	require.Zero(t, f.groups.existsCalls)
}

func TestGetCapacitySummaryGroupIDFiltersToSingleRow(t *testing.T) {
	f := newCapacityFixture(t, newStubAdminService(), nil)

	rec := f.get(t, "group_id=20")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []service.GroupCapacitySummary{f.expectAll[1]}, decodeCapacityRows(t, rec))
	require.Equal(t, []int64{20}, f.accounts.requested)
}

func TestGetCapacitySummaryGroupIDsReturnsRequestedRows(t *testing.T) {
	for _, q := range []string{
		"group_ids=30,10",
		"group_ids=10&group_ids=30",
		"group_id=10&group_ids=30",
		"group_id=30&group_id=10&group_ids=%2010%20,,30",
	} {
		f := newCapacityFixture(t, newStubAdminService(), nil)
		rec := f.get(t, q)
		require.Equal(t, http.StatusOK, rec.Code, q+": "+rec.Body.String())
		require.Equal(t, []service.GroupCapacitySummary{f.expectAll[0], f.expectAll[2]}, decodeCapacityRows(t, rec), q)
		require.Equal(t, []int64{10, 30}, f.accounts.requested, q)
	}
}

func TestGetCapacitySummaryInactiveGroupReturnsEmptyArray(t *testing.T) {
	f := newCapacityFixture(t, newStubAdminService(), nil)

	rec := f.get(t, "group_id=40")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"data":[]`)
	require.Zero(t, f.accounts.calls())
}

func TestGetCapacitySummaryInvalidGroupIDReturns400(t *testing.T) {
	ids := make([]string, 0, service.MaxCapacitySummaryGroupIDs+1)
	for i := 1; i <= service.MaxCapacitySummaryGroupIDs+1; i++ {
		ids = append(ids, strconv.Itoa(i))
	}
	cases := []struct {
		query  string
		reason string
	}{
		{"group_id=abc", "INVALID_GROUP_ID"},
		{"group_id=0", "INVALID_GROUP_ID"},
		{"group_id=-1", "INVALID_GROUP_ID"},
		{"group_id=1,2", "INVALID_GROUP_ID"},
		{"group_id=1.5", "INVALID_GROUP_ID"},
		{"group_id=99999999999999999999", "INVALID_GROUP_ID"},
		{"group_ids=1,x", "INVALID_GROUP_ID"},
		{"group_ids=0", "INVALID_GROUP_ID"},
		{"group_id=10&group_ids=-3", "INVALID_GROUP_ID"},
		{"group_ids=" + strings.Join(ids, ","), "TOO_MANY_IDS"},
	}
	for _, tc := range cases {
		f := newCapacityFixture(t, newStubAdminService(), nil)
		rec := f.get(t, tc.query)
		require.Equal(t, http.StatusBadRequest, rec.Code, tc.query)
		require.Equal(t, tc.reason, decodeCapacityError(t, rec).Reason, tc.query)
		require.Zero(t, f.accounts.calls(), tc.query)
		require.Zero(t, f.groups.existsCalls, tc.query)
		require.Zero(t, f.groups.listCalls, tc.query)
	}

	// 去重后正好 100 个不超限（重复项不计数）。
	f := newCapacityFixture(t, newStubAdminService(), nil)
	atLimit := append(append([]string(nil), ids[:service.MaxCapacitySummaryGroupIDs]...), "1", "2")
	rec := f.get(t, "group_ids="+strings.Join(atLimit, ","))
	require.NotEqual(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestGetCapacitySummaryUnknownGroupReturns404(t *testing.T) {
	f := newCapacityFixture(t, newStubAdminService(), nil)

	rec := f.get(t, "group_ids=10,999")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	body := decodeCapacityError(t, rec)
	require.Equal(t, "GROUP_NOT_FOUND", body.Reason)
	require.Equal(t, "999", body.Metadata["group_id"])
	require.Zero(t, f.accounts.calls())
}

func TestGetCapacitySummaryVendorUngrantedGroupReturns404WithoutExistenceCheck(t *testing.T) {
	admin := &scopedCapacityAdminStub{stubAdminService: newStubAdminService(), granted: map[int64]bool{10: true}}
	scope := service.VendorScope(7, service.WorkspacePermissions{GroupOps: true})
	f := newCapacityFixture(t, admin, &scope)

	for _, q := range []string{"group_id=20", "group_ids=10,20", "group_id=999"} {
		rec := f.get(t, q)
		require.Equal(t, http.StatusNotFound, rec.Code, q)
		body := decodeCapacityError(t, rec)
		require.Equal(t, "RESOURCE_NOT_FOUND", body.Reason, q)
		require.Empty(t, body.Metadata, q)
	}
	require.Zero(t, f.groups.existsCalls)
	require.Zero(t, f.groups.listCalls)
	require.Zero(t, f.accounts.calls())
}

func TestGetCapacitySummaryVendorGrantedGroupUsesScopedCapacity(t *testing.T) {
	admin := &scopedCapacityAdminStub{stubAdminService: newStubAdminService(), granted: map[int64]bool{10: true, 20: true}}
	scope := service.VendorScope(7, service.WorkspacePermissions{GroupOps: true})
	f := newCapacityFixture(t, admin, &scope)

	rec := f.get(t, "group_id=20")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []service.GroupCapacitySummary{f.expectAll[1]}, decodeCapacityRows(t, rec))
	require.Equal(t, 1, f.accounts.scopedCalls)
	require.Equal(t, int64(7), f.accounts.scopedWorkspaceID)
	require.Zero(t, f.accounts.unscopedCalls)

	// 不带参数仍是原来的全量 + 行过滤。
	all := f.get(t, "")
	require.Equal(t, http.StatusOK, all.Code, all.Body.String())
	require.Equal(t, f.expectAll[:2], decodeCapacityRows(t, all))
}
