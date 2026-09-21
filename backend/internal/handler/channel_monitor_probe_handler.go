package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ChannelMonitorProbeHandler struct {
	service *service.ChannelMonitorProbeService
}

func NewChannelMonitorProbeHandler(s *service.ChannelMonitorProbeService) *ChannelMonitorProbeHandler {
	return &ChannelMonitorProbeHandler{service: s}
}
func (h *ChannelMonitorProbeHandler) owner(c *gin.Context) bool {
	if !channelMonitorV2IsAdmin(c) {
		response.Forbidden(c, "station owner required")
		return false
	}
	if err := service.RequireStationOwnerScope(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}
func (h *ChannelMonitorProbeHandler) ListTargets(c *gin.Context) {
	if !h.owner(c) {
		return
	}
	items, err := h.service.ListTargets(c.Request.Context())
	if err != nil {
		monitorProbeError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": len(items)})
}

type monitorProbeTargetRequest struct {
	GroupID  int64  `json:"group_id" binding:"required,gt=0"`
	Model    string `json:"model" binding:"required,max=200"`
	Protocol string `json:"protocol" binding:"required"`
	Enabled  bool   `json:"enabled"`
	Version  int64  `json:"version"`
}

func (h *ChannelMonitorProbeHandler) CreateTarget(c *gin.Context) { h.saveTarget(c, false) }
func (h *ChannelMonitorProbeHandler) UpdateTarget(c *gin.Context) { h.saveTarget(c, true) }
func (h *ChannelMonitorProbeHandler) saveTarget(c *gin.Context, update bool) {
	if !h.owner(c) {
		return
	}
	var body monitorProbeTargetRequest
	if c.ShouldBindJSON(&body) != nil {
		response.BadRequest(c, "invalid probe target")
		return
	}
	t := &service.ChannelMonitorProbeTarget{GroupID: body.GroupID, Model: body.Model, Protocol: body.Protocol, Enabled: body.Enabled, Version: body.Version}
	if update {
		id, ok := monitorProbeID(c)
		if !ok {
			return
		}
		t.ID = id
	}
	if err := h.service.SaveTarget(c.Request.Context(), t); err != nil {
		monitorProbeError(c, err)
		return
	}
	response.Success(c, t)
}
func (h *ChannelMonitorProbeHandler) Probe(c *gin.Context) {
	if !h.owner(c) {
		return
	}
	id, ok := monitorProbeID(c)
	if !ok {
		return
	}
	run, err := h.service.Probe(c.Request.Context(), id, c.GetHeader("Idempotency-Key"))
	if err != nil {
		monitorProbeError(c, err)
		return
	}
	response.Success(c, run)
}
func (h *ChannelMonitorProbeHandler) Budget(c *gin.Context) {
	if !h.owner(c) {
		return
	}
	budget, err := h.service.Budget(c.Request.Context())
	if err != nil {
		monitorProbeError(c, err)
		return
	}
	response.Success(c, budget)
}
func monitorProbeID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid probe target ID")
		return 0, false
	}
	return id, true
}
func monitorProbeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelMonitorProbeNotFound):
		response.NotFound(c, err.Error())
	case errors.Is(err, service.ErrChannelMonitorProbeConflict), errors.Is(err, service.ErrChannelMonitorProbeBusy):
		response.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, service.ErrChannelMonitorProbeBudget):
		response.Error(c, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, service.ErrChannelMonitorProbeInvalid):
		response.BadRequest(c, err.Error())
	case errors.Is(err, service.ErrChannelMonitorProbeDisabled):
		response.Forbidden(c, err.Error())
	default:
		response.ErrorFrom(c, err)
	}
}
