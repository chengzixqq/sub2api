//go:build embed

package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type countingBrandingProvider struct {
	calls    atomic.Int32
	settings any
}

func (p *countingBrandingProvider) GetPublicSettingsForInjection(context.Context) (any, error) {
	p.calls.Add(1)
	return p.settings, nil
}

func TestFrontendServer_BrandingUnknownHashesBoundSettingsLoads(t *testing.T) {
	provider := &countingBrandingProvider{settings: map[string]string{"site_logo": "https://example.com/logo.png"}}
	server, err := NewFrontendServer(provider)
	require.NoError(t, err)
	router := gin.New()
	router.Use(server.Middleware())
	var workers sync.WaitGroup
	statuses := make(chan int, 24)
	for i := 0; i < 24; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			response := httptest.NewRecorder()
			key := strings.Repeat("0", 63) + string("0123456789abcdef"[i%16])
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/branding/site-logo/"+key, nil))
			statuses <- response.Code
		}(i)
	}
	workers.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, http.StatusNotFound, status)
	}
	require.EqualValues(t, 1, provider.calls.Load(), "cold concurrent and distinct misses must share one settings load")
	server.InvalidateCache()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/branding/site-logo/"+strings.Repeat("0", 64), nil))
	require.Equal(t, http.StatusNotFound, response.Code)
	require.EqualValues(t, 2, provider.calls.Load(), "a settings change permits one fresh prime")
}

func TestFrontendServer_BrandingAsset(t *testing.T) {
	var imageBytes bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 3), B: 80, A: 255})
		}
	}
	require.NoError(t, png.Encode(&imageBytes, img))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes.Bytes())
	settings := &mockSettingsProvider{settings: map[string]any{"site_logo": dataURL, "site_name": "Brand test", "other": "preserved"}}
	server, err := NewFrontendServer(settings)
	require.NoError(t, err)
	router := gin.New()
	router.Use(server.Middleware())
	htmlResponse := httptest.NewRecorder()
	router.ServeHTTP(htmlResponse, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	require.Equal(t, http.StatusOK, htmlResponse.Code)
	body := htmlResponse.Body.String()
	require.NotContains(t, body, dataURL, "a large logo must not be repeated inline in every HTML response")
	configPattern := regexp.MustCompile(`window.__APP_CONFIG__=(.*?);</script>`)
	configMatch := configPattern.FindStringSubmatch(body)
	require.Len(t, configMatch, 2)
	var config map[string]any
	require.NoError(t, json.Unmarshal([]byte(configMatch[1]), &config))
	logoURL, ok := config["site_logo"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(logoURL, "/branding/site-logo/"), logoURL)
	require.Contains(t, body, `href="`+logoURL+`"`)
	require.Equal(t, "preserved", config["other"])
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, logoURL, nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, imageBytes.Bytes(), response.Body.Bytes())
	require.Equal(t, "image/png", response.Header().Get("Content-Type"))
	require.Contains(t, response.Header().Get("Cache-Control"), "immutable")
	require.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	req := httptest.NewRequest(http.MethodGet, logoURL, nil)
	req.Header.Set("If-None-Match", response.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	router.ServeHTTP(cached, req)
	require.Equal(t, http.StatusNotModified, cached.Code)
	require.Empty(t, cached.Body.Bytes())
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, logoURL, nil))
	require.Equal(t, http.StatusOK, head.Code)
	require.Empty(t, head.Body.Bytes())
	// A bookmark can request the immutable URL before the first HTML after restart.
	restarted, err := NewFrontendServer(settings)
	require.NoError(t, err)
	secondRouter := gin.New()
	secondRouter.Use(restarted.Middleware())
	first := httptest.NewRecorder()
	secondRouter.ServeHTTP(first, httptest.NewRequest(http.MethodGet, logoURL, nil))
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, imageBytes.Bytes(), first.Body.Bytes())
	unknown := httptest.NewRecorder()
	router.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/branding/site-logo/"+strings.Repeat("0", 64), nil))
	require.Equal(t, http.StatusNotFound, unknown.Code)
}

func TestFrontendServer_BrandingExternalURLPreserved(t *testing.T) {
	server, err := NewFrontendServer(&mockSettingsProvider{settings: map[string]string{"site_logo": "https://example.com/logo.png"}})
	require.NoError(t, err)
	rendered := server.injectSettings([]byte(`{"site_logo":"https://example.com/logo.png"}`))
	require.Contains(t, string(rendered), `https://example.com/logo.png`)
}
