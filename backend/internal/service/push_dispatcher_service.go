package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

const (
	pushDispatchInterval = 15 * time.Second
	// pushEventWindow is how far back events are scanned; events older than this when no instance ran are not
	// pushed late. Overlapping windows are harmless: deliveries are unique per (device, event).
	pushEventWindow = 10 * time.Minute
	// pushEventWindowAhead tolerates clock skew between the instance that wrote an event and the dispatcher.
	pushEventWindowAhead = time.Minute
	pushTickTimeout      = 90 * time.Second
	pushLeaderLockKey    = "push:dispatcher:leader"
	pushLeaderLockTTL    = 2 * time.Minute
	pushClaimLease       = 2 * time.Minute
	pushClaimBatch       = 50
	// pushMaxBatches and pushDeliverBudget bound one round of sending: batches are claimed until none is due, this
	// many were sent or the budget is spent (the tick timeout and the claim lease cover the last batch).
	pushMaxBatches      = 20
	pushDeliverBudget   = 30 * time.Second
	pushSendConcurrency = 8
	pushMaxAttempts     = 3
	pushDeliveryKeep    = 7 * 24 * time.Hour
	pushPurgeInterval   = time.Hour
	pushSeenTTL         = pushEventWindow + 5*time.Minute
)

// pushRetryBackoff is the delay after the 1st and 2nd failed attempt.
var pushRetryBackoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// pushAlertSettingsReader reads the runtime silencing that also suppresses alert emails.
type pushAlertSettingsReader interface {
	GetOpsAlertRuntimeSettings(ctx context.Context) (*OpsAlertRuntimeSettings, error)
}

// PushDispatcherService scans recent alert / account events, fans them out to the subscribed devices of active
// admins and sends due deliveries. One instance works at a time (leader lock); the unique delivery key and
// SKIP LOCKED claims keep each event to one push per device even when leadership moves.
type PushDispatcherService struct {
	repo        PushRepository
	senders     *PushSenders
	codec       *pushCredentialCodec
	alerts      pushAlertSettingsReader
	lockCache   LeaderLockCache
	db          *sql.DB
	defaultSite string
	instanceID  string
	now         func() time.Time

	seenMu    sync.Mutex
	seen      map[string]time.Time
	lastPurge time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

func NewPushDispatcherService(repo PushRepository, senders *PushSenders, encryptor SecretEncryptor, cfg *config.Config, alerts pushAlertSettingsReader, lockCache LeaderLockCache, db *sql.DB) *PushDispatcherService {
	return &PushDispatcherService{
		repo:        repo,
		senders:     senders,
		codec:       newPushCredentialCodec(encryptor, cfg),
		alerts:      alerts,
		lockCache:   lockCache,
		db:          db,
		defaultSite: pushDefaultSite(cfg),
		instanceID:  uuid.NewString(),
		now:         time.Now,
		seen:        map[string]time.Time{},
		stopCh:      make(chan struct{}),
	}
}

// Start runs the dispatcher loop in the background.
func (s *PushDispatcherService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.loop()
	})
}

// Stop ends the loop and waits for the current tick.
func (s *PushDispatcherService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
	s.wg.Wait()
}

func (s *PushDispatcherService) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(pushDispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *PushDispatcherService) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), pushTickTimeout)
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, pushLeaderLockKey, s.instanceID, pushLeaderLockTTL)
	if !ok {
		return
	}
	defer release()
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.push", "[Push] dispatcher tick panicked: %v", r)
		}
	}()
	s.RunOnce(ctx)
}

// RunOnce performs one dispatch cycle (exported for tests): fan out new events, then send due deliveries.
func (s *PushDispatcherService) RunOnce(ctx context.Context) {
	now := s.now()
	devices, err := s.repo.ListActiveAdminDevices(ctx)
	if err != nil {
		logger.LegacyPrintf("service.push", "[Push] list devices failed: %v", err)
		return
	}
	byID := make(map[int64]*PushDevice, len(devices))
	for _, d := range devices {
		byID[d.ID] = d
	}
	if len(devices) > 0 {
		s.fanout(ctx, now, devices, s.collect(ctx, now))
	}
	s.deliver(ctx, now, byID)
	s.maybePurge(ctx, now)
}

