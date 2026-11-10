package admin

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminResponseProjectionPreservesOptionalKeyAndCommittedMoneyWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"optional_without_key", "money_committed", "required_replay"} {
		t.Run(mode, func(t *testing.T) {
			repo := newMemoryIdempotencyRepoStub()
			var coordinator *service.IdempotencyCoordinator
			if mode == "money_committed" {
				coordinator = service.NewIdempotencyCoordinator(&markSucceededUnavailableRepoStub{memoryIdempotencyRepoStub: repo}, service.DefaultIdempotencyConfig())
			} else {
				coordinator = service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig())
			}
			service.SetDefaultIdempotencyCoordinator(coordinator)
			t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
			executed := 0
			router := gin.New()
			router.POST("/projection", func(c *gin.Context) {
				execute := func(context.Context) (any, error) {
					executed++
					return gin.H{"id": 7, "balance": "secret-balance"}, nil
				}
				present := func(any) any { return gin.H{"id": 7} }
				switch mode {
				case "optional_without_key":
					executeAdminOptionalIdempotentJSON(c, "merge.projection", nil, time.Minute, execute, present)
				case "money_committed":
					executeAdminMoneyWriteJSON(c, "merge.projection", nil, execute, present)
				default:
					executeAdminIdempotentJSONWithResponse(c, "merge.projection", nil, time.Minute, execute, present)
				}
			})
			count := 1
			if mode == "required_replay" {
				count = 2
			}
			for i := 0; i < count; i++ {
				req := httptest.NewRequest(http.MethodPost, "/projection", nil)
				if mode != "optional_without_key" {
					req.Header.Set("Idempotency-Key", "projection-merge-key")
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, 200, w.Code, w.Body.String())
				require.NotContains(t, w.Body.String(), "secret-balance")
				require.Contains(t, w.Body.String(), `"id":7`)
				if mode == "money_committed" {
					require.Equal(t, "result-committed", w.Header().Get("X-Idempotency-Degraded"))
				}
				if i == 1 {
					require.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"))
				}
			}
			require.Equal(t, 1, executed)
		})
	}
}
