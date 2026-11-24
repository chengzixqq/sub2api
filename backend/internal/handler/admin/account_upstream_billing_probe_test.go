package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupUpstreamBillingProbeRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetUpstreamBillingProbeService(service.NewUpstreamBillingProbeService(nil, nil, nil))

	router := gin.New()
	router.GET("/admin/accounts/upstream-billing-probe/settings", handler.GetUpstreamBillingProbeSettings)
	router.POST("/admin/accounts/upstream-billing-probe/batch", handler.ProbeUpstreamBillingBatch)
	router.POST("/admin/accounts/upstream-billing-probe/group-preview", handler.PreviewUpstreamGroup)
	router.PUT("/admin/accounts/:id/upstream-billing-probe", handler.SetUpstreamBillingProbeEnabled)
	return router
}

func TestAccountHandlerUpstreamBillingGroupPreviewKeepsStoredCredentialsBound(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	for _, body := range []string{
		`{"account_id":7,"base_url":"https://different.example"}`,
		`{"account_id":7,"proxy_id":8}`,
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/accounts/upstream-billing-probe/group-preview", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
}

func TestAccountHandlerGetUpstreamBillingProbeSettingsReturnsDefaults(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-probe/settings", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data service.UpstreamBillingProbeSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Data.Enabled)
	require.Equal(t, 30, response.Data.IntervalMinutes)
}

func TestAccountHandlerProbeUpstreamBillingBatchValidatesIDs(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()

	for _, body := range []string{`{"account_ids":[]}`, `{"account_ids":[0]}`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/admin/accounts/upstream-billing-probe/batch", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}

func TestAccountHandlerSetUpstreamBillingProbeEnabledRejectsInvalidID(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/not-an-id/upstream-billing-probe", bytes.NewBufferString(`{"enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestAccountHandlerSetUpstreamBillingProbeEnabledRequiresValue(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/1/upstream-billing-probe", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

type groupPreviewTransport struct {
	service.HTTPUpstream
	request *http.Request
	proxy   string
}

func (u *groupPreviewTransport) DoWithTLS(req *http.Request, proxy string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.request, u.proxy = req, proxy
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_name":"Preview"}`))}, nil
}

func TestAccountHandlerUpstreamBillingGroupPreviewWorkspace(t *testing.T) {
	proxyID := int64(8)
	svc := newReauthAdmin(&service.Account{ID: 1, WorkspaceID: 7, ProxyID: &proxyID, Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "stored-key", "base_url": "https://stored.example"}})
	svc.proxyWorkspaces = map[int64]int64{8: 9, 12: 7}
	svc.proxies = []service.Proxy{{ID: 12, Protocol: "http", Host: "proxy.example", Port: 3128}}
	upstream := &groupPreviewTransport{}
	ts := service.NewAccountTestService(nil, nil, nil, nil, nil, upstream, &config.Config{}, nil)
	h := &AccountHandler{adminService: svc, upstreamBillingProbe: service.NewUpstreamBillingProbeService(nil, ts, nil)}
	r := gin.New()
	r.POST("/preview", h.PreviewUpstreamGroup)
	ctx := service.WithScope(t.Context(), service.VendorScope(7, service.WorkspacePermissions{AccountManage: true}))
	send := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/preview", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	for _, body := range []string{
		`{"account_id":1}`,
		`{"account_id":2}`,
		`{"platform":"openai","base_url":"https://draft.example","api_key":"draft-key","proxy_id":8}`,
	} {
		require.Equal(t, http.StatusNotFound, send(body).Code)
		require.Nil(t, upstream.request)
	}
	w := send(`{"platform":"openai","base_url":"https://draft.example","api_key":"draft-key","proxy_id":12}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Preview")
	require.Equal(t, "draft.example", upstream.request.URL.Host)
	require.Equal(t, "Bearer draft-key", upstream.request.Header.Get("Authorization"))
	require.Equal(t, "http://proxy.example:3128", upstream.proxy)
}
