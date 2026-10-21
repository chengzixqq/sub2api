package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

const pushTestAPNsToken = "A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E8F90"

func newPushDeviceServiceForTest(repo *pushMemRepo, keyConfigured bool, sender *pushFakeSender) *PushDeviceService {
	cfg := &config.Config{}
	cfg.Totp.EncryptionKeyConfigured = keyConfigured
	cfg.Server.FrontendURL = "https://api.example.com"
	return NewPushDeviceService(repo, pushHexEncryptor{}, cfg, pushFakeSenders(sender))
}

func TestPushDeviceService_RegisterValidation(t *testing.T) {
	ctx := context.Background()
	svc := newPushDeviceServiceForTest(newPushMemRepo(), true, &pushFakeSender{})

	cases := []struct {
		name   string
		in     PushDeviceInput
		reason string
		field  string
	}{
		{"unknown platform", PushDeviceInput{Platform: "android", Token: "x"}, "INVALID_PUSH_TARGET", "platform"},
		{"apns not hex", PushDeviceInput{Platform: "ios", APNsToken: strings.Repeat("z", 64)}, "INVALID_PUSH_TOKEN", "apns_token"},
		{"apns too short", PushDeviceInput{Platform: "ios", APNsToken: "abcd"}, "INVALID_PUSH_TOKEN", "apns_token"},
		{"apns bad environment", PushDeviceInput{Platform: "ios", APNsToken: pushTestAPNsToken, Environment: "staging"}, "INVALID_PUSH_TARGET", "environment"},
		{"apns bad bundle", PushDeviceInput{Platform: "ios", APNsToken: pushTestAPNsToken, BundleID: "com example"}, "INVALID_PUSH_TARGET", "bundle_id"},
		{"bad timezone", PushDeviceInput{Platform: "ios", APNsToken: pushTestAPNsToken, Timezone: "Mars/Olympus"}, "INVALID_PUSH_TARGET", "timezone"},
		{"webhook private host", PushDeviceInput{Platform: "webhook", Endpoint: "https://127.0.0.1/hook"}, "INVALID_PUSH_TARGET", "endpoint"},
		{"webhook localhost", PushDeviceInput{Platform: "webhook", Endpoint: "https://localhost/hook"}, "INVALID_PUSH_TARGET", "endpoint"},
		{"webhook plain http", PushDeviceInput{Platform: "webhook", Endpoint: "http://hooks.example.com/x"}, "INVALID_PUSH_TARGET", "endpoint"},
		{"webhook missing endpoint", PushDeviceInput{Platform: "webhook"}, "INVALID_PUSH_TARGET", "endpoint"},
		{"bark missing key", PushDeviceInput{Platform: "bark", Endpoint: "https://api.day.app"}, "INVALID_PUSH_TOKEN", "token"},
		{"ntfy bad topic", PushDeviceInput{Platform: "ntfy", Endpoint: "https://ntfy.sh", Token: "a/b"}, "INVALID_PUSH_TOKEN", "token"},
		{"bad subscription", PushDeviceInput{Platform: "ios", APNsToken: pushTestAPNsToken, Subscription: &PushSubscription{MinSeverity: "P9"}}, "INVALID_PUSH_SUBSCRIPTION", "min_severity"},
	}
	for _, tc := range cases {
		_, _, err := svc.Register(ctx, 1, tc.in)
		require.Error(t, err, tc.name)
		require.Equal(t, tc.reason, infraerrors.Reason(err), tc.name)
		require.Equal(t, tc.field, infraerrors.FromError(err).Metadata["field"], tc.name)
	}
}

