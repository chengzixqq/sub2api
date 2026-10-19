package admin

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/gin-gonic/gin"
)

// writeConditionalETag sets the validator headers and answers 304 when the
// client already holds etag. It reports whether the response was written.
// The query still runs; 304 only saves the body transfer and client decode.
func writeConditionalETag(c *gin.Context, etag string) bool {
	if etag == "" {
		return false
	}
	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, no-cache")
	c.Header("Vary", "If-None-Match")
	if ifNoneMatchMatched(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return true
	}
	return false
}

// writeSuccessWithETag hashes basis (the response with volatile fields
// removed) and writes data unless the client's copy is still current.
func writeSuccessWithETag(c *gin.Context, basis, data any) {
	if writeConditionalETag(c, usagequery.CacheETag(basis)) {
		return
	}
	response.Success(c, data)
}

// writePaginatedWithETag is response.Paginated with an ETag over the whole
// paginated body; it has no volatile fields to exclude.
func writePaginatedWithETag(c *gin.Context, items any, total int64, page, pageSize int) {
	payload := response.PaginatedPayload(items, total, page, pageSize)
	writeSuccessWithETag(c, payload, payload)
}

// stableSnapshotV2ETag hashes a copy of the snapshot without the fields that
// change on every rebuild (generated_at, query.generated_at, stats.uptime).
func stableSnapshotV2ETag(payload *dashboardSnapshotV2Response) string {
	if payload == nil {
		return ""
	}
	basis := *payload
	basis.GeneratedAt = ""
	basis.Query.GeneratedAt = time.Time{}
	if payload.Stats != nil {
		stats := *payload.Stats
		stats.Uptime = 0
		basis.Stats = &stats
	}
	return usagequery.CacheETag(&basis)
}
