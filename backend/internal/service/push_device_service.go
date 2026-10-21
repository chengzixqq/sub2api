package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/google/uuid"
)

const (
	pushDeviceNameMaxRunes = 64
	pushSiteTagMaxRunes    = 64
	pushTimezoneMaxLen     = 64
	pushSecretMaxLen       = 512
	pushEndpointMaxLen     = 2048
	pushTestTimeout        = 15 * time.Second
	pushEnvProduction      = "production"
	pushEnvSandbox         = "sandbox"
)

var (
	pushAPNsTokenPattern = regexp.MustCompile(`^[0-9a-f]{64,200}$`)
	pushBarkKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	pushNtfyTopicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	pushBundleIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,154}$`)
	pushLocalePattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,35}$`)
)

// PushDeviceInput registers or refreshes a push target. iOS uses APNsToken (Token is accepted as an alias); Bark uses
// Endpoint (server base URL) + Token (device key); ntfy uses Endpoint (server base URL) + Token (topic) + optional
// Secret (access token); webhook uses Endpoint + optional Secret (HMAC key).
type PushDeviceInput struct {
	Platform     string
	APNsToken    string
	Token        string
	Secret       string
	Endpoint     string
	BundleID     string
	Environment  string
	DeviceName   string
	Locale       string
	Timezone     string
	SiteTag      string
	Subscription *PushSubscription
}

// PushTestResult is the outcome of a synchronous test push.
type PushTestResult struct {
	Delivered      bool
	ProviderStatus int
}

// PushDeviceService manages the push targets of the signed-in admin.
type PushDeviceService struct {
	repo              PushRepository
	codec             *pushCredentialCodec
	senders           *PushSenders
	allowInsecureHTTP bool
	allowPrivate      bool
	defaultSite       string
	now               func() time.Time
}

func NewPushDeviceService(repo PushRepository, encryptor SecretEncryptor, cfg *config.Config, senders *PushSenders) *PushDeviceService {
	s := &PushDeviceService{repo: repo, senders: senders, codec: newPushCredentialCodec(encryptor, cfg), now: time.Now}
	if cfg != nil {
		s.allowInsecureHTTP = cfg.Security.PushTargets.AllowInsecureHTTP
		s.allowPrivate = cfg.Security.PushTargets.AllowPrivateHosts
		s.defaultSite = pushDefaultSite(cfg)
	}
	return s
}

// pushDefaultSite is the host of server.frontend_url, the site label of devices registered without site_tag.
func pushDefaultSite(cfg *config.Config) string {
	if cfg == nil || strings.TrimSpace(cfg.Server.FrontendURL) == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(cfg.Server.FrontendURL))
	if err != nil {
		return ""
	}
	return u.Host
}

func pushSiteFor(d *PushDevice, fallback string) string {
	if d != nil && d.SiteTag != "" {
		return d.SiteTag
	}
	return fallback
}

// Register creates the device, or updates the device with the same token. An APNs token registered by another admin
// moves to userID; a Bark / ntfy / webhook target of another active admin returns ErrPushTargetInUse. It reports
// whether a new device was created.
func (s *PushDeviceService) Register(ctx context.Context, userID int64, in PushDeviceInput) (*PushDevice, bool, error) {
	d, creds, err := s.buildDevice(userID, in)
	if err != nil {
		return nil, false, err
	}
	count, err := s.repo.CountDevicesByUserExcept(ctx, userID, d.Platform, d.TokenHash)
	if err != nil {
		return nil, false, err
	}
	if count >= PushMaxDevicesPerUser {
		return nil, false, ErrPushDeviceLimit.WithMetadata(map[string]string{"max": strconv.Itoa(PushMaxDevicesPerUser)})
	}
	raw, encrypted, err := s.codec.seal(creds)
	if err != nil {
		return nil, false, err
	}
	d.CredentialsRaw, d.CredentialsEncrypted = raw, encrypted
	created, err := s.repo.UpsertDevice(ctx, d, in.Subscription != nil)
	if err != nil {
		return nil, false, err
	}
	// Re-read so an updated device reports its kept history (last success / error).
	if stored, err := s.repo.GetDeviceForUser(ctx, userID, d.ID); err == nil {
		d = stored
	}
	return d, created, nil
}

