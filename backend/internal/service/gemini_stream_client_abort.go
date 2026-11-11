package service

import (
	"context"
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// geminiStreamClientDisconnected keeps cancellation separate from whether usage
// has arrived. The settlement guard owns both explicit-usage and estimate policy.
func geminiStreamClientDisconnected(c *gin.Context, err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return false
	}
	return errors.Is(err, context.Canceled) ||
		(c != nil && c.Request != nil && c.Request.Context().Err() != nil)
}

// geminiStreamHasContent excludes usage-only, finish and keepalive metadata from
// the native real-output signal used by failure settlement estimates.
func geminiStreamHasContent(raw []byte) bool {
	return geminiStreamHasOutput(raw, true)
}

// The Messages and Chat Completions converters discard images, so receiving an
// image alone must not count as content delivered to their clients.
func geminiCompatStreamHasContent(raw []byte) bool {
	return geminiStreamHasOutput(raw, false)
}

func geminiStreamHasOutput(raw []byte, includeImages bool) bool {
	for _, candidate := range gjson.GetBytes(raw, "candidates").Array() {
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("text").String() != "" || part.Get("functionCall").IsObject() ||
				(includeImages && (part.Get("inlineData.data").String() != "" || part.Get("fileData.fileUri").String() != "")) {
				return true
			}
		}
	}
	return false
}
