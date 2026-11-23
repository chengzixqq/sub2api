//go:build unit

package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicConverterOutputCacheFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name           string
		chatUsage      string
		responsesUsage string
		creation       int64
		read           int64
	}{
		{"missing usage", "", "", 0, 0},
		{"null usage", `,"usage":null`, `,"usage":null`, 0, 0},
		{"empty usage", `,"usage":{}`, `,"usage":{}`, 0, 0},
		{"read only", `,"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3}}`, `,"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3}}`, 0, 3},
		{"creation only", `,"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cache_write_tokens":4}}`, `,"usage":{"input_tokens":10,"cache_creation_input_tokens":4}`, 4, 0},
		{"zero write overrides creation alias", `,"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":0,"cache_creation_tokens":4}}`, `,"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":0,"cache_creation_tokens":4}}`, 0, 3},
		{"null Chat write overrides creation alias", `,"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":null,"cache_creation_tokens":4}}`, `,"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":0,"cache_creation_tokens":4}}`, 0, 3},
		{"negative write overrides creation alias", `,"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":-2,"cache_creation_tokens":4}}`, `,"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":-2,"cache_creation_tokens":4}}`, 0, 3},
		{"positive write overrides creation alias", `,"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":2,"cache_creation_tokens":4}}`, `,"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":2,"cache_creation_tokens":4}}`, 2, 3},
	} {
		for _, route := range []string{"chat buffered", "chat terminal", "chat eof", "responses buffered", "responses terminal", "responses eof before output", "responses eof after output"} {
			responsesEOF := strings.HasPrefix(route, "responses eof")
			beforeOutput := route == "responses eof before output"
			if responsesEOF && test.name != "missing usage" {
				continue
			}
			t.Run(test.name+"/"+route, func(t *testing.T) {
				payload := `{"id":"chat_cache","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]` + test.chatUsage + `}`
				if strings.HasPrefix(route, "responses") {
					payload = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_cache\",\"model\":\"gpt-5.4\"}}\n\n"
					if beforeOutput {
						payload = ""
					} else if responsesEOF {
						payload += `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n"
					} else {
						payload += `data: {"type":"response.completed","response":{"id":"resp_cache","status":"completed","output":[]` + test.responsesUsage + `}}` + "\n\n"
					}
				} else if !strings.HasSuffix(route, "buffered") {
					payload = `data: {"id":"chat_cache","choices":[{"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]` + test.chatUsage + `}` + "\n\n"
					if strings.HasSuffix(route, "terminal") {
						payload += "data: [DONE]\n\n"
					}
				}
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
				service := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				var result *OpenAIForwardResult
				var err error
				switch route {
				case "chat buffered":
					result, err = service.bufferChatCompletionsAsAnthropic(ctx, response, "gpt-5.4", "gpt-5.4", "gpt-5.4", nil, nil, time.Now())
				case "chat terminal", "chat eof":
					result, err = service.streamChatCompletionsAsAnthropic(ctx, response, "gpt-5.4", "gpt-5.4", "gpt-5.4", nil, nil, time.Now())
				case "responses buffered":
					result, err = service.handleAnthropicBufferedStreamingResponse(response, ctx, rawChatCompletionsTestAccount(), "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
				default:
					result, err = service.handleAnthropicStreamingResponse(response, ctx, rawChatCompletionsTestAccount(), "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
				}
				var failoverErr *UpstreamFailoverError
				if beforeOutput {
					require.ErrorAs(t, err, &failoverErr)
					require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
					require.Empty(t, recorder.Body.String())
					require.False(t, ctx.Writer.Written())
				} else if responsesEOF {
					require.ErrorContains(t, err, "missing terminal event")
					require.False(t, errors.As(err, &failoverErr))
					require.True(t, ctx.Writer.Written())
				} else {
					require.NoError(t, err)
				}
				require.NotNil(t, result)
				require.EqualValues(t, test.creation, result.Usage.CacheCreationInputTokens)
				require.EqualValues(t, test.read, result.Usage.CacheReadInputTokens)
				require.EqualValues(t, test.creation, result.Usage.billingUsageTokens().CacheCreationTokens)
				if responsesEOF {
					require.Zero(t, result.Usage.InputTokens)
					require.Zero(t, result.Usage.OutputTokens)
				}
				require.Equal(t, http.StatusOK, recorder.Code)
				assertUsage := func(usage gjson.Result, creation, read int64) {
					require.False(t, usage.Get("cache_creation").Exists())
					for field, want := range map[string]int64{"cache_creation_input_tokens": creation, "cache_read_input_tokens": read} {
						tokens := usage.Get(field)
						require.True(t, tokens.Exists(), field)
						require.Equal(t, gjson.Number, tokens.Type, field)
						require.Equal(t, want, tokens.Int(), field)
					}
				}
				if strings.HasSuffix(route, "buffered") {
					assertUsage(gjson.Get(recorder.Body.String(), "usage"), test.creation, test.read)
					return
				}
				starts, deltas, stops, contentDeltas := 0, 0, 0, 0
				for _, line := range strings.Split(recorder.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					event := gjson.Parse(strings.TrimPrefix(line, "data: "))
					switch event.Get("type").String() {
					case "content_block_delta":
						if responsesEOF {
							require.Equal(t, "partial", event.Get("delta.text").String())
						}
						contentDeltas++
					case "message_start":
						assertUsage(event.Get("message.usage"), 0, 0)
						starts++
					case "message_delta":
						assertUsage(event.Get("usage"), test.creation, test.read)
						deltas++
					case "message_stop":
						stops++
					}
				}
				if beforeOutput {
					require.Zero(t, starts)
					require.Zero(t, contentDeltas)
				} else {
					require.Equal(t, 1, starts)
					if responsesEOF {
						require.Equal(t, 1, contentDeltas)
					}
				}
				if responsesEOF {
					require.Zero(t, deltas)
					require.Zero(t, stops)
				} else {
					require.Equal(t, 1, deltas)
					require.Equal(t, 1, stops)
				}
			})
		}
	}
}

func TestResponsesTerminalUsageMatchesAnthropicOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		name := "buffered"
		if stream {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			payload := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_cache\",\"model\":\"gpt-5.4\"}}\n\n" +
				`data: {"type":"response.completed","usage":{"input_tokens":20,"output_tokens":4,"cache_creation_input_tokens":4,"input_tokens_details":{"cached_tokens":5}},"response":{"id":"resp_cache","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":1,"input_tokens_details":{"cached_tokens":2}}}}` + "\n\n"
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
			service := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			var result *OpenAIForwardResult
			var err error
			if stream {
				result, err = service.handleAnthropicStreamingResponse(response, ctx, rawChatCompletionsTestAccount(), "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
			} else {
				result, err = service.handleAnthropicBufferedStreamingResponse(response, ctx, rawChatCompletionsTestAccount(), "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Equal(t, 1, result.Usage.CacheCreationInputTokens)
			require.Equal(t, 2, result.Usage.CacheReadInputTokens)
			tokens := result.Usage.billingUsageTokens()
			require.Equal(t, 7, tokens.InputTokens)
			require.Equal(t, 1, tokens.CacheCreationTokens)
			outputUsage := gjson.Get(recorder.Body.String(), "usage")
			if stream {
				for _, line := range strings.Split(recorder.Body.String(), "\n") {
					if strings.HasPrefix(line, "data: ") {
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						if event.Get("type").String() == "message_delta" {
							outputUsage = event.Get("usage")
						}
					}
				}
			}
			require.True(t, outputUsage.Exists())
			require.EqualValues(t, tokens.InputTokens, outputUsage.Get("input_tokens").Int())
			require.EqualValues(t, tokens.OutputTokens, outputUsage.Get("output_tokens").Int())
			require.EqualValues(t, tokens.CacheCreationTokens, outputUsage.Get("cache_creation_input_tokens").Int())
			require.EqualValues(t, tokens.CacheReadTokens, outputUsage.Get("cache_read_input_tokens").Int())
			require.False(t, outputUsage.Get("cache_creation").Exists())
		})
	}
}

func TestAnthropicZeroCacheDeltaPromptReconciliation(t *testing.T) {
	for _, parser := range []string{"raw", "typed", "generic", "antigravity"} {
		t.Run(parser, func(t *testing.T) {
			usage := &ClaudeUsage{}
			for index, snapshot := range []string{
				`{"input_tokens":100,"prompt_tokens":100,"cache_read_input_tokens":30,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":20}}`,
				`{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":10}}`,
				`{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":10}}`,
			} {
				event := `{"type":"message_delta","usage":` + snapshot + `}`
				if index == 0 {
					event = `{"type":"message_start","message":{"usage":` + snapshot + `}}`
				}
				switch parser {
				case "raw":
					parseSSEUsagePassthrough(event, usage)
				case "typed":
					var source apicompat.AnthropicUsage
					require.NoError(t, source.UnmarshalJSON([]byte(snapshot)))
					mergeAnthropicUsage(usage, source)
				case "generic":
					(&GatewayService{}).parseSSEUsage(event, usage)
				case "antigravity":
					(&AntigravityGatewayService{}).extractSSEUsage("data: "+event, usage)
				}
				creation := 20
				if index > 0 {
					creation = 30
				}
				require.Equal(t, 30, usage.CacheReadInputTokens)
				if parser == "antigravity" {
					require.Equal(t, 100, usage.InputTokens)
				} else {
					require.Equal(t, creation, usage.CacheCreationInputTokens)
					require.Equal(t, 100-30-creation, usage.InputTokens)
				}
				cache5m, cache1h := normalizeCacheCreationBreakdown(usage.billingUsageTokens())
				require.Equal(t, creation, cache5m+cache1h)
			}
		})
	}
}

func TestCacheCreationZeroAggregateBillingProjection(t *testing.T) {
	claude := ClaudeUsage{
		InputTokens: 40, CacheReadInputTokens: 10,
		CacheCreation5mTokens: 20, CacheCreation1hTokens: 30,
		cacheCreationInputTokensPresent: true,
	}
	openAI := OpenAIUsage{
		InputTokens: 100, CacheReadInputTokens: 10,
		CacheCreation5mTokens: 20, CacheCreation1hTokens: 30,
		cacheCreationInputTokensPresent: true,
	}
	for _, tokens := range []UsageTokens{claude.billingUsageTokens(), openAI.billingUsageTokens()} {
		require.Equal(t, 40, tokens.InputTokens)
		require.Equal(t, 50, tokens.CacheCreationTokens)
		require.Equal(t, 10, tokens.CacheReadTokens)
		cache5m, cache1h := normalizeCacheCreationBreakdown(tokens)
		require.Equal(t, 20, cache5m)
		require.Equal(t, 30, cache1h)
		require.InDelta(t, 0.1, (&BillingService{}).computeCacheCreationCost(&ModelPricing{}, tokens, 0.002, 1), 1e-12)
	}
	require.Zero(t, claude.CacheCreationInputTokens)
	require.Zero(t, openAI.CacheCreationInputTokens)
}

func TestMessagesTerminalRawUsage(t *testing.T) {
	const prelude = `data: {"type":"response.created","response":{"id":"r","usage":{"input_tokens":400,"output_tokens":60,"input_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":3,"ttl_12h_tokens":7}}}}}` + "\n\n"
	const competing = `"usage":{"input_tokens":500,"output_tokens":25,"input_tokens_details":{"cache_write_tokens":70,"image_tokens":50}},`
	for _, tc := range []struct {
		name, extra        string
		write, short, long int
	}{
		{"raw split", `,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}`, 10, 4, 6},
		{"inherit same write total", "", 10, 3, 7},
		{"changed write total", "", 11, 0, 0},
		{"invalid split blocks inheritance", `,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":7}`, 10, 0, 0},
		{"fraction rounded by float64 is still invalid", `,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6.0000000000000001}`, 10, 0, 0},
	} {
		for _, mode := range []string{"stream", "buffered", "buffered eof"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				const model = "gpt-6-luna"
				terminal := fmt.Sprintf(`data: {"type":"response.completed",%s"response":{"id":"r","status":"completed","usage":{"input_tokens":100,"output_tokens":15,"input_tokens_details":{"cached_tokens":9,"cached_tokens_details":{"image_tokens":2},"image_tokens":7,"cache_write_tokens":%d%s}},"tool_usage":{"image_gen":{"output_tokens_details":{"image_tokens":5}}}}}`, competing, tc.write, tc.extra)
				if mode != "buffered eof" {
					terminal += "\n\n"
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(prelude + terminal))}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				forward := svc.handleAnthropicStreamingResponse
				if mode != "stream" {
					forward = svc.handleAnthropicBufferedStreamingResponse
				}
				result, err := forward(resp, c, rawChatCompletionsTestAccount(), model, model, model, time.Now())
				require.NoError(t, err)
				require.Equal(t, UsageTokens{InputTokens: 91 - tc.write, OutputTokens: 15, CacheReadTokens: 9, CacheCreationTokens: tc.write, CacheCreation30mTokens: tc.short, CacheCreation12hTokens: tc.long, ImageInputTokens: 5, ImageCacheReadTokens: 2, ImageOutputTokens: 5}, result.Usage.billingUsageTokens())
			})
		}
	}
}

func TestMessagesTerminalUsageEvidence(t *testing.T) {
	const rich = `{"input_tokens":100,"output_tokens":4,"cache_creation_input_tokens":10,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`
	for _, tc := range []struct {
		name, progress, top, nested             string
		input, output, write, short, long, read int
	}{
		{"matching sibling split", "", rich, `{"input_tokens":40,"output_tokens":2,"cache_creation_input_tokens":10,"cache_read_input_tokens":2}`, 40, 2, 10, 4, 6, 2},
		{"top only", "", rich, "", 100, 4, 10, 4, 6, 2},
		{"selected zero keeps progress", rich, rich, `{"input_tokens":0,"output_tokens":0}`, 100, 4, 10, 4, 6, 2},
		{"missing terminal usage", rich, "", "", 100, 4, 10, 4, 6, 2},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				field := func(v string) string {
					if v == "" {
						return ""
					}
					return `,"usage":` + v
				}
				payload := `data: {"type":"response.created","response":{"id":"r"` + field(tc.progress) + "}}\n\n" + `data: {"type":"response.completed","response":{"id":"r","status":"completed"` + field(tc.nested) + "}" + field(tc.top) + "}\n\n"
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				forward := svc.handleAnthropicBufferedStreamingResponse
				if stream {
					forward = svc.handleAnthropicStreamingResponse
				}
				result, err := forward(resp, c, rawChatCompletionsTestAccount(), "gpt-6-luna", "gpt-6-luna", "gpt-6-luna", time.Now())
				require.NoError(t, err)
				require.Equal(t, UsageTokens{InputTokens: tc.input - tc.write - tc.read, OutputTokens: tc.output, CacheCreationTokens: tc.write, CacheCreation30mTokens: tc.short, CacheCreation12hTokens: tc.long, CacheReadTokens: tc.read}, result.Usage.billingUsageTokens())
				wire := gjson.Get(rec.Body.String(), "usage")
				if stream {
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if payload, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(payload, "type").String() == "message_delta" {
							wire = gjson.Get(payload, "usage")
						}
					}
				}
				require.True(t, wire.IsObject())
				require.EqualValues(t, tc.input-tc.write-tc.read, wire.Get("input_tokens").Int())
				require.EqualValues(t, tc.read, wire.Get("cache_read_input_tokens").Int())
				require.EqualValues(t, tc.write, wire.Get("cache_creation_input_tokens").Int())
				require.EqualValues(t, tc.output, wire.Get("output_tokens").Int())
			})
		}
	}
}