func (s *PushDeviceService) buildDevice(userID int64, in PushDeviceInput) (*PushDevice, PushCredentials, error) {
	d := &PushDevice{UserID: userID, Platform: strings.ToLower(strings.TrimSpace(in.Platform))}
	var creds PushCredentials
	var err error
	switch d.Platform {
	case PushPlatformIOS:
		creds, err = s.iosCredentials(d, in)
	case PushPlatformBark:
		creds, err = s.endpointCredentials(d, in, pushBarkKeyPattern, "token must be the Bark device key")
	case PushPlatformNtfy:
		creds, err = s.endpointCredentials(d, in, pushNtfyTopicPattern, "token must be an ntfy topic (letters, digits, - and _)")
	case PushPlatformWebhook:
		creds, err = s.endpointCredentials(d, in, nil, "")
	default:
		return nil, PushCredentials{}, pushFieldError(ErrInvalidPushTarget, "platform", "platform must be ios, bark, ntfy or webhook")
	}
	if err != nil {
		return nil, PushCredentials{}, err
	}
	if err := applyPushDeviceProfile(d, in); err != nil {
		return nil, PushCredentials{}, err
	}
	d.TokenHash = pushTokenHash(d.Platform, creds.Endpoint, creds.Token)
	d.TokenHint = pushTokenHint(creds.Token)
	return d, creds, nil
}

func (s *PushDeviceService) iosCredentials(d *PushDevice, in PushDeviceInput) (PushCredentials, error) {
	token := strings.TrimSpace(in.APNsToken)
	if token == "" {
		token = strings.TrimSpace(in.Token)
	}
	token = strings.ToLower(token)
	if !pushAPNsTokenPattern.MatchString(token) {
		return PushCredentials{}, pushFieldError(ErrInvalidPushToken, "apns_token", "apns_token must be the hex device token (64-200 hex characters)")
	}
	d.BundleID = strings.TrimSpace(in.BundleID)
	if d.BundleID != "" && !pushBundleIDPattern.MatchString(d.BundleID) {
		return PushCredentials{}, pushFieldError(ErrInvalidPushTarget, "bundle_id", "bundle_id must be an app bundle identifier")
	}
	// An omitted environment is stored empty: the sender then uses the site's apns_environment (production when unset),
	// so one site setting covers development builds that do not send it.
	env := strings.ToLower(strings.TrimSpace(in.Environment))
	if env != "" && env != pushEnvProduction && env != pushEnvSandbox {
		return PushCredentials{}, pushFieldError(ErrInvalidPushTarget, "environment", "environment must be production or sandbox")
	}
	d.Environment = env
	return PushCredentials{Token: token}, nil
}

// endpointCredentials validates the endpoint of a Bark / ntfy / webhook target; tokenPattern nil means no token.
func (s *PushDeviceService) endpointCredentials(d *PushDevice, in PushDeviceInput, tokenPattern *regexp.Regexp, tokenHint string) (PushCredentials, error) {
	endpoint, host, err := s.validateEndpoint(in.Endpoint)
	if err != nil {
		return PushCredentials{}, err
	}
	d.EndpointHost = host
	creds := PushCredentials{Endpoint: endpoint}
	if tokenPattern != nil {
		creds.Token = strings.TrimSpace(in.Token)
		if !tokenPattern.MatchString(creds.Token) {
			return PushCredentials{}, pushFieldError(ErrInvalidPushToken, "token", tokenHint)
		}
	}
	if d.Platform != PushPlatformBark {
		creds.Secret = strings.TrimSpace(in.Secret)
		if len(creds.Secret) > pushSecretMaxLen {
			return PushCredentials{}, pushFieldError(ErrInvalidPushTarget, "secret", "secret is too long")
		}
	}
	return creds, nil
}

