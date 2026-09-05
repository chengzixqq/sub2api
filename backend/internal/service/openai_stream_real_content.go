package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// 本文件集中定义「这一帧是否携带上游真实推理内容」的白名单判定，供 OpenAI/Grok 家族
// 各流式叶子在置位 GatewayUpstreamDeliveredKey 时统一门控。
//
// 为什么必须是白名单而不是黑名单：该标记决定失败/中断请求是否计费，误判方向是「多算」
// （上游一个 token 都没产出、只回了错误帧，却按 OutputStarted 分支全额估算 prompt 入账）。
// 黑名单写法每漏一个新错误事件类型就是一个多算缺口；白名单漏掉内容帧只会少算，可接受。
//
// 为什么是三个函数而不是一个：这三类叶子的上游 payload 结构根本不同，硬套同一个判定
// 会互相污染语义 ——
//   - Responses 事件流：语义全在 type 事件名上（response.output_text.delta 等）。
//   - 原生 ChatCompletions chunk：没有事件名，语义在 choices[].delta 的字段上。
//   - 图片流：语义在是否真的带出图片字节（partial_image / b64_json / result / url）上。
//
// 判定粒度对齐本仓库既有最严谨的样板 isOpenAIWSTokenEvent（openai_ws_forwarder_support.go），
// 该样板护住的 3 个 WS 叶子因此天然没有本类缺陷。

// openAIResponsesStreamEventDeliversRealContent 判定一个 Responses 流式事件类型是否携带
// 上游真实推理内容。用于以 Responses 事件为上游形状的叶子（chat_completions / messages /
// responses 直转与 passthrough）。
func openAIResponsesStreamEventDeliversRealContent(payload, eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return false
	}
	switch eventType {
	// 前导帧：上游只确认接单，尚未产出任何内容。
	case "response.created", "response.in_progress", "response.queued":
		return false
	// 终止帧：failed / incomplete / cancelled 代表零产出或中途终止，一律不算投递；
	// completed / done 自身不携带增量内容，真实内容必然已由此前的 delta 帧投递过，
	// 落在这里说明上游从未产出可识别的内容帧。
	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled",
		"response.completed", "response.done":
		return false
	// 裸 error 帧：type:"error"，不是 response.failed 事件类型，黑名单写法正是漏在这里。
	case "error":
		return false
	// 结构性骨架帧：只声明 output item / content part 的边界，不含内容。
	case "response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done":
		return false
	}
	// 增量内容帧：output_text / reasoning_summary_text / function_call_arguments /
	// audio / refusal 等一切 .delta。refusal 也算 —— 上游确实生成了 token 并收费。
	if strings.Contains(eventType, ".delta") {
		if !gjson.Valid(payload) {
			return false
		}
		delta := gjson.Get(payload, "delta")
		return delta.Exists() && delta.String() != ""
	}
	// 图片增量帧：携带真实图片字节，不含 .delta 后缀。
	if strings.HasSuffix(eventType, ".partial_image") {
		if !gjson.Valid(payload) {
			return false
		}
		return gjson.Get(payload, "partial_image_b64").String() != "" ||
			gjson.Get(payload, "partial_image").String() != ""
	}
	if strings.HasPrefix(eventType, "response.output_text") {
		return gjson.Valid(payload) && gjson.Get(payload, "text").String() != ""
	}
	if strings.HasPrefix(eventType, "response.output") {
		if !gjson.Valid(payload) {
			return false
		}
		return gjson.Get(payload, "text").String() != "" ||
			gjson.Get(payload, "arguments").String() != "" ||
			gjson.Get(payload, "input").String() != "" ||
			gjson.Get(payload, "result").String() != "" ||
			gjson.Get(payload, "b64_json").String() != "" ||
			gjson.Get(payload, "url").String() != ""
	}
	return false
}

// openAIChatStreamChunkDeliversRealContent 判定一个原生 ChatCompletions 流式 chunk 是否
// 携带上游真实增量内容。用于 raw CC 直转叶子——该形状没有事件名，只能看 choices[].delta。
func openAIChatStreamChunkDeliversRealContent(payload string) bool {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" || trimmed == "[DONE]" {
		return false
	}
	if !gjson.Valid(trimmed) {
		return false
	}
	// 错误帧：CC 形状下错误以顶层 error 对象或 type:"error" 出现，均不含推理内容。
	if gjson.Get(trimmed, "error").Exists() {
		return false
	}
	if strings.TrimSpace(gjson.Get(trimmed, "type").String()) == "error" {
		return false
	}
	choices := gjson.Get(trimmed, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return false
	}
	delivers := false
	choices.ForEach(func(_, choice gjson.Result) bool {
		if openAIChatStreamDeltaDeliversRealContent(choice.Get("delta")) {
			delivers = true
			return false
		}
		return true
	})
	return delivers
}

// openAIChatStreamDeltaDeliversRealContent 判定单个 choices[].delta 是否带出真实内容。
// 只有 role / finish_reason 的骨架 chunk 不算 —— 上游此时尚未产出任何 token。
func openAIChatStreamDeltaDeliversRealContent(delta gjson.Result) bool {
	if !delta.Exists() || !delta.IsObject() {
		return false
	}
	for _, field := range []string{"content", "reasoning_content", "reasoning", "refusal"} {
		if value := delta.Get(field); value.Exists() && value.String() != "" {
			return true
		}
	}
	if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() &&
		len(toolCalls.Array()) > 0 {
		return true
	}
	return delta.Get("function_call").Exists()
}

// openAIImagesStreamDataDeliversRealContent 判定一个图片流 SSE data 帧是否带出真实图片内容。
// 图片叶子失败时 ImageCount=0、只按估算 prompt 计费，危害低于文本叶子，但同属多算方向。
//
// 复用 openAIImageOutputCounter 而不是另写一套字段探测：计数器已覆盖 response.output_item.done /
// response.completed / image_generation.completed / data[] 等全部图片承载形状，两处口径必须一致，
// 否则会出现「标记说已投递、计费说 ImageCount=0」的自相矛盾。
func openAIImagesStreamDataDeliversRealContent(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "[DONE]" || !gjson.Valid(trimmed) {
		return false
	}
	root := gjson.Parse(trimmed)
	// 错误帧与终止/前导帧：不含图片内容。裸 error 与 response.incomplete 是黑名单的漏点。
	switch strings.TrimSpace(root.Get("type").String()) {
	case "error", "response.failed", "response.incomplete",
		"response.cancelled", "response.canceled",
		"response.created", "response.in_progress", "response.queued":
		return false
	}
	if root.Get("error").Exists() {
		return false
	}
	// 增量图片帧：计数器有意跳过 partial_image（避免重复计数），但它确实是真实内容投递。
	if strings.TrimSpace(root.Get("partial_image_b64").String()) != "" {
		return true
	}
	if strings.TrimSpace(root.Get("b64_json").String()) != "" &&
		strings.HasSuffix(strings.TrimSpace(root.Get("type").String()), ".partial_image") {
		return true
	}
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEData(data)
	return counter.Count() > 0
}
