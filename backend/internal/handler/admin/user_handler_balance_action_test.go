package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type balanceActionCapturingAdmin struct {
	*stubAdminService
	actionIDs []uuid.UUID
}

func (s *balanceActionCapturingAdmin) UpdateUserBalance(ctx context.Context, userID int64, balance, operation, notes string) (*service.User, error) {
	s.actionIDs = append(s.actionIDs, service.AdminAdjustmentMetadataFromContext(ctx).ActionID)
	return &service.User{ID: userID, Balance: 15}, nil
}

func postBalance(t *testing.T, admin service.AdminService, key string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 5})
		c.Next()
	})
	r.POST("/api/v1/admin/users/:id/balance", NewUserHandler(admin, nil, nil, nil, nil, nil, nil).UpdateBalance)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/42/balance", bytes.NewBufferString(`{"balance":"5","operation":"add","notes":"t"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// The balance response tells the client which ledger row to look for, and until when the key replays.
func TestUserHandler_UpdateBalance_ReturnsActionIDAndExpiry(t *testing.T) {
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	admin := &balanceActionCapturingAdmin{stubAdminService: newStubAdminService()}

	first := postBalance(t, admin, "topup-1")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	actionID := first.Header().Get("X-Admin-Adjustment-Action-ID")
	require.NotEmpty(t, actionID)
	require.Equal(t, admin.actionIDs[0].String(), actionID, "the header is the ledger's action id")
	expires, err := time.Parse(time.RFC3339, first.Header().Get("X-Idempotency-Expires-At"))
	require.NoError(t, err)
	require.True(t, expires.After(time.Now()))

	replay := postBalance(t, admin, "topup-1")
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, actionID, replay.Header().Get("X-Admin-Adjustment-Action-ID"), "the replay names the same action")
	require.Len(t, admin.actionIDs, 1)
}
