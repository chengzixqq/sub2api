package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/golang-jwt/jwt/v5"
)

// SettingKeyPushAPNsConfig stores the site's APNs settings as JSON (the p8 key encrypted). It is separate from the
// big admin settings PUT so the key can be written behind step-up on its own route.
const SettingKeyPushAPNsConfig = "push_apns_config"

// pushAPNsResolveTTL bounds how long another instance keeps using settings changed elsewhere.
const pushAPNsResolveTTL = time.Minute

var pushAPNsIDPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// pushAPNsSettings is the stored form; PrivateKey is the encrypted PEM.
type pushAPNsSettings struct {
	Enabled     bool   `json:"apns_enabled"`
	TeamID      string `json:"apns_team_id"`
	KeyID       string `json:"apns_key_id"`
	PrivateKey  string `json:"apns_private_key,omitempty"`
	Topic       string `json:"apns_topic"`
	Environment string `json:"apns_environment"`
}

// PushAPNsConfigView is the APNs configuration without the private key.
type PushAPNsConfigView struct {
	Enabled              bool   `json:"apns_enabled"`
	TeamID               string `json:"apns_team_id"`
	KeyID                string `json:"apns_key_id"`
	PrivateKeyConfigured bool   `json:"apns_private_key_configured"`
	Topic                string `json:"apns_topic"`
	Environment          string `json:"apns_environment"`
	// EncryptionKeyConfigured reports a fixed secret encryption key; without it the private key cannot be saved.
	EncryptionKeyConfigured bool `json:"encryption_key_configured"`
	// Ready reports that APNs pushes can be sent (enabled and complete).
	Ready bool `json:"ready"`
}

// PushAPNsConfigUpdate changes the given fields; nil keeps a field and an empty PrivateKey keeps the stored key.
type PushAPNsConfigUpdate struct {
	Enabled     *bool   `json:"apns_enabled"`
	TeamID      *string `json:"apns_team_id"`
	KeyID       *string `json:"apns_key_id"`
	PrivateKey  *string `json:"apns_private_key"`
	Topic       *string `json:"apns_topic"`
	Environment *string `json:"apns_environment"`
}

// PushAPNsRuntime is the decrypted configuration a sender uses.
type PushAPNsRuntime struct {
	TeamID      string
	KeyID       string
	Topic       string
	Environment string
	PrivateKey  *ecdsa.PrivateKey
	// Fingerprint changes with the key, so a cached provider token is re-signed.
	Fingerprint string
}

// PushConfigService reads and writes the site's APNs settings.
type PushConfigService struct {
	settingRepo   SettingRepository
	encryptor     SecretEncryptor
	keyConfigured bool
	now           func() time.Time

	mu         sync.Mutex
	cached     *PushAPNsRuntime
	cachedErr  error
	cachedAt   time.Time
	cacheValid bool
}

func NewPushConfigService(settingRepo SettingRepository, encryptor SecretEncryptor, cfg *config.Config) *PushConfigService {
	return &PushConfigService{
		settingRepo:   settingRepo,
		encryptor:     encryptor,
		keyConfigured: cfg != nil && cfg.Totp.EncryptionKeyConfigured,
		now:           time.Now,
	}
}

func (s *PushConfigService) load(ctx context.Context) (*pushAPNsSettings, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyPushAPNsConfig)
	if errors.Is(err, ErrSettingNotFound) {
		return &pushAPNsSettings{}, nil
	}
	if err != nil {
		return nil, err
	}
	var stored pushAPNsSettings
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, fmt.Errorf("decode push APNs settings: %w", err)
	}
	return &stored, nil
}

func (s *PushConfigService) view(stored *pushAPNsSettings) *PushAPNsConfigView {
	env := stored.Environment
	if env == "" {
		env = pushEnvProduction
	}
	return &PushAPNsConfigView{
		Enabled:                 stored.Enabled,
		TeamID:                  stored.TeamID,
		KeyID:                   stored.KeyID,
		PrivateKeyConfigured:    stored.PrivateKey != "",
		Topic:                   stored.Topic,
		Environment:             env,
		EncryptionKeyConfigured: s.keyConfigured,
		Ready:                   stored.Enabled && stored.TeamID != "" && stored.KeyID != "" && stored.PrivateKey != "",
	}
}

// Get returns the APNs settings without the private key (station owner only).
func (s *PushConfigService) Get(ctx context.Context) (*PushAPNsConfigView, error) {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	stored, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(stored), nil
}