func TestPushDeviceService_RegisterIsIdempotentAndLowercasesAPNsToken(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	svc := newPushDeviceServiceForTest(repo, true, &pushFakeSender{})

	first, created, err := svc.Register(ctx, 1, PushDeviceInput{
		Platform: "ios", APNsToken: pushTestAPNsToken, BundleID: "com.example.sub2mobile",
		DeviceName: "我的\x00 iPhone\n" + strings.Repeat("长", 80), Locale: "zh-Hans", Timezone: "Asia/Shanghai",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "production", first.Environment, "environment defaults to production")
	require.Equal(t, "8f90", first.TokenHint)
	require.Equal(t, DefaultPushSubscription(), first.Subscription)
	require.NotContains(t, first.DeviceName, "\x00")
	require.NotContains(t, first.DeviceName, "\n")
	require.LessOrEqual(t, len([]rune(first.DeviceName)), 64)
	require.True(t, first.CredentialsEncrypted)
	require.NotContains(t, strings.ToLower(first.CredentialsRaw), strings.ToLower(pushTestAPNsToken), "the token is not stored in clear")

	again, created, err := svc.Register(ctx, 1, PushDeviceInput{Platform: "ios", APNsToken: strings.ToLower(pushTestAPNsToken)})
	require.NoError(t, err)
	require.False(t, created, "the same token (any case) updates the device")
	require.Equal(t, first.ID, again.ID)
	require.Equal(t, first.TokenHash, again.TokenHash)

	creds, err := svc.codec.open(again.CredentialsRaw, again.CredentialsEncrypted)
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(pushTestAPNsToken), creds.Token)
}

func TestPushDeviceService_PlaintextWithoutFixedKeyAndLimit(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	svc := newPushDeviceServiceForTest(repo, false, &pushFakeSender{})

	d, _, err := svc.Register(ctx, 1, PushDeviceInput{Platform: "webhook", Endpoint: "https://hooks.example.com/sub2/T0KEN", Secret: "s3cret"})
	require.NoError(t, err)
	require.False(t, d.CredentialsEncrypted, "an auto-generated key would not survive a restart")
	require.Equal(t, "hooks.example.com", d.EndpointHost)
	require.Empty(t, d.TokenHint)
	creds, err := svc.codec.open(d.CredentialsRaw, d.CredentialsEncrypted)
	require.NoError(t, err)
	require.Equal(t, PushCredentials{Secret: "s3cret", Endpoint: "https://hooks.example.com/sub2/T0KEN"}, creds)

	repo.countOver = PushMaxDevicesPerUser
	_, _, err = svc.Register(ctx, 1, PushDeviceInput{Platform: "ntfy", Endpoint: "https://ntfy.sh", Token: "sub2-alerts"})
	require.Equal(t, "PUSH_DEVICE_LIMIT_REACHED", infraerrors.Reason(err))
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
}

func TestPushDeviceService_PrivateEndpointWhenAllowed(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := NewPushDeviceService(newPushMemRepo(), pushHexEncryptor{}, cfg, pushFakeSenders(&pushFakeSender{}))
	d, created, err := svc.Register(context.Background(), 1, PushDeviceInput{Platform: "ntfy", Endpoint: "http://192.168.1.10:8080", Token: "home"})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "192.168.1.10", d.EndpointHost)
}

