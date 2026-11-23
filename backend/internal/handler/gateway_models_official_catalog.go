package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// models is the final group-visible catalogue, including any allowlist filtering.
func (h *GatewayHandler) writeClaudeModelsResponse(c *gin.Context, models []claude.Model) {
	var official, fill bool
	if middleware.IsAnthropicModelsRequest(c) {
		official, fill = h.settingService.ClaudeModelsCatalogPolicy(c.Request.Context())
	}
	if !official && !fill {
		writeModelsListResponse(c, models)
		return
	}
	if id := c.Param("model"); id != "" {
		for _, model := range models {
			if model.ID != id {
				continue
			}
			if fill {
				service.SetLocalAnthropicRequestID(c)
			}
			if official {
				info, _ := claude.OfficialModelInfo(id)
				writeModelJSON(c, info)
			} else {
				writeModelJSON(c, model)
			}
			return
		}
		if official {
			service.WriteLocalAnthropicModelNotFound(c, id)
		} else {
			writeModelsListResponse(c, models)
		}
		return
	}
	if fill {
		service.SetLocalAnthropicRequestID(c)
	}
	if official {
		writeModelJSON(c, officialModelsPage(models, c.Request.URL.Query()))
		return
	}
	writeModelsListResponse(c, models)
}

func writeModelJSON(c *gin.Context, model any) {
	body, err := json.Marshal(model)
	if err != nil {
		writeOpenAIModelsError(c, http.StatusInternalServerError, "api_error", "Failed to encode model catalogue")
		return
	}
	c.Data(http.StatusOK, "application/json", body)
}

type officialModelPage struct {
	Data    []claude.OfficialModel `json:"data"`
	FirstID *string                `json:"first_id"`
	HasMore bool                   `json:"has_more"`
	LastID  *string                `json:"last_id"`
}

func officialModelsPage(models []claude.Model, query url.Values) officialModelPage {
	lifecycle := modelLifecycleFilter(query)
	data := make([]claude.OfficialModel, 0, len(models))
	for _, model := range models {
		info, _ := claude.OfficialModelInfo(model.ID)
		if lifecycle[info.Lifecycle] {
			data = append(data, info)
		}
	}
	slices.SortFunc(data, func(a, b claude.OfficialModel) int {
		if a.CreatedAt != b.CreatedAt {
			return strings.Compare(b.CreatedAt, a.CreatedAt)
		}
		return strings.Compare(a.ID, b.ID)
	})

	limit := modelPageLimit(query.Get("limit"))
	start, end := 0, len(data)
	before := query.Has("before_id") && !query.Has("after_id")
	if before || query.Has("after_id") {
		cursor := query.Get("after_id")
		if before {
			cursor = query.Get("before_id")
		}
		pos := slices.IndexFunc(data, func(model claude.OfficialModel) bool { return model.ID == cursor })
		if pos < 0 {
			return officialModelPage{Data: data[:0]}
		}
		if before {
			end = pos
		} else {
			start = pos + 1
		}
	}
	if before {
		start = max(0, end-limit)
	} else {
		end = min(end, start+limit)
	}
	page := officialModelPage{Data: data[start:end]}
	if len(page.Data) != 0 {
		page.FirstID = &data[start].ID
		page.LastID = &data[end-1].ID
		page.HasMore = before && start > 0 || !before && end < len(data)
	}
	return page
}

func modelPageLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		// Atoi saturates on overflow. Verify the whole input is an integer
		// since it can report range before reaching a trailing non-digit.
		if !errors.Is(err, strconv.ErrRange) || strings.Trim(strings.TrimLeft(raw, "+-"), "0123456789") != "" {
			return 20
		}
	}
	return min(1000, max(1, n))
}

func modelLifecycleFilter(query url.Values) map[string]bool {
	allowed := make(map[string]bool, 3)
	for _, key := range []string{"lifecycle", "lifecycle[]"} {
		for _, value := range query[key] {
			for item := range strings.SplitSeq(value, ",") {
				stage := strings.TrimSpace(item)
				switch stage {
				case "active", "deprecated", "retired":
					allowed[stage] = true
				}
			}
		}
	}
	if len(allowed) == 0 {
		allowed["active"] = true
		allowed["deprecated"] = true
	}
	return allowed
}
