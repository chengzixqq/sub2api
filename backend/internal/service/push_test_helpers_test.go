package service

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// pushHexEncryptor stands in for AES: the ciphertext does not contain the plaintext, and decrypting something it did
// not produce fails like the real one.
type pushHexEncryptor struct{}

func (pushHexEncryptor) Encrypt(plaintext string) (string, error) {
	return "hex:" + hex.EncodeToString([]byte(plaintext)), nil
}

func (pushHexEncryptor) Decrypt(ciphertext string) (string, error) {
	rest, ok := strings.CutPrefix(ciphertext, "hex:")
	if !ok {
		return "", errors.New("cipher: message authentication failed")
	}
	b, err := hex.DecodeString(rest)
	return string(b), err
}

// pushMemRepo is an in-memory PushRepository. The embedded interface panics on methods a test does not expect.
type pushMemRepo struct {
	PushRepository

	mu         sync.Mutex
	nextID     int64
	devices    map[int64]*PushDevice
	users      map[int64]bool // admin users still allowed to receive pushes
	deliveries []*PushDelivery
	inserts    int

	fired     []*PushAlertEventRow
	resolved  []*PushAlertEventRow
	accounts  []*PushAccountErrorRow
	testRuns  []*PushTestRunRow
	countOver int // CountDevicesByUserExcept override (>0)
}

func newPushMemRepo() *pushMemRepo {
	return &pushMemRepo{devices: map[int64]*PushDevice{}, users: map[int64]bool{}}
}

func clonePushDevice(d *PushDevice) *PushDevice {
	cp := *d
	cp.Subscription.Events = slices.Clone(d.Subscription.Events)
	return &cp
}

func (r *pushMemRepo) addDevice(d *PushDevice) *PushDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	d.ID = r.nextID
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().Add(-time.Hour)
	}
	r.devices[d.ID] = d
	r.users[d.UserID] = true
	return d
}

func (r *pushMemRepo) device(id int64) *PushDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return clonePushDevice(r.devices[id])
}

func (r *pushMemRepo) UpsertDevice(_ context.Context, d *PushDevice, replacePrefs bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.devices {
		if existing.Platform == d.Platform && existing.TokenHash == d.TokenHash {
			ownerChanged := existing.UserID != d.UserID
			if ownerChanged && existing.Platform != PushPlatformIOS && r.users[existing.UserID] {
				return false, ErrPushTargetInUse
			}
			prefs := existing.Subscription
			if replacePrefs || ownerChanged {
				prefs = d.Subscription
			}
			id, created := existing.ID, existing.CreatedAt
			if ownerChanged {
				created = time.Now()
			}
			*existing = *clonePushDevice(d)
			existing.ID, existing.CreatedAt, existing.Subscription = id, created, prefs
			existing.DisabledReason = ""
			*d = *clonePushDevice(existing)
			return false, nil
		}
	}
	r.nextID++
	d.ID = r.nextID
	d.CreatedAt = time.Now()
	r.devices[d.ID] = clonePushDevice(d)
	if _, known := r.users[d.UserID]; !known {
		r.users[d.UserID] = true // whoever registers is an active admin
	}
	return true, nil
}

func (r *pushMemRepo) CountDevicesByUserExcept(_ context.Context, userID int64, platform, tokenHash string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.countOver > 0 {
		return r.countOver, nil
	}
	n := 0
	for _, d := range r.devices {
		if d.UserID == userID && (d.Platform != platform || d.TokenHash != tokenHash) {
			n++
		}
	}
	return n, nil
}

func (r *pushMemRepo) ListDevicesByUser(_ context.Context, userID int64) ([]*PushDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*PushDevice
	for _, d := range r.devices {
		if d.UserID == userID {
			out = append(out, clonePushDevice(d))
		}
	}
	slices.SortFunc(out, func(a, b *PushDevice) int { return int(a.ID - b.ID) })
	return out, nil
}

func (r *pushMemRepo) GetDeviceForUser(_ context.Context, userID, id int64) (*PushDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok || d.UserID != userID {
		return nil, ErrPushDeviceNotFound
	}
	return clonePushDevice(d), nil
}

func (r *pushMemRepo) DeleteDeviceForUser(_ context.Context, userID, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok || d.UserID != userID {
		return ErrPushDeviceNotFound
	}
	delete(r.devices, id)
	return nil
}

func (r *pushMemRepo) UpdateDevicePrefs(_ context.Context, userID, id int64, prefs PushSubscription) (*PushDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok || d.UserID != userID {
		return nil, ErrPushDeviceNotFound
	}
	d.Subscription = prefs
	return clonePushDevice(d), nil
}