func TestPushDeviceService_OwnershipAndSubscription(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	svc := newPushDeviceServiceForTest(repo, true, &pushFakeSender{})

	d, _, err := svc.Register(ctx, 1, PushDeviceInput{Platform: "bark", Endpoint: "https://api.day.app/", Token: "BarkKey123"})
	require.NoError(t, err)

	_, err = svc.UpdateSubscription(ctx, 2, d.ID, PushSubscription{})
	require.ErrorIs(t, err, ErrPushDeviceNotFound)
	require.ErrorIs(t, svc.Delete(ctx, 2, d.ID), ErrPushDeviceNotFound)

	updated, err := svc.UpdateSubscription(ctx, 1, d.ID, PushSubscription{Events: []string{PushEventAccountTestFinished}, QuietHours: &PushQuietHours{Start: "23:00", End: "07:00"}})
	require.NoError(t, err)
	require.Equal(t, []string{PushEventAccountTestFinished}, updated.Subscription.Events)
	_, err = svc.UpdateSubscription(ctx, 1, d.ID, PushSubscription{Events: []string{"nope"}})
	require.Equal(t, "INVALID_PUSH_SUBSCRIPTION", infraerrors.Reason(err))

	list, err := svc.List(ctx, 2)
	require.NoError(t, err)
	require.Empty(t, list)
	require.NoError(t, svc.Delete(ctx, 1, d.ID))
	list, err = svc.List(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestPushDeviceService_Test(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	sender := &pushFakeSender{}
	svc := newPushDeviceServiceForTest(repo, true, sender)

	ios, _, err := svc.Register(ctx, 1, PushDeviceInput{Platform: "ios", APNsToken: pushTestAPNsToken, Locale: "zh-Hans", SiteTag: "我的站点"})
	require.NoError(t, err)

	sender.result = func(PushTarget) (PushSendResult, error) { return PushSendResult{}, ErrPushNotConfigured }
	_, err = svc.Test(ctx, 1, ios.ID)
	require.Equal(t, "PUSH_NOT_CONFIGURED", infraerrors.Reason(err))
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))

	sender.result = nil
	result, err := svc.Test(ctx, 1, ios.ID)
	require.NoError(t, err)
	require.True(t, result.Delivered)
	require.Equal(t, 200, result.ProviderStatus)
	sends := sender.sends()
	last := sends[len(sends)-1]
	require.Equal(t, strings.ToLower(pushTestAPNsToken), last.Target.Token)
	require.Equal(t, PushEventTest, last.Message.Type)
	require.Equal(t, "我的站点", last.Message.Site)
	require.Contains(t, last.Message.Title, "推送测试", "zh locale gets the Chinese text")
	require.NotNil(t, repo.device(ios.ID).LastSuccessAt)

	sender.result = func(PushTarget) (PushSendResult, error) {
		return PushSendResult{ProviderStatus: 410, ProviderReason: "Unregistered", DisableReason: PushDisabledUnregistered}, errPushProviderRejected
	}
	_, err = svc.Test(ctx, 1, ios.ID)
	require.Equal(t, "PUSH_DELIVERY_FAILED", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
	md := infraerrors.FromError(err).Metadata
	require.Equal(t, "410", md["provider_status"])
	require.Equal(t, "Unregistered", md["provider_reason"])
	require.Equal(t, PushDisabledUnregistered, repo.device(ios.ID).DisabledReason, "APNs 410 disables the device")

	_, err = svc.Test(ctx, 2, ios.ID)
	require.ErrorIs(t, err, ErrPushDeviceNotFound)

	webhook, _, err := svc.Register(ctx, 1, PushDeviceInput{Platform: "webhook", Endpoint: "https://hooks.example.com/x"})
	require.NoError(t, err)
	sender.result = nil
	_, err = svc.Test(ctx, 1, webhook.ID)
	require.NoError(t, err)
	require.Equal(t, "api.example.com", sender.sends()[len(sender.sends())-1].Message.Site, "site falls back to the frontend host")
}

func TestPushDeviceService_UndecryptableCredentialsDisableDevice(t *testing.T) {
	ctx := context.Background()
	repo := newPushMemRepo()
	svc := newPushDeviceServiceForTest(repo, true, &pushFakeSender{})
	d := repo.addDevice(&PushDevice{UserID: 1, Platform: PushPlatformBark, CredentialsRaw: "garbage", CredentialsEncrypted: true, Subscription: DefaultPushSubscription()})

	_, err := svc.Test(ctx, 1, d.ID)
	require.Equal(t, "PUSH_DELIVERY_FAILED", infraerrors.Reason(err))
	require.Equal(t, PushDisabledSecretUndecryptable, repo.device(d.ID).DisabledReason)
}
