//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	publichandler "github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Run explicitly against the isolated integration database. First/repeat means
// PostgreSQL query timings, not a claim that the operating system cache is cold.
func TestUsageQuery_Profile(t *testing.T) {
	if os.Getenv("SUB2API_USAGE_PROFILE") != "1" {
		t.Skip("set SUB2API_USAGE_PROFILE=1 for the 100000-row query profile")
	}
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	ctx := context.Background()
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@profile.test"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "profile"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	end := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_logs
		(user_id,api_key_id,account_id,request_id,model,input_tokens,output_tokens,
		cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,provider_cost_recorded,created_at)
		SELECT $1,$2,$3,'profile-'||$4||'-'||i,'profile-model',100,50,20,30,0.01,0.005,TRUE,
		$5::timestamptz - (i * INTERVAL '77.76 seconds') FROM generate_series(1,100000) i`,
		user.ID, key.ID, account.ID, uuid.NewString(), end)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "ANALYZE usage_logs")
	require.NoError(t, err)
	usageService := service.NewUsageService(repo, nil, client, nil)
	admin := adminhandler.NewUsageHandler(usageService, nil, nil, nil)
	userHandler := publichandler.NewUsageHandler(usageService, nil, nil, nil)
	invoke := func(handler gin.HandlerFunc, query, role string) (float64, map[string]any) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("GET", "/?"+query, nil).WithContext(service.WithScope(ctx, service.AdminScope()))
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID})
		c.Set(string(middleware.ContextKeyUserRole), role)
		began := time.Now()
		handler(c)
		elapsed := float64(time.Since(began).Microseconds()) / 1000
		require.Equal(t, 200, recorder.Code, recorder.Body.String())
		var body struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		return elapsed, body.Data
	}
	for _, days := range []int{1, 7, 30, 90} {
		start := end.AddDate(0, 0, -days)
		filters := usagestats.UsageLogFilters{UserID: user.ID, StartTime: &start, EndTime: &end}
		// The same probe runs on the pre-change worktree, where this field is absent.
		deferred := reflect.ValueOf(&filters).Elem().FieldByName("DeferredTotal")
		if deferred.IsValid() {
			deferred.SetBool(true)
		}
		result := map[string]any{"days": days, "fixture_rows": 100000, "deferred": deferred.IsValid()}
		for _, temperature := range []string{"first", "repeat"} {
			began := time.Now()
			logs, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, filters)
			require.NoError(t, err)
			require.Len(t, logs, 50)
			result["page_"+temperature+"_ms"] = float64(time.Since(began).Microseconds()) / 1000
			began = time.Now()
			stats, err := repo.GetStatsWithFilters(ctx, filters)
			require.NoError(t, err)
			result["stats_"+temperature+"_ms"] = float64(time.Since(began).Microseconds()) / 1000
			result["matching_rows"] = stats.TotalRequests
		}
		query := url.Values{"start_date": {start.Format("2006-01-02")}, "end_date": {end.Add(-time.Second).Format("2006-01-02")},
			"timezone": {"UTC"}, "page": {"1"}, "page_size": {"50"}, "count_mode": {"deferred"}, "user_id": {fmt.Sprint(user.ID)}}.Encode()
		for _, role := range []struct {
			name        string
			list, stats gin.HandlerFunc
		}{{"admin", admin.List, admin.Stats}, {"user", userHandler.List, userHandler.Stats}} {
			for _, temperature := range []string{"cold", "hot"} {
				elapsed, body := invoke(role.list, query, role.name)
				require.Len(t, body["items"], 50)
				result[role.name+"_http_page_"+temperature+"_ms"] = elapsed
				elapsed, body = invoke(role.stats, query, role.name)
				require.EqualValues(t, result["matching_rows"], body["total_requests"])
				result[role.name+"_http_stats_"+temperature+"_ms"] = elapsed
			}
		}
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		t.Log(string(raw))
	}
}
