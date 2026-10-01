package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// anthropicSSEGuard contains the protocol checks that are safe for the
// official Anthropic endpoint. Third-party Anthropic-compatible endpoints are
// deliberately excluded because they commonly omit event names or emit
// provider-specific event types.
type anthropicSSEGuard struct {
	strict          bool
	frameBytes      int
	stopped         bool
	sawMessageStart bool
}

// anthropicSSEFrameBuffer holds a complete SSE event for strict upstreams.
// The gateway must not write the first line of a frame before the remaining
// lines have passed validation; otherwise a later invalid line can leave the
// client with a half-written event.
type anthropicSSEFrameBuffer struct {
	lines []string
}

func (b *anthropicSSEFrameBuffer) push(line string) ([]string, bool) {
	if b == nil {
		return []string{line}, true
	}
	b.lines = append(b.lines, line)
	if line != "" {
		return nil, false
	}
	frame := append([]string(nil), b.lines...)
	b.lines = b.lines[:0]
	return frame, true
}

func newAnthropicSSEGuard(strict bool) *anthropicSSEGuard {
	return &anthropicSSEGuard{strict: strict}
}

// observeLine checks a raw SSE line before the handler forwards it. It keeps
// the existing event parsing and usage extraction in the caller; this guard
// only enforces frame boundaries and the terminal event invariant.
func (g *anthropicSSEGuard) observeLine(line string, maxFrameBytes int) error {
	if g == nil || !g.strict {
		return nil
	}
	g.frameBytes += len(line) + 1
	if maxFrameBytes > 0 && g.frameBytes > maxFrameBytes {
		return fmt.Errorf("anthropic SSE event exceeds %d bytes", maxFrameBytes)
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		g.frameBytes = 0
		return nil
	}
	if g.stopped {
		return fmt.Errorf("anthropic SSE emitted data after message_stop")
	}
	if strings.HasPrefix(trimmed, "data:") {
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "" || data == "[DONE]" {
			return nil
		}
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			// Existing behavior forwards non-JSON data for compatibility. The
			// official endpoint may still send an extension event, so only use
			// the guard for state transitions when JSON is valid.
			return nil
		}
		switch event.Type {
		case "message_start":
			g.sawMessageStart = true
		case "message_stop":
			if !g.sawMessageStart {
				return fmt.Errorf("anthropic SSE message_stop before message_start")
			}
			g.stopped = true
		}
	}
	return nil
}

func isNativeAnthropicResponse(account *Account, resp *http.Response) bool {
	if account == nil || account.Platform != PlatformAnthropic || resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(resp.Request.URL.Hostname(), "."))
	return host == "api.anthropic.com"
}
