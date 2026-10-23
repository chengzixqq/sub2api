//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// geminiAbortingStreamBody 先吐出 data，读完后返回 abortErr；onAbort 用于模拟客户端断开时请求 context 被取消。
type geminiAbortingStreamBody struct {
	data     *bytes.Reader
	abortErr error
	onAbort  func()
}

func (b *geminiAbortingStreamBody) Read(p []byte) (int, error) {
	n, err := b.data.Read(p)
	if err == io.EOF {
		if b.onAbort != nil {
			b.onAbort()
		}
		return n, b.abortErr
	}
	return n, err
}

func (b *geminiAbortingStreamBody) Close() error { return nil }

const (
	geminiAbortTextChunk  = `data: {"candidates":[{"content":{"parts":[{"text":"hello"}],"role":"model"}}]}` + "\n\n"
	geminiAbortFinalChunk = `data: {"candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}` + "\n\n"
	geminiAbortImageChunk = `data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1290,"totalTokenCount":1300}}` + "\n\n"
	geminiAbortUsageChunk = `data: {"candidates":[{"content":{"parts":[{"text":"drawing"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}` + "\n\n"
)

// newGeminiAbortingStreamService 返回的上游流在吐完 sse 后中断；clientGone=true 时中断伴随请求 context 取消。
func newGeminiAbortingStreamService(t *testing.T, path string, reqBody []byte, sse string, clientGone bool) (*GeminiMessagesCompatService, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	reqCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBody)).WithContext(reqCtx)

	body := &geminiAbortingStreamBody{data: bytes.NewReader([]byte(sse)), abortErr: io.ErrUnexpectedEOF}
	if clientGone {
		body.abortErr = context.Canceled
		body.onAbort = cancel
	}
	svc, httpStub := newGeminiVertexAPIKeyTestService()
	httpStub.response = &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
	return svc, c, rec
}

func TestGeminiForwardNative_ClientDisconnectAfterFinalChunkBillsCollectedUsage(t *testing.T) {
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-flash-preview:streamGenerateContent", reqBody, geminiAbortTextChunk+geminiAbortFinalChunk, true)

	result, err := svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-flash-preview", "streamGenerateContent", true, reqBody)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Zero(t, result.ImageCount)
}

func TestGeminiForwardNative_ClientDisconnectWithoutUsagePreservesFailurePolicy(t *testing.T) {
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-flash-preview:streamGenerateContent", reqBody, geminiAbortTextChunk, true)

	result, err := svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-flash-preview", "streamGenerateContent", true, reqBody)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
}

func TestGeminiForwardNative_UpstreamReadErrorWithLiveClientStaysError(t *testing.T) {
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-flash-preview:streamGenerateContent", reqBody, geminiAbortTextChunk+geminiAbortFinalChunk, false)

	result, err := svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-flash-preview", "streamGenerateContent", true, reqBody)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NotNil(t, result)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestGeminiForwardNative_ClientDisconnectBillsOnlyDeliveredImages(t *testing.T) {
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}]}`)

	t.Run("image delivered before disconnect", func(t *testing.T) {
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-pro-image:streamGenerateContent", reqBody, geminiAbortImageChunk, true)
		result, err := svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-pro-image", "streamGenerateContent", true, reqBody)
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, result.ClientDisconnect)
		require.Equal(t, 1, result.ImageCount)
	})

	t.Run("disconnect before the image arrives", func(t *testing.T) {
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-pro-image:streamGenerateContent", reqBody, geminiAbortUsageChunk, true)
		result, err := svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-pro-image", "streamGenerateContent", true, reqBody)
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, result.ClientDisconnect)
		require.Zero(t, result.ImageCount)
		require.Equal(t, 2, result.Usage.OutputTokens)
	})
}

func TestGeminiCompatForward_ClientDisconnectAfterFinalChunkBillsCollectedUsage(t *testing.T) {
	reqBody := []byte(`{"model":"gemini-3-flash-preview","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	t.Run("client gone", func(t *testing.T) {
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1/messages", reqBody, geminiAbortTextChunk+geminiAbortFinalChunk, true)
		result, err := svc.Forward(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, result.ClientDisconnect)
		require.Equal(t, 10, result.Usage.InputTokens)
		require.Equal(t, 5, result.Usage.OutputTokens)
	})

	t.Run("upstream read error with live client", func(t *testing.T) {
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1/messages", reqBody, geminiAbortTextChunk+geminiAbortFinalChunk, false)
		result, err := svc.Forward(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		require.NotNil(t, result)
		require.Equal(t, 10, result.Usage.InputTokens)
		require.Equal(t, 5, result.Usage.OutputTokens)
	})
}

