package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// pushHandlerRepo is a minimal in-memory PushRepository for the handler tests.
type pushHandlerRepo struct {
	service.PushRepository
	mu      sync.Mutex
	nextID  int64
	devices map[int64]*service.PushDevice
}

func (r *pushHandlerRepo) UpsertDevice(_ context.Context, d *service.PushDevice, _ bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.devices {
		if existing.Platform == d.Platform && existing.TokenHash == d.TokenHash {
			d.ID, d.CreatedAt = existing.ID, existing.CreatedAt
			cp := *d
			r.devices[d.ID] = &cp
			return false, nil
		}
	}
	r.nextID++
	d.ID, d.CreatedAt, d.UpdatedAt = r.nextID, time.Now(), time.Now()
	cp := *d
	r.devices[d.ID] = &cp
	return true, nil
}

func (r *pushHandlerRepo) CountDevicesByUserExcept(context.Context, int64, string, string) (int, error) {
	return 0, nil
}

func (r *pushHandlerRepo) GetDeviceForUser(_ context.Context, userID, id int64) (*service.PushDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok || d.UserID != userID {
		return nil, service.ErrPushDeviceNotFound
	}
	cp := *d
	return &cp, nil
}

func (r *pushHandlerRepo) ListDevicesByUser(_ context.Context, userID int64) ([]*service.PushDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*service.PushDevice{}
	for _, d := range r.devices {
		if d.UserID == userID {
			cp := *d
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *pushHandlerRepo) MarkDeviceError(context.Context, int64, string, string, time.Time) error {
	return nil
}

type pushHandlerSettings struct{ service.SettingRepository }

func (pushHandlerSettings) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}

func newPushHandlerForTest() *PushHandler {
	cfg := &config.Config{}
	configSvc := service.NewPushConfigService(pushHandlerSettings{}, nil, cfg)
	devices := service.NewPushDeviceService(&pushHandlerRepo{devices: map[int64]*service.PushDevice{}}, nil, cfg, service.NewPushSenders(cfg, configSvc))
	return NewPushHandler(devices, configSvc)
}

func pushHandlerRouter(h *PushHandler, userID int64, extra ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
		c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.AdminScope()))
	})
	r.Use(extra...)
	g := r.Group("/api/v1/admin/push")
	g.POST("/devices", h.RegisterDevice)
	g.GET("/devices", h.ListDevices)
	g.POST("/devices/:id/test", h.TestDevice)
	g.PUT("/devices/:id/subscription", h.UpdateSubscription)
	g.GET("/config", h.GetConfig)
	return r
}

func doPushRequest(t *testing.T, r *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decoded), w.Body.String())
	return w.Code, decoded
}

func TestPushHandler_RegisterAndTestWithoutAPNs(t *testing.T) {
	r := pushHandlerRouter(newPushHandlerForTest(), 7)
	token := strings.Repeat("ab", 32)
	body := `{"platform":"ios","apns_token":"` + token + `","bundle_id":"com.example.sub2mobile","environment":"production","device_name":"我的 iPhone","locale":"zh-Hans","timezone":"Asia/Shanghai"}`

	code, resp := doPushRequest(t, r, http.MethodPost, "/api/v1/admin/push/devices", body)
	require.Equal(t, http.StatusCreated, code)
	data := resp["data"].(map[string]any)
	require.Equal(t, true, data["created"])
	require.Equal(t, "abab", data["token_hint"])
	require.NotContains(t, mustJSON(t, resp), token, "the token is never returned")
	id := data["id"]

	code, resp = doPushRequest(t, r, http.MethodPost, "/api/v1/admin/push/devices", body)
	require.Equal(t, http.StatusOK, code, "registering the same token again is idempotent")
	require.Equal(t, id, resp["data"].(map[string]any)["id"])
	require.Equal(t, false, resp["data"].(map[string]any)["created"])

	code, resp = doPushRequest(t, r, http.MethodPost, "/api/v1/admin/push/devices/1/test", "")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, "PUSH_NOT_CONFIGURED", resp["reason"])

	code, resp = doPushRequest(t, r, http.MethodPost, "/api/v1/admin/push/devices", `{"platform":"ios","apns_token":"xyz"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "INVALID_PUSH_TOKEN", resp["reason"])

	code, resp = doPushRequest(t, r, http.MethodPost, "/api/v1/admin/push/devices", `{"platform":`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "INVALID_PUSH_TARGET", resp["reason"])

	code, resp = doPushRequest(t, r, http.MethodPut, "/api/v1/admin/push/devices/1/subscription", `{"events":["ops_alert.fired"],"min_severity":"P9"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "INVALID_PUSH_SUBSCRIPTION", resp["reason"])

	code, resp = doPushRequest(t, r, http.MethodGet, "/api/v1/admin/push/devices", "")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, resp["data"].(map[string]any)["items"], 1)

	other := pushHandlerRouter(newPushHandlerForTest(), 8)
	code, resp = doPushRequest(t, other, http.MethodPost, "/api/v1/admin/push/devices/1/test", "")
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "PUSH_DEVICE_NOT_FOUND", resp["reason"])

	code, resp = doPushRequest(t, r, http.MethodGet, "/api/v1/admin/push/config", "")
	require.Equal(t, http.StatusOK, code)
	cfg := resp["data"].(map[string]any)
	require.Equal(t, false, cfg["apns_enabled"])
	require.Equal(t, false, cfg["apns_private_key_configured"])
	require.NotContains(t, cfg, "apns_private_key")
}

// A read-only sat_ token may register the App's own APNs device, but not a Bark / ntfy / webhook target: those send
// events to an arbitrary URL chosen by whoever holds the token.
func TestPushHandler_ReadOnlyTokenRegistersOnlyIOS(t *testing.T) {
	r := pushHandlerRouter(newPushHandlerForTest(), 7, func(c *gin.Context) {
		c.Set(middleware.ContextKeyAdminAccessTokenReadOnly, true)
	})
	for _, body := range []string{
		`{"platform":"webhook","endpoint":"https://hooks.example.com/x"}`,
		`{"platform":"ntfy","endpoint":"https://ntfy.sh","token":"sub2-alerts"}`,
		`{"platform":"bark","endpoint":"https://api.day.app","token":"BarkKey123"}`,
		`{"platform":" Webhook ","endpoint":"https://hooks.example.com/x"}`,
	} {
		code, resp := doPushRequest(t, r, http.MethodPost, "/api/v1/admin/push/devices", body)
		require.Equal(t, http.StatusForbidden, code, body)
		require.Equal(t, "ACCESS_TOKEN_READ_ONLY", resp["reason"], body)
	}
	code, resp := doPushRequest(t, r, http.MethodGet, "/api/v1/admin/push/devices", "")
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, resp["data"].(map[string]any)["items"], "nothing was registered")

	code, _ = doPushRequest(t, r, http.MethodPost, "/api/v1/admin/push/devices", `{"platform":"ios","apns_token":"`+strings.Repeat("ab", 32)+`"}`)
	require.Equal(t, http.StatusCreated, code, "the App's own APNs device is fine")

	full := pushHandlerRouter(newPushHandlerForTest(), 7)
	code, _ = doPushRequest(t, full, http.MethodPost, "/api/v1/admin/push/devices", `{"platform":"webhook","endpoint":"https://hooks.example.com/x"}`)
	require.Equal(t, http.StatusCreated, code, "a full session or write token may register any channel")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
