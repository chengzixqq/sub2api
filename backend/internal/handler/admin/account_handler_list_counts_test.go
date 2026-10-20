package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var accountListCountsKeys = []string{
	"all", "needs_attention", "error", "overloaded", "rate_limited",
	"temp_unschedulable", "unschedulable", "inactive", "schedulable_now",
}

func sampleAccountListCounts() *service.AccountListCounts {
	return &service.AccountListCounts{
		All: 120, NeedsAttention: 7, Error: 2, Overloaded: 1, RateLimited: 3,
		TempUnschedulable: 1, Unschedulable: 3, Inactive: 2, SchedulableNow: 100,
	}
}

func decodeAccountListData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.NotNil(t, envelope.Data)
	return envelope.Data
}

func TestAccountHandlerListIncludeCountsReturnsCountsAndKeepsPageShape(t *testing.T) {
	for _, lite := range []bool{false, true} {
		router, adminSvc := setupAccountListRouter()
		adminSvc.accountListCounts = sampleAccountListCounts()
		url := "/api/v1/admin/accounts?page=1&page_size=20&include_counts=1&status=error&platform=openai&type=oauth&search=x&group=7&privacy_mode=training_off"
		if lite {
			url += "&lite=1"
		}

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusOK, rec.Code)

		data := decodeAccountListData(t, rec.Body.Bytes())
		for _, key := range []string{"items", "total", "page", "page_size", "pages", "counts"} {
			require.Contains(t, data, key, "lite=%v", lite)
		}
		require.EqualValues(t, 1, data["total"])
		require.EqualValues(t, 1, data["page"])
		require.EqualValues(t, 20, data["page_size"])
		require.EqualValues(t, 1, data["pages"])
		require.Len(t, data["items"], 1)

		counts, ok := data["counts"].(map[string]any)
		require.True(t, ok)
		require.Len(t, counts, len(accountListCountsKeys))
		for _, key := range accountListCountsKeys {
			require.Contains(t, counts, key)
		}
		require.EqualValues(t, 120, counts["all"])
		require.EqualValues(t, 7, counts["needs_attention"])
		require.EqualValues(t, 2, counts["error"])
		require.EqualValues(t, 1, counts["overloaded"])
		require.EqualValues(t, 3, counts["rate_limited"])
		require.EqualValues(t, 1, counts["temp_unschedulable"])
		require.EqualValues(t, 3, counts["unschedulable"])
		require.EqualValues(t, 2, counts["inactive"])
		require.EqualValues(t, 100, counts["schedulable_now"])

		// Counts use every list filter except status.
		require.Equal(t, 1, adminSvc.lastAccountListCounts.calls)
		require.Equal(t, service.AccountListCountsFilter{
			Platform: "openai", AccountType: "oauth", Search: "x", GroupID: 7, PrivacyMode: "training_off",
		}, adminSvc.lastAccountListCounts.filter)
		require.Equal(t, "error", adminSvc.lastListAccounts.status)
	}
}

func TestAccountHandlerListIncludeCountsForwardsUngroupedFilter(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	adminSvc.accountListCounts = sampleAccountListCounts()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?include_counts=true&group="+accountListGroupUngroupedQueryValue, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.AccountListGroupUngrouped, adminSvc.lastAccountListCounts.filter.GroupID)
}

func TestAccountHandlerListWithoutIncludeCountsUnchanged(t *testing.T) {
	for _, url := range []string{
		"/api/v1/admin/accounts?page=1&page_size=20",
		"/api/v1/admin/accounts?page=1&page_size=20&lite=1",
		"/api/v1/admin/accounts?page=1&page_size=20&include_counts=0",
		"/api/v1/admin/accounts?page=1&page_size=20&include_counts=maybe",
	} {
		router, adminSvc := setupAccountListRouter()
		adminSvc.accountListCounts = sampleAccountListCounts()

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusOK, rec.Code, url)
		data := decodeAccountListData(t, rec.Body.Bytes())
		require.NotContains(t, data, "counts", url)
		require.Len(t, data, 5, url)
		require.Zero(t, adminSvc.lastAccountListCounts.calls, url)
		require.NotEmpty(t, rec.Header().Get("ETag"), url)
	}
}

