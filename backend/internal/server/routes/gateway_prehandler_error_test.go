//go:build unit

package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func requireGatewayPrehandlerError(t *testing.T, w *httptest.ResponseRecorder, envelope, errorType string) string {
	t.Helper()
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, envelope, body.Type)
	require.Equal(t, errorType, body.Error.Type)
	require.NotEmpty(t, body.Error.Message)
	if envelope == "error" {
		require.Regexp(t, `^[\x00-\x7F]+$`, body.Error.Message)
	}
	return body.Error.Message
}

func TestGatewayPrehandlerRequestBodyErrors(t *testing.T) {
	for _, tc := range []struct {
		path, platform      string
		allowlist           bool
		readError           error
		status              int
		envelope, errorType string
	}{
		{"/v1/messages", service.PlatformAnthropic, true, io.ErrUnexpectedEOF, 400, "error", "invalid_request_error"},
		{"/antigravity/v1/messages", service.PlatformAnthropic, true, nil, 413, "error", "request_too_large"},
		{"/messages/count_tokens", service.PlatformComposite, false, nil, 413, "error", "request_too_large"},
		{"/v1/messages/count_tokens", service.PlatformComposite, false, os.ErrDeadlineExceeded, 408, "error", "invalid_request_error"},
		{"/v1/messages", service.PlatformComposite, false, syscall.ECONNRESET, 499, "error", "invalid_request_error"},
		{"/antigravity/v1/messages/count_tokens", service.PlatformAnthropic, true, httputil.ErrRequestBodyMemoryBudget, 503, "error", "api_error"},
		{"/v1/responses", service.PlatformComposite, false, nil, 413, "", "invalid_request_error"},
	} {
		t.Run(tc.path+"/"+tc.platform, func(t *testing.T) {
			router := newGatewayRoutesTestRouterWithGroup(allowlistGroup(tc.platform, tc.allowlist, "claude-*"))
			var body io.Reader
			if tc.readError == nil {
				body = strings.NewReader(strings.Repeat("x", 1024*1024+1))
			} else {
				body = iotest.ErrReader(tc.readError)
			}
			req := httptest.NewRequest(http.MethodPost, tc.path, body)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			requireGatewayPrehandlerError(t, w, tc.envelope, tc.errorType)
			if tc.status == http.StatusServiceUnavailable {
				require.Equal(t, "1", w.Header().Get("Retry-After"))
			}
		})
	}
}

func TestGatewayPrehandlerSharedModelsAllowlist(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(allowlistGroup(service.PlatformOpenAI, true, "gpt-allowed"))
	for _, tc := range []struct {
		path, header, value, envelope, errorType string
	}{
		{"/v1/models", "x-api-key", "key", "error", "not_found_error"},
		{"/models", "anthropic-version", "2023-06-01", "error", "not_found_error"},
		{"/v1/models", "Authorization", "Bearer key", "", "invalid_request_error"},
	} {
		t.Run(tc.path+"/"+tc.header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path+"?model="+url.QueryEscape("模型"), nil)
			req.Header.Set(tc.header, tc.value)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusNotFound, w.Code)
			message := requireGatewayPrehandlerError(t, w, tc.envelope, tc.errorType)
			if tc.envelope == "" {
				require.Equal(t, `Model "模型" is not available for this group`, message)
				require.Contains(t, w.Body.String(), `"code":"model_not_found"`)
			}
		})
	}
}

func TestGatewayPrehandlerCompositeResolutionErrors(t *testing.T) {
	resolver := service.NewCompositeRouteResolver(nil)
	resolver.SetModelOwnershipResolver(func(_ context.Context, _ int64, model string) (service.CompositeModelOwnership, error) {
		if model == "unavailable-alias" {
			return service.CompositeModelOwnership{}, errors.New("catalog unavailable")
		}
		return service.CompositeModelOwnership{Ambiguous: true, OpenAIGatewayOAuthClaimed: true}, nil
	})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{ID: 1, Platform: service.PlatformComposite}})
	}, compositeTargetPlatformMiddleware(resolver))
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		router.POST(path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	}
	for _, tc := range []struct {
		path, model         string
		status              int
		envelope, errorType string
	}{
		{"/v1/messages", "gpt-模型", 400, "error", "invalid_request_error"},
		{"/v1/messages", "unavailable-alias", 500, "error", "api_error"},
		{"/v1/responses", "gpt-模型", 400, "", "invalid_request_error"},
		{"/v1/responses", "unavailable-alias", 500, "", "server_error"},
	} {
		t.Run(tc.path+"/"+tc.model, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"model":"`+tc.model+`"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			message := requireGatewayPrehandlerError(t, w, tc.envelope, tc.errorType)
			if tc.status == 400 {
				require.Contains(t, message, "explicit composite route")
				if tc.envelope == "" {
					require.Contains(t, message, tc.model)
				}
			}
		})
	}
}

func TestClaudePrehandlerFill(t *testing.T) {
	repo := &authAlignmentSettings{channelMonitorRouteSettingRepoStub: channelMonitorRouteSettingRepoStub{
		values: map[string]string{service.SettingKeyClaudeCustomization: `{"request_id_mode":"fill"}`},
	}}
	settings := service.NewSettingService(repo, nil)
	settings.AnyAlignmentReject(t.Context(), service.ClaudeFieldUnknownBetaHeader)
	reads := repo.settingsReads.Load()
	group := allowlistGroup(service.PlatformAnthropic, true, "visible")
	router := newGatewayRoutesTestRouterWithGroupSettings(group, settings)
	for _, tc := range []struct {
		path, method string
		body         io.Reader
		status       int
	}{
		{"/v1/messages", "POST", strings.NewReader(`{"model":"blocked"}`), 404},
		{"/v1/models?model=blocked", "GET", nil, 404},
		{"/v1/messages", "POST", strings.NewReader(strings.Repeat("x", 1024*1024+1)), 413},
		{"/v1/messages", "POST", iotest.ErrReader(io.ErrUnexpectedEOF), 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, tc.body)
		req.Header.Set("x-api-key", "key")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, tc.status, rec.Code, tc.path)
		requireClaudePrehandlerID(t, rec)
	}
	composite := service.NewCompositeRouteResolver(nil)
	composite.SetModelOwnershipResolver(func(_ context.Context, _ int64, model string) (service.CompositeModelOwnership, error) {
		if model == "failed" {
			return service.CompositeModelOwnership{}, errors.New("catalog unavailable")
		}
		return service.CompositeModelOwnership{Ambiguous: true, OpenAIGatewayOAuthClaimed: true}, nil
	})
	router = gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{ID: 1, Platform: service.PlatformComposite}})
	}, middleware.GroupModelAllowlistWithSettings(settings), compositeTargetPlatformMiddleware(composite))
	router.POST("/v1/messages", func(c *gin.Context) { c.Status(204) })
	for model, status := range map[string]int{"gpt-ambiguous": 400, "failed": 500} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+model+`"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, status, rec.Code)
		requireClaudePrehandlerID(t, rec)
	}
	require.Equal(t, reads, repo.settingsReads.Load())
}

func requireClaudePrehandlerID(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	id := rec.Result().Header.Get("Request-Id")
	require.Regexp(t, "^req_011C[1-9A-HJ-NP-Za-km-z]{20}$", id)
	require.Contains(t, rec.Body.String(), `"request_id":"`+id+`"`)
}
