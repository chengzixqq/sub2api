package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type patchAccountRecorder struct {
	*stubAdminService
	lastPatch *service.PatchAccountInput
	patchErr  error
	account   service.Account
}

func (s *patchAccountRecorder) PatchAccount(_ context.Context, id int64, input *service.PatchAccountInput) (*service.Account, error) {
	s.lastPatch = input
	if s.patchErr != nil {
		return nil, s.patchErr
	}
	account := s.account
	account.ID = id
	return &account, nil
}

func (s *patchAccountRecorder) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	account := s.account
	account.ID = id
	return &account, nil
}

func newPatchAccountRouter(svc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.PATCH("/api/v1/admin/accounts/:id", h.Patch)
	r.GET("/api/v1/admin/accounts/:id", h.GetByID)
	return r
}

func TestAccountHandlerPatch_PassesPatchesAndIfMatch(t *testing.T) {
	svc := &patchAccountRecorder{stubAdminService: newStubAdminService(), account: service.Account{Name: "a", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, ConfigVersion: 8}}
	router := newPatchAccountRouter(svc)

	body := `{"priority":10,"credentials_patch":{"base_url":"https://x","custom_error_codes":null},"extra_patch":{"pool_mode":false}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/accounts/5", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"cv-7"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `"cv-8"`, rec.Header().Get("ETag"))
	require.NotNil(t, svc.lastPatch)
	require.NotNil(t, svc.lastPatch.ExpectedConfigVersion)
	require.Equal(t, int64(7), *svc.lastPatch.ExpectedConfigVersion)
	require.Equal(t, 10, *svc.lastPatch.Fields.Priority)
	require.Equal(t, "https://x", svc.lastPatch.CredentialsPatch["base_url"])
	require.Contains(t, svc.lastPatch.CredentialsPatch, "custom_error_codes")
	require.Nil(t, svc.lastPatch.CredentialsPatch["custom_error_codes"])
	require.Equal(t, false, svc.lastPatch.ExtraPatch["pool_mode"])
	require.Nil(t, svc.lastPatch.Fields.Credentials)
	require.Nil(t, svc.lastPatch.Fields.Extra)
}

func TestAccountHandlerPatch_BodyVersionAndConflicts(t *testing.T) {
	svc := &patchAccountRecorder{stubAdminService: newStubAdminService()}
	router := newPatchAccountRouter(svc)

	send := func(body, ifMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/accounts/5", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	rec := send(`{"expected_config_version":3}`, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(3), *svc.lastPatch.ExpectedConfigVersion)

	rec = send(`{"expected_config_version":3}`, `"cv-4"`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = send(`{}`, `"garbage"`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Full credentials / extra objects belong to PUT.
	rec = send(`{"credentials":{"api_key":"x"}}`, "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	svc.patchErr = service.ErrAccountVersionConflict(9)
	rec = send(`{"name":"n"}`, `"cv-1"`)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code)
	var resp struct {
		Reason   string            `json:"reason"`
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "ACCOUNT_VERSION_CONFLICT", resp.Reason)
	require.Equal(t, "9", resp.Metadata["current_config_version"])
}

func TestAccountHandlerGetByID_SetsETag(t *testing.T) {
	svc := &patchAccountRecorder{stubAdminService: newStubAdminService(), account: service.Account{Name: "a", ConfigVersion: 12}}
	router := newPatchAccountRouter(svc)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/5", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, `"cv-12"`, rec.Header().Get("ETag"))
}
