package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// bindAccountDispatchRetryScope keeps confirmed stale candidates excluded across
// repeated selections for one request, without sharing state across requests.
func bindAccountDispatchRetryScope(c *gin.Context) {
	if c != nil && c.Request != nil {
		c.Request = c.Request.WithContext(service.WithAccountDispatchRetryScope(c.Request.Context()))
	}
}