// collect reads the events of the scan window. Alert events covered by a scoped silence or the runtime silencing
// (the same rules as alert emails) are dropped.
func (s *PushDispatcherService) collect(ctx context.Context, now time.Time) []PushEvent {
	from, to := now.Add(-pushEventWindow), now.Add(pushEventWindowAhead)
	var events []PushEvent
	silenced := s.alertSilencer(ctx, now)

	if rows, err := s.repo.ListAlertEventsFiredBetween(ctx, from, to); err != nil {
		logger.LegacyPrintf("service.push", "[Push] scan fired alerts failed: %v", err)
	} else {
		for _, row := range rows {
			if !silenced(row) {
				events = append(events, newAlertFiredPushEvent(row))
			}
		}
	}
	if rows, err := s.repo.ListAlertEventsResolvedBetween(ctx, from, to); err != nil {
		logger.LegacyPrintf("service.push", "[Push] scan resolved alerts failed: %v", err)
	} else {
		for _, row := range rows {
			if !silenced(row) {
				events = append(events, newAlertResolvedPushEvent(row))
			}
		}
	}
	if rows, err := s.repo.ListAccountErrorsBetween(ctx, from, to); err != nil {
		logger.LegacyPrintf("service.push", "[Push] scan account errors failed: %v", err)
	} else {
		for _, row := range rows {
			events = append(events, newAccountErrorPushEvent(row))
		}
	}
	if rows, err := s.repo.ListAsyncTestRunsFinishedBetween(ctx, from, to); err != nil {
		logger.LegacyPrintf("service.push", "[Push] scan account test runs failed: %v", err)
	} else {
		for _, row := range rows {
			events = append(events, newAccountTestFinishedPushEvent(row))
		}
	}
	return events
}

func (s *PushDispatcherService) alertSilencer(ctx context.Context, now time.Time) func(*PushAlertEventRow) bool {
	var silencing *OpsAlertSilencingSettings
	if s.alerts != nil {
		if cfg, err := s.alerts.GetOpsAlertRuntimeSettings(ctx); err == nil && cfg != nil && cfg.Silencing.Enabled {
			silencing = &cfg.Silencing
		}
	}
	return func(row *PushAlertEventRow) bool {
		if row.Silenced {
			return true
		}
		if silencing == nil {
			return false
		}
		return isOpsAlertSilenced(now, &OpsAlertRule{ID: row.RuleID, Severity: row.Severity}, &OpsAlertEvent{Severity: row.Severity}, *silencing)
	}
}

// fanout writes one delivery per matching (device, event). Devices in quiet hours get a skipped row so the event is
// not pushed later; events older than the device are ignored.
func (s *PushDispatcherService) fanout(ctx context.Context, now time.Time, devices []*PushDevice, events []PushEvent) {
	s.pruneSeen(now)
	for i := range events {
		ev := &events[i]
		for _, d := range devices {
			if ev.RecipientUserID != 0 && ev.RecipientUserID != d.UserID {
				continue
			}
			if ev.OccurredAt.Before(d.CreatedAt) || !d.Subscription.Matches(ev) {
				continue
			}
			seenKey := fmt.Sprintf("%d|%s", d.ID, ev.DedupKey)
			if s.wasSeen(seenKey) {
				continue
			}
			delivery := &PushDelivery{
				DeviceID:      d.ID,
				DedupKey:      ev.DedupKey,
				EventType:     ev.Type,
				Payload:       ev.MessageFor(d.Locale, pushSiteFor(d, s.defaultSite)),
				Status:        PushDeliveryPending,
				NextAttemptAt: now,
			}
			if d.Subscription.QuietHours.Contains(now, d.Location()) {
				delivery.Status, delivery.LastError = PushDeliverySkipped, "quiet hours"
			}
			if _, err := s.repo.InsertDelivery(ctx, delivery); err != nil {
				logger.LegacyPrintf("service.push", "[Push] insert delivery failed: device=%d key=%s err=%v", d.ID, ev.DedupKey, err)
				continue
			}
			s.markSeen(seenKey, now)
		}
	}
}

