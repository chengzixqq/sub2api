package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Native session binding modes (session_binding_native_mode). They apply only
// to sessions whose login declared a native client; Web sessions always bind
// IP + UA.
const (
	SessionBindingModeStrict = "strict"
	SessionBindingModeDevice = "device"
	SessionBindingModeUAOnly = "ua_only"
	SessionBindingModeOff    = "off"

	SettingKeySessionBindingNativeMode = "session_binding_native_mode"

	// The stored binding hash carries its mode, so a session keeps the mode it
	// was issued with: a stolen Web token cannot be downgraded. No prefix is the
	// original IP + UA hash.
	bindingPrefixDevice = "d1:"
	bindingPrefixUA     = "u1:"
	bindingPrefixOff    = "o1:"

	maxSessionDeviceIDLen = 128
)

var errInvalidSessionBindingMode = infraerrors.BadRequest("INVALID_SESSION_BINDING_NATIVE_MODE", "session_binding_native_mode must be strict, device, ua_only or off")

// ValidateSessionBindingNativeMode accepts the four modes; empty means strict.
func ValidateSessionBindingNativeMode(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "", SessionBindingModeStrict:
		return SessionBindingModeStrict, nil
	case SessionBindingModeDevice, SessionBindingModeUAOnly, SessionBindingModeOff:
		return strings.TrimSpace(mode), nil
	default:
		return "", errInvalidSessionBindingMode
	}
}

func parseSessionBindingNativeMode(raw string) string {
	mode, err := ValidateSessionBindingNativeMode(raw)
	if err != nil {
		return SessionBindingModeStrict
	}
	return mode
}

// SessionClient is what a native app declares at login.
type SessionClient struct {
	Type       string `json:"type"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

// IsNative reports whether the client is a native app (ios / android).
func (c *SessionClient) IsNative() bool {
	if c == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.Type)) {
	case "ios", "android":
		return true
	default:
		return false
	}
}

// NormalizeSessionDeviceID trims and bounds a client-declared device id.
func NormalizeSessionDeviceID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > maxSessionDeviceIDLen {
		id = id[:maxSessionDeviceIDLen]
	}
	return id
}

type sessionClientCtxKey struct{}
type sessionBindingModeCtxKey struct{}

// WithSessionClient records the login's declared client for token issuance.
func WithSessionClient(ctx context.Context, client *SessionClient) context.Context {
	if client == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionClientCtxKey{}, client)
}

// SessionClientFromContext returns the declared client, if any.
func SessionClientFromContext(ctx context.Context) *SessionClient {
	return sessionClientFromContext(ctx)
}

func sessionClientFromContext(ctx context.Context) *SessionClient {
	if ctx == nil {
		return nil
	}
	client, _ := ctx.Value(sessionClientCtxKey{}).(*SessionClient)
	return client
}

// withInheritedBindingMode makes tokens issued on refresh keep the mode of the
// session they continue.
func withInheritedBindingMode(ctx context.Context, storedHash string) context.Context {
	return context.WithValue(ctx, sessionBindingModeCtxKey{}, bindingModeOf(storedHash))
}

func bindingModeOf(stored string) string {
	switch {
	case strings.HasPrefix(stored, bindingPrefixDevice):
		return SessionBindingModeDevice
	case strings.HasPrefix(stored, bindingPrefixUA):
		return SessionBindingModeUAOnly
	case strings.HasPrefix(stored, bindingPrefixOff):
		return SessionBindingModeOff
	default:
		return SessionBindingModeStrict
	}
}

func bindingShortHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:16])
}

// HashFor computes the binding hash for a mode; "" when the mode needs data
// the request does not have (device mode without a device id).
func (b *SessionBinding) HashFor(mode string) string {
	if b == nil {
		return ""
	}
	switch mode {
	case SessionBindingModeDevice:
		id := NormalizeSessionDeviceID(b.DeviceID)
		if id == "" {
			return ""
		}
		return bindingPrefixDevice + bindingShortHash("device", id)
	case SessionBindingModeUAOnly:
		return bindingPrefixUA + bindingShortHash("ua", strings.TrimSpace(b.UserAgent))
	case SessionBindingModeOff:
		return bindingPrefixOff
	default:
		return b.Hash()
	}
}

// Matches checks the current request against a stored binding hash, using the
// mode encoded in the stored value. An empty stored hash (issued before
// binding existed) always matches, as does a legacy check without any request
// fingerprint.
func (b *SessionBinding) Matches(stored string) bool {
	if stored == "" {
		return true
	}
	mode := bindingModeOf(stored)
	if mode == SessionBindingModeOff {
		return true
	}
	current := b.HashFor(mode)
	if mode == SessionBindingModeStrict && current == "" {
		return true
	}
	return current == stored
}

func sessionBindingMatches(ctx context.Context, stored string) bool {
	return SessionBindingFromContext(ctx).Matches(stored)
}

// issueBindingHash picks the binding for a new token: the inherited mode on
// refresh; the native mode setting for a native login; IP + UA otherwise.
func (s *AuthService) issueBindingHash(ctx context.Context) string {
	binding := SessionBindingFromContext(ctx)
	if binding == nil {
		return ""
	}
	if mode, ok := ctx.Value(sessionBindingModeCtxKey{}).(string); ok {
		if hash := binding.HashFor(mode); hash != "" {
			return hash
		}
		return binding.Hash()
	}
	client := sessionClientFromContext(ctx)
	if !client.IsNative() || s.settingService == nil {
		return binding.Hash()
	}
	mode := s.settingService.GetSessionBindingNativeMode(ctx)
	withDevice := *binding
	if id := NormalizeSessionDeviceID(client.DeviceID); id != "" {
		withDevice.DeviceID = id
	}
	if hash := withDevice.HashFor(mode); hash != "" {
		return hash
	}
	return binding.Hash()
}
