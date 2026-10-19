package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsRuleCaptureRepo struct {
	service.OpsRepository
	created *service.OpsAlertRule
	updated *service.OpsAlertRule
}

func (r *opsRuleCaptureRepo) CreateAlertRule(_ context.Context, rule *service.OpsAlertRule) (*service.OpsAlertRule, error) {
	r.created = rule
	out := *rule
	out.ID = 99
	return &out, nil
}

func (r *opsRuleCaptureRepo) UpdateAlertRule(_ context.Context, rule *service.OpsAlertRule) (*service.OpsAlertRule, error) {
	r.updated = rule
	out := *rule
	return &out, nil
}

func newOpsRuleTestRouter(repo *opsRuleCaptureRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.POST("/alert-rules", h.CreateAlertRule)
	r.PUT("/alert-rules/:id", h.UpdateAlertRule)
	return r
}

// The seeded latency rules (migration 033) must be editable and disableable again.
func TestOpsUpdateAlertRule_LatencyPresetRuleReturns200(t *testing.T) {
	repo := &opsRuleCaptureRepo{}
	r := newOpsRuleTestRouter(repo)

	rec := serveOps(r, http.MethodPut, "/alert-rules/3", map[string]any{
		"name": "P99延迟过高", "metric_type": "p99_latency_ms", "operator": ">", "threshold": 3000,
		"window_minutes": 5, "sustained_minutes": 10, "severity": "P2", "cooldown_minutes": 30,
		"notify_email": true, "enabled": false,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, repo.updated)
	require.Equal(t, int64(3), repo.updated.ID)
	require.Equal(t, "p99_latency_ms", repo.updated.MetricType)
	require.False(t, repo.updated.Enabled)
	require.Equal(t, 3000.0, repo.updated.Threshold)

	rec = serveOps(r, http.MethodPost, "/alert-rules", map[string]any{
		"name": "P95延迟过高", "metric_type": "p95_latency_ms", "operator": ">", "threshold": 2000,
		"window_minutes": 5, "sustained_minutes": 10,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, repo.created)
	require.Equal(t, "p95_latency_ms", repo.created.MetricType)
	require.Equal(t, 2000.0, repo.created.Threshold)
}