func (s *PushDispatcherService) wasSeen(key string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	_, ok := s.seen[key]
	return ok
}

func (s *PushDispatcherService) markSeen(key string, now time.Time) {
	s.seenMu.Lock()
	s.seen[key] = now
	s.seenMu.Unlock()
}

func (s *PushDispatcherService) pruneSeen(now time.Time) {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	for k, at := range s.seen {
		if now.Sub(at) > pushSeenTTL {
			delete(s.seen, k)
		}
	}
}

// deliver sends due deliveries batch by batch until none is due, pushMaxBatches were claimed or pushDeliverBudget is
// spent. The round remembers disabled and unreachable devices across batches, so a dead endpoint costs one timeout
// per round instead of one per delivery and does not hold back the pushes of other devices.
func (s *PushDispatcherService) deliver(ctx context.Context, now time.Time, devices map[int64]*PushDevice) {
	round := newPushRound()
	started := time.Now()
	for i := 0; i < pushMaxBatches && ctx.Err() == nil; i++ {
		claimed, err := s.repo.ClaimDueDeliveries(ctx, now, pushClaimLease, pushClaimBatch)
		if err != nil {
			logger.LegacyPrintf("service.push", "[Push] claim deliveries failed: %v", err)
			return
		}
		s.sendBatch(ctx, now, claimed, devices, round)
		if len(claimed) < pushClaimBatch || time.Since(started) >= pushDeliverBudget {
			return
		}
	}
}

// sendBatch sends claimed deliveries with bounded concurrency.
func (s *PushDispatcherService) sendBatch(ctx context.Context, now time.Time, claimed []*PushDelivery, devices map[int64]*PushDevice, round *pushRound) {
	sem := make(chan struct{}, pushSendConcurrency)
	var wg sync.WaitGroup
	for _, d := range claimed {
		wg.Add(1)
		sem <- struct{}{}
		go func(d *PushDelivery) {
			defer wg.Done()
			defer func() { <-sem }()
			s.deliverOne(ctx, now, d, devices[d.DeviceID], round)
		}(d)
	}
	wg.Wait()
}

// pushSendOutcome is a sender's answer to one delivery.
type pushSendOutcome struct {
	result PushSendResult
	err    error
}

// unreachable reports a network failure before any HTTP answer (timeout, refused, DNS): another delivery to the same
// target right now would most likely wait for the same timeout.
func (o pushSendOutcome) unreachable() bool {
	return o.err != nil && o.result.ProviderStatus == 0 && o.result.Retryable
}

// pushRound is what one round of sending learned about devices: a disabled device gets nothing more, and an
// unreachable one is not dialled again (its other deliveries reuse the failure and are retried with backoff).
type pushRound struct {
	mu          sync.Mutex
	disabled    map[int64]bool
	unreachable map[int64]pushSendOutcome
}

func newPushRound() *pushRound {
	return &pushRound{disabled: map[int64]bool{}, unreachable: map[int64]pushSendOutcome{}}
}

func (r *pushRound) isDisabled(deviceID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.disabled[deviceID]
}

func (r *pushRound) disable(deviceID int64) {
	r.mu.Lock()
	r.disabled[deviceID] = true
	r.mu.Unlock()
}

func (r *pushRound) unreachableOutcome(deviceID int64) (pushSendOutcome, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, ok := r.unreachable[deviceID]
	return out, ok
}

func (r *pushRound) record(deviceID int64, out pushSendOutcome) {
	if !out.unreachable() {
		return
	}
	r.mu.Lock()
	r.unreachable[deviceID] = out
	r.mu.Unlock()
}

