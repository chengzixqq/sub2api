package handler

import (
	"bytes"
	"encoding/json"
	"strings"
)

const channelMonitorCaptureLimit = 256 << 10

// The observer retains at most one bounded JSON document/SSE event. Content is
// examined transiently; only numeric facts and bounded model identifiers escape.
type channelMonitorParser struct {
	protocol                                                     string
	stream                                                       bool
	buffer, event                                                []byte
	output, complete, failed, limited, malformed, pending        bool
	model                                                        string
	inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64
	choices                                                      map[int]bool
	frames                                                       int
}

type channelMonitorParseResult struct {
	Model, Outcome, ErrorCategory     string
	OutputSeen, TerminalSeen          bool
	InputTokens, OutputTokens         int64
	CacheReadTokens, CacheWriteTokens int64
}

func (p *channelMonitorParser) result(status int) channelMonitorParseResult {
	p.finish()
	outcome := "unknown"
	category := ""
	if status >= 400 {
		// A gateway-generated 4xx without an observed upstream attempt is a
		// client error; upstream 5xx remains a channel failure. The capture
		// layer keeps the raw upstream status in attempt facts for diagnostics.
		if status < 500 {
			outcome = "client_error"
			category = "client_http"
		} else {
			outcome = "channel_error"
			category = "upstream_http"
		}
	} else if p.failed {
		outcome = "channel_error"
		category = "stream_error"
	} else if p.output && (p.complete || !p.stream) {
		outcome = "success"
	} else if p.stream {
		outcome = "channel_error"
		category = "empty_stream"
	}
	return channelMonitorParseResult{Model: p.model, Outcome: outcome, ErrorCategory: category, OutputSeen: p.output,
		TerminalSeen: p.complete, InputTokens: p.inputTokens, OutputTokens: p.outputTokens,
		CacheReadTokens: p.cacheReadTokens, CacheWriteTokens: p.cacheWriteTokens}
}

func newChannelMonitorParser(protocol string, stream bool) *channelMonitorParser {
	return &channelMonitorParser{protocol: protocol, stream: stream}
}

func (p *channelMonitorParser) appendBounded(dst *[]byte, data []byte) bool {
	needed := len(*dst) + len(data)
	if needed > channelMonitorCaptureLimit {
		p.limited = true
		*dst = nil
		return false
	}
	if needed > cap(*dst) {
		next := min(channelMonitorCaptureLimit, max(4096, max(needed, 2*cap(*dst))))
		grown := make([]byte, len(*dst), next)
		copy(grown, *dst)
		*dst = grown
	}
	*dst = append(*dst, data...)
	return true
}

func (p *channelMonitorParser) feed(data []byte) {
	if p.limited {
		return
	}
	if !p.stream {
		p.appendBounded(&p.buffer, data)
		return
	}
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			p.appendBounded(&p.buffer, data)
			return
		}
		if !p.appendBounded(&p.buffer, data[:i]) {
			return
		}
		line := bytes.TrimSuffix(p.buffer, []byte{'\r'})
		if len(line) == 0 {
			if len(p.event) > 0 {
				p.parse(bytes.TrimSuffix(p.event, []byte{'\n'}))
			}
			p.event = p.event[:0]
		} else if bytes.HasPrefix(line, []byte("data:")) {
			value := line[5:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			if !p.appendBounded(&p.event, value) || !p.appendBounded(&p.event, []byte{'\n'}) {
				return
			}
		}
		p.buffer = p.buffer[:0]
		data = data[i+1:]
		if p.limited {
			return
		}
	}
}

func (p *channelMonitorParser) finish() {
	if !p.limited {
		if !p.stream && len(p.buffer) > 0 {
			p.parse(p.buffer)
		}
		if p.stream {
			// Servers commonly close an SSE response immediately after the final
			// data line, without sending the optional blank line. Parse that event
			// before classifying any residual bytes as malformed.
			if len(p.event) > 0 {
				p.parse(bytes.TrimSuffix(p.event, []byte{'\n'}))
				p.event = nil
			}
			if len(bytes.TrimSpace(p.buffer)) > 0 {
				p.malformed = true
			}
		}
	}
	if len(p.choices) > 0 {
		all := true
		for _, done := range p.choices {
			all = all && done
		}
		p.complete = p.complete && all
	}
	p.buffer = nil
	p.event = nil
}