func messagesTerminalUsagePayload(progress, nested, top string) string {
	field := func(v string) string {
		if v == "" {
			return ""
		}
		return `,"usage":` + v
	}
	return `data: {"type":"response.created","response":{"id":"r"` + field(progress) + "}}\n\n" +
		`data: {"type":"response.completed","response":{"id":"r","status":"completed"` + field(nested) + "}" + field(top) + "}\n\n"
}

// runMessagesTerminalUsage settles one Responses stream through a Messages
// route and returns the raw settlement snapshot and the usage the client received.
func runMessagesTerminalUsage(t *testing.T, mode, payload string) (OpenAIUsage, gjson.Result) {
	t.Helper()
	if strings.HasSuffix(mode, "eof") {
		payload = strings.TrimSuffix(payload, "\n\n")
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	stream := strings.HasPrefix(mode, "stream")
	forward := svc.handleAnthropicBufferedStreamingResponse
	if stream {
		forward = svc.handleAnthropicStreamingResponse
	}
	result, err := forward(resp, c, rawChatCompletionsTestAccount(), "gpt-6-luna", "gpt-6-luna", "gpt-6-luna", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	wire := gjson.Get(rec.Body.String(), "usage")
	if stream {
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "message_delta" {
				wire = gjson.Get(data, "usage")
			}
		}
	}
	require.True(t, wire.IsObject())
	return result.Usage, wire
}

var messagesTerminalUsageModes = []string{"stream", "stream eof", "buffered", "buffered eof"}

func TestMessagesTerminalUninformativeUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const progress = `{"input_tokens":200,"output_tokens":1,"input_tokens_details":{"cached_tokens":9,"cache_write_tokens":100,"cache_write_tokens_details":{"ttl_30m_tokens":20,"ttl_12h_tokens":80}}}`
	const top = `{"input_tokens":300,"output_tokens":6,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":50}}`
	observed := UsageTokens{InputTokens: 91, OutputTokens: 1, CacheReadTokens: 9, CacheCreationTokens: 100, CacheCreation30mTokens: 20, CacheCreation12hTokens: 80}
	topOnly := UsageTokens{InputTokens: 246, OutputTokens: 6, CacheReadTokens: 4, CacheCreationTokens: 50}
	for _, tc := range []struct {
		name, progress, nested, top string
		want                        UsageTokens
	}{
		{"empty nested keeps progress", progress, `{}`, "", observed},
		{"empty nested and top keep progress", progress, `{}`, `{}`, observed},
		{"omitted nested with empty top keeps progress", progress, "", `{}`, observed},
		{"uninformative nested keeps progress", progress, `{"input_tokens":null,"total_tokens":null,"input_tokens_details":{},"output_tokens_details":null,"model":"x"}`, "", observed},
		{"empty nested uses reported top", progress, `{}`, top, topOnly},
		{"uninformative nested uses reported top", "", `{"input_tokens_details":{"cached_tokens_details":{}}}`, top, topOnly},
		{"explicit nested zero keeps progress", progress, `{"input_tokens":0,"output_tokens":0}`, "", observed},
		{"explicit top zero keeps progress when nested is empty", progress, `{}`, `{"input_tokens":0,"output_tokens":0}`, observed},
	} {
		for _, mode := range messagesTerminalUsageModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				got, wire := runMessagesTerminalUsage(t, mode, messagesTerminalUsagePayload(tc.progress, tc.nested, tc.top))
				require.Equal(t, tc.want, got.billingUsageTokens())
				require.EqualValues(t, tc.want.InputTokens, wire.Get("input_tokens").Int())
				require.EqualValues(t, tc.want.OutputTokens, wire.Get("output_tokens").Int())
				require.EqualValues(t, tc.want.CacheReadTokens, wire.Get("cache_read_input_tokens").Int())
				require.EqualValues(t, tc.want.CacheCreationTokens, wire.Get("cache_creation_input_tokens").Int())
			})
		}
	}
}

