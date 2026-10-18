package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type generateCapturingAdmin struct {
	*stubAdminService
	inputs []*service.GenerateRedeemCodesInput
	batch  []service.RedeemCode
}

func (s *generateCapturingAdmin) GenerateRedeemCodes(_ context.Context, in *service.GenerateRedeemCodesInput) ([]service.RedeemCode, error) {
	s.inputs = append(s.inputs, in)
	if len(s.batch) == 0 {
		for i := 0; i < in.Count; i++ {
			s.batch = append(s.batch, service.RedeemCode{ID: int64(100 + i), Code: strings.Repeat("a", 31) + string(rune('0'+i)), Type: in.Type, Value: in.Value, Notes: in.Notes, BatchID: in.BatchID, CreatedBy: in.CreatedBy})
		}
	}
	return s.batch, nil
}

func (s *generateCapturingAdmin) ListRedeemCodeBatch(_ context.Context, batchID uuid.UUID) ([]service.RedeemCode, error) {
	return s.batch, nil
}

func postGenerate(t *testing.T, admin service.AdminService, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 5})
		c.Next()
	})
	r.POST("/api/v1/admin/redeem-codes/generate", NewRedeemHandler(admin, nil).Generate)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/generate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRedeemHandler_Generate_PassesBatchNotesAndCreator(t *testing.T) {
	admin := &generateCapturingAdmin{stubAdminService: newStubAdminService()}
	batch := uuid.New()

	rec := postGenerate(t, admin, `{"count":2,"type":"balance","value":10,"notes":"双 11 活动 #A123","batch_id":"`+batch.String()+`"}`, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	in := admin.inputs[0]
	require.Equal(t, batch, *in.BatchID)
	require.Equal(t, "双 11 活动 #A123", in.Notes)
	require.Equal(t, int64(5), *in.CreatedBy)
	require.Equal(t, batch.String(), rec.Header().Get("X-Redeem-Batch-ID"))
	require.Contains(t, rec.Body.String(), `"batch_id":"`+batch.String()+`"`)
	require.Contains(t, rec.Body.String(), `"created_by":5`)
}

func TestRedeemHandler_Generate_DerivesBatchFromIdempotencyKey(t *testing.T) {
	admin := &generateCapturingAdmin{stubAdminService: newStubAdminService()}
	body := `{"count":1,"type":"balance","value":1}`

	postGenerate(t, admin, body, "op-1")
	postGenerate(t, admin, body, "op-1")
	postGenerate(t, admin, body, "op-2")
	require.Len(t, admin.inputs, 3)
	require.NotNil(t, admin.inputs[0].BatchID)
	require.Equal(t, *admin.inputs[0].BatchID, *admin.inputs[1].BatchID, "same key → same batch")
	require.NotEqual(t, *admin.inputs[0].BatchID, *admin.inputs[2].BatchID)
}

func TestRedeemHandler_Generate_WebRequestStillWorks(t *testing.T) {
	admin := &generateCapturingAdmin{stubAdminService: newStubAdminService()}

	rec := postGenerate(t, admin, `{"count":1,"type":"balance","value":1}`, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(strings.TrimSpace(extractData(t, rec.Body.Bytes())), "["), "the response stays an array")
	require.NotNil(t, admin.inputs[0].BatchID, "a random batch id is still assigned")
}

func TestRedeemHandler_Generate_Validation(t *testing.T) {
	admin := &generateCapturingAdmin{stubAdminService: newStubAdminService()}

	rec := postGenerate(t, admin, `{"count":1,"type":"balance","value":1,"batch_id":"not-a-uuid"}`, "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "REDEEM_BATCH_ID_INVALID")

	rec = postGenerate(t, admin, `{"count":1,"type":"balance","value":1,"notes":"`+strings.Repeat("备", 1001)+`"}`, "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// Stored replays redact "code" (log redaction); the handler reloads the batch so the client gets the real codes back.
func TestRedeemHandler_Generate_ReplayReturnsRealCodes(t *testing.T) {
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	admin := &generateCapturingAdmin{stubAdminService: newStubAdminService()}
	body := `{"count":2,"type":"balance","value":1}`

	first := postGenerate(t, admin, body, "replay-me")
	require.Equal(t, http.StatusOK, first.Code)
	replay := postGenerate(t, admin, body, "replay-me")
	require.Equal(t, http.StatusOK, replay.Code)
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.NotContains(t, replay.Body.String(), `"***"`)
	require.Contains(t, replay.Body.String(), admin.batch[0].Code)
	require.Len(t, admin.inputs, 1, "the replay does not generate again")
}
