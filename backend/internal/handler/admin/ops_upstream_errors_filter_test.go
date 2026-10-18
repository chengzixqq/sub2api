package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsErrorListCaptureRepo struct {
	service.OpsRepository
	filter *service.OpsErrorLogFilter
}

func (r *opsErrorListCaptureRepo) ListErrorLogs(_ context.Context, f *service.OpsErrorLogFilter) (*service.OpsErrorLogList, error) {
	r.filter = f
	return &service.OpsErrorLogList{Errors: []*service.OpsErrorLog{}, Page: 1, PageSize: 20}, nil
}

func newOpsErrorListRouter(repo *opsErrorListCaptureRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/upstream-errors", h.ListUpstreamErrors)
	r.GET("/request-errors", h.ListRequestErrors)
	return r
}

func TestOpsUpstreamErrors_IdentityFilters(t *testing.T) {
	repo := &opsErrorListCaptureRepo{}
	r := newOpsErrorListRouter(repo)

	rec := serveOps(r, http.MethodGet, "/upstream-errors?user_id=12&api_key_id=7&model=claude-opus-4&request_id=rid-1&client_request_id=crid-1", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	f := repo.filter
	require.Equal(t, int64(12), *f.UserID)
	require.Equal(t, int64(7), *f.APIKeyID)
	require.Equal(t, "claude-opus-4", f.Model)
	require.False(t, f.ModelFuzzy)
	require.Equal(t, "rid-1", f.RequestID)
	require.Equal(t, "crid-1", f.ClientRequestID)
	require.True(t, f.IncludeRecoveredUpstream, "existing upstream semantics are kept")

	for _, bad := range []string{"user_id=abc", "user_id=0", "api_key_id=-3"} {
		rec = serveOps(r, http.MethodGet, "/upstream-errors?"+bad, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}

	rec = serveOps(r, http.MethodGet, "/upstream-errors", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, repo.filter.UserID)
	require.Empty(t, repo.filter.Model)
}

func TestOpsRequestErrors_ClientRequestIDFilter(t *testing.T) {
	repo := &opsErrorListCaptureRepo{}
	r := newOpsErrorListRouter(repo)

	rec := serveOps(r, http.MethodGet, "/request-errors?client_request_id=crid-9&user_id=3", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "crid-9", repo.filter.ClientRequestID)
	require.Equal(t, int64(3), *repo.filter.UserID)
}