func TestMessagesTerminalImageSubsetInheritance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const rich = `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9,"image_tokens":7,"cached_tokens_details":{"image_tokens":2}},"output_tokens_details":{"image_tokens":4}}`
	const omitted = `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9}}`
	images := func(input, cached, output int) UsageTokens {
		return UsageTokens{InputTokens: 191, OutputTokens: 10, CacheReadTokens: 9, ImageInputTokens: input - cached, ImageCacheReadTokens: cached, ImageOutputTokens: output}
	}
	for _, tc := range []struct {
		name, progress, nested, top string
		want                        UsageTokens
	}{
		{"progress with same totals", rich, omitted, "", images(7, 2, 4)},
		{"matching sibling", "", omitted, rich, images(7, 2, 4)},
		{"mismatched sibling falls back to progress", rich, omitted, `{"input_tokens":500,"output_tokens":20,"input_tokens_details":{"cached_tokens":9,"image_tokens":50},"output_tokens_details":{"image_tokens":9}}`, images(7, 2, 4)},
		{"empty nested selects top then progress", rich, `{}`, omitted, images(7, 2, 4)},
		{"changed input total keeps only output subset", rich, `{"input_tokens":201,"output_tokens":10,"input_tokens_details":{"cached_tokens":9}}`, "",
			UsageTokens{InputTokens: 192, OutputTokens: 10, CacheReadTokens: 9, ImageOutputTokens: 4}},
		{"changed cache read blocks cached image", rich, `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":8}}`, "",
			UsageTokens{InputTokens: 192, OutputTokens: 10, CacheReadTokens: 8, ImageInputTokens: 7, ImageOutputTokens: 4}},
		{"changed output total blocks image output", rich, `{"input_tokens":200,"output_tokens":11,"input_tokens_details":{"cached_tokens":9}}`, "",
			UsageTokens{InputTokens: 191, OutputTokens: 11, CacheReadTokens: 9, ImageInputTokens: 5, ImageCacheReadTokens: 2}},
		{"explicit zero image fields", rich, `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9,"image_tokens":0,"cached_tokens_details":{"image_tokens":0}},"output_tokens_details":{"image_tokens":0}}`, "", images(0, 0, 0)},
		{"explicit zero image input blocks cached subset", rich, `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9,"image_tokens":0}}`, "", images(0, 0, 4)},
		{"invalid image details", rich, `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9,"image_tokens":null,"cached_tokens_details":{"image_tokens":-1}},"output_tokens_details":{"image_tokens":null}}`, "", images(0, 0, 0)},
		{"matching sibling explicit zero settles over progress", rich, omitted, `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9,"image_tokens":0},"output_tokens_details":{"image_tokens":0}}`, images(0, 0, 0)},
		{"same reported image input inherits only cached subset", rich, `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9,"image_tokens":7}}`, "", images(7, 2, 4)},
		{"changed reported subsets are not added", rich, `{"input_tokens":200,"output_tokens":10,"input_tokens_details":{"cached_tokens":9,"image_tokens":5},"output_tokens_details":{"image_tokens":3}}`, "", images(5, 0, 3)},
		{"zero output total never inherits", `{"output_tokens_details":{"image_tokens":4}}`, `{"input_tokens":200,"output_tokens":0}`, "", UsageTokens{InputTokens: 200}},
	} {
		for _, mode := range messagesTerminalUsageModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				got, wire := runMessagesTerminalUsage(t, mode, messagesTerminalUsagePayload(tc.progress, tc.nested, tc.top))
				require.Equal(t, tc.want, got.billingUsageTokens())
				require.EqualValues(t, tc.want.InputTokens, wire.Get("input_tokens").Int())
				require.EqualValues(t, tc.want.OutputTokens, wire.Get("output_tokens").Int())
				require.EqualValues(t, tc.want.CacheReadTokens, wire.Get("cache_read_input_tokens").Int())
				require.False(t, wire.Get("image_input_tokens").Exists())
			})
		}
	}
}