func (r *pushMemRepo) ListActiveAdminDevices(context.Context) ([]*PushDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*PushDevice
	for _, d := range r.devices {
		if d.DisabledReason == "" && r.users[d.UserID] {
			out = append(out, clonePushDevice(d))
		}
	}
	slices.SortFunc(out, func(a, b *PushDevice) int { return int(a.ID - b.ID) })
	return out, nil
}

func (r *pushMemRepo) MarkDeviceSuccess(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[id]; ok {
		d.LastSuccessAt = &at
		d.DisabledReason = ""
		d.DisabledAt = nil
	}
	return nil
}

func (r *pushMemRepo) MarkDeviceError(_ context.Context, id int64, message, disableReason string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[id]; ok {
		d.LastError, d.LastErrorAt = message, &at
		if disableReason != "" {
			d.DisabledReason, d.DisabledAt = disableReason, &at
		}
	}
	return nil
}

func (r *pushMemRepo) InsertDelivery(_ context.Context, d *PushDelivery) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inserts++
	for _, existing := range r.deliveries {
		if existing.DeviceID == d.DeviceID && existing.DedupKey == d.DedupKey {
			return false, nil
		}
	}
	r.nextID++
	cp := *d
	cp.ID = r.nextID
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = cp.NextAttemptAt // fanout sets it to the dispatcher's clock, like the database's NOW()
	}
	r.deliveries = append(r.deliveries, &cp)
	return true, nil
}

func (r *pushMemRepo) ListDeliveryKeysSince(_ context.Context, eventType string, since time.Time) ([]PushDeliveryKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []PushDeliveryKey
	for _, d := range r.deliveries {
		if d.EventType == eventType && d.Status != PushDeliverySkipped && !d.CreatedAt.Before(since) {
			out = append(out, PushDeliveryKey{DeviceID: d.DeviceID, DedupKey: d.DedupKey})
		}
	}
	return out, nil
}

func (r *pushMemRepo) ClaimDueDeliveries(_ context.Context, now time.Time, lease time.Duration, limit int) ([]*PushDelivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*PushDelivery
	for _, d := range r.deliveries {
		if len(out) >= limit {
			break
		}
		if (d.Status == PushDeliveryPending || d.Status == PushDeliverySending) && !d.NextAttemptAt.After(now) {
			d.Status = PushDeliverySending
			d.Attempts++
			d.NextAttemptAt = now.Add(lease)
			cp := *d
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *pushMemRepo) FinishDelivery(_ context.Context, id int64, status, lastError string, retryAt, _ *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.deliveries {
		if d.ID == id {
			d.Status, d.LastError = status, lastError
			if retryAt != nil {
				d.NextAttemptAt = *retryAt
			}
		}
	}
	return nil
}

func (r *pushMemRepo) PurgeDeliveriesBefore(context.Context, time.Time) (int64, error) { return 0, nil }

func (r *pushMemRepo) ListAlertEventsFiredBetween(context.Context, time.Time, time.Time) ([]*PushAlertEventRow, error) {
	return r.fired, nil
}

func (r *pushMemRepo) ListAlertEventsResolvedBetween(context.Context, time.Time, time.Time) ([]*PushAlertEventRow, error) {
	return r.resolved, nil
}

func (r *pushMemRepo) ListAccountErrorsBetween(context.Context, time.Time, time.Time) ([]*PushAccountErrorRow, error) {
	return r.accounts, nil
}

func (r *pushMemRepo) ListAsyncTestRunsFinishedBetween(context.Context, time.Time, time.Time) ([]*PushTestRunRow, error) {
	return r.testRuns, nil
}

func (r *pushMemRepo) deliveriesFor(deviceID int64) []*PushDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*PushDelivery
	for _, d := range r.deliveries {
		if d.DeviceID == deviceID {
			cp := *d
			out = append(out, &cp)
		}
	}
	return out
}

// pushFakeSender records sends; result returns the outcome per device token.
type pushFakeSender struct {
	mu     sync.Mutex
	sent   []pushFakeSend
	result func(target PushTarget) (PushSendResult, error)
}

type pushFakeSend struct {
	Target  PushTarget
	Message PushMessage
}

func (s *pushFakeSender) Send(_ context.Context, target PushTarget, msg *PushMessage) (PushSendResult, error) {
	s.mu.Lock()
	s.sent = append(s.sent, pushFakeSend{Target: target, Message: *msg})
	result := s.result
	s.mu.Unlock()
	if result != nil {
		return result(target)
	}
	return PushSendResult{ProviderStatus: 200}, nil
}

func (s *pushFakeSender) sends() []pushFakeSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

func pushFakeSenders(fake *pushFakeSender) *PushSenders {
	return &PushSenders{byPlatform: map[string]PushSender{
		PushPlatformIOS: fake, PushPlatformBark: fake, PushPlatformNtfy: fake, PushPlatformWebhook: fake,
	}}
}
