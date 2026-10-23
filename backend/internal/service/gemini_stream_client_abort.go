package service

import (
	"context"
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// geminiStreamAbortedWithBillableOutput 判断流式读取中断是否由客户端断开引起，且断开前
// 已观测到可计费的输出（token 用量或已回吐的图片）。满足时调用方以已收集的结果收尾并照常计费。
// usageMetadata 为累计值，按断开前收到的最后一份计费：读到结束分片后断开即完整用量；
// 中途断开时中间分片可能只含输入计数或不含用量，此时按已有计数计费或不计费。
func geminiStreamAbortedWithBillableOutput(c *gin.Context, err error, usage *ClaudeUsage) bool {
	return geminiStreamClientDisconnected(c, err) && (usage.hasObservedTokens() || observedGeminiImageOutputs(c) > 0)
}

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
