//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newPushTestAdmin(t *testing.T) *service.User {
	t.Helper()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Email: "push-" + uuid.NewString() + "@example.com", Role: service.RoleAdmin})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM push_devices WHERE user_id = $1", u.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", u.ID)
	})
	return u
}

func pushTestDeviceRow(userID int64, token string) *service.PushDevice {
	return &service.PushDevice{
		UserID: userID, Platform: service.PushPlatformIOS, TokenHash: token, CredentialsRaw: `{"token":"x"}`,
		TokenHint: "abcd", Environment: "production", DeviceName: "iPhone", Locale: "zh-Hans", Timezone: "Asia/Shanghai",
		Subscription: service.DefaultPushSubscription(),
	}
}

func TestPushRepo_RegisterUpsertAndOwnership(t *testing.T) {
	ctx := context.Background()
	repo := NewPushRepository(integrationDB)
	owner, other := newPushTestAdmin(t), newPushTestAdmin(t)
	hash := uuid.NewString()

	d := pushTestDeviceRow(owner.ID, hash)
	created, err := repo.UpsertDevice(ctx, d, false)
	require.NoError(t, err)
	require.True(t, created)
	require.NotZero(t, d.ID)
	firstID := d.ID

	require.NoError(t, repo.MarkDeviceError(ctx, firstID, "HTTP 410: Unregistered", service.PushDisabledUnregistered, time.Now()))
	sub := service.DefaultPushSubscription()
	sub.MinSeverity = "P3"
	_, err = repo.UpdateDevicePrefs(ctx, owner.ID, firstID, sub)
	require.NoError(t, err)

	again := pushTestDeviceRow(owner.ID, hash)
	again.DeviceName = "iPhone 17"
	created, err = repo.UpsertDevice(ctx, again, false)
	require.NoError(t, err)
	require.False(t, created, "the same token updates the device")
	require.Equal(t, firstID, again.ID)
	require.Equal(t, "P3", again.Subscription.MinSeverity, "prefs are kept unless replaced")
	got, err := repo.GetDeviceForUser(ctx, owner.ID, firstID)
	require.NoError(t, err)
	require.Equal(t, "iPhone 17", got.DeviceName)
	require.Empty(t, got.DisabledReason, "re-registering enables a disabled device")
	require.Equal(t, "HTTP 410: Unregistered", got.LastError)

	n, err := repo.CountDevicesByUserExcept(ctx, owner.ID, service.PushPlatformIOS, hash)
	require.NoError(t, err)
	require.Zero(t, n)

	moved := pushTestDeviceRow(other.ID, hash)
	created, err = repo.UpsertDevice(ctx, moved, false)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, firstID, moved.ID)
	require.Equal(t, "P1", moved.Subscription.MinSeverity, "a new owner starts from the given prefs")
	_, err = repo.GetDeviceForUser(ctx, owner.ID, firstID)
	require.ErrorIs(t, err, service.ErrPushDeviceNotFound, "the device moved to the other admin")
	movedRow, err := repo.GetDeviceForUser(ctx, other.ID, firstID)
	require.NoError(t, err)
	require.Empty(t, movedRow.LastError, "the previous owner's history is cleared")

	list, err := repo.ListDevicesByUser(ctx, owner.ID)
	require.NoError(t, err)
	require.Empty(t, list)
	list, err = repo.ListDevicesByUser(ctx, other.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.ErrorIs(t, repo.DeleteDeviceForUser(ctx, owner.ID, firstID), service.ErrPushDeviceNotFound)
	_, err = repo.UpdateDevicePrefs(ctx, owner.ID, firstID, sub)
	require.ErrorIs(t, err, service.ErrPushDeviceNotFound)
	require.NoError(t, repo.DeleteDeviceForUser(ctx, other.ID, firstID))
	require.ErrorIs(t, repo.DeleteDeviceForUser(ctx, other.ID, firstID), service.ErrPushDeviceNotFound)
}

