package handler

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMarkProbeAccountConcurrencyMarksLeadersOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	coalescer := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: 8 * time.Second,
		AttemptBudget: 8,
	})
	candidate := testProbeCandidate()
	leader := coalescer.Begin(c.Request.Context(), candidate, "leader")
	markProbeAccountConcurrency(c, leader, true)
	require.True(t, service.ProbeAccountConcurrencyEnabled(c.Request.Context()))

	follower := coalescer.Begin(c.Request.Context(), candidate, "follower")
	c.Request = c.Request.WithContext(httptest.NewRequest("POST", "/v1/messages", nil).Context())
	markProbeAccountConcurrency(c, follower, true)
	require.False(t, service.ProbeAccountConcurrencyEnabled(c.Request.Context()))
}

func TestPrepareProbeAdmissionCarriesMarkerIntoLeaderContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	coalescer := NewProbeCoalescer(ProbeCoalescerConfig{
		Mode:          ProbeCoalescingActive,
		Window:        time.Minute,
		LeaderTimeout: 8 * time.Second,
		AttemptBudget: 8,
	})

	ctx, _ := prepareProbeAdmission(c, coalescer)
	lease := coalescer.Begin(ctx, testProbeCandidate(), "leader-context")
	require.True(t, service.ProbeAccountConcurrencyEnabled(ctx))
	require.True(t, service.ProbeAccountConcurrencyEnabled(lease.LeaderContext()))
}
