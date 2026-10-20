package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// runtimeBatchAdminStub scopes and resolves accounts from a fixed table and
// records the IDs each step received.
type runtimeBatchAdminStub struct {
	*stubAdminService
	accountsByID map[int64]*service.Account
	denied       map[int64]bool
	filterCalls  [][]int64
	getByIDCalls [][]int64
}

func newRuntimeBatchAdminStub(accounts ...service.Account) *runtimeBatchAdminStub {
	stub := &runtimeBatchAdminStub{
		stubAdminService: newStubAdminService(),
		accountsByID:     make(map[int64]*service.Account, len(accounts)),
		denied:           map[int64]bool{},
	}
	for i := range accounts {
		account := accounts[i]
		stub.accountsByID[account.ID] = &account
	}
	return stub
}

func (s *runtimeBatchAdminStub) FilterAccountIDsByScope(_ context.Context, ids []int64) ([]int64, error) {
	s.filterCalls = append(s.filterCalls, append([]int64(nil), ids...))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !s.denied[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *runtimeBatchAdminStub) GetAccountsByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	s.getByIDCalls = append(s.getByIDCalls, append([]int64(nil), ids...))
	out := make([]*service.Account, 0, len(ids))
	// Return in reverse order: the handler must not depend on repository order.
	for i := len(ids) - 1; i >= 0; i-- {
		if account, ok := s.accountsByID[ids[i]]; ok {
			out = append(out, account)
		}
	}
	return out, nil
}

// runtimeConcurrencyCacheStub answers only the batch read used by admin views.
type runtimeConcurrencyCacheStub struct {
	service.ConcurrencyCache
	counts map[int64]int
	err    error
	calls  int
}

func (s *runtimeConcurrencyCacheStub) GetAccountConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = s.counts[id]
	}
	return out, nil
}

// runtimeProbeConcurrencyCacheStub also reads the probe slots, which are a
// subset of the ordinary count and are split out for admin views.
type runtimeProbeConcurrencyCacheStub struct {
	*runtimeConcurrencyCacheStub
	probeCounts map[int64]int
	probeErr    error
	probeCalls  int
}

func (s *runtimeProbeConcurrencyCacheStub) TrackProbeAccountSlot(context.Context, int64, string) error {
	return nil
}

func (s *runtimeProbeConcurrencyCacheStub) ReleaseProbeAccountSlot(context.Context, int64, string) error {
	return nil
}

func (s *runtimeProbeConcurrencyCacheStub) GetProbeAccountConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	s.probeCalls++
	if s.probeErr != nil {
		return nil, s.probeErr
	}
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = s.probeCounts[id]
	}
	return out, nil
}

var _ service.ProbeConcurrencyCache = (*runtimeProbeConcurrencyCacheStub)(nil)

type runtimeWindowStatsRepo struct {
	service.UsageLogRepository
	mu    sync.Mutex
	calls []int64
	err   error
	cost  float64
}

func (r *runtimeWindowStatsRepo) GetAccountWindowStats(_ context.Context, accountID int64, _ time.Time) (*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, accountID)
	if r.err != nil {
		return nil, r.err
	}
	return &usagestats.AccountStats{StandardCost: r.cost}, nil
}

func (r *runtimeWindowStatsRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type runtimeSessionCacheStub struct {
	service.SessionLimitCache
	counts map[int64]int
	calls  [][]int64
}

func (s *runtimeSessionCacheStub) GetActiveSessionCountBatch(_ context.Context, ids []int64, _ map[int64]time.Duration) (map[int64]int, error) {
	s.calls = append(s.calls, append([]int64(nil), ids...))
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		if count, ok := s.counts[id]; ok {
			out[id] = count
		}
	}
	return out, nil
}

type runtimeBatchEnv struct {
	router  *gin.Engine
	admin   *runtimeBatchAdminStub
	rpm     *accountRuntimeRPMCacheStub
	conc    *runtimeConcurrencyCacheStub
	windows *runtimeWindowStatsRepo
	session *runtimeSessionCacheStub
}

