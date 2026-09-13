package usagequery

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// Range uses an inclusive start and an exclusive end. Nil bounds preserve legacy unbounded lists.
type Range struct {
	Start    *time.Time
	End      *time.Time
	Timezone string
}

type Metadata struct {
	StartTime   *time.Time `json:"start_time"`
	EndTime     *time.Time `json:"end_time"`
	Timezone    string     `json:"timezone"`
	GeneratedAt time.Time  `json:"generated_at"`
}

func (r Range) Metadata() Metadata {
	return Metadata{StartTime: r.Start, EndTime: r.End, Timezone: r.Timezone, GeneratedAt: time.Now().UTC()}
}

func ParseRange(values url.Values, defaultStart, defaultEnd *time.Time) (Range, error) {
	loc := timezone.Location()
	if zone := strings.TrimSpace(values.Get("timezone")); zone != "" {
		var err error
		loc, err = time.LoadLocation(zone)
		if err != nil {
			return Range{}, fmt.Errorf("invalid timezone")
		}
	}
	r := Range{Start: defaultStart, End: defaultEnd, Timezone: loc.String()}
	_, hasStart := values["start_time"]
	_, hasEnd := values["end_time"]
	if hasStart || hasEnd {
		if !hasStart || !hasEnd {
			return Range{}, fmt.Errorf("start_time and end_time must be provided together")
		}
		start, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(values.Get("start_time")))
		if err != nil {
			return Range{}, fmt.Errorf("invalid start_time format, use RFC3339 with timezone")
		}
		end, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(values.Get("end_time")))
		if err != nil {
			return Range{}, fmt.Errorf("invalid end_time format, use RFC3339 with timezone")
		}
		start, end = start.In(loc), end.In(loc)
		r.Start, r.End = &start, &end
	} else {
		if raw := strings.TrimSpace(values.Get("start_date")); raw != "" {
			start, err := time.ParseInLocation("2006-01-02", raw, loc)
			if err != nil {
				return Range{}, fmt.Errorf("invalid start_date format, use YYYY-MM-DD")
			}
			r.Start = &start
		}
		if raw := strings.TrimSpace(values.Get("end_date")); raw != "" {
			end, err := time.ParseInLocation("2006-01-02", raw, loc)
			if err != nil {
				return Range{}, fmt.Errorf("invalid end_date format, use YYYY-MM-DD")
			}
			end = end.AddDate(0, 0, 1)
			r.End = &end
		}
	}
	if r.Start != nil && r.End != nil && !r.Start.Before(*r.End) {
		return Range{}, fmt.Errorf("start time must be before end time")
	}
	return r, nil
}

func DeferredCount(values url.Values) (bool, error) {
	switch strings.TrimSpace(values.Get("count_mode")) {
	case "", "exact":
		return false, nil
	case "deferred":
		return true, nil
	default:
		return false, fmt.Errorf("invalid count_mode, use exact or deferred")
	}
}
