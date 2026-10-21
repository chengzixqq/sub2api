package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Push event types an admin can subscribe a device to; push.test is only sent on request.
const (
	PushEventOpsAlertFired       = "ops_alert.fired"
	PushEventOpsAlertResolved    = "ops_alert.resolved"
	PushEventAccountError        = "account.error"
	PushEventAccountTestFinished = "account.test_finished"
	PushEventTest                = "push.test"
)

// pushSubscribableEvents is the canonical order of subscription events.
var pushSubscribableEvents = []string{
	PushEventOpsAlertFired,
	PushEventOpsAlertResolved,
	PushEventAccountError,
	PushEventAccountTestFinished,
}

// Push target platforms (Q7: APNs plus generic Bark / ntfy / webhook channels).
const (
	PushPlatformIOS     = "ios"
	PushPlatformBark    = "bark"
	PushPlatformNtfy    = "ntfy"
	PushPlatformWebhook = "webhook"
)

// Delivery statuses.
const (
	PushDeliveryPending = "pending"
	PushDeliverySending = "sending"
	PushDeliverySent    = "sent"
	PushDeliveryFailed  = "failed"
	PushDeliverySkipped = "skipped"
)

// Reasons a device is disabled; re-registering the same token enables it again.
const (
	PushDisabledUnregistered        = "unregistered"
	PushDisabledBadDeviceToken      = "bad_device_token"
	PushDisabledTopicMismatch       = "topic_mismatch"
	PushDisabledEndpointGone        = "endpoint_gone"
	PushDisabledSecretUndecryptable = "secret_undecryptable"
)

const (
	// PushMaxDevicesPerUser caps the push targets of one admin.
	PushMaxDevicesPerUser = 20
	pushMaxGroupFilter    = 100
	pushMaxPlatformFilter = 20
	pushDefaultSeverity   = "P1"
	pushLowestSeverity    = 3
)

var (
	ErrInvalidPushToken        = infraerrors.BadRequest("INVALID_PUSH_TOKEN", "invalid push token")
	ErrInvalidPushTarget       = infraerrors.BadRequest("INVALID_PUSH_TARGET", "invalid push target")
	ErrInvalidPushSubscription = infraerrors.BadRequest("INVALID_PUSH_SUBSCRIPTION", "invalid push subscription")
	ErrPushDeviceNotFound      = infraerrors.NotFound("PUSH_DEVICE_NOT_FOUND", "push device not found")
	ErrPushDeviceLimit         = infraerrors.Conflict("PUSH_DEVICE_LIMIT_REACHED", "too many push devices, delete one first")
	ErrPushNotConfigured       = infraerrors.ServiceUnavailable("PUSH_NOT_CONFIGURED", "APNs push is not configured or not enabled on this site")
	ErrPushDeliveryFailed      = infraerrors.New(http.StatusBadGateway, "PUSH_DELIVERY_FAILED", "the push provider did not accept the notification")
	ErrInvalidAPNsConfig       = infraerrors.BadRequest("INVALID_APNS_CONFIG", "invalid APNs configuration")
)

// pushFieldError is base with a specific message and the offending field in metadata.
func pushFieldError(base *infraerrors.ApplicationError, field, message string) error {
	err := base.WithMetadata(map[string]string{"field": field})
	err.Message = message
	return err
}

