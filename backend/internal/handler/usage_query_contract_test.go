package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestUserUsageQueryMinuteRangeAndDeferredCount(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/usage?start_time=2030-01-01T12:15:00Z&end_time=2030-01-01T12:16:00Z&timezone=UTC&count_mode=deferred", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, repo.listFilters.DeferredTotal)
	require.Equal(t, time.Minute, repo.listFilters.EndTime.Sub(*repo.listFilters.StartTime))
	var payload struct {
		Data struct {
			Total *int64              `json:"total"`
			Pages *int                `json:"pages"`
			Exact bool                `json:"total_exact"`
			Query usagequery.Metadata `json:"query"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Nil(t, payload.Data.Total)
	require.Nil(t, payload.Data.Pages)
	require.False(t, payload.Data.Exact)
	require.Equal(t, "UTC", payload.Data.Query.Timezone)
}

func TestUserUsageRejectsIncompleteTimestampPair(t *testing.T) {
	for _, path := range []string{"/usage", "/usage/stats", "/usage/dashboard/models", "/usage/dashboard/snapshot-v2"} {
		w := httptest.NewRecorder()
		newUserUsageRequestTypeTestRouter(&userUsageRepoCapture{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?start_time=2030-01-01T00:00:00Z", nil))
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestUserUsageForceRefreshReplacesCachedStats(t *testing.T) {
	repo := &userUsageRepoCapture{stats: &usagestats.UsageStats{TotalRequests: 1}}
	router := newUserUsageRequestTypeTestRouter(repo)
	query := "/usage/stats?start_time=2031-01-01T00:00:00Z&end_time=2031-01-02T00:00:00Z&model=force-refresh-test&timezone=UTC"
	get := func(url string) int64 {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var payload struct {
			Data struct {
				Requests int64 `json:"total_requests"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		return payload.Data.Requests
	}
	require.Equal(t, int64(1), get(query))
	repo.stats = &usagestats.UsageStats{TotalRequests: 2}
	require.Equal(t, int64(1), get(query))
	require.Equal(t, int64(2), get(query+"&force_refresh=true"))
	require.Equal(t, int64(2), get(query))
}