// validateEndpoint applies the push target policy (security.push_targets allow_insecure_http / allow_private_hosts,
// both off by default) to a target URL; the resolved IP is checked again when sending.
func (s *PushDeviceService) validateEndpoint(raw string) (string, string, error) {
	normalized, err := urlvalidator.ValidateHTTPURL(raw, s.allowInsecureHTTP, urlvalidator.ValidationOptions{AllowPrivate: s.allowPrivate})
	if err != nil {
		return "", "", pushFieldError(ErrInvalidPushTarget, "endpoint",
			"endpoint must be an https URL of a public host (http and private hosts need security.push_targets allow_insecure_http / allow_private_hosts)")
	}
	u, err := url.Parse(normalized)
	if err != nil || len(normalized) > pushEndpointMaxLen {
		return "", "", pushFieldError(ErrInvalidPushTarget, "endpoint", "endpoint is not a valid URL")
	}
	if !s.allowPrivate && isBlockedHostname(u.Hostname()) {
		return "", "", pushFieldError(ErrInvalidPushTarget, "endpoint", "endpoint host is not allowed")
	}
	host := u.Hostname()
	if len(host) > 255 {
		return "", "", pushFieldError(ErrInvalidPushTarget, "endpoint", "endpoint host is too long")
	}
	return normalized, host, nil
}

func applyPushDeviceProfile(d *PushDevice, in PushDeviceInput) error {
	d.DeviceName = sanitizePushText(in.DeviceName, pushDeviceNameMaxRunes)
	d.SiteTag = sanitizePushText(in.SiteTag, pushSiteTagMaxRunes)
	if locale := strings.TrimSpace(in.Locale); pushLocalePattern.MatchString(locale) {
		d.Locale = locale
	}
	tz := strings.TrimSpace(in.Timezone)
	if tz != "" {
		if len(tz) > pushTimezoneMaxLen {
			return pushFieldError(ErrInvalidPushTarget, "timezone", "timezone must be an IANA name such as Asia/Shanghai")
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return pushFieldError(ErrInvalidPushTarget, "timezone", "timezone must be an IANA name such as Asia/Shanghai")
		}
	}
	d.Timezone = tz
	sub := DefaultPushSubscription()
	if in.Subscription != nil {
		normalized, err := NormalizePushSubscription(*in.Subscription)
		if err != nil {
			return err
		}
		sub = normalized
	}
	d.Subscription = sub
	return nil
}

// sanitizePushText drops control characters, collapses whitespace and caps the length in runes.
func sanitizePushText(s string, maxRunes int) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		_, _ = b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if runes := []rune(out); len(runes) > maxRunes {
		out = strings.TrimSpace(string(runes[:maxRunes]))
	}
	return out
}

// pushTokenHash identifies a target without storing its token or endpoint in clear.
func pushTokenHash(platform, endpoint, token string) string {
	sum := sha256.Sum256([]byte(platform + "\n" + endpoint + "\n" + token))
	return hex.EncodeToString(sum[:])
}

func pushTokenHint(token string) string {
	if len(token) < 8 {
		return ""
	}
	return token[len(token)-4:]
}

// List returns the admin's own devices.
func (s *PushDeviceService) List(ctx context.Context, userID int64) ([]*PushDevice, error) {
	return s.repo.ListDevicesByUser(ctx, userID)
}

// Delete removes one of the admin's devices.
func (s *PushDeviceService) Delete(ctx context.Context, userID, id int64) error {
	return s.repo.DeleteDeviceForUser(ctx, userID, id)
}

// UpdateSubscription replaces the subscription of one of the admin's devices.
func (s *PushDeviceService) UpdateSubscription(ctx context.Context, userID, id int64, sub PushSubscription) (*PushDevice, error) {
	normalized, err := NormalizePushSubscription(sub)
	if err != nil {
		return nil, err
	}
	return s.repo.UpdateDevicePrefs(ctx, userID, id, normalized)
}

