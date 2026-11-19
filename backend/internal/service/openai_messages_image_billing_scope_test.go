//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesCachedImageBillingScope(t *testing.T) {
	const responseJSON = `{"id":"resp_image","status":"completed","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":600,"image_tokens":500,"cached_tokens_details":{"image_tokens":400}}}}`
	price := &ModelPricing{
		InputPricePerToken: 10e-6, ImageInputPricePerToken: 30e-6,
		CacheReadPricePerToken: 1e-6, ImageCacheReadPricePerToken: 3e-6,
	}
	checkBill := func(t *testing.T, usage OpenAIUsage, wantCost float64, wantCachedImages int) {
		t.Helper()
		cost := (&BillingService{}).computeTokenBreakdown(price, usage.billingUsageTokens(), 1, "", false)
		t.Logf("billed $%.6f (want $%.6f)", cost.ActualCost, wantCost)
		require.InDelta(t, wantCost, cost.ActualCost, 1e-12)
		require.Equal(t, wantCachedImages, usage.ImageCacheReadTokens)
	}

	t.Run("Responses", func(t *testing.T) {
		usage, ok := extractOpenAIUsageFromJSONBytes([]byte(responseJSON))
		require.True(t, ok)
		checkBill(t, usage, 0.012600, 0)
	})

	t.Run("ResponsesToMessages", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		frame := `data: {"type":"response.completed","response":` + responseJSON + "}\n\n"
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(frame))}
		result, err := (&OpenAIGatewayService{}).handleAnthropicBufferedStreamingResponse(resp, ctx, &Account{Platform: PlatformOpenAI}, "gpt-5", "gpt-5", "gpt-5", time.Now())
		require.NoError(t, err)
		checkBill(t, result.Usage, 0.007400, 400)
	})
}