func newRuntimeBatchEnv(accounts ...service.Account) *runtimeBatchEnv {
	gin.SetMode(gin.TestMode)
	env := &runtimeBatchEnv{
		admin:   newRuntimeBatchAdminStub(accounts...),
		rpm:     &accountRuntimeRPMCacheStub{counts: map[int64]int{}, schedulingCounts: map[int64]int{}},
		conc:    &runtimeConcurrencyCacheStub{counts: map[int64]int{}},
		windows: &runtimeWindowStatsRepo{cost: 12.5},
		session: &runtimeSessionCacheStub{counts: map[int64]int{}},
	}
	usage := service.NewAccountUsageService(nil, env.windows, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := NewAccountHandler(env.admin, nil, nil, nil, nil, nil, nil, usage, nil,
		service.NewConcurrencyService(env.conc), nil, env.session, env.rpm, nil)
	env.router = gin.New()
	env.router.POST("/api/v1/admin/accounts/runtime/batch", handler.GetBatchRuntime)
	return env
}

func (e *runtimeBatchEnv) post(t *testing.T, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/runtime/batch"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	e.router.ServeHTTP(rec, req)
	return rec
}

type runtimeBatchResponse struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
	Data   struct {
		SampledAt string                    `json:"sampled_at"`
		Items     map[string]map[string]any `json:"items"`
	} `json:"data"`
	Metadata map[string]string `json:"metadata"`
}

