package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type adjustmentHandlerRepoStub struct {
	listFilter   service.AdminUserAdjustmentFilter
	streamFilter service.AdminUserAdjustmentFilter
	items        []service.AdminUserAdjustment
	summary      service.AdminUserAdjustmentSummary
	streamErr    error
}

func (s *adjustmentHandlerRepoStub) CreateBatch(context.Context, []service.AdminUserAdjustmentWrite) error {
	return nil
}

func (s *adjustmentHandlerRepoStub) CountByActionID(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}

func (s *adjustmentHandlerRepoStub) LockAction(context.Context, uuid.UUID) error {
	return nil
}

func (s *adjustmentHandlerRepoStub) List(_ context.Context, filter service.AdminUserAdjustmentFilter, _, _ int) ([]service.AdminUserAdjustment, service.AdminUserAdjustmentSummary, error) {
	s.listFilter = filter
	return s.items, s.summary, nil
}

func (s *adjustmentHandlerRepoStub) Stream(_ context.Context, filter service.AdminUserAdjustmentFilter, consume func(service.AdminUserAdjustment) error) error {
	s.streamFilter = filter
	for _, item := range s.items {
		if err := consume(item); err != nil {
			return err
		}
	}
	return s.streamErr
}

func TestParseUserAdjustmentFilterUsesRFC3339HalfOpenRange(t *testing.T) {
	c := adjustmentTestContext(t, "/api/v1/admin/user-adjustments?kind=balance&operation=set&direction=increase&keyword=user&operator=owner&start_time=2026-08-04T16%3A30%3A00%2B08%3A00&end_time=2026-08-10T00%3A00%3A00%2B08%3A00")

	filter, err := parseUserAdjustmentFilter(c)
	require.NoError(t, err)
	require.Equal(t, service.AdjustmentKindBalance, filter.Kind)
	require.Equal(t, service.AdjustmentOperationSet, filter.Operation)
	require.Equal(t, "increase", filter.Direction)
	require.Equal(t, "user", filter.Keyword)
	require.Equal(t, "owner", filter.Operator)
	require.Equal(t, "2026-08-04T16:30:00+08:00", filter.StartTime.Format(time.RFC3339))
	require.Equal(t, "2026-08-10T00:00:00+08:00", filter.EndTime.Format(time.RFC3339))
}

func TestParseUserAdjustmentFilterRejectsInvalidValues(t *testing.T) {
	tests := []string{
		"?kind=other",
		"?operation=delete",
		"?direction=zero",
		"?start_time=not-a-time",
		"?start_time=2026-08-10T00%3A00%3A00Z&end_time=2026-08-10T00%3A00%3A00Z",
	}
	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			_, err := parseUserAdjustmentFilter(adjustmentTestContext(t, "/api/v1/admin/user-adjustments"+query))
			require.Error(t, err)
		})
	}
}

func TestUserAdjustmentExportReusesFiltersAndPreventsFormulaInjection(t *testing.T) {
	operatorName := "@owner"
	userEmail := "=HYPERLINK(\"https://example.test\")"
	notes := " +SUM(1,1)"
	repo := &adjustmentHandlerRepoStub{items: []service.AdminUserAdjustment{{
		ID: 9, ActionID: "11111111-1111-4111-8111-111111111111", Kind: service.AdjustmentKindBalance,
		Operation: service.AdjustmentOperationAdd, RequestedValue: stringPointer("3.00000000"),
		Delta: "3.00000000", BeforeValue: stringPointer("2.00000000"), AfterValue: stringPointer("5.00000000"),
		UserID: int64Pointer(42), UserEmail: &userEmail, OperatorName: &operatorName, Notes: &notes,
		Source: "admin_balance", CreatedAt: time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC),
	}}}
	handler := NewUserAdjustmentHandler(service.NewAdminUserAdjustmentService(repo))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin/user-adjustments/export?kind=balance&keyword=alice", nil)

	handler.Export(c)

	require.Equal(t, 200, recorder.Code)
	require.True(t, bytes.HasPrefix(recorder.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
	require.Equal(t, service.AdjustmentKindBalance, repo.streamFilter.Kind)
	require.Equal(t, "alice", repo.streamFilter.Keyword)

	reader := csv.NewReader(bytes.NewReader(recorder.Body.Bytes()[3:]))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	header := records[0]
	row := records[1]
	require.Contains(t, header, "operator_name")
	require.Equal(t, "'=HYPERLINK(\"https://example.test\")", row[columnIndex(t, header, "user_email")])
	require.Equal(t, "'@owner", row[columnIndex(t, header, "operator_name")])
	require.Equal(t, "' +SUM(1,1)", row[columnIndex(t, header, "notes")])
}

func TestUserAdjustmentExportDoesNotReturnTruncatedCSVOnStreamFailure(t *testing.T) {
	repo := &adjustmentHandlerRepoStub{
		items: []service.AdminUserAdjustment{{
			ID: 1, ActionID: "11111111-1111-4111-8111-111111111111",
			Kind: service.AdjustmentKindBalance, Operation: service.AdjustmentOperationAdd,
			Delta: "1.00000000", Source: "admin_balance", CreatedAt: time.Now(),
		}},
		streamErr: errors.New("forced stream failure"),
	}
	handler := NewUserAdjustmentHandler(service.NewAdminUserAdjustmentService(repo))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin/user-adjustments/export", nil)

	handler.Export(c)

	require.Equal(t, 500, recorder.Code)
	require.False(t, bytes.HasPrefix(recorder.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
	require.NotContains(t, recorder.Header().Get("Content-Type"), "text/csv")
}

func TestCSVSafeTextLeavesOrdinaryAndNumericTextUntouched(t *testing.T) {
	require.Equal(t, "ordinary", csvSafeText("ordinary"))
	require.Equal(t, "12.50", csvSafeText("12.50"))
	require.Equal(t, "'-formula", csvSafeText("-formula"))
	require.Equal(t, "'\t=formula", csvSafeText("\t=formula"))
}

func int64Pointer(value int64) *int64 {
	return &value
}

func adjustmentTestContext(t *testing.T, target string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", target, nil)
	return c
}

func stringPointer(value string) *string { return &value }

func columnIndex(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, column := range header {
		if column == name {
			return i
		}
	}
	t.Fatalf("column %q not found in %s", name, strings.Join(header, ","))
	return -1
}