type monitorBlock struct {
	Type        string         `json:"type"`
	Text        string         `json:"text"`
	Thinking    string         `json:"thinking"`
	Refusal     string         `json:"refusal"`
	Name        string         `json:"name"`
	PartialJSON string         `json:"partial_json"`
	Content     []monitorBlock `json:"content"`
}
type monitorUsage struct {
	Input        *int64 `json:"input_tokens"`
	Output       *int64 `json:"output_tokens"`
	CacheRead    *int64 `json:"cache_read_input_tokens"`
	CacheWrite   *int64 `json:"cache_creation_input_tokens"`
	Prompt       *int64 `json:"prompt_tokens"`
	Completion   *int64 `json:"completion_tokens"`
	InputDetails struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	PromptDetails struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}
type monitorChoiceMessage struct {
	Content      json.RawMessage   `json:"content"`
	Refusal      string            `json:"refusal"`
	ToolCalls    []json.RawMessage `json:"tool_calls"`
	FunctionCall json.RawMessage   `json:"function_call"`
}
type monitorFrame struct {
	Type         string          `json:"type"`
	Model        string          `json:"model"`
	ModelVersion string          `json:"modelVersion"`
	Status       string          `json:"status"`
	StopReason   *string         `json:"stop_reason"`
	Error        json.RawMessage `json:"error"`
	Content      []monitorBlock  `json:"content"`
	Output       []monitorBlock  `json:"output"`
	ContentBlock *monitorBlock   `json:"content_block"`
	Item         *monitorBlock   `json:"item"`
	Delta        json.RawMessage `json:"delta"`
	Message      *monitorFrame   `json:"message"`
	Response     *monitorFrame   `json:"response"`
	Usage        *monitorUsage   `json:"usage"`
	Choices      []struct {
		Index        int                  `json:"index"`
		Delta        monitorChoiceMessage `json:"delta"`
		Message      monitorChoiceMessage `json:"message"`
		Text         string               `json:"text"`
		FinishReason *string              `json:"finish_reason"`
	} `json:"choices"`
	Candidates []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finishReason"`
		Content      struct {
			Parts []struct {
				Text         string `json:"text"`
				FunctionCall *struct {
					Name string `json:"name"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *struct {
		Prompt   int64 `json:"promptTokenCount"`
		Output   int64 `json:"candidatesTokenCount"`
		Thinking int64 `json:"thoughtsTokenCount"`
		Cached   int64 `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

func (p *channelMonitorParser) parse(data []byte) {
	if p.frames++; p.frames > 100000 {
		p.limited = true
		return
	}
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("[DONE]")) {
		if p.protocol == "openai_chat" {
			p.complete = true
		}
		return
	}
	if len(data) > 0 && data[0] == '[' && p.protocol == "gemini" {
		var frames []json.RawMessage
		if json.Unmarshal(data, &frames) != nil {
			p.malformed = true
			return
		}
		for _, frame := range frames {
			p.parse(frame)
		}
		return
	}
	var f monitorFrame
	if json.Unmarshal(data, &f) != nil {
		p.malformed = true
		return
	}
	p.observeFrame(&f)
}

func (p *channelMonitorParser) observeFrame(f *monitorFrame) {
	if monitorJSONPresent(f.Error) || f.Type == "error" || f.Type == "response.failed" {
		p.failed = true
	}
	if model := monitorIdentifier(f.Model); model != "" {
		p.model = model
	}
	if model := monitorIdentifier(f.ModelVersion); model != "" {
		p.model = model
	}
	if f.Message != nil {
		p.observeFrame(f.Message)
	}
	if f.Response != nil {
		p.observeFrame(f.Response)
	}
	p.observeUsage(f.Usage)
	switch p.protocol {
	case "anthropic":
		p.output = p.output || monitorBlocksOutput(f.Content)
		if f.ContentBlock != nil {
			p.output = p.output || monitorBlockOutput(*f.ContentBlock)
		}
		var delta monitorBlock
		if len(f.Delta) > 0 && json.Unmarshal(f.Delta, &delta) == nil {
			p.output = p.output || monitorBlockOutput(delta)
		}
		if f.Type == "message_stop" || (!p.stream && f.Type == "message" && f.StopReason != nil && *f.StopReason != "") {
			p.complete = true
		}
	case "openai_chat":
		for _, choice := range f.Choices {
			p.output = p.output || choice.Text != "" || monitorChoiceOutput(choice.Message) || monitorChoiceOutput(choice.Delta)
			p.choice(choice.Index, choice.FinishReason != nil && *choice.FinishReason != "")
		}
		if len(f.Choices) > 0 {
			p.complete = p.allChoicesComplete()
		}
	case "openai_responses":
		p.output = p.output || monitorBlocksOutput(f.Output)
		if f.Item != nil {
			p.output = p.output || monitorBlockOutput(*f.Item)
		}
		switch f.Type {
		case "response.output_text.delta", "response.refusal.delta", "response.function_call_arguments.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			var delta string
			if json.Unmarshal(f.Delta, &delta) == nil && delta != "" {
				p.output = true
			}
		case "response.completed":
			p.complete = true
		}
		if f.Status == "completed" {
			p.complete = true
		}
		if f.Status == "failed" {
			p.failed = true
		}
		if f.Status == "queued" || f.Status == "in_progress" {
			p.pending = true
		}
		if f.Status == "incomplete" || f.Type == "response.incomplete" {
			p.complete = false
		}
	case "gemini":
		if f.PromptFeedback.BlockReason != "" {
			p.output = true
			p.complete = true
		}
		for _, candidate := range f.Candidates {
			for _, part := range candidate.Content.Parts {
				p.output = p.output || part.Text != "" || (part.FunctionCall != nil && part.FunctionCall.Name != "")
			}
			if candidate.FinishReason == "SAFETY" || candidate.FinishReason == "RECITATION" || candidate.FinishReason == "BLOCKLIST" || candidate.FinishReason == "PROHIBITED_CONTENT" {
				p.output = true
			}
			p.choice(candidate.Index, candidate.FinishReason != "")
		}
		if len(f.Candidates) > 0 {
			p.complete = p.allChoicesComplete()
		}
		if u := f.UsageMetadata; u != nil {
			p.inputTokens = max(0, u.Prompt-u.Cached)
			p.cacheReadTokens = max(0, u.Cached)
			p.outputTokens = max(0, u.Output) + max(0, u.Thinking)
		}
	}
}

func (p *channelMonitorParser) choice(index int, done bool) {
	if index < 0 || index >= 256 {
		p.limited = true
		return
	}
	if p.choices == nil {
		p.choices = make(map[int]bool)
	}
	p.choices[index] = p.choices[index] || done
}
func (p *channelMonitorParser) allChoicesComplete() bool {
	if len(p.choices) == 0 {
		return false
	}
	for _, complete := range p.choices {
		if !complete {
			return false
		}
	}
	return true
}
func (p *channelMonitorParser) observeUsage(u *monitorUsage) {
	if u == nil {
		return
	}
	assign := func(dst *int64, value *int64) {
		if value != nil {
			*dst = max(0, *value)
		}
	}
	assign(&p.inputTokens, u.Input)
	assign(&p.inputTokens, u.Prompt)
	assign(&p.outputTokens, u.Output)
	assign(&p.outputTokens, u.Completion)
	assign(&p.cacheReadTokens, u.CacheRead)
	assign(&p.cacheWriteTokens, u.CacheWrite)
	if p.protocol == "openai_chat" || p.protocol == "openai_responses" {
		assign(&p.cacheReadTokens, u.InputDetails.Cached)
		assign(&p.cacheReadTokens, u.PromptDetails.Cached)
		if u.Input != nil || u.Prompt != nil {
			p.inputTokens = max(0, p.inputTokens-p.cacheReadTokens)
		}
	}
}
func monitorChoiceOutput(m monitorChoiceMessage) bool {
	if m.Refusal != "" || len(m.ToolCalls) > 0 || monitorJSONPresent(m.FunctionCall) {
		return true
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return text != ""
	}
	var blocks []monitorBlock
	return json.Unmarshal(m.Content, &blocks) == nil && monitorBlocksOutput(blocks)
}
func monitorBlocksOutput(blocks []monitorBlock) bool {
	for _, block := range blocks {
		if monitorBlockOutput(block) {
			return true
		}
	}
	return false
}
func monitorBlockOutput(b monitorBlock) bool {
	if b.Text != "" || b.Thinking != "" || b.Refusal != "" || b.PartialJSON != "" {
		return true
	}
	switch b.Type {
	case "tool_use", "server_tool_use", "function_call", "computer_call", "web_search_call", "file_search_call":
		return true
	}
	return monitorBlocksOutput(b.Content)
}
func monitorJSONPresent(data []byte) bool {
	return len(data) > 0 && !bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}
func monitorIdentifier(value string) string {
	if len(value) == 0 || len(value) > 128 || strings.Contains(value, "://") {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-:/", c)) {
			return ""
		}
	}
	return value
}