// deliverOne sends one claimed delivery (or reuses the round's failure of an unreachable target) and records the
// outcome.
func (s *PushDispatcherService) deliverOne(ctx context.Context, now time.Time, d *PushDelivery, device *PushDevice, round *pushRound) {
	if device == nil || round.isDisabled(device.ID) {
		s.finishDelivery(ctx, d.ID, PushDeliverySkipped, "device disabled, deleted or its owner is no longer an active admin", nil, nil)
		return
	}
	if d.Attempts > pushMaxAttempts {
		s.finishDelivery(ctx, d.ID, PushDeliveryFailed, "too many attempts", nil, nil)
		return
	}
	creds, err := s.codec.open(device.CredentialsRaw, device.CredentialsEncrypted)
	if err != nil {
		round.disable(device.ID)
		s.markDeviceError(ctx, device.ID, "stored credentials cannot be decrypted", PushDisabledSecretUndecryptable, now)
		s.finishDelivery(ctx, d.ID, PushDeliveryFailed, PushDisabledSecretUndecryptable, nil, nil)
		return
	}
	out, reused := round.unreachableOutcome(device.ID)
	if !reused {
		msg := d.Payload
		msg.DeliveryID = fmt.Sprintf("pd_%d", d.ID)
		sendCtx, cancel := context.WithTimeout(ctx, pushSendTimeout+2*time.Second)
		out.result, out.err = s.senders.Send(sendCtx, pushTargetOf(device, creds), &msg)
		cancel()
		round.record(device.ID, out)
	}
	s.recordOutcome(ctx, now, d, device, out, reused, round)
}

// recordOutcome finishes a delivery: sent, failed for good (disabling the device when the provider says the target is
// gone) or pending again with backoff. A reused unreachable outcome is not written to the device a second time.
func (s *PushDispatcherService) recordOutcome(ctx context.Context, now time.Time, d *PushDelivery, device *PushDevice, out pushSendOutcome, reused bool, round *pushRound) {
	if out.err == nil {
		sentAt := s.now()
		s.finishDelivery(ctx, d.ID, PushDeliverySent, "", nil, &sentAt)
		if err := s.repo.MarkDeviceSuccess(ctx, device.ID, sentAt); err != nil {
			logger.LegacyPrintf("service.push", "[Push] record success failed: device=%d err=%v", device.ID, err)
		}
		return
	}
	if errors.Is(out.err, ErrPushNotConfigured) {
		s.finishDelivery(ctx, d.ID, PushDeliveryFailed, "APNs is not configured", nil, nil)
		return
	}
	failure := describePushFailure(out.result, out.err)
	if reused {
		failure = sanitizePushText("not sent, the target did not answer earlier in this round: "+failure, 300)
	} else {
		s.markDeviceError(ctx, device.ID, failure, out.result.DisableReason, now)
	}
	if out.result.DisableReason != "" {
		round.disable(device.ID)
		s.finishDelivery(ctx, d.ID, PushDeliveryFailed, failure, nil, nil)
		return
	}
	if out.result.Retryable && d.Attempts < pushMaxAttempts {
		retryAt := now.Add(pushRetryBackoff[min(d.Attempts-1, len(pushRetryBackoff)-1)])
		s.finishDelivery(ctx, d.ID, PushDeliveryPending, failure, &retryAt, nil)
		return
	}
	s.finishDelivery(ctx, d.ID, PushDeliveryFailed, failure, nil, nil)
}

func (s *PushDispatcherService) finishDelivery(ctx context.Context, id int64, status, lastError string, retryAt, sentAt *time.Time) {
	if err := s.repo.FinishDelivery(ctx, id, status, lastError, retryAt, sentAt); err != nil {
		logger.LegacyPrintf("service.push", "[Push] finish delivery failed: delivery=%d err=%v", id, err)
	}
}

func (s *PushDispatcherService) markDeviceError(ctx context.Context, id int64, message, disableReason string, at time.Time) {
	if err := s.repo.MarkDeviceError(ctx, id, strings.TrimSpace(message), disableReason, at); err != nil {
		logger.LegacyPrintf("service.push", "[Push] record failure failed: device=%d err=%v", id, err)
	}
}

func (s *PushDispatcherService) maybePurge(ctx context.Context, now time.Time) {
	if !s.lastPurge.IsZero() && now.Sub(s.lastPurge) < pushPurgeInterval {
		return
	}
	s.lastPurge = now
	if _, err := s.repo.PurgeDeliveriesBefore(ctx, now.Add(-pushDeliveryKeep)); err != nil {
		logger.LegacyPrintf("service.push", "[Push] purge deliveries failed: %v", err)
	}
}
