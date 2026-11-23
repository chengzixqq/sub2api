package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/render"
	"github.com/tidwall/gjson"
)

// Buffered protocol conversions use Gin's normal renderer. Inspect its single
// accepted JSON write only when the upstream did not supply usable counters.
func writeOpenAIConvertedJSON(c *gin.Context, usage *OpenAIUsage, body any) bool {
	if !openAIUsageNeedsDelivery(usage) {
		c.JSON(http.StatusOK, body)
		return false
	}
	w := openAIBufferedDeliveryWriter{ResponseWriter: c.Writer}
	c.Status(http.StatusOK)
	if err := render.WriteJSON(&w, body); err != nil {
		_ = c.Error(err)
		c.Abort()
	}
	return w.delivered
}

type openAIBufferedDeliveryWriter struct {
	gin.ResponseWriter
	delivered bool
}

func (w *openAIBufferedDeliveryWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	w.delivered = w.delivered || (n == len(body) && w.Status() < 300 && openAIClientPayloadHasContent(body))
	return n, err
}

// NeedsContentEstimate gates successful delivery accounting on the upstream
// counters before the handler reads any billing policy or request body.
func (r *OpenAIForwardResult) NeedsContentEstimate() bool {
	return r.ClientContentDelivered && r.ImageCount <= 0 && openAIUsageNeedsDelivery(&r.Usage)
}

func openAIUsageNeedsDelivery(u *OpenAIUsage) bool {
	return u == nil || u.RetainedAfterZero || (u.ReportedOutputFloor <= 0 && !openAIUsageHasTokens(u) && u.CacheCreation5mTokens <= 0 &&
		u.CacheCreation30mInputTokens <= 0 && u.CacheCreation12hInputTokens <= 0 &&
		u.CacheCreation1hTokens <= 0 && !u.HasAdvisorUsage() && !u.HasCompactionUsage())
}

func writeOpenAIResponseData(c *gin.Context, status int, contentType string, body []byte, usage *OpenAIUsage) bool {
	before := max(c.Writer.Size(), 0)
	c.Data(status, contentType, body)
	return bufferedOpenAIContentDelivered(c, usage, body, c.Writer.Size()-before)
}

func bufferedOpenAIContentDelivered(c *gin.Context, usage *OpenAIUsage, body []byte, written int) bool {
	if !openAIUsageNeedsDelivery(usage) || c.Writer.Status() >= 300 || written != len(body) {
		return false
	}
	return openAIClientPayloadHasContent(body)
}

func openAIClientPayloadHasContent(body []byte) bool {
	payload := openAIWSPayloadStringView(body)
	if strings.HasPrefix(strings.TrimSpace(payload), "{") {
		return openAIResponseBodyHasContent(payload)
	}
	for line := range strings.SplitSeq(payload, "\n") {
		if data, ok := extractOpenAISSEDataLine(line); ok && openAIClientEventHasContent(data) {
			return true
		}
	}
	return false
}

func anthropicEventHasContent(event apicompat.AnthropicStreamEvent) bool {
	if d := event.Delta; event.Type == "content_block_delta" && d != nil {
		return d.Text != "" || d.Thinking != "" || d.Signature != "" || d.PartialJSON != ""
	}
	if b := event.ContentBlock; event.Type == "content_block_start" && b != nil {
		return b.Text != "" || b.Thinking != "" || b.Signature != "" || b.Data != "" || b.Name != ""
	}
	return false
}

func chatChunkHasContent(chunk apicompat.ChatCompletionsChunk) bool {
	for _, choice := range chunk.Choices {
		d := choice.Delta
		for _, field := range [...]*string{d.Content, d.ReasoningContent, d.Reasoning, d.Refusal} {
			if field != nil && *field != "" {
				return true
			}
		}
		for _, call := range d.ToolCalls {
			if call.Function.Name != "" || call.Function.Arguments != "" {
				return true
			}
		}
	}
	return false
}

func openAIClientEventHasContent(payload string) bool {
	eventType := gjson.Get(payload, "type").String()
	switch eventType {
	case "":
		_, client := openAIChatStreamContent(payload)
		return client
	case "content_block_delta":
		if !gjson.Valid(payload) {
			return false
		}
		delta := gjson.Get(payload, "delta")
		return delta.Get("text").String() != "" || delta.Get("thinking").String() != "" ||
			delta.Get("partial_json").String() != "" || delta.Get("signature").String() != ""
	case "content_block_start":
		return gjson.Valid(payload) && openAIMessageBlockHasContent(rawJSONView(strings.TrimSpace(payload)).field("content_block"))
	default:
		_, client := openAIResponsesStreamContent(payload, eventType)
		return client || openAIFinalEventHasContent(payload, eventType)
	}
}

