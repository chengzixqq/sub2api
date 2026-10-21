package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// PushHandler manages the signed-in admin's push targets (APNs, Bark, ntfy, webhook) and the site's APNs settings.
type PushHandler struct {
	devices *service.PushDeviceService
	config  *service.PushConfigService
}

// errPushReadOnlyTokenIOSOnly: a read-only sat_ token may register the App's APNs device, but not a Bark / ntfy /
// webhook target, which would send every subscribed event to a URL chosen by whoever holds the token.
var errPushReadOnlyTokenIOSOnly = func() error {
	err := service.ErrAdminAccessTokenReadOnly.WithMetadata(map[string]string{"field": "platform"})
	err.Message = "a read-only admin access token can only register iOS (APNs) devices"
	return err
}()

func NewPushHandler(devices *service.PushDeviceService, config *service.PushConfigService) *PushHandler {
	return &PushHandler{devices: devices, config: config}
}

type registerPushDeviceRequest struct {
	Platform     string                    `json:"platform"`
	APNsToken    string                    `json:"apns_token"`
	Token        string                    `json:"token"`
	Secret       string                    `json:"secret"`
	Endpoint     string                    `json:"endpoint"`
	BundleID     string                    `json:"bundle_id"`
	Environment  string                    `json:"environment"`
	DeviceName   string                    `json:"device_name"`
	Locale       string                    `json:"locale"`
	Timezone     string                    `json:"timezone"`
	SiteTag      string                    `json:"site_tag"`
	Subscription *service.PushSubscription `json:"subscription"`
}

// pushDeviceView never contains the token, secret or full endpoint.
type pushDeviceView struct {
	ID             int64                    `json:"id"`
	Platform       string                   `json:"platform"`
	TokenHint      string                   `json:"token_hint"`
	EndpointHost   string                   `json:"endpoint_host"`
	BundleID       string                   `json:"bundle_id"`
	Environment    string                   `json:"environment"`
	DeviceName     string                   `json:"device_name"`
	Locale         string                   `json:"locale"`
	Timezone       string                   `json:"timezone"`
	SiteTag        string                   `json:"site_tag"`
	Enabled        bool                     `json:"enabled"`
	DisabledReason string                   `json:"disabled_reason"`
	DisabledAt     *time.Time               `json:"disabled_at"`
	LastSuccessAt  *time.Time               `json:"last_success_at"`
	LastError      string                   `json:"last_error"`
	LastErrorAt    *time.Time               `json:"last_error_at"`
	Encrypted      bool                     `json:"credentials_encrypted"`
	Subscription   service.PushSubscription `json:"subscription"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

type registeredPushDeviceView struct {
	pushDeviceView
	Created bool `json:"created"`
}

func toPushDeviceView(d *service.PushDevice) pushDeviceView {
	return pushDeviceView{
		ID: d.ID, Platform: d.Platform, TokenHint: d.TokenHint, EndpointHost: d.EndpointHost, BundleID: d.BundleID,
		Environment: d.Environment, DeviceName: d.DeviceName, Locale: d.Locale, Timezone: d.Timezone, SiteTag: d.SiteTag,
		Enabled: d.Enabled(), DisabledReason: d.DisabledReason, DisabledAt: d.DisabledAt, LastSuccessAt: d.LastSuccessAt,
		LastError: d.LastError, LastErrorAt: d.LastErrorAt, Encrypted: d.CredentialsEncrypted, Subscription: d.Subscription,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

func pushSubject(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}

func pushDeviceID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, service.ErrPushDeviceNotFound)
		return 0, false
	}
	return id, true
}

// RegisterDevice registers a push target of the current admin; 201 when created, 200 when the same token was
// registered before (an APNs token of another admin moves to the current one; a Bark / ntfy / webhook target of
// another active admin is 409 PUSH_TARGET_IN_USE).
// POST /api/v1/admin/push/devices
func (h *PushHandler) RegisterDevice(c *gin.Context) {
	userID, ok := pushSubject(c)
	if !ok {
		return
	}
	var req registerPushDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrInvalidPushTarget.WithMetadata(map[string]string{"field": "body"}))
		return
	}
	if c.GetBool(middleware.ContextKeyAdminAccessTokenReadOnly) && !strings.EqualFold(strings.TrimSpace(req.Platform), service.PushPlatformIOS) {
		response.ErrorFrom(c, errPushReadOnlyTokenIOSOnly)
		return
	}
	device, created, err := h.devices.Register(c.Request.Context(), userID, service.PushDeviceInput{
		Platform: req.Platform, APNsToken: req.APNsToken, Token: req.Token, Secret: req.Secret, Endpoint: req.Endpoint,
		BundleID: req.BundleID, Environment: req.Environment, DeviceName: req.DeviceName, Locale: req.Locale,
		Timezone: req.Timezone, SiteTag: req.SiteTag, Subscription: req.Subscription,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	view := registeredPushDeviceView{pushDeviceView: toPushDeviceView(device), Created: created}
	if created {
		response.Created(c, view)
		return
	}
	response.Success(c, view)
}

// ListDevices lists the current admin's push targets.
// GET /api/v1/admin/push/devices
func (h *PushHandler) ListDevices(c *gin.Context) {
	userID, ok := pushSubject(c)
	if !ok {
		return
	}
	devices, err := h.devices.List(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items := make([]pushDeviceView, 0, len(devices))
	for _, d := range devices {
		items = append(items, toPushDeviceView(d))
	}
	response.Success(c, gin.H{"items": items})
}

// DeleteDevice removes one of the current admin's push targets (the App calls it when signing out).
// DELETE /api/v1/admin/push/devices/:id
func (h *PushHandler) DeleteDevice(c *gin.Context) {
	userID, ok := pushSubject(c)
	if !ok {
		return
	}
	id, ok := pushDeviceID(c)
	if !ok {
		return
	}
	if err := h.devices.Delete(c.Request.Context(), userID, id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Push device deleted"})
}

// UpdateSubscription replaces a device's subscription.
// PUT /api/v1/admin/push/devices/:id/subscription
func (h *PushHandler) UpdateSubscription(c *gin.Context) {
	userID, ok := pushSubject(c)
	if !ok {
		return
	}
	id, ok := pushDeviceID(c)
	if !ok {
		return
	}
	var sub service.PushSubscription
	if err := c.ShouldBindJSON(&sub); err != nil {
		response.ErrorFrom(c, service.ErrInvalidPushSubscription.WithMetadata(map[string]string{"field": "body"}))
		return
	}
	device, err := h.devices.UpdateSubscription(c.Request.Context(), userID, id, sub)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toPushDeviceView(device))
}

// TestDevice sends a push.test notification right away.
// POST /api/v1/admin/push/devices/:id/test
func (h *PushHandler) TestDevice(c *gin.Context) {
	userID, ok := pushSubject(c)
	if !ok {
		return
	}
	id, ok := pushDeviceID(c)
	if !ok {
		return
	}
	result, err := h.devices.Test(c.Request.Context(), userID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"delivered": result.Delivered, "provider_status": result.ProviderStatus})
}

// GetConfig returns the site's APNs settings without the private key.
// GET /api/v1/admin/push/config
func (h *PushHandler) GetConfig(c *gin.Context) {
	view, err := h.config.Get(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// UpdateConfig changes the site's APNs settings (route behind step-up).
// PUT /api/v1/admin/push/config
func (h *PushHandler) UpdateConfig(c *gin.Context) {
	var req service.PushAPNsConfigUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrInvalidAPNsConfig.WithMetadata(map[string]string{"field": "body"}))
		return
	}
	view, err := h.config.Update(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}
