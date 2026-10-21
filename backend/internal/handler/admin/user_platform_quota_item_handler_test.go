//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type singleQuotaUpsertCall struct {
	userID int64
	rec    service.UserPlatformQuotaRecord
}

type singleQuotaPlatformCall struct {
	userID   int64
	platform string
}

// singleQuotaRepoFake 只实现单平台三个方法；整表 UpsertForUser 被调用即记一次，用来断言新接口不走整表写。
type singleQuotaRepoFake struct {
	service.UserPlatformQuotaRepository

	upsertOneCalls    []singleQuotaUpsertCall
	upsertOneCurrent  *service.UserPlatformQuotaRecord
	upsertOnePrevious *service.UserPlatformQuotaRecord
	upsertOneErr      error

	deleteCalls    []singleQuotaPlatformCall
	deletePrevious *service.UserPlatformQuotaRecord
	deleteErr      error

	restoreCalls  []singleQuotaPlatformCall
	restoreRecord *service.UserPlatformQuotaRecord
	restoreErr    error

	fullUpsertCalls int
}

func (f *singleQuotaRepoFake) UpsertOneForUser(_ context.Context, userID int64, rec service.UserPlatformQuotaRecord) (*service.UserPlatformQuotaRecord, *service.UserPlatformQuotaRecord, error) {
	f.upsertOneCalls = append(f.upsertOneCalls, singleQuotaUpsertCall{userID: userID, rec: rec})
	return f.upsertOneCurrent, f.upsertOnePrevious, f.upsertOneErr
}

func (f *singleQuotaRepoFake) SoftDeleteOne(_ context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	f.deleteCalls = append(f.deleteCalls, singleQuotaPlatformCall{userID, platform})
	return f.deletePrevious, f.deleteErr
}

func (f *singleQuotaRepoFake) RestoreLatestDeleted(_ context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	f.restoreCalls = append(f.restoreCalls, singleQuotaPlatformCall{userID, platform})
	return f.restoreRecord, f.restoreErr
}

func (f *singleQuotaRepoFake) UpsertForUser(context.Context, int64, []service.UserPlatformQuotaRecord) error {
	f.fullUpsertCalls++
	return nil
}

func (f *singleQuotaRepoFake) writeCalls() int {
	return len(f.upsertOneCalls) + len(f.deleteCalls) + len(f.restoreCalls) + f.fullUpsertCalls
}

func quotaItemRequest(t *testing.T, method, id, platform, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1/admin/users/"+id+"/platform-quotas/"+platform, reader)
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: id}, {Key: "platform", Value: platform}}
	return c, w
}

type quotaItemEnvelope struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Reason  string         `json:"reason"`
	Data    map[string]any `json:"data"`
}

func decodeQuotaItemBody(t *testing.T, w *httptest.ResponseRecorder) quotaItemEnvelope {
	t.Helper()
	var env quotaItemEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	return env
}

func quotaItemLimit(v float64) *float64 { return &v }

// currentDailyWindowStart 返回当天日窗口的起点（与 NeedsDailyReset 同一时区口径），
// 避免“now 减一分钟”在午夜刚过时落到昨天、用量被按过期窗口归零。
func currentDailyWindowStart() time.Time { return timezone.StartOfDay(time.Now()) }