// The relay supplies the event type it already decoded. Use borrowed JSON
// for deltas so escaped or large strings need no decoding or copying.
func openAIWSFrameHasContent(eventType string, message []byte) bool {
	data := openAIWSPayloadStringView(message)
	return gjson.Valid(data) && openAIWSValidatedFrameHasContent(eventType, data)
}

func openAIWSValidatedFrameHasContent(eventType, data string) bool {
	body := rawJSONView(strings.TrimSpace(data))
	if strings.Contains(eventType, ".delta") {
		return body.field("delta").hasString()
	}
	switch eventType {
	case "response.output_item.done":
		return openAIOutputItemHasContent(body.field("item"))
	case "response.done", "response.completed":
		return openAIResponseObjectHasContent(body.field("response"))
	}
	_, client := openAIResponsesStreamContent(data, eventType)
	return client
}

func openAIResponseBodyHasContent(payload string) bool {
	return gjson.Valid(payload) && openAIResponseObjectHasContent(rawJSONView(strings.TrimSpace(payload)))
}

func openAIResponseObjectHasContent(body rawJSONView) bool {
	if err := body.field("error"); err != "" && err != "null" {
		return false
	}
	return body.field("choices").any(func(choice rawJSONView) bool {
		return openAIClientChatMessageHasContent(choice.field("message"))
	}) || body.field("output").any(openAIOutputItemHasContent) ||
		body.field("content").any(openAIMessageBlockHasContent)
}

// Some upstreams send content only in the finished item or final response.
func openAIFinalEventHasContent(data, eventType string) bool {
	switch eventType {
	case "response.output_item.done":
		return gjson.Valid(data) && openAIOutputItemHasContent(rawJSONView(strings.TrimSpace(data)).field("item"))
	case "response.completed", "response.done":
		return gjson.Valid(data) && openAIResponseObjectHasContent(rawJSONView(strings.TrimSpace(data)).field("response"))
	default:
		return false
	}
}

func openAIOutputItemHasContent(item rawJSONView) bool {
	if item.field("arguments").hasValue() || item.field("input").hasValue() || item.field("result").hasValue() {
		return true
	}
	var scratch [32]byte
	switch item.field("type").identifier(&scratch) {
	case "reasoning", "compaction":
		if encrypted := item.field("encrypted_content"); encrypted.hasString() {
			return true
		}
	case "function_call", "custom_tool_call":
		if item.field("name").hasValue() {
			return true
		}
	}
	if item.field("b64_json").hasValue() {
		return true
	}
	return item.field("content").any(func(part rawJSONView) bool {
		return openAIResponsePartHasText(part) || openAIMessageBlockHasContent(part)
	}) || item.field("summary").any(openAIResponsePartHasText)
}

func openAIResponsePartHasText(part rawJSONView) bool {
	return part.field("text").hasValue() || part.field("transcript").hasValue()
}

func openAIClientChatMessageHasContent(message rawJSONView) bool {
	content := message.field("content")
	if content.hasString() ||
		message.field("reasoning_content").hasValue() || message.field("reasoning").hasValue() ||
		message.field("refusal").hasValue() || message.field("audio.data").hasValue() || message.field("audio.transcript").hasString() ||
		openAICompletionFunctionHasContent(message.field("function_call")) {
		return true
	}
	return message.field("tool_calls").any(func(call rawJSONView) bool {
		return openAICompletionFunctionHasContent(call.field("function")) || call.field("custom.input").hasValue()
	}) || content.any(openAIMessageBlockHasContent)
}

func openAICompletionFunctionHasContent(call rawJSONView) bool {
	return call.field("name").hasValue() || call.field("arguments").hasValue()
}

func openAIMessageBlockHasContent(part rawJSONView) bool {
	var scratch [32]byte
	switch part.field("type").identifier(&scratch) {
	case "text", "output_text":
		return part.field("text").hasValue()
	case "refusal":
		return part.field("refusal").hasValue()
	case "thinking":
		return part.field("thinking").hasValue() || part.field("signature").hasValue()
	case "image":
		return part.field("source.data").hasValue() || part.field("source.url").hasValue()
	case "image_url":
		return part.field("image_url.url").hasValue()
	case "file":
		return part.field("file.file_data").hasValue() || part.field("file.file_id").hasValue()
	case "redacted_thinking":
		return part.field("data").hasValue()
	case "tool_use", "server_tool_use":
		input := part.field("input")
		return part.field("name").hasValue() || (len(input) > 0 && input[0] == '{' && jsonObjectHasMembers(string(input)))
	}
	return false
}
