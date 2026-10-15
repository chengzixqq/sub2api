//go:build embed

package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFrontendHTMLLoad_ConditionalRequestRetainsNonceBodyCompatibility(t *testing.T) {
	provider := &countingBrandingProvider{settings: map[string]string{"site_name": "Nonce compatibility"}}
	server, err := NewFrontendServer(provider)
	require.NoError(t, err)
	router := gin.New()
	router.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true}, nil), server.Middleware())
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	require.Equal(t, http.StatusOK, first.Code)
	noncePattern := regexp.MustCompile(`'nonce-([^']+)'`)
	firstNonce := noncePattern.FindStringSubmatch(first.Header().Get("Content-Security-Policy"))
	require.Len(t, firstNonce, 2)
	require.Contains(t, first.Body.String(), `nonce="`+firstNonce[1]+`"`)
	require.NotEmpty(t, first.Header().Get("ETag"))

	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	router.ServeHTTP(second, request)
	secondNonce := noncePattern.FindStringSubmatch(second.Header().Get("Content-Security-Policy"))
	require.Len(t, secondNonce, 2)
	require.NotEqual(t, firstNonce[1], secondNonce[1], "each actual middleware request must receive a fresh CSP nonce")
	t.Logf("conditional nonce HTML: status=%d, body bytes=%d, provider reads=%d", second.Code, second.Body.Len(), provider.calls.Load())
	require.Equal(t, http.StatusOK, second.Code, "a 304 would pair the new CSP nonce with the cached body containing the previous nonce")
	require.Contains(t, second.Body.String(), `nonce="`+secondNonce[1]+`"`)
	require.NotContains(t, second.Body.String(), `nonce="`+firstNonce[1]+`"`)
	require.Equal(t, first.Header().Get("ETag"), second.Header().Get("ETag"), "only the request nonce changed")
	require.EqualValues(t, 1, provider.calls.Load(), "a fresh response body still uses the shared server-side settings cache")
}

func TestFrontendHTMLLoad_SettingsFailureIsRetryable(t *testing.T) {
	var calls atomic.Int32
	server, router := htmlLoadRouter(t, htmlLoadProvider{load: func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("temporary settings failure")
		}
		return map[string]string{"site_name": "Recovered settings"}, nil
	}})
	first := readHTML(router, context.Background())
	require.Equal(t, http.StatusOK, first.Code)
	require.NotContains(t, first.Body.String(), "Recovered settings")
	require.Nil(t, server.cache.Get(), "failed settings must not become a valid shared cache entry")
	second := readHTML(router, context.Background())
	require.Equal(t, http.StatusOK, second.Code)
	require.Contains(t, second.Body.String(), "Recovered settings")
	third := readHTML(router, context.Background())
	require.Contains(t, third.Body.String(), "Recovered settings")
	require.EqualValues(t, 2, calls.Load(), "the failed load is retried once and the successful result is reused")
}