func TestUpsertUserPlatformQuota_UpsertsOnlyThatPlatform(t *testing.T) {
	start := currentDailyWindowStart()
	repo := &singleQuotaRepoFake{
		upsertOneCurrent:  &service.UserPlatformQuotaRecord{UserID: 42, Platform: "anthropic", DailyLimitUSD: quotaItemLimit(20), DailyUsageUSD: 1.5, DailyWindowStart: &start},
		upsertOnePrevious: &service.UserPlatformQuotaRecord{UserID: 42, Platform: "anthropic", DailyLimitUSD: quotaItemLimit(10), DailyUsageUSD: 1.5, DailyWindowStart: &start},
	}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)

	c, w := quotaItemRequest(t, http.MethodPut, "42", "anthropic", `{"daily_limit_usd":20,"monthly_limit_usd":null}`)
	h.UpsertUserPlatformQuota(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, repo.upsertOneCalls, 1)
	call := repo.upsertOneCalls[0]
	require.Equal(t, int64(42), call.userID)
	require.Equal(t, int64(42), call.rec.UserID)
	require.Equal(t, "anthropic", call.rec.Platform)
	require.NotNil(t, call.rec.DailyLimitUSD)
	require.Equal(t, 20.0, *call.rec.DailyLimitUSD)
	require.Nil(t, call.rec.WeeklyLimitUSD, "a missing field means no limit")
	require.Nil(t, call.rec.MonthlyLimitUSD)
	require.Zero(t, repo.fullUpsertCalls, "the whole-table write must not be used")
	require.Equal(t, []deleteCall{{42, "anthropic"}}, cache.deleteCalls, "only this platform's cache is invalidated")

	env := decodeQuotaItemBody(t, w)
	quota, ok := env.Data["platform_quota"].(map[string]any)
	require.True(t, ok, w.Body.String())
	require.Equal(t, "anthropic", quota["platform"])
	require.Equal(t, 20.0, quota["daily_limit_usd"])
	require.Equal(t, 1.5, quota["daily_usage_usd"])
	require.Contains(t, quota, "daily_window_start", "same shape as the admin GET item")
	previous, ok := env.Data["previous"].(map[string]any)
	require.True(t, ok, w.Body.String())
	require.Equal(t, 10.0, previous["daily_limit_usd"])
	require.Equal(t, 1.5, previous["daily_usage_usd"])
}

func TestUpsertUserPlatformQuota_InsertedHasNullPrevious(t *testing.T) {
	repo := &singleQuotaRepoFake{
		upsertOneCurrent: &service.UserPlatformQuotaRecord{UserID: 42, Platform: "zhipu", DailyLimitUSD: quotaItemLimit(0)},
	}
	h := buildTestHandler(repo, &billingCacheStub{})
	c, w := quotaItemRequest(t, http.MethodPut, "42", "zhipu", `{"daily_limit_usd":0}`)
	h.UpsertUserPlatformQuota(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 0.0, *repo.upsertOneCalls[0].rec.DailyLimitUSD, "0 blocks the platform and is kept")
	env := decodeQuotaItemBody(t, w)
	require.Contains(t, env.Data, "previous")
	require.Nil(t, env.Data["previous"])
	require.NotNil(t, env.Data["platform_quota"])
}

func TestUpsertUserPlatformQuota_AllNullReturnsNullQuota(t *testing.T) {
	for _, body := range []string{`{}`, `{"daily_limit_usd":null,"weekly_limit_usd":null,"monthly_limit_usd":null}`} {
		t.Run(body, func(t *testing.T) {
			repo := &singleQuotaRepoFake{
				upsertOnePrevious: &service.UserPlatformQuotaRecord{UserID: 42, Platform: "openai", WeeklyLimitUSD: quotaItemLimit(30)},
			}
			cache := &billingCacheStub{}
			h := buildTestHandler(repo, cache)
			c, w := quotaItemRequest(t, http.MethodPut, "42", "openai", body)
			h.UpsertUserPlatformQuota(c)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Len(t, repo.upsertOneCalls, 1)
			require.False(t, repo.upsertOneCalls[0].rec.HasAnyLimit())
			env := decodeQuotaItemBody(t, w)
			require.Contains(t, env.Data, "platform_quota")
			require.Nil(t, env.Data["platform_quota"])
			previous, ok := env.Data["previous"].(map[string]any)
			require.True(t, ok, w.Body.String())
			require.Equal(t, 30.0, previous["weekly_limit_usd"])
			require.Equal(t, []deleteCall{{42, "openai"}}, cache.deleteCalls)
		})
	}
}

