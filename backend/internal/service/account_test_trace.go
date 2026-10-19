package service

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// accountTestTraceContextKey stores the per-test timing / status / usage trace
// on the gin context, so the handler (test_result) and background runners can
// read what the SSE probe observed without changing TestEvent.
const accountTestTraceContextKey = "account_test_trace"

// accountTestTraceMaxTextBytes caps the captured response text.
const accountTestTraceMaxTextBytes = 8 << 10

// accountTestUpstreamStatusPattern recovers an upstream HTTP status from the
// free-form error texts of the probe paths ("API returned 401", "返回 429",
// "(HTTP 403)", "(500)") when no structured status was recorded.
var accountTestUpstreamStatusPattern = regexp.MustCompile(`(?:returned|返回|HTTP)\s*(\d{3})\b|\((\d{3})\)`)

type accountTestTrace struct {
	mu             sync.Mutex
	startedAt      time.Time
	firstOutputAt  time.Time
	finishedAt     time.Time
	upstreamStatus int
	usage          *AccountTestUsage
	model          string
	responseText   strings.Builder
	errorMessage   string
}

// AccountTestUsage is the token usage reported by the upstream during a test.
type AccountTestUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// AccountTestTraceSnapshot is a copy of what one TestAccountConnection run observed.
type AccountTestTraceSnapshot struct {
	StartedAt      time.Time
	FirstOutputAt  *time.Time
	FinishedAt     *time.Time
	UpstreamStatus *int
	Usage          *AccountTestUsage
	Model          string
	ResponseText   string
	ErrorMessage   string
}

func beginAccountTestTrace(c *gin.Context) {
	if c == nil {
		return
	}
	if _, ok := accountTestTraceOf(c); ok {
		return
	}
	c.Set(accountTestTraceContextKey, &accountTestTrace{startedAt: time.Now()})
}

func accountTestTraceOf(c *gin.Context) (*accountTestTrace, bool) {
	if c == nil {
		return nil, false
	}
	raw, ok := c.Get(accountTestTraceContextKey)
	if !ok {
		return nil, false
	}
	trace, ok := raw.(*accountTestTrace)
	return trace, ok && trace != nil
}

func withAccountTestTrace(c *gin.Context, fn func(*accountTestTrace)) {
	trace, ok := accountTestTraceOf(c)
	if !ok {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	fn(trace)
}

func markAccountTestTraceFinished(c *gin.Context) {
	withAccountTestTrace(c, func(t *accountTestTrace) { t.finishedAt = time.Now() })
}

// noteAccountTestUpstreamStatus records the status of the latest upstream
// response; the last call wins.
func noteAccountTestUpstreamStatus(c *gin.Context, code int) {
	if code < 100 || code > 599 {
		return
	}
	withAccountTestTrace(c, func(t *accountTestTrace) { t.upstreamStatus = code })
}

// addAccountTestUsage accumulates usage; adaptive tests probe several endpoints.
func addAccountTestUsage(c *gin.Context, input, output int64) {
	withAccountTestTrace(c, func(t *accountTestTrace) {
		if t.usage == nil {
			t.usage = &AccountTestUsage{}
		}
		t.usage.InputTokens += input
		t.usage.OutputTokens += output
	})
}

// observeAccountTestEvent is called for every SSE event the probe writes.
func observeAccountTestEvent(c *gin.Context, event TestEvent) {
	withAccountTestTrace(c, func(t *accountTestTrace) {
		switch event.Type {
		case "test_start":
			if t.model == "" {
				t.model = event.Model
			}
		case "content", "image", "audio", "video":
			if t.firstOutputAt.IsZero() {
				t.firstOutputAt = time.Now()
			}
			if event.Type == "content" && t.responseText.Len() < accountTestTraceMaxTextBytes {
				t.responseText.WriteString(event.Text)
			}
		case "error":
			t.errorMessage = event.Error
			if t.upstreamStatus == 0 {
				if code := accountTestUpstreamStatus(0, event.Error); code != nil {
					t.upstreamStatus = *code
				}
			}
		}
	})
}

// AccountTestTraceFrom returns the trace recorded on c by TestAccountConnection.
func AccountTestTraceFrom(c *gin.Context) (AccountTestTraceSnapshot, bool) {
	var out AccountTestTraceSnapshot
	trace, ok := accountTestTraceOf(c)
	if !ok {
		return out, false
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	out.StartedAt = trace.startedAt
	out.FirstOutputAt = nonZeroTimePtr(trace.firstOutputAt)
	out.FinishedAt = nonZeroTimePtr(trace.finishedAt)
	if trace.upstreamStatus > 0 {
		code := trace.upstreamStatus
		out.UpstreamStatus = &code
	}
	if trace.usage != nil {
		usage := *trace.usage
		out.Usage = &usage
	}
	out.Model = trace.model
	out.ResponseText = truncateString(trace.responseText.String(), accountTestTraceMaxTextBytes)
	out.ErrorMessage = trace.errorMessage
	return out, true
}

// accountTestUpstreamStatus prefers a structured status and otherwise parses
// the error text; only 100–599 is accepted.
func accountTestUpstreamStatus(structured int, message string) *int {
	if structured >= 100 && structured <= 599 {
		return &structured
	}
	match := accountTestUpstreamStatusPattern.FindStringSubmatch(message)
	if match == nil {
		return nil
	}
	raw := match[1]
	if raw == "" {
		raw = match[2]
	}
	code, err := strconv.Atoi(raw)
	if err != nil || code < 100 || code > 599 {
		return nil
	}
	return &code
}

func nonZeroTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// accountTestStreamUsage keeps the latest usage numbers seen in one stream and
// adds them to the trace once when the stream processor returns.
type accountTestStreamUsage struct {
	input, output int64
	seen          bool
}

func (u *accountTestStreamUsage) set(input, output int64, hasInput, hasOutput bool) {
	if hasInput {
		u.input = input
		u.seen = true
	}
	if hasOutput {
		u.output = output
		u.seen = true
	}
}

func (u *accountTestStreamUsage) commit(c *gin.Context) {
	if u.seen {
		addAccountTestUsage(c, u.input, u.output)
	}
}

// observeAnthropic reads message_start.message.usage and
// message_delta.usage from an Anthropic Messages stream event.
func (u *accountTestStreamUsage) observeAnthropic(data map[string]any) {
	usage, _ := data["usage"].(map[string]any)
	if message, ok := data["message"].(map[string]any); ok && usage == nil {
		usage, _ = message["usage"].(map[string]any)
	}
	u.observeFields(usage, "input_tokens", "output_tokens")
}

func (u *accountTestStreamUsage) observeFields(usage map[string]any, inputKey, outputKey string) {
	if usage == nil {
		return
	}
	input, hasInput := accountTestJSONInt(usage[inputKey])
	output, hasOutput := accountTestJSONInt(usage[outputKey])
	// Anthropic repeats input_tokens=0 in message_delta; keep the real count.
	if hasInput && input == 0 && u.input > 0 {
		hasInput = false
	}
	u.set(input, output, hasInput, hasOutput)
}

func accountTestJSONInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return 0, false
		}
		return int64(n), true
	case int64:
		return n, n >= 0
	case int:
		return int64(n), n >= 0
	}
	return 0, false
}