// Test sends a push.test notification to one of the admin's devices right away. iOS devices return
// ErrPushNotConfigured while APNs is not set up; a rejected push returns ErrPushDeliveryFailed with the provider's
// status and reason. A success also enables a disabled device again.
func (s *PushDeviceService) Test(ctx context.Context, userID, id int64) (*PushTestResult, error) {
	d, err := s.repo.GetDeviceForUser(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	creds, err := s.codec.open(d.CredentialsRaw, d.CredentialsEncrypted)
	if err != nil {
		_ = s.repo.MarkDeviceError(ctx, d.ID, "stored credentials cannot be decrypted", PushDisabledSecretUndecryptable, now)
		return nil, ErrPushDeliveryFailed.WithMetadata(map[string]string{"provider_status": "0", "provider_reason": PushDisabledSecretUndecryptable})
	}
	ev := newPushTestEvent(now)
	msg := ev.MessageFor(d.Locale, pushSiteFor(d, s.defaultSite))
	msg.DeliveryID = "test_" + uuid.NewString()

	sendCtx, cancel := context.WithTimeout(ctx, pushTestTimeout)
	defer cancel()
	result, sendErr := s.senders.Send(sendCtx, pushTargetOf(d, creds), &msg)
	if sendErr == nil {
		if err := s.repo.MarkDeviceSuccess(ctx, d.ID, now); err != nil {
			logger.LegacyPrintf("service.push", "[Push] record test success failed: device=%d err=%v", d.ID, err)
		}
		return &PushTestResult{Delivered: true, ProviderStatus: result.ProviderStatus}, nil
	}
	if errors.Is(sendErr, ErrPushNotConfigured) {
		return nil, ErrPushNotConfigured
	}
	if err := s.repo.MarkDeviceError(ctx, d.ID, describePushFailure(result, sendErr), result.DisableReason, now); err != nil {
		logger.LegacyPrintf("service.push", "[Push] record test failure failed: device=%d err=%v", d.ID, err)
	}
	return nil, ErrPushDeliveryFailed.WithMetadata(map[string]string{
		"provider_status": strconv.Itoa(result.ProviderStatus),
		"provider_reason": result.ProviderReason,
	})
}

func pushTargetOf(d *PushDevice, creds PushCredentials) PushTarget {
	return PushTarget{
		Platform:    d.Platform,
		Token:       creds.Token,
		Secret:      creds.Secret,
		Endpoint:    creds.Endpoint,
		BundleID:    d.BundleID,
		Environment: d.Environment,
	}
}

// describePushFailure is the device's last_error: provider status and reason, never the target URL.
func describePushFailure(result PushSendResult, err error) string {
	parts := []string{}
	if result.ProviderStatus > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", result.ProviderStatus))
	}
	if result.ProviderReason != "" {
		parts = append(parts, result.ProviderReason)
	}
	if len(parts) == 0 && err != nil {
		parts = append(parts, err.Error())
	}
	return sanitizePushText(strings.Join(parts, ": "), 300)
}

// pushCredentialCodec stores credentials as JSON, encrypted only with a fixed secret encryption key: an
// auto-generated key changes on restart and would make the stored value undecryptable.
type pushCredentialCodec struct {
	encryptor SecretEncryptor
	encrypt   bool
}

func newPushCredentialCodec(encryptor SecretEncryptor, cfg *config.Config) *pushCredentialCodec {
	return &pushCredentialCodec{encryptor: encryptor, encrypt: encryptor != nil && cfg != nil && cfg.Totp.EncryptionKeyConfigured}
}

func (c *pushCredentialCodec) seal(creds PushCredentials) (string, bool, error) {
	raw, err := json.Marshal(creds)
	if err != nil {
		return "", false, err
	}
	if !c.encrypt {
		return string(raw), false, nil
	}
	enc, err := c.encryptor.Encrypt(string(raw))
	if err != nil {
		return "", false, fmt.Errorf("encrypt push credentials: %w", err)
	}
	return enc, true, nil
}

func (c *pushCredentialCodec) open(raw string, encrypted bool) (PushCredentials, error) {
	plain := raw
	if encrypted {
		if c.encryptor == nil {
			return PushCredentials{}, errors.New("no encryptor for encrypted push credentials")
		}
		dec, err := c.encryptor.Decrypt(raw)
		if err != nil {
			return PushCredentials{}, err
		}
		plain = dec
	}
	var creds PushCredentials
	if err := json.Unmarshal([]byte(plain), &creds); err != nil {
		return PushCredentials{}, err
	}
	return creds, nil
}