func TestUpsertUserPlatformQuota_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name, id, platform, body string
		reason, message          string
	}{
		{name: "unknown platform", id: "42", platform: "foo", body: `{"daily_limit_usd":1}`, reason: "INVALID_PLATFORM", message: "invalid platform: foo"},
		{name: "composite", id: "42", platform: "composite", body: `{"daily_limit_usd":1}`, reason: "INVALID_PLATFORM", message: "invalid platform: composite"},
		{name: "negative daily", id: "42", platform: "anthropic", body: `{"daily_limit_usd":-1}`, reason: "INVALID_QUOTA_LIMIT", message: "daily_limit_usd must be >= 0"},
		{name: "negative monthly", id: "42", platform: "anthropic", body: `{"weekly_limit_usd":1,"monthly_limit_usd":-0.5}`, reason: "INVALID_QUOTA_LIMIT", message: "monthly_limit_usd must be >= 0"},
		{name: "unknown field", id: "42", platform: "anthropic", body: `{"daily_limit":5}`, reason: "VALIDATION_ERROR"},
		{name: "wrong type", id: "42", platform: "anthropic", body: `{"daily_limit_usd":"5"}`, reason: "VALIDATION_ERROR"},
		{name: "overflow", id: "42", platform: "anthropic", body: `{"daily_limit_usd":1e999}`, reason: "VALIDATION_ERROR"},
		{name: "above column range", id: "42", platform: "anthropic", body: `{"daily_limit_usd":10000000000}`, reason: "INVALID_QUOTA_LIMIT", message: "daily_limit_usd must be less than 10000000000"},
		{name: "far above column range", id: "42", platform: "anthropic", body: `{"weekly_limit_usd":1,"monthly_limit_usd":1e300}`, reason: "INVALID_QUOTA_LIMIT", message: "monthly_limit_usd must be less than 10000000000"},
		{name: "empty body", id: "42", platform: "anthropic", body: ``, reason: "VALIDATION_ERROR"},
		{name: "null body", id: "42", platform: "anthropic", body: `null`, reason: "VALIDATION_ERROR"},
		{name: "array body", id: "42", platform: "anthropic", body: `[]`, reason: "VALIDATION_ERROR"},
		{name: "trailing data", id: "42", platform: "anthropic", body: `{} {}`, reason: "VALIDATION_ERROR"},
		{name: "bad user id", id: "abc", platform: "anthropic", body: `{"daily_limit_usd":1}`, reason: "INVALID_USER_ID", message: "Invalid user ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &singleQuotaRepoFake{}
			cache := &billingCacheStub{}
			h := buildTestHandler(repo, cache)
			c, w := quotaItemRequest(t, http.MethodPut, tc.id, tc.platform, tc.body)
			h.UpsertUserPlatformQuota(c)

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			env := decodeQuotaItemBody(t, w)
			require.Equal(t, tc.reason, env.Reason)
			if tc.message != "" {
				require.Equal(t, tc.message, env.Message)
			}
			require.Zero(t, repo.writeCalls(), "invalid input must not reach the repository")
			require.Empty(t, cache.deleteCalls)
		})
	}
}

func TestValidateQuotaLimitFields(t *testing.T) {
	require.Empty(t, validateQuotaLimitFields(nil, nil, nil))
	require.Empty(t, validateQuotaLimitFields(quotaItemLimit(0), quotaItemLimit(1), nil))
	require.Equal(t, "weekly_limit_usd must be >= 0", validateQuotaLimitFields(nil, quotaItemLimit(-1), nil))
	inf := math.Inf(1)
	require.Equal(t, "daily_limit_usd must be a finite number", validateQuotaLimitFields(&inf, nil, nil))
	nan := math.NaN()
	require.Equal(t, "monthly_limit_usd must be a finite number", validateQuotaLimitFields(nil, nil, &nan))

	// 列类型 decimal(20,10) 只能存绝对值小于 1e10 的数，超出时数据库报 numeric field overflow（500）。
	require.Empty(t, validateQuotaLimitFields(nil, quotaItemLimit(math.Nextafter(1e10, 0)), nil), "largest float64 below 1e10 fits the column")
	require.Equal(t, "weekly_limit_usd must be less than 10000000000", validateQuotaLimitFields(nil, quotaItemLimit(1e10), nil))
}

