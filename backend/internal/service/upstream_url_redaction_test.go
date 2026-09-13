package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRedactUpstreamURLPreservesPath(t *testing.T) {
	got := redactUpstreamURL("https://baidu.cn/v1/messages?api_key=secret")
	if got != "https://***.***/v1/messages" {
		t.Fatalf("unexpected redacted URL: %q", got)
	}
}

func TestHandleErrorResponse_RedactsURLFromClientBodyAndReturnedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Set(redactUpstreamURLContextKey, true)

	upstreamBody := `{"type":"error","error":{"type":"invalid_request_error","message":"request failed at https://private-upstream.example/v1/messages?key=secret"}}`
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}
	svc := &GatewayService{cfg: &config.Config{}}
	account := &Account{ID: 66, Name: "api-key-account", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, "claude-fable-5")
	require.Error(t, err)
	require.NotContains(t, rec.Body.String(), "private-upstream.example")
	require.Contains(t, rec.Body.String(), "https://***.***/v1/messages")
	require.NotContains(t, err.Error(), "private-upstream.example")
	require.NotContains(t, err.Error(), "key=secret")
	require.Contains(t, err.Error(), "https://***.***/v1/messages")
}

func TestRedactUpstreamURLs(t *testing.T) {
	got := redactUpstreamURLs(`upstream https://example.com/v1 and https://foo.bar/x`)
	want := `upstream https://***.***/v1 and https://***.***/x`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSanitizeUpstreamErrorMessageForContextAndOps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(redactUpstreamURLContextKey, true)

	message := "see https://secret.example/v1/messages?token=hidden"
	if got := sanitizeUpstreamErrorMessageForContext(c, message); got != "see https://***.***/v1/messages" {
		t.Fatalf("sanitized message = %q", got)
	}
	setOpsUpstreamError(c, http.StatusBadRequest, message, message)
	if got, _ := c.Get(OpsUpstreamErrorMessageKey); got != "see https://***.***/v1/messages" {
		t.Fatalf("ops message = %q", got)
	}
	if got, _ := c.Get(OpsUpstreamErrorDetailKey); got != "see https://***.***/v1/messages" {
		t.Fatalf("ops detail = %q", got)
	}
}
