package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

type openAISSEDataAccumulator struct {
	lines []string
}

func (a *openAISSEDataAccumulator) AddLine(line string, fn func([]byte)) {
	if fn == nil {
		return
	}
	trimmedLine := strings.TrimRight(line, "\r\n")
	if data, ok := extractOpenAISSEDataLine(trimmedLine); ok {
		a.lines = append(a.lines, data)
		return
	}
	if strings.TrimSpace(trimmedLine) == "" {
		a.Flush(fn)
	}
}

func (a *openAISSEDataAccumulator) Flush(fn func([]byte)) {
	if fn == nil || len(a.lines) == 0 {
		return
	}
	emitOpenAISSEDataPayloads(a.lines, fn)
	clear(a.lines)
	a.lines = a.lines[:0]
}

func forEachOpenAISSEDataPayload(body string, fn func([]byte)) {
	visitOpenAISSEDataPayloads(body, false, fn)
}

// A truncated body cannot dispatch an unfinished frame at EOF.
func visitOpenAISSEDataPayloads(body string, truncated bool, fn func([]byte)) {
	if fn == nil || strings.TrimSpace(body) == "" {
		return
	}
	var acc openAISSEDataAccumulator
	completeLines := 0
	for len(body) > 0 {
		if truncated && body[0] == '\r' {
			// A standalone CR can dispatch the preceding data before the
			// cutoff. Defer it so a complete multiline frame still joins
			// exactly as it would on an ordinary read.
			completeLines = len(acc.lines)
		}
		line, rest, ended := strings.Cut(body, "\n")
		if truncated && !ended {
			break
		}
		acc.AddLine(line, fn)
		if len(acc.lines) == 0 {
			completeLines = 0
		}
		body = rest
	}
	if truncated {
		acc.lines = acc.lines[:completeLines]
	}
	acc.Flush(fn)
}

func forEachOpenAISSEFrame(body string, fn func(string, []byte)) {
	if fn == nil || strings.TrimSpace(body) == "" {
		return
	}
	var parser openAICompatSSEFrameParser
	emit := func(frame openAICompatSSEFrame, ok bool) {
		if !ok {
			return
		}
		emitData := func(value string) {
			value = strings.TrimSpace(value)
			if value == "" || value == "[DONE]" {
				return
			}
			data := []byte(value)
			fn(effectiveOpenAISSEEventType(data, frame.EventType), data)
		}
		if gjson.Valid(frame.Data) {
			emitData(frame.Data)
			return
		}
		for _, value := range strings.Split(frame.Data, "\n") {
			emitData(value)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		emit(parser.AddLine(strings.TrimRight(line, "\r")))
	}
	emit(parser.Finish())
}

func emitOpenAISSEDataPayloads(lines []string, fn func([]byte)) {
	if fn == nil || len(lines) == 0 {
		return
	}
	if len(lines) == 1 {
		emitOpenAISSEDataPayload(lines[0], fn)
		return
	}
	joined := strings.Join(lines, "\n")
	if gjson.Valid(joined) {
		emitOpenAISSEDataPayload(joined, fn)
		return
	}
	for _, line := range lines {
		emitOpenAISSEDataPayload(line, fn)
	}
}

func emitOpenAISSEDataPayload(data string, fn func([]byte)) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return
	}
	fn([]byte(data))
}
