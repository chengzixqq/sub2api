package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type metaCatalogEnvelope struct {
	Code int `json:"code"`
	Data struct {
		PlatformCatalog      json.RawMessage     `json:"platform_catalog"`
		AccountTypes         []string            `json:"account_types"`
		PlatformAuth         map[string][]string `json:"platform_auth"`
		AccountStatusFilters []string            `json:"account_status_filters"`
		RequestTypes         []string            `json:"request_types"`
		ErrorPhases          []string            `json:"error_phases"`
		UserRoles            []string            `json:"user_roles"`
		APIProtocols         []string            `json:"api_protocols"`
	} `json:"data"`
}

// newMetaCatalogRouter mounts the handler behind a middleware that installs
// scope; a nil scope leaves the context without one.
func newMetaCatalogRouter(scope *service.Scope) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/admin/meta/catalog", func(c *gin.Context) {
		if scope != nil {
			c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), *scope))
		}
		c.Next()
	}, NewMetaHandler().Catalog)
	return r
}

func getMetaCatalog(t *testing.T, r http.Handler, ifNoneMatch string) (*httptest.ResponseRecorder, metaCatalogEnvelope) {
	t.Helper()
	rec := conditionalGet(r, "/api/v1/admin/meta/catalog", ifNoneMatch)
	var body metaCatalogEnvelope
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	}
	return rec, body
}

func TestMetaCatalogReturnsCatalogWithETag(t *testing.T) {
	admin := service.AdminScope()
	rec, body := getMetaCatalog(t, newMetaCatalogRouter(&admin), "")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, body.Code)
	want, err := json.Marshal(service.BuildPlatformCatalog())
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(body.Data.PlatformCatalog))
	require.Equal(t, service.BuildAdminMetaCatalog(admin).AccountTypes, body.Data.AccountTypes)
	require.Contains(t, body.Data.PlatformAuth, service.PlatformGrok)
	require.Contains(t, body.Data.AccountStatusFilters, service.AccountListStatusSchedulableNow)
	require.Equal(t, []string{"sync", "stream", "ws_v2", "cyber", "live"}, body.Data.RequestTypes)
	require.NotEmpty(t, body.Data.ErrorPhases)
	require.NotEmpty(t, body.Data.APIProtocols)

	etag := requireConditionalHeaders(t, rec)
	require.Regexp(t, `^"[0-9a-f]{64}"$`, etag)
}

func TestMetaCatalogNotModified(t *testing.T) {
	admin := service.AdminScope()
	r := newMetaCatalogRouter(&admin)
	first, _ := getMetaCatalog(t, r, "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")

	same, _ := getMetaCatalog(t, r, etag)
	require.Equal(t, http.StatusNotModified, same.Code)
	require.Empty(t, same.Body.String())
	require.Equal(t, etag, same.Header().Get("ETag"))

	weak, _ := getMetaCatalog(t, r, "W/"+etag)
	require.Equal(t, http.StatusNotModified, weak.Code)

	stale, _ := getMetaCatalog(t, r, `"`+strings.Repeat("0", 64)+`"`)
	require.Equal(t, http.StatusOK, stale.Code)
	require.NotEmpty(t, stale.Body.String())
}

func TestMetaCatalogETagDiffersByScope(t *testing.T) {
	admin := service.AdminScope()
	adminRec, _ := getMetaCatalog(t, newMetaCatalogRouter(&admin), "")
	require.Equal(t, http.StatusOK, adminRec.Code)

	vendor := service.VendorScope(7, service.WorkspacePermissions{AccountManage: true})
	vendorRec, vendorBody := getMetaCatalog(t, newMetaCatalogRouter(&vendor), "")
	require.Equal(t, http.StatusOK, vendorRec.Code)
	require.NotEqual(t, adminRec.Header().Get("ETag"), vendorRec.Header().Get("ETag"))
	require.NotContains(t, vendorBody.Data.PlatformAuth, service.PlatformGrok)
	require.Contains(t, vendorBody.Data.PlatformAuth, service.PlatformAnthropic)

	// The admin validator never revalidates a vendor's copy.
	cross, _ := getMetaCatalog(t, newMetaCatalogRouter(&vendor), adminRec.Header().Get("ETag"))
	require.Equal(t, http.StatusOK, cross.Code)

	missingRec, missingBody := getMetaCatalog(t, newMetaCatalogRouter(nil), "")
	require.Equal(t, http.StatusOK, missingRec.Code)
	require.NotNil(t, missingBody.Data.PlatformAuth)
	require.Empty(t, missingBody.Data.PlatformAuth)
	require.Contains(t, missingRec.Body.String(), `"platform_auth":{}`)
}

func TestMetaCatalogMatchesFrontendBuiltin(t *testing.T) {
	builtin, err := os.ReadFile("../../../../frontend/src/constants/platformCatalog.builtin.json")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("frontend sources not available")
	}
	require.NoError(t, err)

	admin := service.AdminScope()
	rec, body := getMetaCatalog(t, newMetaCatalogRouter(&admin), "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, string(builtin), string(body.Data.PlatformCatalog))
}

// TestMetaCatalogEnumsMatchBindingTags keeps the catalog in step with the
// request validation it describes.
func TestMetaCatalogEnumsMatchBindingTags(t *testing.T) {
	admin := service.AdminScope()
	rec, body := getMetaCatalog(t, newMetaCatalogRouter(&admin), "")
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, bindingOneOf(t, reflect.TypeOf(CreateAccountRequest{}), "Type"), body.Data.AccountTypes)
	require.Equal(t, bindingOneOf(t, reflect.TypeOf(CreateUserRequest{}), "Role"), body.Data.UserRoles)
}

func bindingOneOf(t *testing.T, typ reflect.Type, field string) []string {
	t.Helper()
	f, ok := typ.FieldByName(field)
	require.True(t, ok, "%s.%s", typ.Name(), field)
	for _, rule := range strings.Split(f.Tag.Get("binding"), ",") {
		if values, found := strings.CutPrefix(rule, "oneof="); found {
			return strings.Fields(values)
		}
	}
	t.Fatalf("%s.%s has no oneof binding", typ.Name(), field)
	return nil
}