func TestGeminiForwardAsChatCompletions_ClientDisconnectAfterFinalChunkBillsCollectedUsage(t *testing.T) {
	reqBody := []byte(`{"model":"gemini-3-flash-preview","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	t.Run("client gone", func(t *testing.T) {
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1/chat/completions", reqBody, geminiAbortTextChunk+geminiAbortFinalChunk, true)
		result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, result.ClientDisconnect)
		require.Equal(t, 10, result.Usage.InputTokens)
		require.Equal(t, 5, result.Usage.OutputTokens)
	})

	t.Run("client gone before usage", func(t *testing.T) {
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1/chat/completions", reqBody, geminiAbortTextChunk, true)
		result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
		require.ErrorIs(t, err, context.Canceled)
		require.NotNil(t, result)
	})
}

func TestGeminiForwardNative_ClientDisconnectAfterFinishKeepsImageFallback(t *testing.T) {
	// 结束分片已到达但图片不以 inlineData 回吐时，断开与正常完成同口径按模型名兜底。
	finishWithoutInline := `data: {"candidates":[{"content":{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/cat.png"}}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1290,"totalTokenCount":1300}}` + "\n\n"
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}]}`)
	svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-pro-image:streamGenerateContent", reqBody, finishWithoutInline, true)

	result, err := svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-pro-image", "streamGenerateContent", true, reqBody)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 1, result.ImageCount)
}

func TestGeminiForwardNative_ClientDisconnectIgnoresTruncatedTrailingLine(t *testing.T) {
	complete := `data: {"candidates":[{"content":{"parts":[{"text":"hello"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":300,"totalTokenCount":1300}}` + "\n\n"
	truncated := `data: {"candidates":[{"content":{"parts":[{"text":" more"}],"role":"model"}}],"usageMetadata":{`
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-flash-preview:streamGenerateContent", reqBody, complete+truncated, true)

	result, err := svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-flash-preview", "streamGenerateContent", true, reqBody)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 1000, result.Usage.InputTokens)
	require.Equal(t, 300, result.Usage.OutputTokens)
}

func TestGeminiForwardAsChatCompletions_ClientDisconnectImageCount(t *testing.T) {
	t.Run("disconnect before finish bills no image", func(t *testing.T) {
		reqBody := []byte(`{"model":"gemini-3-pro-image","stream":true,"messages":[{"role":"user","content":"draw a cat"}]}`)
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1/chat/completions", reqBody, geminiAbortUsageChunk, true)
		result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, result.ClientDisconnect)
		require.Zero(t, result.ImageCount)
		require.Equal(t, 2, result.Usage.OutputTokens)
	})

	t.Run("disconnect after finish keeps completed-request image count", func(t *testing.T) {
		reqBody := []byte(`{"model":"gemini-3-pro-image","stream":true,"messages":[{"role":"user","content":"draw a cat"}]}`)
		svc, c, _ := newGeminiAbortingStreamService(t, "/v1/chat/completions", reqBody, geminiAbortImageChunk, true)
		result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, result.ClientDisconnect)
		require.Equal(t, 1, result.ImageCount)
	})
}

// geminiFailingResponseWriter 模拟客户端已断开：所有写入失败。
type geminiFailingResponseWriter struct {
	header http.Header
}

func (w *geminiFailingResponseWriter) Header() http.Header       { return w.header }
func (w *geminiFailingResponseWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (w *geminiFailingResponseWriter) WriteHeader(int)           {}
func (w *geminiFailingResponseWriter) Flush()                    {}

func TestGeminiForwardAsChatCompletions_WriteFailureOnImageModelBillsNoImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reqBody := []byte(`{"model":"gemini-3-pro-image","stream":true,"messages":[{"role":"user","content":"draw a cat"}]}`)
	c, _ := gin.CreateTestContext(&geminiFailingResponseWriter{header: http.Header{}})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
	svc, httpStub := newGeminiVertexAPIKeyTestService()
	httpStub.response = &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(geminiAbortUsageChunk))),
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.True(t, result.ClientDisconnect)
	require.Zero(t, result.ImageCount)
}

func TestGeminiCompatForward_ClientDisconnectBeforeFinishBillsNoImage(t *testing.T) {
	inlineWithoutFinish := `data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}],"role":"model"}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1290,"totalTokenCount":1300}}` + "\n\n"
	reqBody := []byte(`{"model":"gemini-3-pro-image","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"draw a cat"}]}`)
	svc, c, _ := newGeminiAbortingStreamService(t, "/v1/messages", reqBody, inlineWithoutFinish, true)

	result, err := svc.Forward(context.Background(), c, geminiVertexAPIKeyTestAccount(""), reqBody)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, result.ClientDisconnect)
	require.Zero(t, result.ImageCount)
	require.Equal(t, 1290, result.Usage.OutputTokens)
}

func TestGeminiStreamInterrupted_RealOutputSignalExcludesMetadata(t *testing.T) {
	for _, endpoint := range []string{"native", "messages", "chat/completions"} {
		for _, tc := range []struct {
			name, chunk string
			wantOutput  bool
		}{
			{name: "usage only", chunk: `data: {"usageMetadata":{"promptTokenCount":10}}` + "\n\n"},
			{name: "finish only", chunk: `data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\n"},
			{name: "thought first", chunk: `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"thinking"},{"text":"answer"}]}}]}` + "\n\n", wantOutput: true},
			{name: "tool only", chunk: `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}` + "\n\n", wantOutput: true},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				body := []byte(`{"model":"gemini-3-flash-preview","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}],"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
				svc, c, _ := newGeminiAbortingStreamService(t, "/v1/"+endpoint, body, tc.chunk, true)
				var result *ForwardResult
				var err error
				switch endpoint {
				case "native":
					result, err = svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-flash-preview", "streamGenerateContent", true, body)
				case "messages":
					result, err = svc.Forward(context.Background(), c, geminiVertexAPIKeyTestAccount(""), body)
				default:
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, geminiVertexAPIKeyTestAccount(""), body)
				}
				require.ErrorIs(t, err, context.Canceled)
				require.NotNil(t, result)
				require.Equal(t, tc.wantOutput, c.GetBool(GatewayUpstreamDeliveredKey))
				if tc.name == "usage only" {
					require.Equal(t, 10, result.Usage.InputTokens)
				}
			})
		}
	}
}

