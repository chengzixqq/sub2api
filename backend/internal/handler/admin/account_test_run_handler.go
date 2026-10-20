package admin

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type accountTestRunAPI interface {
	Start(ctx context.Context, in service.StartAccountTestRunInput) (*service.AccountTestRun, error)
	Latest(ctx context.Context, accountID int64) (*service.AccountTestRun, error)
	Get(ctx context.Context, runID string) (*service.AccountTestRun, error)
}

// AccountTestRunHandler serves async account tests: start one, leave, and read
// the stored outcome later.
type AccountTestRunHandler struct {
	runs accountTestRunAPI
}

// NewAccountTestRunHandler creates an AccountTestRunHandler.
func NewAccountTestRunHandler(runs *service.AccountTestRunService) *AccountTestRunHandler {
	h := &AccountTestRunHandler{}
	if runs != nil {
		h.runs = runs
	}
	return h
}

type startAccountTestRunRequest struct {
	ModelID          string `json:"model_id"`
	Prompt           string `json:"prompt"`
	Mode             string `json:"mode"`
	RecoverOnSuccess *bool  `json:"recover_on_success"`
}

type accountTestRunView struct {
	RunID              string                          `json:"run_id"`
	AccountID          int64                           `json:"account_id"`
	Source             string                          `json:"source"`
	Status             string                          `json:"status"`
	Model              string                          `json:"model"`
	LatencyMs          *int64                          `json:"latency_ms"`
	FirstTokenMs       *int64                          `json:"first_token_ms"`
	UpstreamStatusCode *int                            `json:"upstream_status_code"`
	ErrorKind          *string                         `json:"error_kind"`
	ErrorSummary       string                          `json:"error_summary"`
	ErrorMessage       string                          `json:"error_message"`
	ResponseText       string                          `json:"response_text"`
	Recovery           *service.AccountTestRunRecovery `json:"recovery"`
	StartedAt          time.Time                       `json:"started_at"`
	FinishedAt         *time.Time                      `json:"finished_at"`
}

func toAccountTestRunView(run *service.AccountTestRun) accountTestRunView {
	return accountTestRunView{
		RunID:              run.RunID,
		AccountID:          run.AccountID,
		Source:             run.Source,
		Status:             run.Status,
		Model:              run.ModelID,
		LatencyMs:          run.LatencyMs,
		FirstTokenMs:       run.FirstTokenMs,
		UpstreamStatusCode: run.UpstreamStatusCode,
		ErrorKind:          run.ErrorKind(),
		ErrorSummary:       run.ErrorSummary(),
		ErrorMessage:       run.ErrorMessage,
		ResponseText:       run.ResponseText,
		Recovery:           run.Recovery,
		StartedAt:          run.StartedAt,
		FinishedAt:         run.FinishedAt,
	}
}

func (h *AccountTestRunHandler) available(c *gin.Context) bool {
	if h == nil || h.runs == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return false
	}
	return true
}

// Start runs an account test in the background and returns 202 at once.
// POST /api/v1/admin/accounts/:id/test-runs
func (h *AccountTestRunHandler) Start(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if !h.available(c) {
		return
	}
	var req startAccountTestRunRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	in := service.StartAccountTestRunInput{
		AccountID:        accountID,
		ModelID:          req.ModelID,
		Prompt:           req.Prompt,
		Mode:             req.Mode,
		RecoverOnSuccess: req.RecoverOnSuccess,
		CreatedBy:        accountTestRunActor(c),
	}
	run, err := h.runs.Start(c.Request.Context(), in)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, toAccountTestRunView(run))
}

// Latest returns the newest stored test of an account (async or manual).
// GET /api/v1/admin/accounts/:id/test-runs/latest
func (h *AccountTestRunHandler) Latest(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if !h.available(c) {
		return
	}
	run, err := h.runs.Latest(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toAccountTestRunView(run))
}

// Get returns one stored test run.
// GET /api/v1/admin/account-test-runs/:run_id
func (h *AccountTestRunHandler) Get(c *gin.Context) {
	if !h.available(c) {
		return
	}
	run, err := h.runs.Get(c.Request.Context(), c.Param("run_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toAccountTestRunView(run))
}

// accountTestRunActor is the admin who started the test, kept for later
// notifications.
func accountTestRunActor(c *gin.Context) *int64 {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		return nil
	}
	userID := subject.UserID
	return &userID
}
