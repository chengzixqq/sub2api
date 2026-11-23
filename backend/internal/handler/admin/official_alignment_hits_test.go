//go:build unit

package admin

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type adminHitStore struct{ readErr error }

func (*adminHitStore) IncrBy(context.Context, map[string]int64, time.Duration) error { return nil }
func (s *adminHitStore) Read(_ context.Context, keys []string) ([]int64, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return make([]int64, len(keys)), nil
}

func TestCustomizationHitsGlobalAndAccount(t *testing.T) {
	store := &adminHitStore{}
	hits := service.NewAlignmentHitRecorder(store)
	repo := &claudeCustomizationHandlerRepo{values: map[string]string{}}
	settings := service.ProvideSettingService(nil, repo, nil, nil, &config.Config{}, hits)
	global := NewSettingHandler(settings, nil, nil, nil, nil, nil, nil)
	account := &AccountHandler{adminService: &claudeOverrideAdminService{account: &service.Account{ID: 7}}, settingService: settings}
	for _, id := range []int64{7, 8} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		hits.Record(c, service.ClaudeFieldUnknownBetaHeader, id, "passthrough")
	}
	for _, fail := range []bool{false, true} {
		if fail {
			store.readErr = errors.New("Redis unavailable")
		}
		for _, tc := range []struct {
			fn   func(*gin.Context)
			want int64
		}{{global.GetClaudeCustomization, 2}, {account.GetClaudeCustomization, 1}} {
			rec := claudeCustomizationRequest(t, "GET", nil, gin.Params{{Key: "id", Value: "7"}}, tc.fn)
			require.Equal(t, 200, rec.Code)
			result := gjson.Get(rec.Body.String(), "data.hits")
			if fail {
				require.Equal(t, "null", result.Raw)
				continue
			}
			require.Len(t, result.Map(), 4)
			require.Equal(t, tc.want, result.Get("unknown_beta_header.last_24h").Int())
			require.Equal(t, tc.want, result.Get("unknown_beta_header.last_7d").Int())
		}
	}
}
