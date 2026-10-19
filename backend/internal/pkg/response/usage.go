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
// the largest visible id, not a safe watermark: ids are allocated at INSERT
// but rows become visible at COMMIT, and concurrent writers commit out of id
// order, so a smaller id can appear after latest_id was returned. Clients
// must poll with since_id rewound below latest_id and dedupe by id. has_more
// means more new rows than one page, so the client should reload page 1.
func UsageIncremental(c *gin.Context, items any, pageSize int, hasMore bool, sinceID, latestID int64, query usagequery.Metadata) {
	Success(c, gin.H{"items": items, "total": nil, "page": 1, "page_size": pageSize, "pages": nil, "has_more": hasMore, "total_exact": false, "query": query, "since_id": sinceID, "latest_id": latestID})
}
