package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsAlertEventCaptureRepo struct {
	service.OpsRepository
	statusCalls  int
	statusID     int64
	status       string
	resolvedAt   *time.Time
	resolvedBy   *int64
	countsFilter *service.OpsAlertEventFilter
}

func (r *opsAlertEventCaptureRepo) UpdateAlertEventStatus(_ context.Context, eventID int64, status string, resolvedAt *time.Time, resolvedBy *int64) error {
	r.statusCalls++
	r.statusID, r.status, r.resolvedAt, r.resolvedBy = eventID, status, resolvedAt, resolvedBy
	return nil
}

func (r *opsAlertEventCaptureRepo) CountAlertEventsBySeverity(_ context.Context, filter *service.OpsAlertEventFilter) (map[string]int64, error) {
	r.countsFilter = filter
	return map[string]int64{"P1": 1, "P2": 1}, nil
}

func newOpsAlertEventTestRouter(repo *opsAlertEventCaptureRepo, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if userID != 0 {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
		}
		c.Next()
	})
	r.GET("/alert-events/counts", h.GetAlertEventCounts)
	r.PUT("/alert-events/:id/status", h.UpdateAlertEventStatus)
	return r
}

func TestUpdateAlertEventStatus_RecordsResolverForManualResolve(t *testing.T) {
	repo := &opsAlertEventCaptureRepo{}
	r := newOpsAlertEventTestRouter(repo, 7)

	rec := serveOps(r, http.MethodPut, "/alert-events/5/status", map[string]any{"status": "manual_resolved"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"code":0,"message":"success","data":{"updated":true}}`, rec.Body.String())
	require.Equal(t, int64(5), repo.statusID)
	require.Equal(t, service.OpsAlertStatusManualResolved, repo.status)
	require.NotNil(t, repo.resolvedAt)
	require.NotNil(t, repo.resolvedBy)
	require.Equal(t, int64(7), *repo.resolvedBy)

	rec = serveOps(r, http.MethodPut, "/alert-events/6/status", map[string]any{"status": "resolved"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, service.OpsAlertStatusResolved, repo.status)
	require.Nil(t, repo.resolvedBy, "only manual resolution records who resolved it")
	require.Equal(t, 2, repo.statusCalls)
}

func TestUpdateAlertEventStatus_NoSubjectLeavesResolverEmpty(t *testing.T) {
	repo := &opsAlertEventCaptureRepo{}
	r := newOpsAlertEventTestRouter(repo, 0)

	rec := serveOps(r, http.MethodPut, "/alert-events/5/status", map[string]any{"status": "manual_resolved"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, repo.resolvedBy)
}

func TestGetAlertEventCounts_ParsesScopeAndShape(t *testing.T) {
	repo := &opsAlertEventCaptureRepo{}
	r := newOpsAlertEventTestRouter(repo, 7)

	rec := serveOps(r, http.MethodGet, "/alert-events/counts?platform=openai&group_id=2", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, float64(2), body.Data["firing"])
	require.Equal(t, map[string]any{"P0": float64(0), "P1": float64(1), "P2": float64(1), "P3": float64(0)}, body.Data["by_severity"])
	require.Equal(t, service.OpsAlertStatusFiring, repo.countsFilter.Status)
	require.Equal(t, "openai", repo.countsFilter.Platform)
	require.Equal(t, int64(2), *repo.countsFilter.GroupID)

	rec = serveOps(r, http.MethodGet, "/alert-events/counts", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "", repo.countsFilter.Platform)
	require.Nil(t, repo.countsFilter.GroupID)

	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodGet, "/alert-events/counts?group_id=abc", nil).Code)
	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodGet, "/alert-events/counts?group_id=0", nil).Code)
}
