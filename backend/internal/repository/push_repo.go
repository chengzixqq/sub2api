package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// pushRepository stores push devices and deliveries (migration 257) with plain SQL, like admin_access_tokens.
type pushRepository struct {
	db *sql.DB
}

// NewPushRepository stores push targets in push_devices and their deliveries in push_deliveries.
func NewPushRepository(db *sql.DB) service.PushRepository {
	return &pushRepository{db: db}
}

const pushDeviceColumns = `push_devices.id, push_devices.user_id, push_devices.platform, push_devices.token_hash,
 push_devices.credentials, push_devices.credentials_encrypted, push_devices.token_hint, push_devices.endpoint_host,
 push_devices.bundle_id, push_devices.environment, push_devices.device_name, push_devices.locale, push_devices.timezone,
 push_devices.site_tag, push_devices.prefs, push_devices.disabled_reason, push_devices.disabled_at,
 push_devices.last_success_at, push_devices.last_error, push_devices.last_error_at, push_devices.created_at,
 push_devices.updated_at`

func scanPushDevice(row rowScanner) (*service.PushDevice, error) {
	var d service.PushDevice
	var prefs []byte
	var disabledAt, lastSuccess, lastErrorAt sql.NullTime
	if err := row.Scan(&d.ID, &d.UserID, &d.Platform, &d.TokenHash, &d.CredentialsRaw, &d.CredentialsEncrypted,
		&d.TokenHint, &d.EndpointHost, &d.BundleID, &d.Environment, &d.DeviceName, &d.Locale, &d.Timezone, &d.SiteTag,
		&prefs, &d.DisabledReason, &disabledAt, &lastSuccess, &d.LastError, &lastErrorAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrPushDeviceNotFound
		}
		return nil, err
	}
	d.Subscription = decodePushPrefs(prefs)
	d.DisabledAt = pushNullTimePtr(disabledAt)
	d.LastSuccessAt = pushNullTimePtr(lastSuccess)
	d.LastErrorAt = pushNullTimePtr(lastErrorAt)
	return &d, nil
}

// decodePushPrefs reads the stored subscription; an unreadable value falls back to the default.
func decodePushPrefs(raw []byte) service.PushSubscription {
	var sub service.PushSubscription
	if len(raw) == 0 || json.Unmarshal(raw, &sub) != nil {
		return service.DefaultPushSubscription()
	}
	normalized, err := service.NormalizePushSubscription(sub)
	if err != nil {
		return service.DefaultPushSubscription()
	}
	return normalized
}

func pushNullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	at := v.Time
	return &at
}

