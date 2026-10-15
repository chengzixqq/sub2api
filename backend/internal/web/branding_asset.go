//go:build embed

package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const brandingAssetPrefix = "/branding/site-logo/"
const maxBrandingImageBytes = 2 << 20

type brandingAsset struct {
	body        []byte
	contentType string
}

// Replace inline raster branding with the same bytes at an immutable URL. The
// persisted setting stays intact, including SVG/data URLs we do not externalize.
func (s *FrontendServer) externalizeBranding(settingsJSON []byte) []byte {
	var settings map[string]json.RawMessage
	if json.Unmarshal(settingsJSON, &settings) != nil {
		return settingsJSON
	}
	var value string
	if json.Unmarshal(settings["site_logo"], &value) != nil {
		return settingsJSON
	}
	value = strings.TrimSpace(value)
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(strings.ToLower(header), "data:image/") || !strings.HasSuffix(strings.ToLower(header), ";base64") || len(encoded) > base64.StdEncoding.EncodedLen(maxBrandingImageBytes) {
		return settingsJSON
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(body) == 0 || len(body) > maxBrandingImageBytes {
		return settingsJSON
	}
	mediaType := http.DetectContentType(body)
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/x-icon":
	default:
		return settingsJSON
	}
	sum := sha256.Sum256(body)
	key := hex.EncodeToString(sum[:])
	s.brandingMu.Lock()
	if s.brandingAssets == nil {
		s.brandingAssets = make(map[string]*brandingAsset)
	}
	if _, exists := s.brandingAssets[key]; !exists {
		s.brandingAssets[key] = &brandingAsset{body: body, contentType: mediaType}
		s.brandingOrder = append(s.brandingOrder, key)
		if len(s.brandingOrder) > 4 {
			delete(s.brandingAssets, s.brandingOrder[0])
			s.brandingOrder = s.brandingOrder[1:]
		}
	}
	s.brandingMu.Unlock()
	logoJSON, err := json.Marshal(brandingAssetPrefix + key)
	if err != nil {
		return settingsJSON
	}
	settings["site_logo"] = logoJSON
	optimized, err := json.Marshal(settings)
	if err != nil {
		return settingsJSON
	}
	return optimized
}

func (s *FrontendServer) serveBrandingAsset(c *gin.Context) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Header("Allow", "GET, HEAD")
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(c.Request.URL.Path, brandingAssetPrefix)
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != sha256.Size || key != strings.ToLower(key) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	s.brandingMu.RLock()
	asset := s.brandingAssets[key]
	s.brandingMu.RUnlock()
	if asset == nil {
		s.primeBranding(c.Request.Context())
		s.brandingMu.RLock()
		asset = s.brandingAssets[key]
		s.brandingMu.RUnlock()
	}
	if asset == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	etag := `"` + key + `"`
	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	if c.GetHeader("If-None-Match") == etag {
		c.AbortWithStatus(http.StatusNotModified)
		return
	}
	c.Header("Content-Type", asset.contentType)
	c.Header("Content-Length", strconv.Itoa(len(asset.body)))
	if c.Request.Method == http.MethodHead {
		c.AbortWithStatus(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, asset.contentType, asset.body)
	c.Abort()
}

// A cached immutable URL may arrive before HTML after a restart. Prime once per
// settings generation, including negative results, rather than querying settings
// for every unknown hash. Concurrent misses share the same bounded load.
func (s *FrontendServer) primeBranding(requestContext context.Context) {
	s.brandingLoadMu.Lock()
	defer s.brandingLoadMu.Unlock()
	if s.brandingPrimed || s.settings == nil || time.Now().Before(s.brandingRetryAfter) {
		return
	}
	if s.cache.Get() != nil {
		s.brandingPrimed = true
		return
	}
	ctx, cancel := context.WithTimeout(requestContext, 2*time.Second)
	defer cancel()
	settings, err := s.settings.GetPublicSettingsForInjection(ctx)
	if err != nil {
		// Keep transient provider failures retryable without per-request DB work.
		s.brandingRetryAfter = time.Now().Add(2 * time.Second)
		return
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		s.brandingRetryAfter = time.Now().Add(2 * time.Second)
		return
	}
	s.externalizeBranding(raw)
	s.brandingPrimed = true
}
