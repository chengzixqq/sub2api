//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOfficialCatalog_AnthropicVariantListAndRetrieveShareVisibility(t *testing.T) {
	group := &service.Group{ID: 20, Platform: service.PlatformAnthropic,
		ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"public[1m]"}}}
	shared := service.Account{ID: 2, Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: true,
		GroupIDs: []int64{group.ID}, Credentials: map[string]any{"model_mapping": map[string]any{"public": "short"}}}
	account := service.Account{ID: 1, Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{group.ID},
		DispatchScope: service.DispatchScopeAuthorizedOnly, Credentials: map[string]any{
			"model_mapping": map[string]any{"public": "target"}}}
	account.SetUpstreamModelMetadataSnapshot(service.UpstreamModelMetadataSnapshot{
		Models: map[string]service.UpstreamModelMetadata{"target": {ContextWindow: 900_000}}})
	repo := &gatewayModelsDispatchFailureRepo{
		gatewayModelsAccountRepoStub: &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{group.ID: {shared, account}}},
		userPolicies: map[int64]*service.AccountDispatchPolicy{101: {Mode: service.AccountDispatchModeAppend,
			Accounts: []service.AccountDispatchGrant{{AccountID: 1, Origin: service.AccountDispatchOriginMember}}}},
	}
	h := newGatewayModelsHandlerForTest(repo)
	h.settingService = service.NewSettingService(&catalogSettingsRepo{policy: `{"models_catalog":"official","request_id_mode":"fill"}`}, nil)
	for _, userID := range []int64{101, 100} {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{UserID: userID, Group: group, GroupID: &group.ID})
		})
		router.GET("/v1/models", h.Models)
		router.GET("/v1/models/:model", h.Models)
		page := catalogGET(router, "/v1/models", "x-api-key", "key")
		detail := catalogGET(router, "/v1/models/public%5B1m%5D", "x-api-key", "key")
		require.Equal(t, http.StatusOK, page.Code)
		require.Regexp(t, catalogIDPattern, page.Header().Get("Request-Id"))
		if userID == 100 {
			require.JSONEq(t, emptyCatalogPage, page.Body.String())
			require.Equal(t, http.StatusNotFound, detail.Code)
			continue
		}

		require.Equal(t, http.StatusOK, detail.Code)
		require.Regexp(t, catalogIDPattern, detail.Header().Get("Request-Id"))
		var body struct {
			Data    []json.RawMessage `json:"data"`
			FirstID string            `json:"first_id"`
			LastID  string            `json:"last_id"`
		}
		require.NoError(t, json.Unmarshal(page.Body.Bytes(), &body))
		require.Equal(t, "public[1m]", body.FirstID)
		require.Equal(t, body.FirstID, body.LastID)
		require.Len(t, body.Data, 1)
		require.JSONEq(t, string(body.Data[0]), detail.Body.String())
		require.Contains(t, detail.Body.String(), `"lifecycle":"active"`)
	}
}