// PushQuietHours is a daily window in the device's timezone ("HH:MM", end exclusive, may cross midnight).
type PushQuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Contains reports whether now falls in the window, read in loc. A nil or empty window contains nothing.
func (q *PushQuietHours) Contains(now time.Time, loc *time.Location) bool {
	if q == nil {
		return false
	}
	start, okStart := parsePushClock(q.Start)
	end, okEnd := parsePushClock(q.End)
	if !okStart || !okEnd || start == end {
		return false
	}
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	minute := local.Hour()*60 + local.Minute()
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

var pushClockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

func parsePushClock(s string) (int, bool) {
	m := pushClockPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	return h*60 + mm, true
}

// PushSubscription selects the events a device receives. MinSeverity only applies to ops_alert.*; GroupIDs and
// Platforms only filter events that carry group / platform information (site-wide alerts always pass).
type PushSubscription struct {
	Events      []string        `json:"events"`
	MinSeverity string          `json:"min_severity"`
	GroupIDs    []int64         `json:"group_ids"`
	Platforms   []string        `json:"platforms"`
	QuietHours  *PushQuietHours `json:"quiet_hours"`
}

// DefaultPushSubscription is used when a device registers without a subscription.
func DefaultPushSubscription() PushSubscription {
	return PushSubscription{
		Events:      []string{PushEventOpsAlertFired, PushEventAccountError},
		MinSeverity: pushDefaultSeverity,
		GroupIDs:    []int64{},
		Platforms:   []string{},
	}
}

var pushPlatformFilterPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// NormalizePushSubscription validates in: nil events take the default list, an explicit empty list mutes the
// device; events, groups and platforms are deduplicated; start == end clears the quiet hours.
func NormalizePushSubscription(in PushSubscription) (PushSubscription, error) {
	out := PushSubscription{GroupIDs: []int64{}, Platforms: []string{}}
	if in.Events == nil {
		out.Events = DefaultPushSubscription().Events
	} else {
		events, err := normalizePushEvents(in.Events)
		if err != nil {
			return PushSubscription{}, err
		}
		out.Events = events
	}

	severity := strings.ToUpper(strings.TrimSpace(in.MinSeverity))
	if severity == "" {
		severity = pushDefaultSeverity
	}
	if _, ok := pushSeverityRanks[severity]; !ok {
		return PushSubscription{}, pushFieldError(ErrInvalidPushSubscription, "min_severity", "min_severity must be one of P0, P1, P2, P3")
	}
	out.MinSeverity = severity

	if len(in.GroupIDs) > pushMaxGroupFilter {
		return PushSubscription{}, pushFieldError(ErrInvalidPushSubscription, "group_ids", "group_ids accepts at most 100 IDs")
	}
	for _, id := range in.GroupIDs {
		if id <= 0 {
			return PushSubscription{}, pushFieldError(ErrInvalidPushSubscription, "group_ids", "group_ids must be positive integers")
		}
		if !slices.Contains(out.GroupIDs, id) {
			out.GroupIDs = append(out.GroupIDs, id)
		}
	}
	slices.Sort(out.GroupIDs)

	if len(in.Platforms) > pushMaxPlatformFilter {
		return PushSubscription{}, pushFieldError(ErrInvalidPushSubscription, "platforms", "platforms accepts at most 20 values")
	}
	for _, raw := range in.Platforms {
		p := strings.ToLower(strings.TrimSpace(raw))
		if !pushPlatformFilterPattern.MatchString(p) {
			return PushSubscription{}, pushFieldError(ErrInvalidPushSubscription, "platforms", "platforms must be account platform names")
		}
		if !slices.Contains(out.Platforms, p) {
			out.Platforms = append(out.Platforms, p)
		}
	}
	slices.Sort(out.Platforms)

	if in.QuietHours != nil {
		start, okStart := parsePushClock(in.QuietHours.Start)
		end, okEnd := parsePushClock(in.QuietHours.End)
		if !okStart || !okEnd {
			return PushSubscription{}, pushFieldError(ErrInvalidPushSubscription, "quiet_hours", "quiet_hours start and end must be HH:MM (00:00-23:59)")
		}
		if start != end {
			out.QuietHours = &PushQuietHours{Start: strings.TrimSpace(in.QuietHours.Start), End: strings.TrimSpace(in.QuietHours.End)}
		}
	}
	return out, nil
}

func normalizePushEvents(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, raw := range in {
		ev := strings.TrimSpace(raw)
		if !slices.Contains(pushSubscribableEvents, ev) {
			return nil, pushFieldError(ErrInvalidPushSubscription, "events",
				"events must be ops_alert.fired, ops_alert.resolved, account.error or account.test_finished")
		}
		seen[ev] = true
	}
	out := make([]string, 0, len(seen))
	for _, ev := range pushSubscribableEvents {
		if seen[ev] {
			out = append(out, ev)
		}
	}
	return out, nil
}

var pushSeverityRanks = map[string]int{"P0": 0, "P1": 1, "P2": 2, "P3": 3}

// pushSeverityRank orders alert severities (P0 is the most severe); unknown values rank lowest.
func pushSeverityRank(severity string) int {
	if rank, ok := pushSeverityRanks[strings.ToUpper(strings.TrimSpace(severity))]; ok {
		return rank
	}
	return pushLowestSeverity
}

func isPushOpsAlertEvent(eventType string) bool {
	return eventType == PushEventOpsAlertFired || eventType == PushEventOpsAlertResolved
}

// Matches reports whether the subscription selects ev. The initiator's own async test result is personal, so the
// group and platform filters do not apply to it.
func (s PushSubscription) Matches(ev *PushEvent) bool {
	if ev == nil || !slices.Contains(s.Events, ev.Type) {
		return false
	}
	if isPushOpsAlertEvent(ev.Type) && pushSeverityRank(ev.Severity) > pushSeverityRank(s.MinSeverity) {
		return false
	}
	if ev.Type == PushEventAccountTestFinished {
		return true
	}
	if len(s.Platforms) > 0 && ev.Platform != "" && !slices.Contains(s.Platforms, ev.Platform) {
		return false
	}
	if len(s.GroupIDs) > 0 && len(ev.GroupIDs) > 0 && !slices.ContainsFunc(ev.GroupIDs, func(id int64) bool {
		return slices.Contains(s.GroupIDs, id)
	}) {
		return false
	}
	return true
}

// PushEvent is one notifiable occurrence found by the dispatcher.
type PushEvent struct {
	Type string
	// DedupKey identifies the event per device: each (device, key) is delivered at most once.
	DedupKey   string
	OccurredAt time.Time
	Severity   string
	Platform   string
	GroupIDs   []int64
	// RecipientUserID limits the event to one admin (the initiator of an async account test); 0 means every admin.
	RecipientUserID int64
	Content         PushContent
}

// PushContent is the localized notification text plus the IDs a client needs to open the right screen. It never
// carries credentials, account names or user data.
type PushContent struct {
	TitleZh           string
	BodyZh            string
	TitleEn           string
	BodyEn            string
	ThreadID          string
	InterruptionLevel string
	Data              map[string]any
}

// PushMessage is what a sender delivers; it is stored with the delivery so retries send the same content.
type PushMessage struct {
	Type              string         `json:"type"`
	Title             string         `json:"title"`
	Body              string         `json:"body"`
	ThreadID          string         `json:"thread_id"`
	InterruptionLevel string         `json:"interruption_level"`
	Severity          string         `json:"severity,omitempty"`
	Site              string         `json:"site,omitempty"`
	CollapseID        string         `json:"collapse_id,omitempty"`
	OccurredAt        time.Time      `json:"occurred_at"`
	Data              map[string]any `json:"data,omitempty"`
	// DeliveryID is stable across retries of one delivery (webhook X-Sub2-Delivery); set when sending.
	DeliveryID string `json:"-"`
}

// pushPrefersChinese picks the Chinese text for zh* locales.
func pushPrefersChinese(locale string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "zh")
}

