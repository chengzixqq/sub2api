//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// E10: the initial balance of a new user lands in the adjustment ledger, PUT /admin/users/:id no longer accepts a
// balance (Q13), and balance-history shows the before/after of admin adjustments plus where the top-ups came from.

type balanceLedgerCapturingAdmin struct {
	*stubAdminService
	createMetadata []service.AdminAdjustmentMetadata
	history        []service.RedeemCode
	breakdown      *service.UserRechargeBreakdown
}

func (s *balanceLedgerCapturingAdmin) CreateUser(ctx context.Context, input *service.CreateUserInput) (*service.User, error) {
	s.createMetadata = append(s.createMetadata, service.AdminAdjustmentMetadataFromContext(ctx))
	return s.stubAdminService.CreateUser(ctx, input)
}

func (s *balanceLedgerCapturingAdmin) GetUserBalanceHistory(ctx context.Context, userID int64, page, pageSize int, codeType string) ([]service.RedeemCode, int64, float64, error) {
	return s.history, int64(len(s.history)), 60, nil
}

func (s *balanceLedgerCapturingAdmin) GetUserRechargeBreakdown(ctx context.Context, userID int64) (*service.UserRechargeBreakdown, error) {
	return s.breakdown, nil
}

func newBalanceLedgerRouter(admin service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewUserHandler(admin, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/users", h.Create)
	router.PUT("/api/v1/admin/users/:id", h.Update)
	router.GET("/api/v1/admin/users/:id/balance-history", h.GetBalanceHistory)
	return router
}

func TestUserHandler_Update_RejectsBalanceField(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	installMemoryIdempotency(t)

	for _, body := range []string{`{"balance":5}`, `{"balance":0,"notes":"x"}`} {
		rec := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/1", body, "")
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, "USER_BALANCE_NOT_EDITABLE", responseReason(t, rec))
	}
	// The rejection happens before the idempotency wrapper: the key is not burnt by the refused request.
	rejected := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/1", `{"balance":5}`, "put-balance-key")
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	require.Empty(t, rejected.Header().Get("X-Idempotency-Expires-At"))
	require.Equal(t, 0, adminSvc.updateUserCalls, "a request carrying balance must not reach UpdateUser")

	for _, body := range []string{`{"balance":null,"notes":"x"}`, `{"email":"a@b.cc"}`} {
		rec := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/1", body, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	keyed := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/1", `{"notes":"x"}`, "put-balance-key")
	require.Equal(t, http.StatusOK, keyed.Code, keyed.Body.String())
	require.Equal(t, 3, adminSvc.updateUserCalls)
}

func TestUserHandler_Create_KeyedRequestNamesTheLedgerAction(t *testing.T) {
	admin := &balanceLedgerCapturingAdmin{stubAdminService: newStubAdminService()}
	router := newBalanceLedgerRouter(admin)
	installMemoryIdempotency(t)
	body := `{"email":"e10@example.com","password":"secret-1","balance":50}`

	first, replay := assertKeyedReplay(t, router, http.MethodPost, "/api/v1/admin/users", body, "user-create-ledger")

	require.Len(t, admin.createMetadata, 1, "the keyed retry must not create a second user")
	actionID := first.Header().Get("X-Admin-Adjustment-Action-ID")
	require.NotEmpty(t, actionID)
	require.Equal(t, actionID, replay.Header().Get("X-Admin-Adjustment-Action-ID"))
	require.Equal(t, actionID, admin.createMetadata[0].ActionID.String())
	require.True(t, admin.createMetadata[0].Idempotent)
	require.Empty(t, admin.createMetadata[0].Notes, "the service fills in the initial-balance note")

	// Only the password changes: the fingerprint leaves it out, so the action (and the replay) stay the same.
	samePassword := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/users", `{"email":"e10@example.com","password":"secret-2","balance":50}`, "user-create-ledger")
	require.Equal(t, http.StatusOK, samePassword.Code, samePassword.Body.String())
	require.Equal(t, actionID, samePassword.Header().Get("X-Admin-Adjustment-Action-ID"))

	keyless := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/users", body, "")
	require.Equal(t, http.StatusOK, keyless.Code, keyless.Body.String())
	require.Empty(t, keyless.Header().Get("X-Admin-Adjustment-Action-ID"))
	require.Len(t, admin.createMetadata, 2)
	require.False(t, admin.createMetadata[1].Idempotent)
}

func TestUserHandler_GetBalanceHistory_AdjustmentFieldsAndBreakdown(t *testing.T) {
	usedAt := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	userID := int64(1)
	adjustmentID := int64(5521)
	before, after, operator := "12.40000000", "32.40000000", "ops@example.com"
	admin := &balanceLedgerCapturingAdmin{
		stubAdminService: newStubAdminService(),
		history: []service.RedeemCode{
			{ID: 901, Code: "A", Type: service.AdjustmentTypeAdminBalance, Value: 20, Status: service.StatusUsed, UsedBy: &userID, UsedAt: &usedAt, Notes: "补偿",
				Adjustment: &service.AdminAdjustmentLink{AdjustmentID: adjustmentID, BeforeValue: &before, AfterValue: &after, OperatorEmail: &operator}},
			{ID: 902, Code: "B", Type: service.RedeemTypeBalance, Value: 40, Status: service.StatusUsed, UsedBy: &userID, UsedAt: &usedAt},
		},
		breakdown: &service.UserRechargeBreakdown{Redeem: 40, Admin: 20, Affiliate: 5, Promo: 3},
	}
	router := newBalanceLedgerRouter(admin)

	rec := optionalWriteRequest(router, http.MethodGet, "/api/v1/admin/users/1/balance-history", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	var items []map[string]any
	require.NoError(t, json.Unmarshal(envelope.Data["items"], &items))
	require.Len(t, items, 2)
	require.Equal(t, float64(5521), items[0]["adjustment_id"])
	require.Equal(t, before, items[0]["before_value"])
	require.Equal(t, after, items[0]["after_value"])
	require.Equal(t, operator, items[0]["operator_email"])
	require.Equal(t, "补偿", items[0]["notes"])
	for _, key := range []string{"adjustment_id", "before_value", "after_value", "operator_email"} {
		value, present := items[1][key]
		require.True(t, present, "%s must be present (null) on non-adjustment rows", key)
		require.Nil(t, value, key)
	}
	require.Equal(t, float64(902), items[1]["id"])

	require.JSONEq(t, `60`, string(envelope.Data["total_recharged"]))
	require.JSONEq(t, `2`, string(envelope.Data["total"]))
	require.JSONEq(t, `1`, string(envelope.Data["pages"]))
	require.JSONEq(t, `{"redeem":40,"admin":20,"affiliate":5,"promo":3}`, string(envelope.Data["total_recharged_breakdown"]))

	// A server without the breakdown reader still answers, with null.
	admin.breakdown = nil
	rec = optionalWriteRequest(router, http.MethodGet, "/api/v1/admin/users/1/balance-history", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.JSONEq(t, `null`, string(envelope.Data["total_recharged_breakdown"]))
}
