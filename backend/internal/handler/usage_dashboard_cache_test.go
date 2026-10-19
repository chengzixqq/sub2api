package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardSummaryRepo struct {
	service.UsageLogRepository
	mu       sync.Mutex
	value    int64
	calls    int
	keyCalls int
	err      error
	block    <-chan struct{}
}

func (r *dashboardSummaryRepo) GetUserDashboardStats(ctx context.Context, userID int64) (*usagestats.UserDashboardStats, error) {
	r.mu.Lock()
	r.calls++
	value, err, block := r.value, r.err, r.block
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &usagestats.UserDashboardStats{TotalRequests: value + userID}, err
}

func (r *dashboardSummaryRepo) GetBatchAPIKeyUsageStats(_ context.Context, ids []int64, _, _ time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyCalls++
	stats := make(map[int64]*usagestats.BatchAPIKeyUsageStats, len(ids))
	for _, id := range ids {
		stats[id] = &usagestats.BatchAPIKeyUsageStats{TotalActualCost: float64(r.value)}
	}
	return stats, r.err
}

type dashboardOwnershipRepo struct {
	service.APIKeyRepository
	owners map[int64]int64
	calls  int
}

func (r *dashboardOwnershipRepo) VerifyOwnership(_ context.Context, userID int64, ids []int64) ([]int64, error) {
	r.calls++
	var valid []int64
	for _, id := range ids {
		if r.owners[id] == userID {
			valid = append(valid, id)
		}
	}
	return valid, nil
}

func summaryTestRouter(t *testing.T, repo *dashboardSummaryRepo, keys *dashboardOwnershipRepo) *gin.Engine {
	t.Helper()
	previous := userUsageCache
	userUsageCache = usagequery.NewCache(30 * time.Second)
	t.Cleanup(func() { userUsageCache = previous })
	gin.SetMode(gin.TestMode)
	h := NewUsageHandler(service.NewUsageService(repo, nil, nil, nil), service.NewAPIKeyService(keys, nil, nil, nil, nil, nil, nil), nil, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.GetHeader("X-Test-User"), 10, 64)
		if id == 0 {
			id = 42
		}
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: id})
		c.Next()
	})
	r.GET("/stats", h.DashboardStats)
	r.POST("/keys", h.DashboardAPIKeysUsage)
	return r
}

