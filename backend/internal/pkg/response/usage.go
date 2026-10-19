package response

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/gin-gonic/gin"
)

// UsagePaginated keeps legacy exact responses and makes deferred totals explicitly unknown.
func UsagePaginated(c *gin.Context, items any, total int64, page, pageSize int, deferred bool, query usagequery.Metadata) {
	if deferred {
		Success(c, gin.H{"items": items, "total": nil, "page": page, "page_size": pageSize, "pages": nil, "has_more": total > int64(page*pageSize), "total_exact": false, "query": query})
		return
	}
	pages := (total + int64(pageSize) - 1) / int64(pageSize)
	Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize, "pages": pages, "has_more": total > int64(page*pageSize), "total_exact": true, "query": query})
}

// UsageIncremental is the deferred envelope for since_id reads. latest_id is
// the cursor for the next poll; has_more means rows were skipped between the
// cursor and this page, so the client should reload page 1 instead.
func UsageIncremental(c *gin.Context, items any, pageSize int, hasMore bool, sinceID, latestID int64, query usagequery.Metadata) {
	Success(c, gin.H{"items": items, "total": nil, "page": 1, "page_size": pageSize, "pages": nil, "has_more": hasMore, "total_exact": false, "query": query, "since_id": sinceID, "latest_id": latestID})
}
