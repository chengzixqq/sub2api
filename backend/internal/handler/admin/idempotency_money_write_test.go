package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ctxCheckingIdempotencyRepo fails writes made with a cancelled context, like a real database driver does.
type ctxCheckingIdempotencyRepo struct {
	*memoryIdempotencyRepoStub
}

func (r *ctxCheckingIdempotencyRepo) MarkSucceeded(ctx context.Context, id int64, status int, body string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.memoryIdempotencyRepoStub.MarkSucceeded(ctx, id, status, body, expiresAt)
}

// A money write that is accepted must finish and store its result even if the phone drops the connection mid-run;
// the retry with the same key then gets the stored result instead of IDEMPOTENCY_IN_PROGRESS.
func TestAdminMoneyWrite_ClientDisconnectStillCommitsAndReplays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &ctxCheckingIdempotencyRepo{memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub()}
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	var executed atomic.Int32
	var cancelRequest context.CancelFunc
	router := gin.New()
	router.POST("/balance", func(c *gin.Context) {
		executeAdminMoneyWriteJSON(c, "admin.users.balance.update", map[string]any{"balance": 5}, func(ctx context.Context) (any, error) {
			executed.Add(1)
			if cancelRequest != nil {
				cancelRequest() // the client goes away while the adjustment is running
			}
			require.NoError(t, ctx.Err(), "an accepted money write must not be interrupted by the client")
			return gin.H{"balance": 15}, nil
		})
	})

	send := func(ctx context.Context) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/balance", bytes.NewBufferString(`{"balance":5}`)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "phone-dropped")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	var ctx context.Context
	ctx, cancelRequest = context.WithCancel(context.Background())
	first := send(ctx)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	cancelRequest = nil
	retry := send(context.Background())
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	require.Equal(t, "true", retry.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, int32(1), executed.Load())
}

// generate keeps working for the Web, which sends no Idempotency-Key, even once observe-only mode is switched off.
func TestAdminMoneyWriteOptional_RunsWithoutKeyWhenKeysAreEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	var executed atomic.Int32
	router := gin.New()
	router.POST("/generate", func(c *gin.Context) {
		executeAdminOptionalMoneyWriteJSON(c, "admin.redeem_codes.generate", map[string]any{"count": 1}, func(context.Context) (any, error) {
			executed.Add(1)
			return gin.H{"codes": 1}, nil
		})
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/generate", bytes.NewBufferString(`{}`)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int32(1), executed.Load())
}
