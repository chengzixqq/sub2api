package admin

import (
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/gin-gonic/gin"
)

// opsOpenAITokenStatsCustomRange is the time_range label echoed when the
// window comes from start_time / end_time.
const opsOpenAITokenStatsCustomRange = "custom"

// resolveOpsOpenAITokenStatsWindow picks the token-stats window. A start_time
// or end_time key switches to the custom range (paired, RFC3339, optional
// timezone) and wins over time_range; otherwise time_range applies as before
// (default 30d, ending now). The span cap is enforced by the service.
func resolveOpsOpenAITokenStatsWindow(c *gin.Context) (label string, start, end time.Time, err error) {
	query := c.Request.URL.Query()
	_, hasStart := query["start_time"]
	_, hasEnd := query["end_time"]
	if hasStart || hasEnd {
		r, err := usagequery.ParseRange(query, nil, nil)
		if err != nil {
			return "", time.Time{}, time.Time{}, err
		}
		return opsOpenAITokenStatsCustomRange, *r.Start, *r.End, nil
	}

	label = strings.TrimSpace(c.Query("time_range"))
	if label == "" {
		label = "30d"
	}
	dur, ok := parseOpsOpenAITokenStatsDuration(label)
	if !ok {
		return "", time.Time{}, time.Time{}, fmt.Errorf("invalid time_range")
	}
	end = time.Now().UTC()
	return label, end.Add(-dur), end, nil
}
