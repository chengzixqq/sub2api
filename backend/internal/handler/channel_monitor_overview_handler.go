package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *ChannelMonitorV2Handler) ObservationOverview(c *gin.Context) {
	h.observationOverview(c, false)
}
func (h *ChannelMonitorV2Handler) AdminObservationOverview(c *gin.Context) {
	h.observationOverview(c, true)
}

func (h *ChannelMonitorV2Handler) ObservationCapture() gin.HandlerFunc {
	if h == nil || h.collector == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return ChannelMonitorCaptureMiddleware(h.collector)
}

func (h *ChannelMonitorV2Handler) observationOverview(c *gin.Context, adminRoute bool) {
	if h.overview == nil {
		response.Error(c, http.StatusServiceUnavailable, "observation service unavailable")
		return
	}
	admin := adminRoute && channelMonitorV2IsAdmin(c)
	if adminRoute && !admin {
		response.Forbidden(c, "administrator required")
		return
	}
	audience := c.Query("audience")
	if audience != "" && audience != "user" {
		response.BadRequest(c, "invalid audience")
		return
	}
	if audience == "user" {
		admin = false
	}
	f, ok := h.parseFilter(c)
	if !ok {
		return
	}
	if !h.scopeFilter(c, &f, admin) {
		return
	}
	if raw := strings.TrimSpace(c.Query("end_time")); raw != "" {
		end, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || end.After(time.Now().Add(time.Minute)) || end.Before(time.Now().Add(-90*24*time.Hour)) {
			response.BadRequest(c, "invalid end_time")
			return
		}
		f.End = end.UTC().Truncate(time.Minute)
		f.Start = f.End.Add(-observationWindow(f.Range))
	} else {
		f.End = time.Now().UTC().Truncate(time.Minute)
		f.Start = f.End.Add(-observationWindow(f.Range))
	}
	refresh := false
	if raw := c.Query("refresh"); raw != "" {
		var err error
		refresh, err = strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "invalid refresh")
			return
		}
	}
	subject, exists := middleware.GetAuthSubjectFromContext(c)
	if !exists {
		response.Unauthorized(c, "user not found in context")
		return
	}
	result, err := h.overview.Overview(c.Request.Context(), f, admin, subject.UserID, refresh)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !admin {
		groups, groupErr := h.apiKeyService.GetAvailableGroups(c.Request.Context(), subject.UserID)
		if groupErr != nil {
			response.ErrorFrom(c, groupErr)
			return
		}
		reader, available := h.apiKeyService.(interface {
			GetUserGroupRates(context.Context, int64) (map[int64]float64, error)
		})
		if available {
			rates, rateErr := reader.GetUserGroupRates(c.Request.Context(), subject.UserID)
			if rateErr != nil {
				response.Error(c, http.StatusServiceUnavailable, "group rates temporarily unavailable")
				return
			}
			byID := map[int64]service.Group{}
			for _, g := range groups {
				byID[g.ID] = g
			}
			for i := range result.Items {
				g, allowed := byID[result.Items[i].GroupID]
				if !allowed {
					continue
				}
				rate := g.RateMultiplier
				if override, ok := rates[g.ID]; ok {
					rate = override
				}
				rate *= g.PeakMultiplierAt(time.Now())
				result.Items[i].RateMultiplier = &rate
			}
		}
	}
	response.Success(c, result)
}

func observationWindow(value string) time.Duration {
	switch value {
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 90 * time.Minute
	}
}

func (h *ChannelMonitorV2Handler) ObservationConfig(c *gin.Context) {
	if !channelMonitorV2IsAdmin(c) {
		response.Forbidden(c, "administrator required")
		return
	}
	if h.overview == nil {
		response.Error(c, http.StatusServiceUnavailable, "observation service unavailable")
		return
	}
	cfg, err := h.overview.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *ChannelMonitorV2Handler) UpdateObservationConfig(c *gin.Context) {
	if !channelMonitorV2IsAdmin(c) {
		response.Forbidden(c, "administrator required")
		return
	}
	if h.overview == nil {
		response.Error(c, http.StatusServiceUnavailable, "observation service unavailable")
		return
	}
	var cfg service.ChannelMonitorObservationConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "invalid observation policy")
		return
	}
	if err := service.ValidateChannelMonitorObservationConfig(cfg); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	updated, err := h.overview.UpdateConfig(c.Request.Context(), cfg)
	if err != nil {
		if errors.Is(err, service.ErrChannelMonitorV2ConfigConflict) {
			response.Error(c, http.StatusConflict, "observation policy changed; reload before saving")
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	response.Success(c, updated)
}