func TestPushRepo_DeliveryClaimOnce(t *testing.T) {
	ctx := context.Background()
	repo := NewPushRepository(integrationDB)
	owner := newPushTestAdmin(t)
	d := pushTestDeviceRow(owner.ID, uuid.NewString())
	_, err := repo.UpsertDevice(ctx, d, false)
	require.NoError(t, err)

	now := time.Now()
	key := "ops_alert.fired:" + uuid.NewString()
	var wg sync.WaitGroup
	var mu sync.Mutex
	inserted := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.InsertDelivery(ctx, &service.PushDelivery{
				DeviceID: d.ID, DedupKey: key, EventType: service.PushEventOpsAlertFired, Status: service.PushDeliveryPending,
				NextAttemptAt: now.Add(-time.Second),
				Payload:       service.PushMessage{Type: service.PushEventOpsAlertFired, Title: "t", Data: map[string]any{"alert_event_id": int64(881)}},
			})
			require.NoError(t, err)
			if ok {
				mu.Lock()
				inserted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, inserted, "each (device, event) is stored once")

	claims := make([][]*service.PushDelivery, 2)
	for i := range claims {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := repo.ClaimDueDeliveries(ctx, now, 2*time.Minute, 1000)
			require.NoError(t, err)
			for _, c := range got {
				if c.DeviceID == d.ID {
					claims[i] = append(claims[i], c)
				}
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, 1, len(claims[0])+len(claims[1]), "two instances never claim the same delivery")
	claimed := append(claims[0], claims[1]...)[0]
	require.Equal(t, 1, claimed.Attempts)
	require.Equal(t, service.PushDeliverySending, claimed.Status)
	require.Equal(t, "881", jsonNumberString(claimed.Payload.Data["alert_event_id"]))

	again, err := repo.ClaimDueDeliveries(ctx, now, 2*time.Minute, 1000)
	require.NoError(t, err)
	for _, c := range again {
		require.NotEqual(t, claimed.ID, c.ID, "a leased delivery is not claimed again")
	}
	expired, err := repo.ClaimDueDeliveries(ctx, now.Add(3*time.Minute), 2*time.Minute, 1000)
	require.NoError(t, err)
	found := false
	for _, c := range expired {
		if c.ID == claimed.ID {
			found = true
			require.Equal(t, 2, c.Attempts, "an expired lease (crashed instance) is claimed again")
		}
	}
	require.True(t, found)

	sentAt := time.Now()
	require.NoError(t, repo.FinishDelivery(ctx, claimed.ID, service.PushDeliverySent, "", nil, &sentAt))
	after, err := repo.ClaimDueDeliveries(ctx, now.Add(time.Hour), 2*time.Minute, 1000)
	require.NoError(t, err)
	for _, c := range after {
		require.NotEqual(t, claimed.ID, c.ID, "sent deliveries are never claimed")
	}
	require.NoError(t, repo.MarkDeviceSuccess(ctx, d.ID, sentAt))
	_, err = repo.PurgeDeliveriesBefore(ctx, time.Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
}

func jsonNumberString(v any) string {
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}

func TestPushRepo_ListActiveAdminDevicesExcludesDemoted(t *testing.T) {
	ctx := context.Background()
	repo := NewPushRepository(integrationDB)
	admin, demoted, disabledUser := newPushTestAdmin(t), newPushTestAdmin(t), newPushTestAdmin(t)
	ids := map[string]int64{}
	for name, u := range map[string]*service.User{"admin": admin, "demoted": demoted, "disabled": disabledUser} {
		d := pushTestDeviceRow(u.ID, uuid.NewString())
		_, err := repo.UpsertDevice(ctx, d, false)
		require.NoError(t, err)
		ids[name] = d.ID
	}
	off := pushTestDeviceRow(admin.ID, uuid.NewString())
	_, err := repo.UpsertDevice(ctx, off, false)
	require.NoError(t, err)
	require.NoError(t, repo.MarkDeviceError(ctx, off.ID, "gone", service.PushDisabledUnregistered, time.Now()))

	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET role = 'user' WHERE id = $1", demoted.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = $1", disabledUser.ID)
	require.NoError(t, err)

	devices, err := repo.ListActiveAdminDevices(ctx)
	require.NoError(t, err)
	seen := map[int64]bool{}
	for _, d := range devices {
		seen[d.ID] = true
	}
	require.True(t, seen[ids["admin"]])
	require.False(t, seen[ids["demoted"]], "a demoted admin gets no pushes")
	require.False(t, seen[ids["disabled"]], "a disabled admin gets no pushes")
	require.False(t, seen[off.ID], "a disabled device gets no pushes")
}

func TestPushRepo_EventSourceQueries(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewPushRepository(integrationDB)
	admin := newPushTestAdmin(t)
	now := time.Now().UTC()

	var ruleID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_alert_rules (name, severity, metric_type, operator, threshold, window_minutes)
 VALUES ($1, 'P1', 'error_rate', '>', 5, 10) RETURNING id`, "push-rule-"+uuid.NewString()).Scan(&ruleID))
	var firedID, silencedID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_alert_events (rule_id, severity, status, title, metric_value, threshold_value, dimensions, fired_at, resolved_at, rule_snapshot)
 VALUES ($1, 'P1', 'resolved', 'P1: x', 12.5, 5, '{"platform":"anthropic","group_id":7}', $2, $3, '{"name":"快照里的规则名","operator":">=","window_minutes":5}') RETURNING id`,
		ruleID, now.Add(-3*time.Minute), now.Add(-time.Minute)).Scan(&firedID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_alert_events (rule_id, severity, status, title, dimensions, fired_at)
 VALUES ($1, 'P0', 'firing', 'P0: y', '{"platform":"openai"}', $2) RETURNING id`, ruleID, now.Add(-2*time.Minute)).Scan(&silencedID))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_alert_silences (rule_id, platform, until, reason) VALUES ($1, 'openai', $2, 'push test')`,
		ruleID, now.Add(time.Hour))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM ops_alert_silences WHERE rule_id = $1", ruleID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM ops_alert_events WHERE rule_id = $1", ruleID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM ops_alert_rules WHERE id = $1", ruleID)
	})

	fired, err := repo.ListAlertEventsFiredBetween(ctx, now.Add(-10*time.Minute), now.Add(time.Minute))
	require.NoError(t, err)
	byID := map[int64]*service.PushAlertEventRow{}
	for _, row := range fired {
		byID[row.EventID] = row
	}
	row := byID[firedID]
	require.NotNil(t, row)
	require.Equal(t, "快照里的规则名", row.RuleName, "the D06 snapshot name wins")
	require.Equal(t, ">=", row.Operator)
	require.Equal(t, 5, row.WindowMinutes)
	require.Equal(t, "anthropic", row.Platform)
	require.Equal(t, int64(7), *row.GroupID)
	require.InDelta(t, 12.5, *row.MetricValue, 0.001)
	require.False(t, row.Silenced)
	require.NotNil(t, byID[silencedID])
	require.True(t, byID[silencedID].Silenced, "an active scoped silence covers the event")
	require.Equal(t, byID[silencedID].RuleName[:10], "push-rule-", "without a snapshot the current rule name is used")

	resolved, err := repo.ListAlertEventsResolvedBetween(ctx, now.Add(-2*time.Minute), now.Add(time.Minute))
	require.NoError(t, err)
	found := false
	for _, r := range resolved {
		if r.EventID == firedID {
			found = true
		}
		require.NotEqual(t, silencedID, r.EventID, "firing events are not resolved")
	}
	require.True(t, found)
	early, err := repo.ListAlertEventsFiredBetween(ctx, now.Add(-time.Hour), now.Add(-30*time.Minute))
	require.NoError(t, err)
	for _, r := range early {
		require.NotEqual(t, firedID, r.EventID, "outside the window")
	}

	group := mustCreateGroup(t, client, &service.Group{Name: "push-g-" + uuid.NewString()})
	account := mustCreateAccount(t, client, &service.Account{Name: "push-a-" + uuid.NewString()})
	deleted := mustCreateAccount(t, client, &service.Account{Name: "push-d-" + uuid.NewString()})
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_test_runs WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE account_id IN ($1, $2)", account.ID, deleted.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2)", account.ID, deleted.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", group.ID)
	})
	// The same columns accountRepository.SetError writes (status, error_message, error_at).
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET status = 'error', error_message = 'Authentication failed (401): bad key',
 error_at = NOW() WHERE id IN ($1, $2)`, account.ID, deleted.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at = NOW() WHERE id = $1", deleted.ID)
	require.NoError(t, err)

	errs, err := repo.ListAccountErrorsBetween(ctx, now.Add(-time.Minute), time.Now().Add(time.Minute))
	require.NoError(t, err)
	var accountRow *service.PushAccountErrorRow
	for _, r := range errs {
		require.NotEqual(t, deleted.ID, r.AccountID, "deleted accounts are not pushed")
		if r.AccountID == account.ID {
			accountRow = r
		}
	}
	require.NotNil(t, accountRow)
	require.Equal(t, service.PlatformAnthropic, accountRow.Platform)
	require.Equal(t, []int64{group.ID}, accountRow.GroupIDs)
	require.Contains(t, accountRow.ErrorMessage, "Authentication failed")

	runID := "tr_" + uuid.NewString()
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_test_runs (run_id, account_id, source, status, model_id, latency_ms, upstream_status_code, finished_at, created_by)
 VALUES ($1, $2, 'async', 'failed', 'claude-x', 900, 401, NOW(), $3), ($4, $2, 'manual', 'success', 'm', 1, NULL, NOW(), $3)`,
		runID, account.ID, admin.ID, "tr_"+uuid.NewString())
	require.NoError(t, err)
	runs, err := repo.ListAsyncTestRunsFinishedBetween(ctx, now.Add(-time.Minute), time.Now().Add(time.Minute))
	require.NoError(t, err)
	var runRow *service.PushTestRunRow
	for _, r := range runs {
		if r.AccountID == account.ID {
			require.Nil(t, runRow, "manual SSE tests are not pushed")
			runRow = r
		}
	}
	require.NotNil(t, runRow)
	require.Equal(t, runID, runRow.RunID)
	require.Equal(t, admin.ID, runRow.CreatedBy)
	require.Equal(t, 401, *runRow.UpstreamStatusCode)
	require.Equal(t, int64(900), *runRow.LatencyMs)
	require.Equal(t, []int64{group.ID}, runRow.GroupIDs)
}
