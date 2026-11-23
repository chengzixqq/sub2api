package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func isThinkingCacheControlBlock(blockType string) bool {
	return blockType == "thinking" || blockType == "redacted_thinking"
}

func mayHaveThinkingCacheControl(raw []byte) bool {
	return scanCacheControl(raw, missingJSONRange()).thinking
}

// Inspect the original message arrays before mimicry moves or injects blocks.
func firstThinkingCacheControlPath(body, messagesRaw []byte) (msgIndex, contentIndex int, blockType string, ok bool) {
	search := messagesRaw
	if search == nil {
		search = body
	}
	if !mayHaveThinkingCacheControl(search) {
		return
	}
	return findThinkingCacheControlPath(body, messagesRaw)
}

func findThinkingCacheControlPath(body, messagesRaw []byte) (msgIndex, contentIndex int, blockType string, ok bool) {
	var messages gjson.Result
	if messagesRaw == nil {
		messages = gjson.Get(openAIWSPayloadStringView(body), "messages")
	} else {
		messages = gjson.Parse(openAIWSPayloadStringView(messagesRaw))
	}
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(i, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(j, block gjson.Result) bool {
			cc := block.Get("cache_control")
			if cc.Exists() && cc.Type != gjson.Null {
				kind := block.Get("type").String()
				if isThinkingCacheControlBlock(kind) {
					msgIndex, contentIndex, blockType, ok = int(i.Int()), int(j.Int()), strings.Clone(kind), true
					return false
				}
			}
			return true
		})
		return !ok
	})
	return
}

func requestThinkingCacheControlMode(c *gin.Context, account *Account, policy ClaudeCustomizationSettings) string {
	switch policy.ThinkingCacheControl {
	case ClaudeAlignPassthrough, ClaudeAlignReject:
		if IsAnthropicMessagesRequest(c) && isNativeCacheControlAccount(account) {
			return policy.ThinkingCacheControl
		}
	}
	return ClaudeAlignStrip
}

func rejectableThinkingCacheControlPath(body, messagesRaw []byte, header http.Header) (int, int, string, bool) {
	i, j, kind, ok := firstThinkingCacheControlPath(body, messagesRaw)
	return i, j, kind, ok && len(claude.UnknownTopLevelRequestFields(body, anthropicBetaHeader(header))) == 0
}

func (r *AlignmentHitRecorder) checkCacheControls(c *gin.Context, account *Account, thinkingMode, limitMode string, parsed *ParsedRequest) error {
	if (r == nil && thinkingMode != ClaudeAlignReject && limitMode != ClaudeAlignReject) || !IsAnthropicMessagesRequest(c) || !isNativeCacheControlAccount(account) || opsUpstreamAttemptClockFromGin(c).hasDispatched() {
		return nil
	}
	body := parsed.Body.Bytes()
	candidates := scanCacheControl(body, parsed.messagesRange)
	if candidates.thinking && (r != nil || thinkingMode == ClaudeAlignReject) {
		i, j, kind, ok := findThinkingCacheControlPath(body, parsed.MessagesRaw())
		if ok && len(claude.UnknownTopLevelRequestFields(body, anthropicBetaHeader(c.Request.Header))) == 0 {
			r.Record(c, ClaudeFieldThinkingCacheControl, account.ID, thinkingMode)
			if thinkingMode == ClaudeAlignReject {
				return WriteLocalAnthropicInvalidRequest(c, fmt.Sprintf("messages.%d.content.%d.%s.cache_control: Extra inputs are not permitted", i, j, kind))
			}
		}
	}
	if (r != nil || limitMode == ClaudeAlignReject) && candidates.mayOverflow() {
		if n := parsedClientCacheControlOverflow(body, c.Request.Header); n > 0 {
			r.Record(c, ClaudeFieldCacheControlLimitMode, account.ID, limitMode)
			if limitMode == ClaudeAlignReject {
				return WriteLocalAnthropicInvalidRequest(c, fmt.Sprintf("A maximum of 4 blocks with cache_control may be provided. Found %d.", n))
			}
		}
	}
	return nil
}

func thinkingCacheControlQueueCondition(body []byte, header http.Header, _ []BetaPolicyRule) func(ClaudeCustomizationSettings) bool {
	if _, _, _, ok := rejectableThinkingCacheControlPath(body, nil, header); !ok {
		return nil
	}
	return func(policy ClaudeCustomizationSettings) bool { return policy.ThinkingCacheControl == ClaudeAlignReject }
}
