package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type pushStaticAlertSettings struct{ cfg *OpsAlertRuntimeSettings }

func (s pushStaticAlertSettings) GetOpsAlertRuntimeSettings(context.Context) (*OpsAlertRuntimeSettings, error) {
	return s.cfg, nil
}

var pushTestNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func newPushDispatcherForTest(repo *pushMemRepo, sender *pushFakeSender, alerts pushAlertSettingsReader) *PushDispatcherService {
	cfg := &config.Config{}
	cfg.Server.FrontendURL = "https://api.example.com"
	s := NewPushDispatcherService(repo, pushFakeSenders(sender), nil, cfg, alerts, nil, nil)
	s.now = func() time.Time { return pushTestNow }
	return s
}

func pushPlainCreds(token string) string {
	raw, _, _ := (&pushCredentialCodec{}).seal(PushCredentials{Token: token})
	return raw
}

func pushTestDevice(userID int64, token string, sub PushSubscription) *PushDevice {
	return &PushDevice{
		UserID: userID, Platform: PushPlatformIOS, TokenHash: token, CredentialsRaw: pushPlainCreds(token),
		Subscription: sub, CreatedAt: pushTestNow.Add(-time.Hour), Locale: "zh-Hans",
	}
}

func firedRow(id int64, severity string, at time.Time) *PushAlertEventRow {
	value, threshold, group := 12.346, 5.0, int64(7)
	return &PushAlertEventRow{
		EventID: id, RuleID: 3, Severity: severity, Status: OpsAlertStatusFiring, RuleName: "错误率", Operator: ">",
		WindowMinutes: 5, MetricValue: &value, ThresholdValue: &threshold, Platform: "anthropic", GroupID: &group, FiredAt: at,
	}
}

func TestPushDispatcher_FanoutDedupAndQuietHours(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	sender := &pushFakeSender{}
	normal := repo.addDevice(pushTestDevice(1, "tok-normal", DefaultPushSubscription()))
	quietSub := DefaultPushSubscription()
	quietSub.QuietHours = &PushQuietHours{Start: "19:00", End: "21:00"} // 20:00 in Asia/Shanghai
	quiet := pushTestDevice(1, "tok-quiet", quietSub)
	quiet.Timezone = "Asia/Shanghai"
	quiet = repo.addDevice(quiet)
	late := pushTestDevice(2, "tok-late", DefaultPushSubscription())
	late.CreatedAt = pushTestNow.Add(-time.Minute)
	late = repo.addDevice(late)

	repo.fired = []*PushAlertEventRow{firedRow(881, "P1", pushTestNow.Add(-2*time.Minute))}
	d := newPushDispatcherForTest(repo, sender, nil)
	d.RunOnce(ctx)

	sends := sender.sends()
	require.Len(t, sends, 1, "only the device outside quiet hours is pushed")
	require.Equal(t, "tok-normal", sends[0].Target.Token)
	msg := sends[0].Message
	require.Equal(t, PushEventOpsAlertFired, msg.Type)
	require.Equal(t, "[P1] 错误率", msg.Title)
	require.Equal(t, "当前 12.35（阈值 > 5） · 近 5 分钟 · anthropic · 分组 #7", msg.Body)
	require.Equal(t, "time-sensitive", msg.InterruptionLevel)
	require.Equal(t, "api.example.com", msg.Site)
	require.Equal(t, int64(881), msg.Data["alert_event_id"])
	require.Equal(t, "pd_"+strconv.FormatInt(repo.deliveriesFor(normal.ID)[0].ID, 10), msg.DeliveryID)
	require.Equal(t, PushDeliverySent, repo.deliveriesFor(normal.ID)[0].Status)
	require.NotNil(t, repo.device(normal.ID).LastSuccessAt)

	quietRows := repo.deliveriesFor(quiet.ID)
	require.Len(t, quietRows, 1)
	require.Equal(t, PushDeliverySkipped, quietRows[0].Status, "quiet hours skip the event for good")
	require.Empty(t, repo.deliveriesFor(late.ID), "events older than the device are not pushed")

	d.RunOnce(ctx)
	require.Len(t, sender.sends(), 1, "the same event is not pushed twice")

	other := newPushDispatcherForTest(repo, sender, nil) // another instance, empty in-memory cache
	other.RunOnce(ctx)
	require.Len(t, sender.sends(), 1, "the unique delivery key stops a second instance")
}

