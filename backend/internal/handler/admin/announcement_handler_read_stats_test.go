package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type readStatsHandlerAnnRepo struct {
	service.AnnouncementRepository
	items []service.Announcement
}

func (r *readStatsHandlerAnnRepo) List(_ context.Context, params pagination.PaginationParams, _ service.AnnouncementListFilters) ([]service.Announcement, *pagination.PaginationResult, error) {
	return r.items, &pagination.PaginationResult{Total: int64(len(r.items)), Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
}

func (r *readStatsHandlerAnnRepo) GetByID(_ context.Context, id int64) (*service.Announcement, error) {
	for i := range r.items {
		if r.items[i].ID == id {
			item := r.items[i]
			return &item, nil
		}
	}
	return nil, service.ErrAnnouncementNotFound
}

type readStatsHandlerReadRepo struct {
	service.AnnouncementReadRepository
	counts     map[int64]service.AnnouncementReadCounts
	summarized int
	listCalls  int
	listFilter service.AnnouncementReadStatusFilter
	listParams pagination.PaginationParams
}

func (r *readStatsHandlerReadRepo) SummarizeReads(_ context.Context, id int64, _ service.AnnouncementTargeting) (service.AnnouncementReadCounts, error) {
	r.summarized++
	return r.counts[id], nil
}

func (r *readStatsHandlerReadRepo) ListUserReadStatus(_ context.Context, _ int64, _ service.AnnouncementTargeting, params pagination.PaginationParams, filter service.AnnouncementReadStatusFilter) ([]service.AnnouncementUserReadStatus, *pagination.PaginationResult, error) {
	r.listCalls++
	r.listParams = params
	r.listFilter = filter
	return []service.AnnouncementUserReadStatus{{UserID: 8, Email: "u@x", Eligible: true}}, &pagination.PaginationResult{Total: 7, Page: params.Page, PageSize: params.Limit(), Pages: 1}, nil
}

func (r *readStatsHandlerReadRepo) GetReadMapByUsers(context.Context, int64, []int64) (map[int64]time.Time, error) {
	return map[int64]time.Time{}, nil
}

func readStatsHandlerFixture() (*gin.Engine, *readStatsHandlerReadRepo, *announcementUserRepoCapture, []service.Announcement) {
	gin.SetMode(gin.TestMode)
	created := time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC)
	items := []service.Announcement{
		{ID: 31, Title: "a", Content: "c", Status: service.AnnouncementStatusActive, NotifyMode: service.AnnouncementNotifyModeSilent, CreatedAt: created, UpdatedAt: created},
		{ID: 32, Title: "b", Content: "d", Status: service.AnnouncementStatusDraft, NotifyMode: service.AnnouncementNotifyModePopup, CreatedAt: created, UpdatedAt: created},
	}
	readRepo := &readStatsHandlerReadRepo{counts: map[int64]service.AnnouncementReadCounts{
		31: {TotalUsers: 1036, EligibleCount: 1036, ReadCount: 412, EligibleReadCount: 410},
		32: {TotalUsers: 1036, EligibleCount: 5, ReadCount: 0, EligibleReadCount: 0},
	}}
	userRepo := &announcementUserRepoCapture{}
	svc := service.NewAnnouncementService(&readStatsHandlerAnnRepo{items: items}, readRepo, userRepo, &announcementUserSubRepoCapture{})
	h := NewAnnouncementHandler(svc)
	router := gin.New()
	router.GET("/admin/announcements", h.List)
	router.GET("/admin/announcements/:id", h.GetByID)
	router.GET("/admin/announcements/:id/read-status", h.ListReadStatus)
	router.GET("/admin/announcements/:id/read-summary", h.ReadSummary)
	return router, readRepo, userRepo, items
}

func serveReadStats(router *gin.Engine, target string) (*httptest.ResponseRecorder, map[string]any) {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func TestAdminAnnouncementReadSummary(t *testing.T) {
	router, _, _, _ := readStatsHandlerFixture()

	rec, body := serveReadStats(router, "/admin/announcements/31/read-summary")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := body["data"].(map[string]any)
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	require.ElementsMatch(t, []string{"announcement_id", "total_users", "eligible_count", "read_count", "eligible_read_count", "computed_at"}, keys)
	require.EqualValues(t, 31, data["announcement_id"])
	require.EqualValues(t, 1036, data["total_users"])
	require.EqualValues(t, 1036, data["eligible_count"])
	require.EqualValues(t, 412, data["read_count"])
	require.EqualValues(t, 410, data["eligible_read_count"])
	computedAt := data["computed_at"].(string)
	require.True(t, strings.HasSuffix(computedAt, "Z"), computedAt)
	_, err := time.Parse(time.RFC3339, computedAt)
	require.NoError(t, err)

	rec, _ = serveReadStats(router, "/admin/announcements/abc/read-summary")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid announcement ID")

	rec, body = serveReadStats(router, "/admin/announcements/404/read-summary")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "ANNOUNCEMENT_NOT_FOUND", body["reason"])
}