// MessageFor renders ev for a device with the given locale and site label.
func (ev *PushEvent) MessageFor(locale, site string) PushMessage {
	title, body := ev.Content.TitleEn, ev.Content.BodyEn
	if pushPrefersChinese(locale) {
		title, body = ev.Content.TitleZh, ev.Content.BodyZh
	}
	return PushMessage{
		Type:              ev.Type,
		Title:             title,
		Body:              body,
		ThreadID:          ev.Content.ThreadID,
		InterruptionLevel: ev.Content.InterruptionLevel,
		Severity:          strings.ToUpper(strings.TrimSpace(ev.Severity)),
		Site:              site,
		CollapseID:        pushCollapseID(site, ev.DedupKey),
		OccurredAt:        ev.OccurredAt.UTC(),
		Data:              ev.Content.Data,
	}
}

// pushCollapseID is the apns-collapse-id of an event: a hash of the site label and the dedup key. The same APNs token
// can be registered on several sites whose dedup keys repeat (ops_alert.fired:5, account.error:12:<bucket>), and a
// shared collapse ID would let one site's notification replace the other's. 32 hex characters, within the 64-byte cap.
func pushCollapseID(site, dedupKey string) string {
	sum := sha256.Sum256([]byte(site + "\n" + dedupKey))
	return hex.EncodeToString(sum[:16])
}

