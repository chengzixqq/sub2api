package admin

import (
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// parseSinceIDQuery reads the optional since_id cursor of incremental log
// lists. It writes a 400 and returns ok=false for a malformed value or when
// combined with page > 1; a nil cursor means a normal paged list.
func parseSinceIDQuery(c *gin.Context, page int) (sinceID *int64, ok bool) {
	raw, present := c.GetQuery("since_id")
	if !present {
		return nil, true
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_SINCE_ID", "Invalid since_id"))
		return nil, false
	}
	if page > 1 {
		response.ErrorFrom(c, infraerrors.BadRequest("SINCE_ID_WITH_PAGE", "since_id cannot be combined with page"))
		return nil, false
	}
	return &value, true
}

// hasExplicitOpsTimeParams reports whether the caller chose a time window;
// incremental reads without one are bounded only by the id cursor.
func hasExplicitOpsTimeParams(c *gin.Context) bool {
	for _, key := range []string{"time_range", "start_time", "end_time", "start_date", "end_date"} {
		if _, ok := c.GetQuery(key); ok {
			return true
		}
	}
	return false
}

// incrementalLatestID is the largest returned id, or the unchanged cursor
// when nothing new arrived. It is not a commit watermark (see
// response.UsageIncremental); the server deliberately adds no lag because
// pg_snapshot_xmin cannot map in-flight xids to ids and created_at is taken
// before the async queue, so clients overlap the cursor instead.
func incrementalLatestID(sinceID int64, ids ...int64) int64 {
	latest := sinceID
	for _, id := range ids {
		if id > latest {
			latest = id
		}
	}
	return latest
}
