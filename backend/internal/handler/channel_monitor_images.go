package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const monitorImageResultKey = "channel_monitor_image_terminal"

// Only a trusted protocol adapter supplies this metadata, never request headers.
func observeMonitorImageResult(c *gin.Context, result *service.OpenAIForwardResult, err error) {
	if c == nil || result == nil {
		return
	}
	out := channelMonitorParseResult{Model: monitorIdentifier(result.Model), Outcome: "unknown", ErrorCategory: "empty_output",
		OutputSeen: result.ImageCount > 0, TerminalSeen: err == nil,
		InputTokens: int64(result.Usage.InputTokens), OutputTokens: int64(result.Usage.OutputTokens), CacheReadTokens: int64(result.Usage.CacheReadInputTokens)}
	if err == nil && out.OutputSeen {
		out.Outcome, out.ErrorCategory = "success", ""
	}
	if err != nil {
		out.ErrorCategory = "incomplete_terminal"
	}
	c.Set(monitorImageResultKey, out)
}

func monitorImageResult(c *gin.Context) (channelMonitorParseResult, bool) {
	value, ok := c.Get(monitorImageResultKey)
	if !ok {
		return channelMonitorParseResult{}, false
	}
	result, ok := value.(channelMonitorParseResult)
	return result, ok
}
