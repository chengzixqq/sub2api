package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsSilenceCaptureRepo struct {
	service.OpsRepository
	listFilter  *service.OpsAlertSilenceFilter
	updateID    int64
	updateUntil time.Time
	updateWhy   *string
	endedID     int64
}

func (r *opsSilenceCaptureRepo) ListAlertSilences(_ context.Context, f *service.OpsAlertSilenceFilter) ([]*service.OpsAlertSilence, error) {
	r.listFilter = f
	return []*service.OpsAlertSilence{{ID: 7, RuleID: 41, Platform: "", Until: time.Now().Add(time.Hour), CreatedByName: "admin"}}, nil
}

func (r *opsSilenceCaptureRepo) UpdateAlertSilenceUntil(_ context.Context, id int64, until time.Time, reason *string) (*service.OpsAlertSilence, error) {
	if id == 404 {
		return nil, sql.ErrNoRows
	}
	r.updateID, r.updateUntil, r.updateWhy = id, until, reason
	return &service.OpsAlertSilence{ID: id, RuleID: 41, Until: until}, nil
}

func (r *opsSilenceCaptureRepo) EndAlertSilence(_ context.Context, id int64, _ time.Time) error {
	if id == 404 {
		return sql.ErrNoRows
	}
	r.endedID = id
	return nil
}

func newOpsSilenceTestRouter(repo *opsSilenceCaptureRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/alert-silences", h.ListAlertSilences)
	r.PUT("/alert-silences/:id", h.UpdateAlertSilence)
	r.DELETE("/alert-silences/:id", h.DeleteAlertSilence)
	return r
}

func serveOps(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestOpsAlertSilences_List(t *testing.T) {
	repo := &opsSilenceCaptureRepo{}
	r := newOpsSilenceTestRouter(repo)

	rec := serveOps(r, http.MethodGet, "/alert-silences?active=true&rule_id=41&limit=50", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, repo.listFilter.ActiveOnly)
	require.Equal(t, int64(41), *repo.listFilter.RuleID)
	require.Equal(t, 50, repo.listFilter.Limit)
	require.Contains(t, rec.Body.String(), `"created_by_name":"admin"`)

	// active defaults to true: the App asks “what is silenced right now”.
	serveOps(r, http.MethodGet, "/alert-silences", nil)
	require.True(t, repo.listFilter.ActiveOnly)
	serveOps(r, http.MethodGet, "/alert-silences?active=false", nil)
	require.False(t, repo.listFilter.ActiveOnly)

	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodGet, "/alert-silences?rule_id=abc", nil).Code)
	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodGet, "/alert-silences?active=maybe", nil).Code)
}

func TestOpsAlertSilences_Update(t *testing.T) {
	repo := &opsSilenceCaptureRepo{}
	r := newOpsSilenceTestRouter(repo)
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	rec := serveOps(r, http.MethodPut, "/alert-silences/7", map[string]any{"until": until.Format(time.RFC3339), "reason": " 改为 1 小时 "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int64(7), repo.updateID)
	require.True(t, until.Equal(repo.updateUntil))
	require.Equal(t, "改为 1 小时", *repo.updateWhy)

	// Reason is optional; omitting it keeps the stored one.
	serveOps(r, http.MethodPut, "/alert-silences/7", map[string]any{"until": until.Format(time.RFC3339)})
	require.Nil(t, repo.updateWhy)

	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodPut, "/alert-silences/7", map[string]any{"until": "tomorrow"}).Code)
	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodPut, "/alert-silences/7", map[string]any{"until": time.Now().Add(-time.Hour).Format(time.RFC3339)}).Code)
	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodPut, "/alert-silences/x", map[string]any{"until": until.Format(time.RFC3339)}).Code)
	require.Equal(t, http.StatusNotFound, serveOps(r, http.MethodPut, "/alert-silences/404", map[string]any{"until": until.Format(time.RFC3339)}).Code)
}

func TestOpsAlertSilences_Delete(t *testing.T) {
	repo := &opsSilenceCaptureRepo{}
	r := newOpsSilenceTestRouter(repo)

	rec := serveOps(r, http.MethodDelete, "/alert-silences/7", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(7), repo.endedID)
	require.Contains(t, rec.Body.String(), `"deleted":true`)

	require.Equal(t, http.StatusNotFound, serveOps(r, http.MethodDelete, "/alert-silences/404", nil).Code)
	require.Equal(t, http.StatusBadRequest, serveOps(r, http.MethodDelete, "/alert-silences/0", nil).Code)
}