// Each string is a member list from a Responses envelope. Keep the tool report
// beside (not inside) usage and deliver content between the snapshots.
func TestMessagesTerminalMeteringEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const rich = `,"usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":8,"image_tokens":7,"cached_tokens_details":{"image_tokens":2},"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}},"output_tokens_details":{"image_tokens":4}}`
	const changed = `,"usage":{"input_tokens":200,"output_tokens":20,"input_tokens_details":{"cached_tokens":9,"cache_write_tokens":12}}`
	const reasoning = `,"usage":{"output_tokens_details":{"reasoning_tokens":5}}`
	const imageTool = `,"tool_usage":{"image_gen":{"output_tokens":20,"output_tokens_details":{"image_tokens":4}}}`
	const output = `,"usage":{"output_tokens":20}`
	const imageOnly = `,"usage":{"output_tokens_details":{"image_tokens":4}}`
	const imageInput = `,"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":9,"image_tokens":7}}`
	const write = `,"usage":{"input_tokens":100,"cache_creation_input_tokens":10}`
	const conflictingWrite = `,"usage":{"cache_creation_input_tokens":12,"cache_write_tokens":10,"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`
	const validWrite = `,"usage":{"cache_creation_input_tokens":10,"cache_write_tokens":12,"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`
	newTotals := UsageTokens{InputTokens: 179, OutputTokens: 20, CacheReadTokens: 9, CacheCreationTokens: 12}
	cachedImage := UsageTokens{InputTokens: 91, CacheReadTokens: 9, ImageInputTokens: 4, ImageCacheReadTokens: 3}
	pricing := &ModelPricing{InputPricePerToken: 10e-6, ImageInputPricePerToken: 30e-6, CacheReadPricePerToken: 1e-6, ImageCacheReadPricePerToken: 3e-6, OutputPricePerToken: 20e-6, ImageOutputPricePerToken: 50e-6, CacheCreationPricePerToken: 5e-6, CacheCreation12hPricePerToken: float64Ptr(10e-6)}
	for _, tc := range []struct {
		name, created, inProgress, nested, top string
		rawInput, rawImageInput                int
		want                                   UsageTokens
		costUSD                                float64
	}{
		{"new totals retire all old image and TTL buckets", rich, changed, changed, "", 200, 0, newTotals, .002259},
		{"empty terminal uses coherent second snapshot", rich, changed, `,"usage":{}`, "", 200, 0, newTotals, .002259},
		{"progress explicit image zero retires the old subset", `,"usage":{"output_tokens":20,"output_tokens_details":{"image_tokens":4}}`, `,"usage":{"output_tokens":20,"output_tokens_details":{"image_tokens":0}}`, output, "", 0, 0, UsageTokens{OutputTokens: 20}, .000400},
		{"progress changing only output keeps verified input details", rich, `,"usage":{"output_tokens":20}`, `,"usage":{}`, "", 100, 7, UsageTokens{InputTokens: 82, OutputTokens: 20, CacheReadTokens: 8, CacheCreationTokens: 10, CacheCreation30mTokens: 4, CacheCreation12hTokens: 6, ImageInputTokens: 5, ImageCacheReadTokens: 2}, .001412},
		{"unanchored cached image and TTL do not replace totals", `,"usage":{"input_tokens":200,"output_tokens":3}`, "", `,"usage":{"input_tokens_details":{"cached_tokens_details":{"image_tokens":2},"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`, "", 200, 0, UsageTokens{InputTokens: 200, OutputTokens: 3}, .002060},
		{"reasoning without total cannot erase observed usage", `,"usage":{"input_tokens":200,"output_tokens":3}`, "", reasoning, "", 200, 0, UsageTokens{InputTokens: 200, OutputTokens: 3}, .002060},
		{"reasoning without total defers to reported sibling", "", "", reasoning, output, 0, 0, UsageTokens{OutputTokens: 20}, .000400},
		{"terminal image alone cannot erase observed totals", `,"usage":{"input_tokens":200,"output_tokens":3}`, "", imageOnly, "", 200, 0, UsageTokens{InputTokens: 200, OutputTokens: 3}, .002060},
		{"terminal image alone cannot classify a larger prior output", output, "", imageOnly, "", 0, 0, UsageTokens{OutputTokens: 20}, .000400},
		{"terminal image alone defers to sibling totals without prior usage", "", "", imageOnly, output, 0, 0, UsageTokens{OutputTokens: 20}, .000400},
		{"terminal image alone defers to sibling with verified image output", `,"usage":{"input_tokens":200,"output_tokens":3}`, "", imageOnly, `,"usage":{"output_tokens":20,"output_tokens_details":{"image_tokens":2}}`, 0, 0, UsageTokens{OutputTokens: 20, ImageOutputTokens: 2}, .000460},
		{"total proves independent reasoning", "", "", `,"usage":{"total_tokens":5,"output_tokens_details":{"reasoning_tokens":5}}`, "", 0, 0, UsageTokens{OutputTokens: 5}, .000100},
		{"tool beside empty usage classifies matching output", output, "", `,"usage":{}` + imageTool, "", 0, 0, UsageTokens{OutputTokens: 20, ImageOutputTokens: 4}, .000520},
		{"tool beside omitted usage classifies matching output", output, "", imageTool, "", 0, 0, UsageTokens{OutputTokens: 20, ImageOutputTokens: 4}, .000520},
		{"tool alone cannot supply the whole response total", "", "", imageTool, "", 0, 0, UsageTokens{}, 0},
		{"empty terminal keeps directly billed image output", imageOnly, "", `,"usage":{}`, "", 0, 0, UsageTokens{ImageOutputTokens: 4}, .000200},
		{"tool image subset exceeding its total is unverified", output, "", `,"tool_usage":{"image_gen":{"output_tokens":20,"output_tokens_details":{"image_tokens":21}}}`, "", 0, 0, UsageTokens{OutputTokens: 20}, .000400},
		{"tool cannot replace main image zero", "", "", `,"usage":{"output_tokens":20,"output_tokens_details":{"image_tokens":0}}` + imageTool, "", 0, 0, UsageTokens{OutputTokens: 20}, .000400},
		{"tool cannot replace main input image zero", "", "", `,"usage":{"input_tokens":20,"input_tokens_details":{"image_tokens":0}},"tool_usage":{"image_gen":{"input_tokens":20,"input_tokens_details":{"image_tokens":4}}}`, "", 20, 0, UsageTokens{InputTokens: 20}, .000200},
		{"tool without matching total cannot classify prior output", `,"usage":{"output_tokens":19}`, "", imageTool, "", 0, 0, UsageTokens{OutputTokens: 19}, .000380},
		{"tool with conflicting total cannot classify primary output", "", "", `,"usage":{"output_tokens":19}` + imageTool, "", 0, 0, UsageTokens{OutputTokens: 19}, .000380},
		{"inherit image input before bounding cached image", imageInput, "", `,"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":9,"cached_tokens_details":{"image_tokens":3}}}`, "", 100, 7, cachedImage, .001005},
		{"irrelevant tool total cannot hide main cached image zero", `,"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":9,"image_tokens":7,"cached_tokens_details":{"image_tokens":2}}}`, "", `,"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":9,"cached_tokens_details":{"image_tokens":0}}},"tool_usage":{"image_gen":{"input_tokens":200,"input_tokens_details":{"image_tokens":10}}}`, "", 100, 7, UsageTokens{InputTokens: 91, CacheReadTokens: 9, ImageInputTokens: 7}, .001059},
		{"prompt detail alias contributes cached image", imageInput, "", `,"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":9,"cached_tokens_details":{"image_tokens":3}}}`, "", 100, 7, cachedImage, .001005},
		{"sibling split disagrees with official aggregate", "", "", write, conflictingWrite, 100, 0, UsageTokens{InputTokens: 90, CacheCreationTokens: 10}, .000950},
		{"progress split disagrees with official aggregate", conflictingWrite, "", write, "", 100, 0, UsageTokens{InputTokens: 90, CacheCreationTokens: 10}, .000950},
		{"sibling split agrees with official aggregate", "", "", write, validWrite, 100, 0, UsageTokens{InputTokens: 90, CacheCreationTokens: 10, CacheCreation30mTokens: 4, CacheCreation12hTokens: 6}, .000980},
	} {
		for _, mode := range messagesTerminalUsageModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				payload := `data: {"type":"response.created","response":{"id":"r"` + tc.created + "}}\n\n"
				if tc.inProgress != "" {
					payload += `data: {"type":"response.in_progress","response":{"id":"r"` + tc.inProgress + "}}\n\n"
				}
				payload += `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"ok"}` + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]` + tc.nested + "}" + tc.top + "}\n\n"
				raw, wire := runMessagesTerminalUsage(t, mode, payload)
				require.Equal(t, tc.rawInput, raw.InputTokens)
				require.Equal(t, tc.rawImageInput, raw.ImageInputTokens)
				tokens := raw.billingUsageTokens()
				require.Equal(t, tc.want, tokens)
				cost := (&BillingService{}).computeTokenBreakdown(pricing, tokens, 1, "", false)
				require.InDelta(t, tc.costUSD, cost.ActualCost, 1e-12)
				for key, want := range map[string]int{"input_tokens": tc.want.InputTokens, "output_tokens": tc.want.OutputTokens, "cache_read_input_tokens": tc.want.CacheReadTokens, "cache_creation_input_tokens": tc.want.CacheCreationTokens} {
					require.Equal(t, gjson.Number, wire.Get(key).Type, key)
					require.EqualValues(t, want, wire.Get(key).Int(), key)
				}
			})
		}
	}
}

func TestBufferedResponsesFinalZeroKeepsProgress(t *testing.T) {
	testBufferedResponsesFinalUsageConflict(t, "", `{"input_tokens":0,"output_tokens":0}`)
}

func TestBufferedResponsesFinalEmptyUsageAfterPositiveEnvelope(t *testing.T) {
	// The frame reader sees the positive envelope first. The buffered response
	// reader must independently recognize the nested empty usage snapshot.
	testBufferedResponsesFinalUsageConflict(t, `"usage":{"input_tokens":5,"output_tokens":3},`, `{}`)
}

func testBufferedResponsesFinalUsageConflict(t *testing.T, envelopeUsage, finalUsage string) {
	t.Helper()
	const progress = "data: " + `{"type":"response.in_progress","response":{"usage":{"input_tokens":5,"output_tokens":3}}}` + "\n\n"
	final := `{"id":"resp_final","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":` + finalUsage + `}`
	body := []byte(progress + "data: " + `{"type":"response.completed",` + envelopeUsage + `"response":` + final + "}\n\n")
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	for _, route := range []string{"SSE", "passthrough", "WS"} {
		t.Run(route, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{StatusCode: 200, Header: make(http.Header)}
			var got *OpenAIUsage
			switch route {
			case "SSE":
				r, err := svc.handleSSEToJSON(resp, c, rawChatCompletionsTestAccount(), body, "m", "m")
				require.NoError(t, err)
				require.True(t, r.delivered)
				got = r.usage
			case "passthrough":
				r, err := svc.handlePassthroughSSEToJSON(resp, c, rawChatCompletionsTestAccount(), body, "m", "m")
				require.NoError(t, err)
				require.True(t, r.delivered)
				got = r.usage
			case "WS":
				got = &OpenAIUsage{InputTokens: 5, OutputTokens: 3}
				populateOpenAIUsageFromResponseJSON([]byte(final), got)
			}
			require.Equal(t, OpenAIUsage{InputTokens: 5, OutputTokens: 3, RetainedAfterZero: true}, *got)
			if route != "WS" {
				require.JSONEq(t, finalUsage, gjson.Get(rec.Body.String(), "usage").Raw)
			}
		})
	}
}
