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