func TestAdminAnnouncementIncludeReadStats(t *testing.T) {
	router, readRepo, _, items := readStatsHandlerFixture()

	baselineList, err := json.Marshal([]any{dto.AnnouncementFromService(&items[0]), dto.AnnouncementFromService(&items[1])})
	require.NoError(t, err)
	baselineItem, err := json.Marshal(dto.AnnouncementFromService(&items[0]))
	require.NoError(t, err)

	for _, target := range []string{"/admin/announcements", "/admin/announcements?include_read_stats=false"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotContains(t, rec.Body.String(), "read_stats")
		var envelope struct {
			Data struct {
				Items json.RawMessage `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		require.JSONEq(t, string(baselineList), string(envelope.Data.Items), target)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/announcements/31", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "read_stats")
	var single struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &single))
	require.JSONEq(t, string(baselineItem), string(single.Data))
	require.Zero(t, readRepo.summarized, "stats are only computed on request")

	_, body := serveReadStats(router, "/admin/announcements?include_read_stats=true")
	list := body["data"].(map[string]any)["items"].([]any)
	require.Len(t, list, 2)
	require.Equal(t, map[string]any{"eligible_count": float64(1036), "read_count": float64(412), "eligible_read_count": float64(410)}, list[0].(map[string]any)["read_stats"])
	require.Equal(t, map[string]any{"eligible_count": float64(5), "read_count": float64(0), "eligible_read_count": float64(0)}, list[1].(map[string]any)["read_stats"])

	_, body = serveReadStats(router, "/admin/announcements/32?include_read_stats=1")
	require.Equal(t, map[string]any{"eligible_count": float64(5), "read_count": float64(0), "eligible_read_count": float64(0)}, body["data"].(map[string]any)["read_stats"])
}

func TestAdminAnnouncementReadStatusInvalidFilter(t *testing.T) {
	router, readRepo, userRepo, _ := readStatsHandlerFixture()

	for _, query := range []string{"read_state=foo", "eligible=maybe", "read_state=read&eligible=yes"} {
		rec, body := serveReadStats(router, "/admin/announcements/31/read-status?"+query)
		require.Equal(t, http.StatusBadRequest, rec.Code, query)
		require.Equal(t, "INVALID_READ_STATUS_FILTER", body["reason"], query)
	}
	require.Zero(t, readRepo.listCalls)

	rec, body := serveReadStats(router, "/admin/announcements/31/read-status?read_state=Unread&eligible=true&search=%20bob%20&page=2&page_size=50")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, readRepo.listCalls)
	require.Equal(t, service.AnnouncementReadStateUnread, readRepo.listFilter.ReadState)
	require.NotNil(t, readRepo.listFilter.Eligible)
	require.True(t, *readRepo.listFilter.Eligible)
	require.Equal(t, "bob", readRepo.listFilter.Search)
	require.Equal(t, pagination.PaginationParams{Page: 2, PageSize: 50, SortBy: "email", SortOrder: "asc"}, readRepo.listParams)
	data := body["data"].(map[string]any)
	require.EqualValues(t, 7, data["total"], "total reflects the filtered count")
	require.Len(t, data["items"], 1)

	rec, _ = serveReadStats(router, "/admin/announcements/31/read-status?sort_by=read_at&sort_order=desc&eligible=")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 2, readRepo.listCalls)
	require.Nil(t, readRepo.listFilter.Eligible, "empty eligible means no filter")
	require.Equal(t, "read_at", readRepo.listParams.SortBy)

	rec, _ = serveReadStats(router, "/admin/announcements/31/read-status?sort_by=balance")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 2, readRepo.listCalls, "legacy parameters keep the legacy path")
	require.Equal(t, "balance", userRepo.listParams.SortBy)
}