func summaryRequest(r http.Handler, method, url, body string, user int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("X-Test-User", strconv.FormatInt(user, 10))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestDashboardSummaryCache_IdentityRefreshAndFailures(t *testing.T) {
	repo := &dashboardSummaryRepo{value: 1}
	r := summaryTestRouter(t, repo, &dashboardOwnershipRepo{})
	get := func(url string, user int64) int64 {
		w := summaryRequest(r, http.MethodGet, url, "", user)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var p struct {
			Data struct {
				Requests int64 `json:"total_requests"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
		return p.Data.Requests
	}
	require.Equal(t, int64(43), get("/stats", 42))
	repo.value = 2
	require.Equal(t, int64(43), get("/stats", 42))
	require.Equal(t, int64(44), get("/stats?force_refresh=true", 42))
	repo.value = 3
	require.Equal(t, int64(45), get("/stats?nocache=true", 42))
	require.Equal(t, int64(45), get("/stats", 42))
	require.Equal(t, int64(102), get("/stats", 99))
	require.Equal(t, 4, repo.calls, "cache hits must avoid database work and remain user-scoped")
	repo.err = errors.New("temporary database failure")
	w := summaryRequest(r, http.MethodGet, "/stats?force_refresh=true", "", 42)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	repo.err = nil
	repo.value = 4
	require.Equal(t, int64(46), get("/stats", 42))
	require.Equal(t, 6, repo.calls, "failed refresh must not leave a cached error or stale entry")
}

func TestDashboardSummaryCache_CoalescesConcurrentLoads(t *testing.T) {
	block := make(chan struct{})
	repo := &dashboardSummaryRepo{value: 1, block: block}
	r := summaryTestRouter(t, repo, &dashboardOwnershipRepo{})
	var wg sync.WaitGroup
	results := make(chan int, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- summaryRequest(r, http.MethodGet, "/stats", "", 42).Code }()
	}
	require.Eventually(t, func() bool { repo.mu.Lock(); defer repo.mu.Unlock(); return repo.calls > 0 }, time.Second, time.Millisecond)
	close(block)
	wg.Wait()
	close(results)
	for status := range results {
		require.Equal(t, http.StatusOK, status)
	}
	require.Equal(t, 1, repo.calls, "one DB load per user for concurrent dashboard requests")
}

func TestDashboardAPIKeyCache_RechecksOwnershipAndSeparatesIDSets(t *testing.T) {
	repo := &dashboardSummaryRepo{value: 1}
	keys := &dashboardOwnershipRepo{owners: map[int64]int64{7: 42, 8: 42, 9: 99}}
	r := summaryTestRouter(t, repo, keys)
	post := func(url, body string, user int64) map[string]json.RawMessage {
		w := summaryRequest(r, http.MethodPost, url, body, user)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var p struct {
			Data struct {
				Stats map[string]json.RawMessage `json:"stats"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
		return p.Data.Stats
	}
	first := post("/keys", `{"api_key_ids":[8,7,9]}`, 42)
	require.Len(t, first, 2)
	post("/keys", `{"api_key_ids":[7,8,7]}`, 42)
	require.Equal(t, 1, repo.keyCalls, "order/duplicates should reuse the same owned-ID cache entry")
	post("/keys", `{"api_key_ids":[7]}`, 42)
	require.Equal(t, 2, repo.keyCalls, "different key sets must not share payloads")
	delete(keys.owners, 7)
	removed := post("/keys", `{"api_key_ids":[7]}`, 42)
	require.Empty(t, removed, "cached usage must not bypass ownership after deletion/transfer")
	other := post("/keys", `{"api_key_ids":[7,8,9]}`, 99)
	require.Len(t, other, 1)
	require.Contains(t, other, "9")
	post("/keys?force_refresh=true", `{"api_key_ids":[8]}`, 42)
	post("/keys?nocache=true", `{"api_key_ids":[8]}`, 42)
	require.Equal(t, 5, repo.keyCalls)
	require.Equal(t, 7, keys.calls, "ownership must run on every request including cache hits")
}

func TestDashboardAPIKeysUsage_TooManyIDsCarriesStableReason(t *testing.T) {
	repo := &dashboardSummaryRepo{value: 1}
	keys := &dashboardOwnershipRepo{owners: map[int64]int64{}}
	r := summaryTestRouter(t, repo, keys)
	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	body, err := json.Marshal(map[string][]int64{"api_key_ids": ids})
	require.NoError(t, err)

	w := summaryRequest(r, http.MethodPost, "/keys", string(body), 42)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var p struct {
		Code     int               `json:"code"`
		Message  string            `json:"message"`
		Reason   string            `json:"reason"`
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
	require.Equal(t, http.StatusBadRequest, p.Code)
	require.Equal(t, "Too many API key IDs (maximum 1000 allowed)", p.Message, "Web shows this text; it must not change")
	require.Equal(t, "TOO_MANY_IDS", p.Reason)
	require.Equal(t, map[string]string{"field": "api_key_ids", "max": "1000"}, p.Metadata)
	require.Zero(t, keys.calls, "limit must be enforced before ownership checks")
	require.Zero(t, repo.keyCalls, "limit must be enforced before aggregating usage_logs")
}

func TestDashboardAPIKeyCache_DoesNotCacheFailures(t *testing.T) {
	repo := &dashboardSummaryRepo{err: errors.New("temporary key statistics failure")}
	keys := &dashboardOwnershipRepo{owners: map[int64]int64{7: 42}}
	r := summaryTestRouter(t, repo, keys)
	w := summaryRequest(r, http.MethodPost, "/keys", `{"api_key_ids":[7]}`, 42)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	repo.err = nil
	w = summaryRequest(r, http.MethodPost, "/keys", `{"api_key_ids":[7]}`, 42)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 2, repo.keyCalls)
	require.Equal(t, 2, keys.calls)
}

func TestDashboardSummaryCache_ExpiresPresentationStats(t *testing.T) {
	repo := &dashboardSummaryRepo{value: 1}
	r := summaryTestRouter(t, repo, &dashboardOwnershipRepo{})
	// A short test TTL exercises expiry without a slow real-time sleep; production
	// uses the existing bounded 30-second cache.
	userUsageCache = usagequery.NewCache(5 * time.Millisecond)
	w := summaryRequest(r, http.MethodGet, "/stats", "", 42)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, repo.calls)
	require.Eventually(t, func() bool {
		w = summaryRequest(r, http.MethodGet, "/stats", "", 42)
		return w.Code == http.StatusOK && repo.calls == 2
	}, time.Second, time.Millisecond)
}

func TestDashboardSummaryCache_ResponseCopiesDoNotMutateRetainedStats(t *testing.T) {
	summary := &usagestats.UserDashboardStats{TotalRequests: 1, ByPlatform: []usagestats.PlatformDashboardStats{{Platform: "openai", TotalRequests: 1}}}
	copy := cloneUserDashboardStats(summary)
	copy.TotalRequests = 99
	copy.ByPlatform[0].TotalRequests = 99
	require.Equal(t, int64(1), summary.TotalRequests)
	require.Equal(t, int64(1), summary.ByPlatform[0].TotalRequests)
	keys := map[int64]*usagestats.BatchAPIKeyUsageStats{7: {APIKeyID: 7, TotalActualCost: 1}, 8: nil}
	keyCopy := cloneAPIKeyUsageStats(keys)
	keyCopy[7].TotalActualCost = 99
	delete(keyCopy, 8)
	require.Equal(t, float64(1), keys[7].TotalActualCost)
	require.Contains(t, keys, int64(8))
	require.Nil(t, keys[8])
}