func TestUpsertUserPlatformQuota_UserNotFoundAndNoRepo(t *testing.T) {
	repo := &singleQuotaRepoFake{}
	adminSvc := newStubAdminService()
	adminSvc.getUserErr = service.ErrUserNotFound
	h := &UserHandler{userPlatformQuotaRepo: repo, billingCache: &billingCacheStub{}, adminService: adminSvc}
	c, w := quotaItemRequest(t, http.MethodPut, "42", "anthropic", `{"daily_limit_usd":1}`)
	h.UpsertUserPlatformQuota(c)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Equal(t, "USER_NOT_FOUND", decodeQuotaItemBody(t, w).Reason)
	require.Zero(t, repo.writeCalls())

	h = &UserHandler{adminService: newStubAdminService()}
	for _, run := range []struct {
		method string
		call   func(*gin.Context)
	}{
		{http.MethodPut, h.UpsertUserPlatformQuota},
		{http.MethodDelete, h.DeleteUserPlatformQuota},
		{http.MethodPost, h.RestoreUserPlatformQuota},
	} {
		c, w = quotaItemRequest(t, run.method, "42", "anthropic", `{"daily_limit_usd":1}`)
		run.call(c)
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		env := decodeQuotaItemBody(t, w)
		require.Equal(t, "SERVICE_UNAVAILABLE", env.Reason)
		require.Equal(t, "platform quota service not available", env.Message)
	}
}

func TestUpsertUserPlatformQuota_RepoFailure(t *testing.T) {
	repo := &singleQuotaRepoFake{upsertOneErr: errors.New("db down")}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)
	c, w := quotaItemRequest(t, http.MethodPut, "42", "anthropic", `{"daily_limit_usd":1}`)
	h.UpsertUserPlatformQuota(c)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	require.Empty(t, cache.deleteCalls, "nothing was written, nothing to invalidate")
}

func TestDeleteUserPlatformQuota_RemovesAndReturnsPrevious(t *testing.T) {
	repo := &singleQuotaRepoFake{
		deletePrevious: &service.UserPlatformQuotaRecord{UserID: 42, Platform: "zhipu", DailyLimitUSD: quotaItemLimit(5), DailyUsageUSD: 3.5},
	}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)
	c, w := quotaItemRequest(t, http.MethodDelete, "42", "zhipu", "")
	h.DeleteUserPlatformQuota(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []singleQuotaPlatformCall{{42, "zhipu"}}, repo.deleteCalls)
	require.Equal(t, []deleteCall{{42, "zhipu"}}, cache.deleteCalls)
	env := decodeQuotaItemBody(t, w)
	require.Equal(t, true, env.Data["removed"])
	previous, ok := env.Data["previous"].(map[string]any)
	require.True(t, ok, w.Body.String())
	require.Equal(t, "zhipu", previous["platform"])
	require.Equal(t, 5.0, previous["daily_limit_usd"])
	require.Equal(t, 3.5, previous["daily_usage_usd"])
}

func TestDeleteUserPlatformQuota_NotFound(t *testing.T) {
	repo := &singleQuotaRepoFake{deleteErr: fmt.Errorf("%w: %w", service.ErrUserPlatformQuotaNotFound, errors.New("repo sentinel"))}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)
	c, w := quotaItemRequest(t, http.MethodDelete, "42", "zhipu", "")
	h.DeleteUserPlatformQuota(c)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	env := decodeQuotaItemBody(t, w)
	require.Equal(t, "USER_PLATFORM_QUOTA_NOT_FOUND", env.Reason)
	require.Equal(t, "user platform quota not found", env.Message)
	require.Empty(t, cache.deleteCalls)
}