func TestPushDispatcher_PermanentFailureDisablesAndRetries(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	sender := &pushFakeSender{}
	gone := repo.addDevice(pushTestDevice(1, "tok-gone", DefaultPushSubscription()))
	flaky := repo.addDevice(pushTestDevice(1, "tok-flaky", DefaultPushSubscription()))
	sender.result = func(target PushTarget) (PushSendResult, error) {
		if target.Token == "tok-gone" {
			return PushSendResult{ProviderStatus: 410, ProviderReason: "Unregistered", DisableReason: PushDisabledUnregistered}, errPushProviderRejected
		}
		return PushSendResult{ProviderStatus: 503, ProviderReason: "ServiceUnavailable", Retryable: true}, errPushProviderRejected
	}
	repo.fired = []*PushAlertEventRow{firedRow(1, "P0", pushTestNow.Add(-time.Minute)), firedRow(2, "P0", pushTestNow.Add(-time.Minute))}

	d := newPushDispatcherForTest(repo, sender, nil)
	d.RunOnce(ctx)
	require.Equal(t, PushDisabledUnregistered, repo.device(gone.ID).DisabledReason)
	goneRows := repo.deliveriesFor(gone.ID)
	require.Len(t, goneRows, 2)
	statuses := []string{goneRows[0].Status, goneRows[1].Status}
	require.Contains(t, statuses, PushDeliveryFailed)

	flakyRows := repo.deliveriesFor(flaky.ID)
	require.Equal(t, PushDeliveryPending, flakyRows[0].Status, "5xx is retried")
	require.Equal(t, pushTestNow.Add(30*time.Second), flakyRows[0].NextAttemptAt)
	require.Empty(t, repo.device(flaky.ID).DisabledReason)

	sender.result = nil
	before := len(sender.sends())
	d.now = func() time.Time { return pushTestNow.Add(31 * time.Second) }
	d.RunOnce(ctx)
	require.Len(t, sender.sends(), before+2, "both pending deliveries of the flaky device are retried; the disabled device gets nothing")
	require.Equal(t, PushDeliverySent, repo.deliveriesFor(flaky.ID)[0].Status)
}

func TestPushDispatcher_SilencedAlertNotPushed(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	sender := &pushFakeSender{}
	repo.addDevice(pushTestDevice(1, "tok", DefaultPushSubscription()))
	scoped := firedRow(10, "P0", pushTestNow.Add(-time.Minute))
	scoped.Silenced = true
	repo.fired = []*PushAlertEventRow{scoped, firedRow(11, "P0", pushTestNow.Add(-time.Minute))}
	repo.accounts = []*PushAccountErrorRow{{AccountID: 5, Platform: "openai", ErrorMessage: "Authentication failed (401): invalid api key sk-abcdefgh1234567890", ErrorAt: pushTestNow.Add(-time.Minute)}}

	runtime := defaultOpsAlertRuntimeSettings()
	runtime.Silencing.Enabled = true
	runtime.Silencing.GlobalUntilRFC3339 = pushTestNow.Add(time.Hour).Format(time.RFC3339)
	d := newPushDispatcherForTest(repo, sender, pushStaticAlertSettings{cfg: runtime})
	d.RunOnce(ctx)

	sends := sender.sends()
	require.Len(t, sends, 1, "silenced alerts are dropped like alert emails; account errors still go out")
	msg := sends[0].Message
	require.Equal(t, PushEventAccountError, msg.Type)
	require.Equal(t, "账号异常 #5", msg.Title)
	require.Equal(t, "openai · 认证失败 (401)", msg.Body)
	require.NotContains(t, msg.Body, "sk-", "raw upstream error text is never pushed")
	require.Equal(t, int64(5), msg.Data["account_id"])
}