func (r *pushRepository) queryDevices(ctx context.Context, query string, args ...any) ([]*service.PushDevice, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.PushDevice{}
	for rows.Next() {
		d, err := scanPushDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *pushRepository) UpsertDevice(ctx context.Context, d *service.PushDevice, replacePrefs bool) (bool, error) {
	prefs, err := json.Marshal(d.Subscription)
	if err != nil {
		return false, err
	}
	// SET expressions read the existing row, so "push_devices.user_id <> EXCLUDED.user_id" is the ownership change.
	// An APNs token moves to the admin now signed in on the phone. A Bark / ntfy / webhook target is identified by a
	// URL (plus key or topic) that another admin may know, so it only moves when its owner is no longer an active
	// admin (as in ListActiveAdminDevices); otherwise no row is updated and the caller gets ErrPushTargetInUse.
	var created bool
	var stored []byte
	err = r.db.QueryRowContext(ctx, `
INSERT INTO push_devices (user_id, platform, token_hash, credentials, credentials_encrypted, token_hint, endpoint_host,
  bundle_id, environment, device_name, locale, timezone, site_tag, prefs)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb)
ON CONFLICT (platform, token_hash) DO UPDATE SET
  credentials = EXCLUDED.credentials,
  credentials_encrypted = EXCLUDED.credentials_encrypted,
  token_hint = EXCLUDED.token_hint,
  endpoint_host = EXCLUDED.endpoint_host,
  bundle_id = EXCLUDED.bundle_id,
  environment = EXCLUDED.environment,
  device_name = EXCLUDED.device_name,
  locale = EXCLUDED.locale,
  timezone = EXCLUDED.timezone,
  site_tag = EXCLUDED.site_tag,
  prefs = CASE WHEN $15::BOOLEAN OR push_devices.user_id <> EXCLUDED.user_id THEN EXCLUDED.prefs ELSE push_devices.prefs END,
  created_at = CASE WHEN push_devices.user_id <> EXCLUDED.user_id THEN NOW() ELSE push_devices.created_at END,
  last_success_at = CASE WHEN push_devices.user_id <> EXCLUDED.user_id THEN NULL ELSE push_devices.last_success_at END,
  last_error = CASE WHEN push_devices.user_id <> EXCLUDED.user_id THEN '' ELSE push_devices.last_error END,
  last_error_at = CASE WHEN push_devices.user_id <> EXCLUDED.user_id THEN NULL ELSE push_devices.last_error_at END,
  user_id = EXCLUDED.user_id,
  disabled_reason = '',
  disabled_at = NULL,
  updated_at = NOW()
WHERE $16::BOOLEAN OR push_devices.user_id = EXCLUDED.user_id OR NOT EXISTS (
  SELECT 1 FROM users u WHERE u.id = push_devices.user_id AND u.role = $17 AND u.status = $18 AND u.deleted_at IS NULL)
RETURNING id, (xmax = 0), prefs, created_at, updated_at`,
		d.UserID, d.Platform, d.TokenHash, d.CredentialsRaw, d.CredentialsEncrypted, d.TokenHint, d.EndpointHost,
		d.BundleID, d.Environment, d.DeviceName, d.Locale, d.Timezone, d.SiteTag, string(prefs), replacePrefs,
		d.Platform == service.PushPlatformIOS, service.RoleAdmin, service.StatusActive,
	).Scan(&d.ID, &created, &stored, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrPushTargetInUse
	}
	if err != nil {
		return false, err
	}
	d.Subscription = decodePushPrefs(stored)
	d.DisabledReason, d.DisabledAt = "", nil
	return created, nil
}

func (r *pushRepository) CountDevicesByUserExcept(ctx context.Context, userID int64, platform, tokenHash string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_devices
 WHERE user_id = $1 AND NOT (platform = $2 AND token_hash = $3)`, userID, platform, tokenHash).Scan(&n)
	return n, err
}

func (r *pushRepository) ListDevicesByUser(ctx context.Context, userID int64) ([]*service.PushDevice, error) {
	return r.queryDevices(ctx, `SELECT `+pushDeviceColumns+` FROM push_devices WHERE user_id = $1 ORDER BY id`, userID)
}

func (r *pushRepository) GetDeviceForUser(ctx context.Context, userID, id int64) (*service.PushDevice, error) {
	return scanPushDevice(r.db.QueryRowContext(ctx,
		`SELECT `+pushDeviceColumns+` FROM push_devices WHERE id = $1 AND user_id = $2`, id, userID))
}

func (r *pushRepository) DeleteDeviceForUser(ctx context.Context, userID, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM push_devices WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return service.ErrPushDeviceNotFound
	}
	return nil
}

func (r *pushRepository) UpdateDevicePrefs(ctx context.Context, userID, id int64, prefs service.PushSubscription) (*service.PushDevice, error) {
	raw, err := json.Marshal(prefs)
	if err != nil {
		return nil, err
	}
	return scanPushDevice(r.db.QueryRowContext(ctx, `UPDATE push_devices SET prefs = $3::jsonb, updated_at = NOW()
 WHERE id = $1 AND user_id = $2 RETURNING `+pushDeviceColumns, id, userID, string(raw)))
}

func (r *pushRepository) ListActiveAdminDevices(ctx context.Context) ([]*service.PushDevice, error) {
	return r.queryDevices(ctx, `SELECT `+pushDeviceColumns+`
 FROM push_devices JOIN users u ON u.id = push_devices.user_id
 WHERE push_devices.disabled_reason = '' AND u.role = $1 AND u.status = $2 AND u.deleted_at IS NULL
 ORDER BY push_devices.id`, service.RoleAdmin, service.StatusActive)
}

func (r *pushRepository) MarkDeviceSuccess(ctx context.Context, id int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE push_devices SET last_success_at = $2, disabled_reason = '', disabled_at = NULL
 WHERE id = $1`, id, at)
	return err
}