// PushCredentials are the secret parts of a target, stored as (optionally encrypted) JSON.
type PushCredentials struct {
	Token    string `json:"token,omitempty"`
	Secret   string `json:"secret,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

// PushDevice is a registered push target of one admin.
type PushDevice struct {
	ID       int64
	UserID   int64
	Platform string
	// TokenHash identifies the target (sha256 of platform, endpoint and token) without storing it in clear.
	TokenHash            string
	CredentialsRaw       string
	CredentialsEncrypted bool
	TokenHint            string
	EndpointHost         string
	BundleID             string
	Environment          string
	DeviceName           string
	Locale               string
	Timezone             string
	SiteTag              string
	Subscription         PushSubscription
	DisabledReason       string
	DisabledAt           *time.Time
	LastSuccessAt        *time.Time
	LastError            string
	LastErrorAt          *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Enabled reports whether the device receives pushes.
func (d *PushDevice) Enabled() bool { return d != nil && d.DisabledReason == "" }

// Location is the device timezone (UTC when unset or unknown).
func (d *PushDevice) Location() *time.Location {
	if d == nil || strings.TrimSpace(d.Timezone) == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// PushDelivery is one queued notification for one device.
type PushDelivery struct {
	ID            int64
	DeviceID      int64
	DedupKey      string
	EventType     string
	Payload       PushMessage
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
}

// PushAlertEventRow is an ops alert event as the dispatcher reads it.
type PushAlertEventRow struct {
	EventID        int64
	RuleID         int64
	Severity       string
	Status         string
	RuleName       string
	Title          string
	Operator       string
	WindowMinutes  int
	MetricValue    *float64
	ThresholdValue *float64
	Platform       string
	GroupID        *int64
	FiredAt        time.Time
	ResolvedAt     *time.Time
	// Silenced reports an active scoped silence (D01) covering the event.
	Silenced bool
}

// PushAccountErrorRow is an account in the error state.
type PushAccountErrorRow struct {
	AccountID    int64
	Platform     string
	ErrorMessage string
	ErrorAt      time.Time
	GroupIDs     []int64
}

// PushTestRunRow is a finished async account test (C08).
type PushTestRunRow struct {
	RunID              string
	AccountID          int64
	Platform           string
	Status             string
	ModelID            string
	LatencyMs          *int64
	UpstreamStatusCode *int
	ErrorMessage       string
	FinishedAt         time.Time
	CreatedBy          int64
	GroupIDs           []int64
}

// PushRepository stores push devices and deliveries and reads the events the dispatcher pushes.
type PushRepository interface {
	// UpsertDevice inserts d or updates the device with the same (platform, token_hash), moving it to d.UserID.
	// Prefs are replaced when replacePrefs is set or the owner changes. It fills d.ID, CreatedAt and UpdatedAt.
	UpsertDevice(ctx context.Context, d *PushDevice, replacePrefs bool) (created bool, err error)
	// CountDevicesByUserExcept counts the user's devices other than (platform, tokenHash).
	CountDevicesByUserExcept(ctx context.Context, userID int64, platform, tokenHash string) (int, error)
	ListDevicesByUser(ctx context.Context, userID int64) ([]*PushDevice, error)
	// GetDeviceForUser returns ErrPushDeviceNotFound when the device does not exist or belongs to someone else.
	GetDeviceForUser(ctx context.Context, userID, id int64) (*PushDevice, error)
	DeleteDeviceForUser(ctx context.Context, userID, id int64) error
	UpdateDevicePrefs(ctx context.Context, userID, id int64, prefs PushSubscription) (*PushDevice, error)
	// ListActiveAdminDevices returns enabled devices of active, non-deleted admins.
	ListActiveAdminDevices(ctx context.Context) ([]*PushDevice, error)
	// MarkDeviceSuccess records a delivered push and enables the device again.
	MarkDeviceSuccess(ctx context.Context, id int64, at time.Time) error
	// MarkDeviceError records a failed push; a non-empty disableReason disables the device.
	MarkDeviceError(ctx context.Context, id int64, message, disableReason string, at time.Time) error

	// InsertDelivery stores d unless (device, dedup_key) exists; it reports whether d was inserted.
	InsertDelivery(ctx context.Context, d *PushDelivery) (bool, error)
	// ClaimDueDeliveries moves due pending rows (and sending rows whose lease expired) to sending, bumping
	// attempts and setting the lease; concurrent callers never claim the same row.
	ClaimDueDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*PushDelivery, error)
	// FinishDelivery writes the outcome of a claimed delivery; a non-nil retryAt puts it back to pending.
	FinishDelivery(ctx context.Context, id int64, status, lastError string, retryAt, sentAt *time.Time) error
	PurgeDeliveriesBefore(ctx context.Context, before time.Time) (int64, error)

	ListAlertEventsFiredBetween(ctx context.Context, from, to time.Time) ([]*PushAlertEventRow, error)
	ListAlertEventsResolvedBetween(ctx context.Context, from, to time.Time) ([]*PushAlertEventRow, error)
	ListAccountErrorsBetween(ctx context.Context, from, to time.Time) ([]*PushAccountErrorRow, error)
	ListAsyncTestRunsFinishedBetween(ctx context.Context, from, to time.Time) ([]*PushTestRunRow, error)
}