func TestPushDispatcher_TestFinishedOnlyToInitiatorAndResolved(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	sender := &pushFakeSender{}
	sub := DefaultPushSubscription()
	sub.Events = []string{PushEventOpsAlertResolved, PushEventAccountTestFinished}
	sub.GroupIDs = []int64{99}
	starter := pushTestDevice(1, "tok-starter", sub)
	starter.Locale = "en-US"
	repo.addDevice(starter)
	repo.addDevice(pushTestDevice(2, "tok-other", sub))

	latency := int64(812)
	repo.testRuns = []*PushTestRunRow{{RunID: "tr_abc", AccountID: 9, Platform: "anthropic", Status: AccountTestRunStatusSuccess,
		ModelID: "claude-sonnet", LatencyMs: &latency, FinishedAt: pushTestNow.Add(-time.Minute), CreatedBy: 1, GroupIDs: []int64{3}}}
	resolved := firedRow(20, "P2", pushTestNow.Add(-20*time.Minute))
	resolvedAt := pushTestNow.Add(-time.Minute)
	resolved.ResolvedAt, resolved.Status, resolved.GroupID = &resolvedAt, OpsAlertStatusManualResolved, nil
	repo.resolved = []*PushAlertEventRow{resolved}

	newPushDispatcherForTest(repo, sender, nil).RunOnce(ctx)
	byToken := map[string][]PushMessage{}
	for _, s := range sender.sends() {
		byToken[s.Target.Token] = append(byToken[s.Target.Token], s.Message)
	}
	require.Len(t, byToken["tok-starter"], 1, "min_severity P1 drops the P2 resolution")
	require.Empty(t, byToken["tok-other"], "test results go to the admin who started the test")
	msg := byToken["tok-starter"][0]
	require.Equal(t, "Account test passed #9", msg.Title)
	require.Equal(t, "anthropic · claude-sonnet · 812 ms", msg.Body)
	require.Equal(t, "tr_abc", msg.Data["run_id"])

	sender2 := &pushFakeSender{}
	repo2 := newPushMemRepo()
	sub.MinSeverity = "P3"
	repo2.addDevice(pushTestDevice(2, "tok", sub))
	repo2.resolved = []*PushAlertEventRow{resolved}
	newPushDispatcherForTest(repo2, sender2, nil).RunOnce(ctx)
	require.Len(t, sender2.sends(), 1)
	require.Equal(t, "[P2] 已恢复：错误率", sender2.sends()[0].Message.Title)
	require.Equal(t, "已手动解决 · 持续 19 分钟 · anthropic", sender2.sends()[0].Message.Body)
}

func TestPushDispatcher_DemotedAdminAndUndecryptable(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	sender := &pushFakeSender{}
	broken := pushTestDevice(1, "tok-broken", DefaultPushSubscription())
	broken.CredentialsRaw, broken.CredentialsEncrypted = "not-decryptable", true
	broken = repo.addDevice(broken)
	demoted := repo.addDevice(pushTestDevice(2, "tok-demoted", DefaultPushSubscription()))
	repo.fired = []*PushAlertEventRow{firedRow(30, "P0", pushTestNow.Add(-time.Minute))}

	d := newPushDispatcherForTest(repo, sender, nil)
	repo.mu.Lock()
	repo.users[2] = false // no longer an active admin
	repo.mu.Unlock()
	d.RunOnce(ctx)

	require.Empty(t, sender.sends())
	require.Empty(t, repo.deliveriesFor(demoted.ID))
	require.Equal(t, PushDisabledSecretUndecryptable, repo.device(broken.ID).DisabledReason)
}

// A target that does not answer (timeout, refused) is dialled once per round: its other deliveries reuse that failure
// and are retried later, and the round keeps claiming batches, so a dead endpoint does not hold back other devices.
func TestPushDispatcher_UnreachableTargetDoesNotHoldBackOthers(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	sender := &pushFakeSender{}
	dead := pushTestDevice(1, "tok-dead", DefaultPushSubscription())
	dead.Platform = PushPlatformWebhook
	dead = repo.addDevice(dead)
	ok := repo.addDevice(pushTestDevice(2, "tok-ok", DefaultPushSubscription()))
	sender.result = func(target PushTarget) (PushSendResult, error) {
		if target.Token == "tok-dead" {
			return PushSendResult{ProviderReason: "dial tcp 203.0.113.9:443: i/o timeout", Retryable: true}, errors.New("i/o timeout")
		}
		return PushSendResult{ProviderStatus: 200}, nil
	}
	const events = 40 // 80 deliveries: more than one claim batch
	for i := int64(1); i <= events; i++ {
		repo.fired = append(repo.fired, firedRow(1000+i, "P0", pushTestNow.Add(-time.Minute)))
	}

	newPushDispatcherForTest(repo, sender, nil).RunOnce(ctx)

	deadSends := 0
	for _, s := range sender.sends() {
		if s.Target.Token == "tok-dead" {
			deadSends++
		}
	}
	require.LessOrEqual(t, deadSends, pushSendConcurrency, "only the deliveries already in flight dial the dead target")
	for _, row := range repo.deliveriesFor(ok.ID) {
		require.Equal(t, PushDeliverySent, row.Status, "every delivery of the healthy device goes out in the same round")
	}
	require.Len(t, repo.deliveriesFor(ok.ID), events)
	deadRows := repo.deliveriesFor(dead.ID)
	require.Len(t, deadRows, events)
	for _, row := range deadRows {
		require.Equal(t, PushDeliveryPending, row.Status)
		require.Equal(t, 1, row.Attempts)
		require.Equal(t, pushTestNow.Add(30*time.Second), row.NextAttemptAt, "retried with the normal backoff")
		require.Contains(t, row.LastError, "i/o timeout")
	}
	require.Empty(t, repo.device(dead.ID).DisabledReason, "an unreachable target is not disabled")
}