func TestAccountHandlerListIncludeCountsOmittedWhenRepositoryHasNoCounts(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	adminSvc.accountListCounts = nil

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?include_counts=1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	data := decodeAccountListData(t, rec.Body.Bytes())
	require.NotContains(t, data, "counts")
	require.Equal(t, 1, adminSvc.lastAccountListCounts.calls)
}

func TestAccountHandlerListIncludeCountsErrorFails(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	adminSvc.accountListCountsErr = errors.New("count failed")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?include_counts=1", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Empty(t, rec.Header().Get("ETag"))
}

// Without counts the ETag payload must serialize byte-for-byte like before
// include_counts existed, so clients holding an old ETag still get 304.
func TestBuildAccountsListETagWithoutCountsMatchesLegacyPayload(t *testing.T) {
	items := []map[string]any{{"id": 1, "name": "a"}, {"id": 2, "name": "b"}}
	for _, lite := range []bool{false, true} {
		legacy := struct {
			Total       int64            `json:"total"`
			Page        int              `json:"page"`
			PageSize    int              `json:"page_size"`
			Platform    string           `json:"platform"`
			AccountType string           `json:"type"`
			Status      string           `json:"status"`
			Search      string           `json:"search"`
			Lite        bool             `json:"lite"`
			Items       []map[string]any `json:"items"`
		}{
			Total: 2, Page: 1, PageSize: 20, Platform: "openai", AccountType: "oauth",
			Status: "error", Search: "x", Lite: lite, Items: items,
		}
		raw, err := json.Marshal(legacy)
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		want := "\"" + hex.EncodeToString(sum[:]) + "\""

		got := buildAccountsListETag(items, 2, 1, 20, "openai", "oauth", "error", "x", lite, nil)
		require.Equal(t, want, got)
		withCounts := buildAccountsListETag(items, 2, 1, 20, "openai", "oauth", "error", "x", lite, sampleAccountListCounts())
		require.NotEqual(t, want, withCounts)
	}
}

func TestAccountHandlerListIncludeCountsETagReflectsCounts(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	adminSvc.accountListCounts = sampleAccountListCounts()
	url := "/api/v1/admin/accounts?page=1&page_size=20&lite=1&include_counts=1"

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	plain := httptest.NewRecorder()
	router.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&lite=1", nil))
	require.Equal(t, http.StatusOK, plain.Code)
	require.NotEqual(t, etag, plain.Header().Get("ETag"), "counts are part of the representation")

	same := httptest.NewRecorder()
	sameReq := httptest.NewRequest(http.MethodGet, url, nil)
	sameReq.Header.Set("If-None-Match", etag)
	router.ServeHTTP(same, sameReq)
	require.Equal(t, http.StatusNotModified, same.Code)

	changed := sampleAccountListCounts()
	changed.NeedsAttention++
	adminSvc.accountListCounts = changed
	refreshed := httptest.NewRecorder()
	refreshedReq := httptest.NewRequest(http.MethodGet, url, nil)
	refreshedReq.Header.Set("If-None-Match", etag)
	router.ServeHTTP(refreshed, refreshedReq)
	require.Equal(t, http.StatusOK, refreshed.Code, "items unchanged but counts changed must not be 304")
	require.NotEqual(t, etag, refreshed.Header().Get("ETag"))
	counts := decodeAccountListData(t, refreshed.Body.Bytes())["counts"].(map[string]any)
	require.EqualValues(t, 8, counts["needs_attention"])
}

func TestAccountHandlerListForwardsMultiStatusAndUrgency(t *testing.T) {
	router, adminSvc := setupAccountListRouter()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?status=error,rate_limited&sort_by=urgency&sort_order=desc", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "error,rate_limited", adminSvc.lastListAccounts.status)
	require.Equal(t, "urgency", adminSvc.lastListAccounts.sortBy)
	require.Equal(t, "desc", adminSvc.lastListAccounts.sortOrder)
}
