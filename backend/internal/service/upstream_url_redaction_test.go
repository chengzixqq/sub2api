package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedactUpstreamURLPreservesPath(t *testing.T) {
	got := redactUpstreamURL("https://baidu.cn/v1/messages?api_key=secret")
	if got != "https://***.***/v1/messages" {
		t.Fatalf("unexpected redacted URL: %q", got)
	}
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
