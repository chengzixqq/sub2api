package admin

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type UserAdjustmentHandler struct {
	service *service.AdminUserAdjustmentService
}

func NewUserAdjustmentHandler(adjustmentService *service.AdminUserAdjustmentService) *UserAdjustmentHandler {
	return &UserAdjustmentHandler{service: adjustmentService}
}

type userAdjustmentListResponse struct {
	Items    []service.AdminUserAdjustment      `json:"items"`
	Total    int64                              `json:"total"`
	Page     int                                `json:"page"`
	PageSize int                                `json:"page_size"`
	Pages    int                                `json:"pages"`
	Summary  service.AdminUserAdjustmentSummary `json:"summary"`
}

func (h *UserAdjustmentHandler) List(c *gin.Context) {
	filter, err := parseUserAdjustmentFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	page, pageSize := response.ParsePagination(c)
	if pageSize > 200 {
		pageSize = 200
	}
	items, summary, err := h.service.List(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	pages := int(math.Ceil(float64(summary.RecordCount) / float64(pageSize)))
	if pages < 1 {
		pages = 1
	}
	response.Success(c, userAdjustmentListResponse{
		Items: items, Total: summary.RecordCount, Page: page, PageSize: pageSize, Pages: pages, Summary: summary,
	})
}

func (h *UserAdjustmentHandler) Export(c *gin.Context) {
	filter, err := parseUserAdjustmentFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	tempFile, err := os.CreateTemp("", "sub2api-admin-user-adjustments-*.csv")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	if _, err := tempFile.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	writer := csv.NewWriter(tempFile)
	if err := writer.Write([]string{
		"id", "action_id", "created_at", "kind", "operation", "requested_value", "before_value", "after_value", "delta",
		"user_id", "user_email", "user_name", "operator_user_id", "operator_email", "operator_name", "notes", "client_ip", "auth_method", "request_id", "source", "legacy_redeem_code_id",
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	rowCount := 0
	err = h.service.Stream(c.Request.Context(), filter, func(item service.AdminUserAdjustment) error {
		row := []string{
			strconv.FormatInt(item.ID, 10), csvSafeText(item.ActionID), item.CreatedAt.UTC().Format(time.RFC3339Nano), item.Kind, item.Operation,
			stringValue(item.RequestedValue), stringValue(item.BeforeValue), stringValue(item.AfterValue), item.Delta,
			int64Value(item.UserID), csvSafeText(stringValue(item.UserEmail)), csvSafeText(stringValue(item.UserName)),
			int64Value(item.OperatorUserID), csvSafeText(stringValue(item.OperatorEmail)), csvSafeText(stringValue(item.OperatorName)), csvSafeText(stringValue(item.Notes)),
			csvSafeText(stringValue(item.ClientIP)), csvSafeText(stringValue(item.AuthMethod)), csvSafeText(stringValue(item.RequestID)), csvSafeText(item.Source), int64Value(item.LegacyRedeemCodeID),
		}
		if err := writer.Write(row); err != nil {
			return err
		}
		rowCount++
		if rowCount%100 == 0 {
			writer.Flush()
			if err := writer.Error(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	fileInfo, err := tempFile.Stat()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filename := fmt.Sprintf("admin-user-adjustments-%s.csv", time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	http.ServeContent(c.Writer, c.Request, filename, fileInfo.ModTime(), tempFile)
}

func parseUserAdjustmentFilter(c *gin.Context) (service.AdminUserAdjustmentFilter, error) {
	filter := service.AdminUserAdjustmentFilter{
		Kind: strings.TrimSpace(c.Query("kind")), Operation: strings.TrimSpace(c.Query("operation")),
		Direction: strings.TrimSpace(c.Query("direction")), Keyword: strings.TrimSpace(c.Query("keyword")),
		Operator: strings.TrimSpace(c.Query("operator")),
	}
	if filter.Kind != "" && filter.Kind != service.AdjustmentKindBalance && filter.Kind != service.AdjustmentKindConcurrency {
		return filter, fmt.Errorf("kind must be balance or concurrency")
	}
	if filter.Operation != "" && filter.Operation != service.AdjustmentOperationAdd && filter.Operation != service.AdjustmentOperationSubtract && filter.Operation != service.AdjustmentOperationSet && filter.Operation != service.AdjustmentOperationLegacy {
		return filter, fmt.Errorf("operation must be add, subtract, set or legacy")
	}
	if filter.Direction != "" && filter.Direction != "increase" && filter.Direction != "decrease" {
		return filter, fmt.Errorf("direction must be increase or decrease")
	}
	if len([]rune(filter.Keyword)) > 100 || len([]rune(filter.Operator)) > 100 {
		return filter, fmt.Errorf("keyword and operator cannot exceed 100 characters")
	}
	if raw := strings.TrimSpace(c.Query("start_time")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return filter, fmt.Errorf("start_time must be RFC3339")
		}
		filter.StartTime = &value
	}
	if raw := strings.TrimSpace(c.Query("end_time")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return filter, fmt.Errorf("end_time must be RFC3339")
		}
		filter.EndTime = &value
	}
	if filter.StartTime != nil && filter.EndTime != nil && !filter.StartTime.Before(*filter.EndTime) {
		return filter, fmt.Errorf("start_time must be before end_time")
	}
	return filter, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64Value(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func csvSafeText(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}
