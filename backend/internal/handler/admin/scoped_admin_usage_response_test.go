//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type scopedUsageResponseRepo struct {
	service.UsageLogRepository
	records []service.UsageLog
}

func (r *scopedUsageResponseRepo) ListWithFilters(context.Context, pagination.PaginationParams, usagestats.UsageLogFilters) ([]service.UsageLog, *pagination.PaginationResult, error) {
	return r.records, &pagination.PaginationResult{Total: int64(len(r.records))}, nil
}
func TestScopedAdminUsageListDoesNotGrantNestedResourceRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	privateUser := &service.User{ID: 7, Email: "private-email@example.invalid", Balance: 97531, Notes: "private-user-notes"}
	privateKey := &service.APIKey{ID: 8, Key: "private-api-key-secret", Name: "private-key-name", User: privateUser}
	privateGroup := &service.Group{ID: 9, Name: "private-group-name", Description: "private-group-description", RateMultiplier: 7}
	privateSub := &service.UserSubscription{ID: 10, User: privateUser, Group: privateGroup, DailyUsageUSD: 100}
	privateAccount := &service.Account{ID: 11, Name: "private-account-name", Credentials: map[string]any{"access_token": "private-account-token"}}
	for _, scoped := range []bool{true, false} {
		t.Run(map[bool]string{true: "受限用量读取", false: "完整管理员保留响应"}[scoped], func(t *testing.T) {
			repo := &scopedUsageResponseRepo{records: []service.UsageLog{{ID: 12, UserID: 7, APIKeyID: 8, AccountID: 11, RequestID: "request-public", RequestedModel: "requested-model", Model: "effective-model", InputTokens: 123, OutputTokens: 456, TotalCost: 1.25, ActualCost: 1, User: privateUser, APIKey: privateKey, Group: privateGroup, Subscription: privateSub, Account: privateAccount}}}
			h := NewUsageHandler(service.NewUsageService(repo, nil, nil, nil), nil, nil, nil)
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.AdminScope()))
				if scoped {
					c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{Policy: adminaccess.Policy{GlobalPermissions: []string{"usage.read"}}}))
				}
			})
			r.GET("/api/v1/admin/usage", h.List)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/admin/usage", nil))
			require.Equal(t, 200, w.Code, w.Body.String())
			var result struct {
				Data struct {
					Items []map[string]json.RawMessage `json:"items"`
					Total int64                        `json:"total"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
			require.Len(t, result.Data.Items, 1)
			require.EqualValues(t, 1, result.Data.Total)
			item := result.Data.Items[0]
			require.JSONEq(t, `123`, string(item["input_tokens"]))
			require.JSONEq(t, `456`, string(item["output_tokens"]))
			require.JSONEq(t, `1.25`, string(item["total_cost"]))
			require.JSONEq(t, `"requested-model"`, string(item["model"]))
			require.JSONEq(t, `8`, string(item["api_key_id"]))
			if scoped {
				require.NotContains(t, w.Body.String(), "private-")
				require.NotContains(t, w.Body.String(), "97531")
				for _, field := range []string{"user", "api_key", "group", "subscription", "account", "balance", "credentials"} {
					require.NotContains(t, item, field)
				}
			} else {
				require.Contains(t, w.Body.String(), "private-api-key-secret")
				require.Contains(t, w.Body.String(), `"key_masked"`)
				require.Contains(t, w.Body.String(), "private-email@example.invalid")
				require.Contains(t, item, "user")
				require.Contains(t, item, "api_key")
				require.Contains(t, item, "account")
			}
			require.Equal(t, "private-api-key-secret", repo.records[0].APIKey.Key, "输出投影不得修改服务返回对象")
		})
	}
}
func TestScopedAdminUsageListEmptyRemainsArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewUsageHandler(service.NewUsageService(&scopedUsageResponseRepo{}, nil, nil, nil), nil, nil, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{}))
	})
	r.GET("/usage", h.List)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/usage", nil))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"items":[]`)
}

func TestScopedAdminUsageBillingDetailsDoNotExposeAccountPricing(t *testing.T) {
	amount := 1.25
	record := &service.UsageLog{Model: "internal-executor", RequestedModel: "public-executor", BillingDetails: &service.BillingDetails{
		Version: 1, Status: "partial_unpriced", Components: []service.BillingComponent{
			{Role: "executor", Model: "internal-executor", TotalCost: &amount, AccountStatsCost: &amount, PricingSource: "private-pricing"},
			{Role: "advisor", Model: "advisor-model", UnpricedReason: "private-reason", PricingSource: "private-pricing"},
		},
	}}
	body, err := json.Marshal(scopedAdminUsageLogResponse(record)["billing_details"])
	require.NoError(t, err)
	require.NotContains(t, string(body), "account_stats_cost")
	require.NotContains(t, string(body), "pricing_source")
	require.NotContains(t, string(body), "unpriced_reason")
	require.NotContains(t, string(body), "internal-executor")
	require.Contains(t, string(body), "public-executor")
	require.Contains(t, string(body), "advisor-model")
	require.Contains(t, string(body), `"total_cost":null`)
	require.Equal(t, "private-pricing", record.BillingDetails.Components[0].PricingSource)
}