// Update merges in, validates and saves (station owner only). A new private key must be a P-256 PEM (Apple .p8)
// and needs a fixed secret encryption key.
func (s *PushConfigService) Update(ctx context.Context, in PushAPNsConfigUpdate) (*PushAPNsConfigView, error) {
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	stored, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	next := *stored
	if err := applyPushAPNsUpdate(&next, in); err != nil {
		return nil, err
	}
	if in.PrivateKey != nil && strings.TrimSpace(*in.PrivateKey) != "" {
		pemKey := strings.TrimSpace(*in.PrivateKey)
		if _, err := parseAPNsPrivateKey(pemKey); err != nil {
			return nil, pushFieldError(ErrInvalidAPNsConfig, "apns_private_key", "apns_private_key must be the PEM of an APNs auth key (.p8, P-256)")
		}
		if !s.keyConfigured || s.encryptor == nil {
			return nil, ErrSecretEncryptionKeyNotConfigured
		}
		enc, err := s.encryptor.Encrypt(pemKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt APNs private key: %w", err)
		}
		next.PrivateKey = enc
	}
	if next.Enabled && (next.TeamID == "" || next.KeyID == "" || next.PrivateKey == "") {
		return nil, pushFieldError(ErrInvalidAPNsConfig, "apns_enabled", "enabling APNs needs apns_team_id, apns_key_id and apns_private_key")
	}
	data, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyPushAPNsConfig, string(data)); err != nil {
		return nil, fmt.Errorf("save push APNs settings: %w", err)
	}
	s.Invalidate()
	return s.view(&next), nil
}

func applyPushAPNsUpdate(next *pushAPNsSettings, in PushAPNsConfigUpdate) error {
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.TeamID != nil {
		v := strings.ToUpper(strings.TrimSpace(*in.TeamID))
		if v != "" && !pushAPNsIDPattern.MatchString(v) {
			return pushFieldError(ErrInvalidAPNsConfig, "apns_team_id", "apns_team_id must be the 10-character Apple Team ID")
		}
		next.TeamID = v
	}
	if in.KeyID != nil {
		v := strings.ToUpper(strings.TrimSpace(*in.KeyID))
		if v != "" && !pushAPNsIDPattern.MatchString(v) {
			return pushFieldError(ErrInvalidAPNsConfig, "apns_key_id", "apns_key_id must be the 10-character key ID")
		}
		next.KeyID = v
	}
	if in.Topic != nil {
		v := strings.TrimSpace(*in.Topic)
		if v != "" && !pushBundleIDPattern.MatchString(v) {
			return pushFieldError(ErrInvalidAPNsConfig, "apns_topic", "apns_topic must be the app bundle identifier")
		}
		next.Topic = v
	}
	if in.Environment != nil {
		v := strings.ToLower(strings.TrimSpace(*in.Environment))
		if v == "" {
			v = pushEnvProduction
		}
		if v != pushEnvProduction && v != pushEnvSandbox {
			return pushFieldError(ErrInvalidAPNsConfig, "apns_environment", "apns_environment must be production or sandbox")
		}
		next.Environment = v
	}
	return nil
}

func parseAPNsPrivateKey(pemKey string) (*ecdsa.PrivateKey, error) {
	key, err := jwt.ParseECPrivateKeyFromPEM([]byte(pemKey))
	if err != nil {
		return nil, err
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("APNs auth keys use P-256")
	}
	return key, nil
}

// Invalidate drops the cached runtime configuration.
func (s *PushConfigService) Invalidate() {
	s.mu.Lock()
	s.cacheValid = false
	s.cached, s.cachedErr = nil, nil
	s.mu.Unlock()
}

// Resolve returns the decrypted APNs configuration, or ErrPushNotConfigured while APNs is disabled, incomplete or
// its key cannot be decrypted. The result is cached for a minute.
func (s *PushConfigService) Resolve(ctx context.Context) (*PushAPNsRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.cacheValid && now.Sub(s.cachedAt) < pushAPNsResolveTTL {
		return s.cached, s.cachedErr
	}
	rt, err := s.resolveUncached(ctx)
	if err != nil && !errors.Is(err, ErrPushNotConfigured) {
		// Storage errors are not cached: the next push retries.
		return nil, err
	}
	s.cached, s.cachedErr, s.cachedAt, s.cacheValid = rt, err, now, true
	return rt, err
}

func (s *PushConfigService) resolveUncached(ctx context.Context) (*PushAPNsRuntime, error) {
	stored, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if !stored.Enabled || stored.TeamID == "" || stored.KeyID == "" || stored.PrivateKey == "" || s.encryptor == nil {
		return nil, ErrPushNotConfigured
	}
	pemKey, err := s.encryptor.Decrypt(stored.PrivateKey)
	if err != nil {
		logger.LegacyPrintf("service.push", "[Push] APNs private key cannot be decrypted (secret encryption key changed?); APNs pushes are disabled")
		return nil, ErrPushNotConfigured
	}
	key, err := parseAPNsPrivateKey(pemKey)
	if err != nil {
		return nil, ErrPushNotConfigured
	}
	sum := sha256.Sum256([]byte(pemKey))
	env := stored.Environment
	if env == "" {
		env = pushEnvProduction
	}
	return &PushAPNsRuntime{
		TeamID: stored.TeamID, KeyID: stored.KeyID, Topic: stored.Topic, Environment: env,
		PrivateKey: key, Fingerprint: hex.EncodeToString(sum[:8]),
	}, nil
}