func TestDeleteAndRestoreUserPlatformQuota_RejectInvalidPath(t *testing.T) {
	for _, run := range []struct {
		name   string
		method string
		call   func(*UserHandler) func(*gin.Context)
	}{
		{"delete", http.MethodDelete, func(h *UserHandler) func(*gin.Context) { return h.DeleteUserPlatformQuota }},
		{"restore", http.MethodPost, func(h *UserHandler) func(*gin.Context) { return h.RestoreUserPlatformQuota }},
	} {
		t.Run(run.name, func(t *testing.T) {
			repo := &singleQuotaRepoFake{}
			h := buildTestHandler(repo, &billingCacheStub{})
			c, w := quotaItemRequest(t, run.method, "42", "foo", "")
			run.call(h)(c)
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Equal(t, "INVALID_PLATFORM", decodeQuotaItemBody(t, w).Reason)

			c, w = quotaItemRequest(t, run.method, "x", "anthropic", "")
			run.call(h)(c)
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Equal(t, "INVALID_USER_ID", decodeQuotaItemBody(t, w).Reason)

			adminSvc := newStubAdminService()
			adminSvc.getUserErr = service.ErrUserNotFound
			h.adminService = adminSvc
			c, w = quotaItemRequest(t, run.method, "42", "anthropic", "")
			run.call(h)(c)
			require.Equal(t, http.StatusNotFound, w.Code)
			require.Equal(t, "USER_NOT_FOUND", decodeQuotaItemBody(t, w).Reason)
			require.Zero(t, repo.writeCalls())
		})
	}
}

func TestRestoreUserPlatformQuota_Responses(t *testing.T) {
	start := currentDailyWindowStart()
	repo := &singleQuotaRepoFake{
		restoreRecord: &service.UserPlatformQuotaRecord{UserID: 42, Platform: "anthropic", DailyLimitUSD: quotaItemLimit(10), DailyUsageUSD: 7.5, DailyWindowStart: &start},
	}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)
	c, w := quotaItemRequest(t, http.MethodPost, "42", "anthropic", "")
	h.RestoreUserPlatformQuota(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []singleQuotaPlatformCall{{42, "anthropic"}}, repo.restoreCalls)
	require.Equal(t, []deleteCall{{42, "anthropic"}}, cache.deleteCalls)
	env := decodeQuotaItemBody(t, w)
	require.Equal(t, true, env.Data["restored"])
	quota, ok := env.Data["platform_quota"].(map[string]any)
	require.True(t, ok, w.Body.String())
	require.Equal(t, 7.5, quota["daily_usage_usd"])
	require.Equal(t, 10.0, quota["daily_limit_usd"])

	for _, tc := range []struct {
		err    error
		status int
		reason string
	}{
		{fmt.Errorf("%w: %w", service.ErrUserPlatformQuotaActiveExists, errors.New("repo")), http.StatusConflict, "USER_PLATFORM_QUOTA_ACTIVE_EXISTS"},
		{fmt.Errorf("%w: %w", service.ErrUserPlatformQuotaNotFound, errors.New("repo")), http.StatusNotFound, "USER_PLATFORM_QUOTA_NOT_FOUND"},
	} {
		repo := &singleQuotaRepoFake{restoreErr: tc.err}
		cache := &billingCacheStub{}
		h := buildTestHandler(repo, cache)
		c, w := quotaItemRequest(t, http.MethodPost, "42", "anthropic", "")
		h.RestoreUserPlatformQuota(c)
		require.Equal(t, tc.status, w.Code, w.Body.String())
		require.Equal(t, tc.reason, decodeQuotaItemBody(t, w).Reason)
		require.Empty(t, cache.deleteCalls)
	}
}

func TestResetUserPlatformQuotaWindow_NotFoundCarriesReason(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{resetErr: fmt.Errorf("%w: %w", service.ErrUserPlatformQuotaNotFound, errors.New("repo"))}
	h := buildTestHandler(repo, &billingCacheStub{})
	c, w := postReq(t, `{"platform":"anthropic","window":"daily"}`)
	h.ResetUserPlatformQuotaWindow(c)
	require.Equal(t, http.StatusNotFound, w.Code)
	env := decodeQuotaItemBody(t, w)
	require.Equal(t, "user platform quota not found", env.Message, "message unchanged")
	require.Equal(t, "USER_PLATFORM_QUOTA_NOT_FOUND", env.Reason)
}
