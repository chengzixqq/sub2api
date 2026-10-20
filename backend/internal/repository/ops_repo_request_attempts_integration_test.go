//go:build integration

package repository

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOpsRequestAttempts_LinksErrorLogAndUsageByClientRequestID(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	reqID := "req-" + suffix[:18]
	cid := uuid.NewString()
	usageRequestID := "client:" + cid

	user := mustCreateUser(t, client, &service.User{Email: "attempts-" + suffix + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-attempts-" + suffix})
	failed := mustCreateAccount(t, client, &service.Account{Name: "attempts-a1-" + suffix, Platform: service.PlatformAnthropic})
	succeeded := mustCreateAccount(t, client, &service.Account{Name: "attempts-a2-" + suffix, Platform: service.PlatformAnthropic})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE request_id = $1", reqID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE request_id = $1", usageRequestID)
	})

	// A fixed past instant no other test writes to.
	base := time.Date(2012, 5, 6, 10, 0, 0, 0, time.UTC)
	startedMs := base.Add(-12 * time.Second).UnixMilli()
	events := `[{"at_unix_ms":` + strconv.FormatInt(startedMs+400, 10) + `,"started_at_unix_ms":` + strconv.FormatInt(startedMs, 10) + `,"duration_ms":400,` +
		`"platform":"anthropic","account_id":` + strconv.FormatInt(failed.ID, 10) + `,"proxy_id":null,"proxy_name":"direct/no_proxy",` +
		`"upstream_status_code":429,"kind":"failover","message":"rate limited"}]`
	var errorLogID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_error_logs
		(request_id, client_request_id, user_id, error_phase, error_type, severity, status_code, upstream_errors, error_owner, created_at)
		VALUES ($1,$2,$3,'upstream','upstream_error','P2',200,$4::jsonb,'provider',$5) RETURNING id`,
		reqID, cid, user.ID, events, base).Scan(&errorLogID))
	var usageID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs
		(user_id, api_key_id, account_id, request_id, model, input_tokens, output_tokens, duration_ms, first_token_ms, upstream_request_id, created_at)
		VALUES ($1,$2,$3,$4,'m',10,5,9800,1400,$5,$6) RETURNING id`,
		user.ID, key.ID, succeeded.ID, usageRequestID, "up-"+suffix[:8], base.Add(-time.Second)).Scan(&usageID))

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	rows, err := repo.ListRequestAttemptErrorLogs(ctx, service.OpsRequestAttemptLookup{RequestIDs: []string{reqID}, ClientRequestIDs: []string{reqID}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, errorLogID, rows[0].ID)
	require.Equal(t, reqID, rows[0].RequestID)
	require.Equal(t, cid, rows[0].ClientRequestID)
	require.Equal(t, 200, rows[0].StatusCode)
	require.Equal(t, "upstream", rows[0].ErrorPhase)
	require.True(t, rows[0].CreatedAt.Equal(base))
	parsed, err := service.ParseOpsUpstreamErrors(rows[0].UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, parsed, 1)
	require.Equal(t, startedMs, parsed[0].StartedAtUnixMs)
	require.Equal(t, int64(400), *parsed[0].DurationMs)

	rows, err = repo.ListRequestAttemptErrorLogs(ctx, service.OpsRequestAttemptLookup{RequestIDs: []string{}, ClientRequestIDs: []string{cid}})
	require.NoError(t, err)
	require.Len(t, rows, 1, "client_request_id alone finds the row")

	usage, err := repo.ListRequestAttemptUsageLogs(ctx, service.OpsRequestAttemptLookup{RequestIDs: []string{reqID, usageRequestID}})
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.Equal(t, usageID, usage[0].ID)
	require.Equal(t, usageRequestID, usage[0].RequestID)
	require.Equal(t, succeeded.ID, usage[0].AccountID)
	require.Equal(t, succeeded.Name, usage[0].AccountName)
	require.Equal(t, service.PlatformAnthropic, usage[0].Platform)
	require.Equal(t, int64(9800), *usage[0].DurationMs)
	require.Equal(t, int64(1400), *usage[0].FirstTokenMs)
	require.Equal(t, "up-"+suffix[:8], usage[0].UpstreamRequestID)

	// The usage read keeps the vendor account allow-list as defense in depth.
	scoped, err := repo.ListRequestAttemptUsageLogs(service.WithUsageAccountScope(ctx, []int64{failed.ID}), service.OpsRequestAttemptLookup{RequestIDs: []string{usageRequestID}})
	require.NoError(t, err)
	require.Empty(t, scoped)
	scoped, err = repo.ListRequestAttemptUsageLogs(service.WithUsageAccountScope(ctx, []int64{succeeded.ID}), service.OpsRequestAttemptLookup{RequestIDs: []string{usageRequestID}})
	require.NoError(t, err)
	require.Len(t, scoped, 1)

	// Time bounds apply to both tables.
	far := base.Add(48 * time.Hour)
	farEnd := far.Add(time.Hour)
	rows, err = repo.ListRequestAttemptErrorLogs(ctx, service.OpsRequestAttemptLookup{RequestIDs: []string{reqID}, ClientRequestIDs: []string{cid}, StartTime: &far, EndTime: &farEnd})
	require.NoError(t, err)
	require.Empty(t, rows)
	usage, err = repo.ListRequestAttemptUsageLogs(ctx, service.OpsRequestAttemptLookup{RequestIDs: []string{usageRequestID}, StartTime: &far, EndTime: &farEnd})
	require.NoError(t, err)
	require.Empty(t, usage)

	// Names include soft-deleted accounts: the chain outlives the account.
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at = NOW() WHERE id = $1", failed.ID)
	require.NoError(t, err)
	names, err := repo.ListRequestAttemptAccountNames(ctx, []int64{failed.ID, succeeded.ID})
	require.NoError(t, err)
	require.Equal(t, failed.Name, names[failed.ID])
	require.Equal(t, succeeded.Name, names[succeeded.ID])

	svc := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	around := base
	for _, tc := range []struct {
		id     string
		around *time.Time
	}{{reqID, nil}, {cid, &around}, {usageRequestID, nil}} {
		chain, err := svc.GetRequestAttempts(ctx, tc.id, tc.around)
		require.NoError(t, err, tc.id)
		require.Equal(t, reqID, chain.RequestID, tc.id)
		require.Equal(t, cid, chain.ClientRequestID, tc.id)
		require.NotNil(t, chain.ClientStatusCode)
		require.Equal(t, 200, *chain.ClientStatusCode)
		require.Equal(t, []int64{errorLogID}, chain.ErrorLogIDs)
		require.Len(t, chain.Attempts, 2, tc.id)
		first, second := chain.Attempts[0], chain.Attempts[1]
		require.Equal(t, "failed", first.Outcome)
		require.Equal(t, failed.ID, *first.AccountID)
		require.Equal(t, failed.Name, first.AccountName, "failover events get their name from accounts")
		require.Equal(t, int64(400), *first.DurationMs)
		require.Equal(t, 429, *first.UpstreamStatusCode)
		require.Equal(t, "success", second.Outcome)
		require.Equal(t, usageID, *second.UsageID)
		require.Equal(t, int64(9800), *second.DurationMs)
	}

	_, err = svc.GetRequestAttempts(ctx, "req-missing-"+suffix[:8], nil)
	require.ErrorIs(t, err, service.ErrOpsRequestNotFound)
}

