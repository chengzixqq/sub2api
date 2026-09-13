package admin

import (
	"fmt"
	"net/url"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
)

func parseUsageCleanupRange(req CreateUsageCleanupTaskRequest) (time.Time, time.Time, bool, error) {
	values := url.Values{"start_date": {req.StartDate}, "end_date": {req.EndDate}, "timezone": {req.Timezone}}
	exact := req.StartTime != nil || req.EndTime != nil
	if req.StartTime != nil {
		values.Set("start_time", *req.StartTime)
	}
	if req.EndTime != nil {
		values.Set("end_time", *req.EndTime)
	}
	rangeValue, err := usagequery.ParseRange(values, nil, nil)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if rangeValue.Start == nil || rangeValue.End == nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("a complete cleanup time range is required")
	}
	end := *rangeValue.End
	if !exact {
		// Keep existing calendar-day clients and persisted tasks on inclusive end semantics.
		end = end.Add(-time.Nanosecond)
	}
	return *rangeValue.Start, end, exact, nil
}