func TestGeminiStreamInterrupted_ImageOnlyUpstreamReadErrorUsesDeliveredOutput(t *testing.T) {
	for _, media := range []struct {
		name, part string
	}{
		{name: "inline image", part: `{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}`},
		{name: "file image", part: `{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/cat.png"}}`},
	} {
		for _, endpoint := range []string{"native", "messages", "chat/completions"} {
			t.Run(endpoint+"/"+media.name, func(t *testing.T) {
				// No finish or usage arrives before an upstream error while the client
				// remains connected. The compat endpoints discard both image forms.
				chunk := `data: {"candidates":[{"content":{"parts":[` + media.part + `],"role":"model"}}]}` + "\n\n"
				body := []byte(`{"model":"gemini-3-pro-image","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"draw a cat"}],"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}]}`)
				svc, c, rec := newGeminiAbortingStreamService(t, "/v1/"+endpoint, body, chunk, false)
				var result *ForwardResult
				var err error
				switch endpoint {
				case "native":
					result, err = svc.ForwardNative(context.Background(), c, geminiVertexAPIKeyTestAccount(""), "gemini-3-pro-image", "streamGenerateContent", true, body)
				case "messages":
					result, err = svc.Forward(context.Background(), c, geminiVertexAPIKeyTestAccount(""), body)
				default:
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, geminiVertexAPIKeyTestAccount(""), body)
				}
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
				require.NotNil(t, result)
				require.False(t, result.ClientDisconnect)
				require.False(t, result.Usage.hasObservedTokens())
				decision := DecideFailureBilling(FailureBillingInput{
					Usage: result.Usage, ImageCount: result.ImageCount,
					OutputStarted:    c.GetBool(GatewayUpstreamDeliveredKey),
					ClientDisconnect: result.ClientDisconnect, EstimatedPromptTokens: 10,
				})
				if endpoint == "native" {
					require.True(t, c.GetBool(GatewayUpstreamDeliveredKey))
					require.Contains(t, rec.Body.String(), media.part)
					require.True(t, decision.Billable, "native actually forwards the image")
					return
				}
				require.Zero(t, result.ImageCount)
				require.NotContains(t, rec.Body.String(), "aGVsbG8=")
				require.NotContains(t, rec.Body.String(), "gs://bucket/cat.png")
				require.False(t, decision.Billable, "discarded images cannot trigger an estimated charge")
				require.Equal(t, "no_output_no_usage", decision.Reason)
				require.False(t, c.GetBool(GatewayUpstreamDeliveredKey))
			})
		}
	}
}