// A client that reuses one X-Request-ID must see its newest requests in the
// 409 metadata, not the oldest rows of the retention window.
func TestOpsRequestAttempts_ReusedRequestIDListsNewestRows(t *testing.T) {
	ctx := context.Background()
	reqID := "reused-" + uuid.NewString()[:18]
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE request_id = $1", reqID)
	})

	base := time.Date(2012, 7, 8, 10, 0, 0, 0, time.UTC)
	const total = opsRequestAttemptErrorRowLimit + 3
	cids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		cid := uuid.NewString()
		cids = append(cids, cid)
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs
			(request_id, client_request_id, error_phase, error_type, severity, status_code, error_owner, created_at)
			VALUES ($1,$2,'upstream','upstream_error','P2',502,'provider',$3)`, reqID, cid, base.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
	}

	repo := NewOpsRepository(integrationDB).(*opsRepository)
	rows, err := repo.ListRequestAttemptErrorLogs(ctx, service.OpsRequestAttemptLookup{RequestIDs: []string{reqID}, ClientRequestIDs: []string{reqID}})
	require.NoError(t, err)
	require.Len(t, rows, opsRequestAttemptErrorRowLimit)
	require.Equal(t, cids[total-opsRequestAttemptErrorRowLimit], rows[0].ClientRequestID, "the newest rows are kept")
	require.Equal(t, cids[total-1], rows[len(rows)-1].ClientRequestID, "and returned oldest first")
	for i := 1; i < len(rows); i++ {
		require.True(t, rows[i-1].CreatedAt.Before(rows[i].CreatedAt))
	}

	svc := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = svc.GetRequestAttempts(ctx, reqID, nil)
	require.ErrorIs(t, err, service.ErrOpsRequestAmbiguous)
	md := infraerrors.FromError(err).Metadata
	require.Equal(t, strings.Join([]string{cids[total-1], cids[total-2], cids[total-3], cids[total-4], cids[total-5]}, ","), md["client_request_ids"])
	require.Equal(t, base.Add(time.Duration(total-1)*time.Minute).Format(time.RFC3339Nano), strings.Split(md["client_request_id_times"], ",")[0])
}

// A coalesced probe leader without X-Request-ID is billed as probe:<ops
// request_id>; ids that Postgres cannot store are rejected before querying.
func TestOpsRequestAttempts_ProbeUsageAndUnstorableIDs(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	reqID := uuid.NewString()
	cid := uuid.NewString()
	probeID := "probe:" + reqID

	user := mustCreateUser(t, client, &service.User{Email: "attempts-probe-" + suffix + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-attempts-probe-" + suffix})
	failed := mustCreateAccount(t, client, &service.Account{Name: "attempts-probe-a1-" + suffix, Platform: service.PlatformAnthropic})
	succeeded := mustCreateAccount(t, client, &service.Account{Name: "attempts-probe-a2-" + suffix, Platform: service.PlatformAnthropic})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE request_id = $1", reqID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE request_id = $1", probeID)
	})

	base := time.Date(2012, 9, 10, 10, 0, 0, 0, time.UTC)
	events := `[{"at_unix_ms":` + strconv.FormatInt(base.Add(-3*time.Second).UnixMilli(), 10) +
		`,"account_id":` + strconv.FormatInt(failed.ID, 10) + `,"upstream_status_code":429,"kind":"failover"}]`
	var errorLogID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_error_logs
		(request_id, client_request_id, user_id, error_phase, error_type, severity, status_code, upstream_errors, error_owner, created_at)
		VALUES ($1,$2,$3,'upstream','upstream_error','P2',200,$4::jsonb,'provider',$5) RETURNING id`,
		reqID, cid, user.ID, events, base).Scan(&errorLogID))
	var usageID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs
		(user_id, api_key_id, account_id, request_id, model, input_tokens, output_tokens, duration_ms, created_at)
		VALUES ($1,$2,$3,$4,'m',10,5,1200,$5) RETURNING id`,
		user.ID, key.ID, succeeded.ID, probeID, base.Add(-time.Second)).Scan(&usageID))

	svc := service.NewOpsService(NewOpsRepository(integrationDB), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, id := range []string{reqID, cid, probeID} {
		chain, err := svc.GetRequestAttempts(ctx, id, nil)
		require.NoError(t, err, id)
		require.Equal(t, reqID, chain.RequestID, id)
		require.Equal(t, cid, chain.ClientRequestID, id)
		require.Equal(t, []int64{errorLogID}, chain.ErrorLogIDs, id)
		require.Len(t, chain.Attempts, 2, id)
		require.Equal(t, failed.ID, *chain.Attempts[0].AccountID, id)
		require.Equal(t, "success", chain.Attempts[1].Outcome, id)
		require.Equal(t, usageID, *chain.Attempts[1].UsageID, id)
	}

	for _, id := range []string{"\x00", "req\x00" + suffix[:4], "\xff", "req-\xc3"} {
		_, err := svc.GetRequestAttempts(ctx, id, nil)
		require.Equal(t, "INVALID_REQUEST_ID", infraerrors.Reason(err), "%q", id)
	}
}