func (r *pushRepository) MarkDeviceError(ctx context.Context, id int64, message, disableReason string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE push_devices SET last_error = $2, last_error_at = $3,
  disabled_reason = CASE WHEN $4 <> '' THEN $4 ELSE disabled_reason END,
  disabled_at = CASE WHEN $4 <> '' THEN $3 ELSE disabled_at END
 WHERE id = $1`, id, message, at, disableReason)
	return err
}

func (r *pushRepository) InsertDelivery(ctx context.Context, d *service.PushDelivery) (bool, error) {
	payload, err := json.Marshal(d.Payload)
	if err != nil {
		return false, err
	}
	err = r.db.QueryRowContext(ctx, `
INSERT INTO push_deliveries (device_id, dedup_key, event_type, payload, status, next_attempt_at, last_error)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7)
ON CONFLICT (device_id, dedup_key) DO NOTHING
RETURNING id, created_at`, d.DeviceID, d.DedupKey, d.EventType, string(payload), d.Status, d.NextAttemptAt, d.LastError).Scan(&d.ID, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *pushRepository) ClaimDueDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*service.PushDelivery, error) {
	rows, err := r.db.QueryContext(ctx, `
UPDATE push_deliveries d SET status = $4, attempts = d.attempts + 1, next_attempt_at = $2
FROM (
  SELECT id FROM push_deliveries
  WHERE status IN ($4, $5) AND next_attempt_at <= $1
  ORDER BY next_attempt_at, id
  LIMIT $3
  FOR UPDATE SKIP LOCKED
) due
WHERE d.id = due.id
RETURNING d.id, d.device_id, d.dedup_key, d.event_type, d.payload, d.status, d.attempts, d.next_attempt_at, d.last_error, d.created_at`,
		now, now.Add(lease), limit, service.PushDeliverySending, service.PushDeliveryPending)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.PushDelivery{}
	for rows.Next() {
		var d service.PushDelivery
		var payload []byte
		if err := rows.Scan(&d.ID, &d.DeviceID, &d.DedupKey, &d.EventType, &payload, &d.Status, &d.Attempts,
			&d.NextAttemptAt, &d.LastError, &d.CreatedAt); err != nil {
			return nil, err
		}
		if err := decodePushPayload(payload, &d.Payload); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// decodePushPayload keeps integer IDs as json.Number so they are re-encoded unchanged.
func decodePushPayload(raw []byte, msg *service.PushMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(msg)
}

func (r *pushRepository) FinishDelivery(ctx context.Context, id int64, status, lastError string, retryAt, sentAt *time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE push_deliveries SET status = $2, last_error = $3,
  next_attempt_at = COALESCE($4, next_attempt_at), sent_at = COALESCE($5, sent_at)
 WHERE id = $1`, id, status, lastError, pushNullableTime(retryAt), pushNullableTime(sentAt))
	return err
}

func pushNullableTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func (r *pushRepository) ListDeliveryKeysSince(ctx context.Context, eventType string, since time.Time) ([]service.PushDeliveryKey, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT device_id, dedup_key FROM push_deliveries
 WHERE created_at >= $1 AND event_type = $2 AND status <> $3`, since, eventType, service.PushDeliverySkipped)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.PushDeliveryKey{}
	for rows.Next() {
		var k service.PushDeliveryKey
		if err := rows.Scan(&k.DeviceID, &k.DedupKey); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *pushRepository) PurgeDeliveriesBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM push_deliveries WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