func decodeRuntimeBatch(t *testing.T, rec *httptest.ResponseRecorder) runtimeBatchResponse {
	t.Helper()
	var out runtimeBatchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

func TestAccountRuntimeBatchReturnsOneItemPerRequestedAccount(t *testing.T) {
	resetAt := time.Date(2026, 10, 17, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	overload := time.Date(2026, 10, 17, 9, 45, 0, 0, time.UTC)
	tempUntil := time.Date(2026, 10, 17, 9, 50, 0, 0, time.UTC)
	env := newRuntimeBatchEnv(
		service.Account{ID: 11, Name: "a", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, RateLimitResetAt: &resetAt},
		service.Account{ID: 12, Name: "b", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusError, Schedulable: false, OverloadUntil: &overload},
		service.Account{ID: 13, Name: "c", Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, TempUnschedulableUntil: &tempUntil},
	)
	env.conc.counts = map[int64]int{11: 3}
	env.rpm.counts = map[int64]int{11: 42, 12: 7, 13: 0}
	env.rpm.schedulingCounts = map[int64]int{11: 40, 12: 6, 13: 0}

	before := time.Now().UTC().Add(-time.Second)
	rec := env.post(t, "", `{"account_ids":[13,11,12,11]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeRuntimeBatch(t, rec)

	sampledAt, err := time.Parse(time.RFC3339, out.Data.SampledAt)
	require.NoError(t, err)
	require.False(t, sampledAt.Before(before.Truncate(time.Second)))
	require.True(t, strings.HasSuffix(out.Data.SampledAt, "Z"), "sampled_at must be UTC")

	require.Len(t, out.Data.Items, 3)
	first := out.Data.Items["11"]
	require.Equal(t, float64(3), first["current_concurrency"])
	require.Equal(t, float64(0), first["probe_concurrency"])
	require.Equal(t, true, first["concurrency_available"])
	require.Equal(t, float64(42), first["current_rpm"])
	require.Equal(t, float64(40), first["scheduling_rpm"])
	require.Equal(t, "active", first["status"])
	require.Equal(t, true, first["schedulable"])
	require.Equal(t, "2026-10-17T02:00:00Z", first["rate_limit_reset_at"], "times are reported in UTC")
	require.Contains(t, first, "overload_until")
	require.Nil(t, first["overload_until"])
	require.Nil(t, first["temp_unschedulable_until"])
	require.NotContains(t, first, "current_window_cost", "not an Anthropic OAuth account")
	require.NotContains(t, first, "active_sessions")

	second := out.Data.Items["12"]
	require.Equal(t, float64(0), second["current_concurrency"], "a real zero from a successful read")
	require.Equal(t, true, second["concurrency_available"])
	require.Equal(t, "error", second["status"])
	require.Equal(t, false, second["schedulable"])
	require.Equal(t, "2026-10-17T09:45:00Z", second["overload_until"])

	third := out.Data.Items["13"]
	require.Equal(t, float64(0), third["current_rpm"])
	require.Equal(t, "2026-10-17T09:50:00Z", third["temp_unschedulable_until"])

	// One batched read per counter, over the de-duplicated, sorted IDs.
	require.Equal(t, [][]int64{{11, 12, 13}}, env.admin.filterCalls)
	require.Equal(t, [][]int64{{11, 12, 13}}, env.admin.getByIDCalls)
	require.Equal(t, [][]int64{{11, 12, 13}}, env.rpm.batchCalls)
	require.Equal(t, [][]int64{{11, 12, 13}}, env.rpm.rollingBatchCalls)
	require.Empty(t, env.rpm.getIDs, "no per-account RPM reads")
	require.Equal(t, 1, env.conc.calls)
}

func TestAccountRuntimeBatchConcurrencyReadFailureIsUnavailable(t *testing.T) {
	env := newRuntimeBatchEnv(
		service.Account{ID: 21, Name: "a", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true},
		service.Account{ID: 22, Name: "b", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true},
	)
	env.conc.err = errors.New("redis down")
	env.rpm.counts = map[int64]int{21: 5, 22: 6}

	rec := env.post(t, "", `{"account_ids":[21,22]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"current_concurrency":null`)
	require.Contains(t, rec.Body.String(), `"probe_concurrency":null`)
	require.NotContains(t, rec.Body.String(), `"current_concurrency":0`)
	out := decodeRuntimeBatch(t, rec)
	require.Len(t, out.Data.Items, 2)
	for id, item := range out.Data.Items {
		require.Equal(t, false, item["concurrency_available"], id)
		require.Contains(t, item, "current_concurrency", id)
		require.Nil(t, item["current_concurrency"], id)
		require.NotNil(t, item["current_rpm"], "other counters still report: %s", id)
	}

	// A handler without a concurrency counter reports the same way.
	gin.SetMode(gin.TestMode)
	bare := &AccountHandler{adminService: env.admin}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/runtime/batch", bare.GetBatchRuntime)
	bareRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/runtime/batch", strings.NewReader(`{"account_ids":[21]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(bareRec, req)
	require.Equal(t, http.StatusOK, bareRec.Code, bareRec.Body.String())
	bareOut := decodeRuntimeBatch(t, bareRec)
	require.Equal(t, false, bareOut.Data.Items["21"]["concurrency_available"])
	require.Nil(t, bareOut.Data.Items["21"]["current_concurrency"])
	require.NotContains(t, bareOut.Data.Items["21"], "current_rpm")
}

// The probe slots are read separately from the ordinary ones. When only that
// second read fails, the split is unknown: neither probe_concurrency=0 nor the
// raw ordinary count (which still includes the probe slots) may be reported.
func TestAccountRuntimeBatchProbeReadFailureIsUnavailable(t *testing.T) {
	accounts := []service.Account{
		{ID: 61, Name: "a", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true},
		{ID: 62, Name: "b", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true},
	}
	gin.SetMode(gin.TestMode)
	admin := newRuntimeBatchAdminStub(accounts...)
	adminSvc := newStubAdminService()
	adminSvc.accounts = accounts
	adminSvc.getAccountResult = &accounts[0]
	rpm := &accountRuntimeRPMCacheStub{counts: map[int64]int{61: 9, 62: 4}, schedulingCounts: map[int64]int{}}
	conc := &runtimeProbeConcurrencyCacheStub{
		runtimeConcurrencyCacheStub: &runtimeConcurrencyCacheStub{counts: map[int64]int{61: 5, 62: 1}},
		probeCounts:                 map[int64]int{61: 2},
	}
	concurrency := service.NewConcurrencyService(conc)
	router := gin.New()
	batch := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, concurrency, nil, nil, rpm, nil)
	router.POST("/api/v1/admin/accounts/runtime/batch", batch.GetBatchRuntime)
	views := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, concurrency, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts", views.List)
	router.GET("/api/v1/admin/accounts/:id", views.GetByID)

	postBatch := func() runtimeBatchResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/runtime/batch", strings.NewReader(`{"account_ids":[61,62]}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeRuntimeBatch(t, rec)
	}
	getJSON := func(url string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var payload struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		return payload.Data
	}
	listFirst := func(url string) map[string]any {
		t.Helper()
		items, ok := getJSON(url)["items"].([]any)
		require.True(t, ok, url)
		require.Len(t, items, 2, url)
		first, ok := items[0].(map[string]any)
		require.True(t, ok, url)
		return first
	}

	// Both reads succeed: probe slots are split out of the ordinary count.
	out := postBatch()
	require.Equal(t, float64(3), out.Data.Items["61"]["current_concurrency"])
	require.Equal(t, float64(2), out.Data.Items["61"]["probe_concurrency"])
	require.Equal(t, true, out.Data.Items["61"]["concurrency_available"])
	require.Equal(t, float64(1), out.Data.Items["62"]["current_concurrency"])
	require.Equal(t, float64(0), out.Data.Items["62"]["probe_concurrency"], "a real zero from a successful probe read")
	require.Equal(t, 1, conc.probeCalls)

	// Only the probe read fails.
	conc.probeErr = errors.New("redis TIME: i/o timeout")
	out = postBatch()
	require.Len(t, out.Data.Items, 2)
	for id, item := range out.Data.Items {
		require.Equal(t, false, item["concurrency_available"], id)
		require.Contains(t, item, "current_concurrency", id)
		require.Nil(t, item["current_concurrency"], "no raw count that still includes probe slots: %s", id)
		require.Contains(t, item, "probe_concurrency", id)
		require.Nil(t, item["probe_concurrency"], "no fake probe 0: %s", id)
		require.NotNil(t, item["current_rpm"], "other counters still report: %s", id)
	}

	// The list (full and lite) and the detail keep int placeholders for the
	// Web but no longer claim the counts were observed.
	for _, url := range []string{"/api/v1/admin/accounts?page=1&page_size=20", "/api/v1/admin/accounts?page=1&page_size=20&lite=1"} {
		first := listFirst(url)
		require.Equal(t, false, first["concurrency_available"], url)
		require.Equal(t, float64(0), first["current_concurrency"], url)
	}
	detail := getJSON("/api/v1/admin/accounts/61")
	require.Equal(t, false, detail["concurrency_available"])
	require.Equal(t, float64(0), detail["current_concurrency"])
	require.Equal(t, float64(0), detail["probe_concurrency"])
}

func TestAccountRuntimeBatchScopesIDsBeforeReading(t *testing.T) {
	env := newRuntimeBatchEnv(
		service.Account{ID: 11, Name: "a", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive},
		service.Account{ID: 12, Name: "b", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive},
		service.Account{ID: 13, Name: "other-workspace", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive},
	)
	env.admin.denied[13] = true

	rec := env.post(t, "", `{"account_ids":[11,12,13]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeRuntimeBatch(t, rec)
	require.Len(t, out.Data.Items, 2)
	require.NotContains(t, out.Data.Items, "13")
	require.Equal(t, [][]int64{{11, 12}}, env.admin.getByIDCalls)
	require.Equal(t, [][]int64{{11, 12}}, env.rpm.batchCalls)

	// Every ID out of scope: 200 with no items and no counter reads.
	env.admin.denied[11], env.admin.denied[12] = true, true
	env.rpm.batchCalls, env.conc.calls = nil, 0
	rec = env.post(t, "", `{"account_ids":[11,12,13]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"items":{}`)
	require.Len(t, env.admin.getByIDCalls, 1, "nothing left to load")
	require.Empty(t, env.rpm.batchCalls)
	require.Zero(t, env.conc.calls)

	// IDs that do not exist simply do not appear.
	env.admin.denied = map[int64]bool{}
	rec = env.post(t, "", `{"account_ids":[11,999]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	out = decodeRuntimeBatch(t, rec)
	require.Len(t, out.Data.Items, 1)
	require.Contains(t, out.Data.Items, "11")
}

func TestAccountRuntimeBatchRejectsMoreThan200Accounts(t *testing.T) {
	env := newRuntimeBatchEnv()
	// Literal 200, not service.MaxAccountRuntimeBatchIDs: a silent change must fail here.
	ids := make([]string, 0, 201)
	for i := 1; i <= 201; i++ {
		ids = append(ids, strconv.Itoa(i))
	}
	rec := env.post(t, "", `{"account_ids":[`+strings.Join(ids, ",")+`]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	out := decodeRuntimeBatch(t, rec)
	require.Equal(t, "TOO_MANY_IDS", out.Reason, "same contract as the other batch reads: split and retry")
	require.Equal(t, "200", out.Metadata["max"])
	require.Equal(t, "account_ids", out.Metadata["field"])
	require.Empty(t, env.admin.filterCalls, "rejected before any lookup")

	// 200 distinct IDs plus duplicates and non-positive IDs stay within the cap.
	within := append(append([]string(nil), ids[:200]...), "1", "2", "0", "-5")
	rec = env.post(t, "", `{"account_ids":[`+strings.Join(within, ",")+`]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, env.admin.filterCalls, 1)
	require.Len(t, env.admin.filterCalls[0], 200)

	rec = env.post(t, "", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "account_ids is required")

	rec = env.post(t, "", `{"account_ids":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"items":{}`)
	require.Len(t, env.admin.filterCalls, 1, "an empty list needs no scope lookup")
}

func TestAccountRuntimeBatchWindowCostOptional(t *testing.T) {
	oauth := service.Account{
		ID: 31, Name: "claude", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true,
		Extra: map[string]any{"window_cost_limit": 50.0, "max_sessions": 3},
	}
	apiKey := service.Account{
		ID: 32, Name: "claude-key", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Extra: map[string]any{"window_cost_limit": 50.0, "max_sessions": 3},
	}
	env := newRuntimeBatchEnv(oauth, apiKey)
	env.session.counts = map[int64]int{31: 2}

	rec := env.post(t, "", `{"account_ids":[31,32]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeRuntimeBatch(t, rec)
	require.Equal(t, 12.5, out.Data.Items["31"]["current_window_cost"])
	require.Equal(t, float64(2), out.Data.Items["31"]["active_sessions"])
	require.NotContains(t, out.Data.Items["32"], "current_window_cost", "only Anthropic OAuth / Setup Token accounts")
	require.NotContains(t, out.Data.Items["32"], "active_sessions")
	require.Equal(t, 1, env.windows.callCount())
	require.Equal(t, [][]int64{{31}}, env.session.calls)

	rec = env.post(t, "?include_window_cost=0", `{"account_ids":[31,32]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out = decodeRuntimeBatch(t, rec)
	require.NotContains(t, out.Data.Items["31"], "current_window_cost")
	require.Equal(t, float64(2), out.Data.Items["31"]["active_sessions"], "sessions are cheap and still sampled")
	require.Equal(t, 1, env.windows.callCount(), "no window SQL when skipped")

	env.windows.err = errors.New("stats timeout")
	rec = env.post(t, "", `{"account_ids":[31]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out = decodeRuntimeBatch(t, rec)
	require.NotContains(t, out.Data.Items["31"], "current_window_cost", "a failed read is omitted, not 0")
	require.Equal(t, true, out.Data.Items["31"]["concurrency_available"])
	require.Equal(t, 2, env.windows.callCount())

	// Without a usage service the window cost is simply unavailable.
	gin.SetMode(gin.TestMode)
	bare := &AccountHandler{adminService: env.admin}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/runtime/batch", bare.GetBatchRuntime)
	bareRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/runtime/batch", strings.NewReader(`{"account_ids":[31]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(bareRec, req)
	require.Equal(t, http.StatusOK, bareRec.Code, bareRec.Body.String())
	require.NotContains(t, bareRec.Body.String(), "current_window_cost")
}

func TestAccountRuntimeBatchSkipsAudit(t *testing.T) {
	env := newRuntimeBatchEnv(service.Account{ID: 41, Name: "a", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	usage := service.NewAccountUsageService(nil, env.windows, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := NewAccountHandler(env.admin, nil, nil, nil, nil, nil, nil, usage, nil, nil, nil, nil, env.rpm, nil)
	var skipped bool
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		skipped = c.GetBool("audit_skip")
	})
	router.POST("/api/v1/admin/accounts/runtime/batch", handler.GetBatchRuntime)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/runtime/batch", strings.NewReader(`{"account_ids":[41]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, skipped, "a read-only poll must not write an audit entry")
}

func TestAccountListAndDetailReportConcurrencyAvailable(t *testing.T) {
	accounts := []service.Account{
		{ID: 51, Name: "a", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive},
		{ID: 52, Name: "b", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive},
	}
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.accounts = accounts
	adminSvc.getAccountResult = &accounts[1]
	conc := &runtimeConcurrencyCacheStub{counts: map[int64]int{51: 2}}
	newRouter := func(concurrency *service.ConcurrencyService) *gin.Engine {
		handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, concurrency, nil, nil, nil, nil)
		router := gin.New()
		router.GET("/api/v1/admin/accounts", handler.List)
		router.GET("/api/v1/admin/accounts/:id", handler.GetByID)
		return router
	}
	listURLs := []string{"/api/v1/admin/accounts?page=1&page_size=20", "/api/v1/admin/accounts?page=1&page_size=20&lite=1"}

	check := func(router *gin.Engine, wantAvailable bool, wantFirst float64) {
		t.Helper()
		for _, url := range listURLs {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var payload struct {
				Data struct {
					Items []map[string]any `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			require.Len(t, payload.Data.Items, 2, url)
			for _, item := range payload.Data.Items {
				require.Equal(t, wantAvailable, item["concurrency_available"], url)
			}
			require.Equal(t, wantFirst, payload.Data.Items[0]["current_concurrency"], "list keeps an int for the Web: %s", url)
			require.Equal(t, float64(0), payload.Data.Items[1]["current_concurrency"], url)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/52", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var detail struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
		require.Equal(t, wantAvailable, detail.Data["concurrency_available"])
		require.Equal(t, float64(0), detail.Data["current_concurrency"])
	}

	check(newRouter(service.NewConcurrencyService(conc)), true, 2)

	conc.err = errors.New("redis down")
	check(newRouter(service.NewConcurrencyService(conc)), false, 0)

	check(newRouter(nil), false, 0)
}

// TestAccountRuntimeBatchItemJSONShape locks which keys are always present
// (null when unknown) and which are omitted when unavailable.
func TestAccountRuntimeBatchItemJSONShape(t *testing.T) {
	item := AccountRuntimeItem{Status: "active"}
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	body := string(raw)
	for _, key := range []string{"current_concurrency", "probe_concurrency", "concurrency_available", "status", "schedulable", "rate_limit_reset_at", "overload_until", "temp_unschedulable_until"} {
		require.Contains(t, body, fmt.Sprintf("%q:", key), "always present: %s", key)
	}
	for _, key := range []string{"current_rpm", "scheduling_rpm", "active_sessions", "current_window_cost"} {
		require.NotContains(t, body, fmt.Sprintf("%q:", key), "omitted when unavailable: %s", key)
	}
}
