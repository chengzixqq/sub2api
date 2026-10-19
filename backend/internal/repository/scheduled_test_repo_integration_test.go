//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestScheduledTestResultRepository_UpstreamStatusRoundTrip(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	account := mustCreateAccount(t, client, &service.Account{Name: "scheduled-upstream-" + uuid.NewString()})

	planRepo := NewScheduledTestPlanRepository(integrationDB)
	resultRepo := NewScheduledTestResultRepository(integrationDB)
	nextRun := time.Now().Add(time.Hour)
	plan, err := planRepo.Create(ctx, &service.ScheduledTestPlan{
		AccountID:      account.ID,
		ModelID:        "claude-sonnet-4-5",
		CronExpression: "*/30 * * * *",
		Enabled:        false,
		MaxResults:     10,
		NextRunAt:      &nextRun,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduled_test_plans WHERE id = $1", plan.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})

	startedAt := time.Now().Add(-2 * time.Second).UTC().Truncate(time.Microsecond)
	finishedAt := startedAt.Add(1500 * time.Millisecond)
	code := 429
	failed, err := resultRepo.Create(ctx, &service.ScheduledTestResult{
		PlanID:             plan.ID,
		Status:             "failed",
		ErrorMessage:       "API returned 429: slow down",
		LatencyMs:          1500,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		UpstreamStatusCode: &code,
	})
	require.NoError(t, err)
	require.NotNil(t, failed.UpstreamStatusCode)
	require.Equal(t, 429, *failed.UpstreamStatusCode)
	require.Equal(t, "API returned 429: slow down", failed.ErrorMessage)
	require.Nil(t, failed.ErrorKind, "error_kind is derived on read, not stored")

	succeeded, err := resultRepo.Create(ctx, &service.ScheduledTestResult{
		PlanID:       plan.ID,
		Status:       "success",
		ResponseText: "pong",
		LatencyMs:    800,
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
	})
	require.NoError(t, err)
	require.Nil(t, succeeded.UpstreamStatusCode)

	listed, err := resultRepo.ListByPlanID(ctx, plan.ID, 10)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	byID := map[int64]*service.ScheduledTestResult{}
	for _, r := range listed {
		byID[r.ID] = r
	}
	require.Contains(t, byID, failed.ID)
	require.Contains(t, byID, succeeded.ID)

	gotFailed := byID[failed.ID]
	require.NotNil(t, gotFailed.UpstreamStatusCode)
	require.Equal(t, 429, *gotFailed.UpstreamStatusCode)
	require.Equal(t, "failed", gotFailed.Status)
	require.Equal(t, int64(1500), gotFailed.LatencyMs)
	require.True(t, gotFailed.StartedAt.Equal(startedAt))
	require.True(t, gotFailed.FinishedAt.Equal(finishedAt))

	gotSucceeded := byID[succeeded.ID]
	require.Nil(t, gotSucceeded.UpstreamStatusCode)
	require.Equal(t, "pong", gotSucceeded.ResponseText)

	svc := service.NewScheduledTestService(planRepo, resultRepo)
	viewed, err := svc.ListResults(ctx, plan.ID, 10)
	require.NoError(t, err)
	for _, r := range viewed {
		if r.ID == failed.ID {
			require.NotNil(t, r.ErrorKind)
			require.Equal(t, service.AccountErrorKindRateLimit, *r.ErrorKind)
		} else {
			require.Nil(t, r.ErrorKind)
		}
	}
}
