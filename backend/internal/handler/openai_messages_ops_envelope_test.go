//go:build unit

package handler

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIMessagesOpsEnvelope(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"anthropic", "openai", "queue", "implicit_anthropic"} {
			t.Run(fmt.Sprintf("%s/stream=%t", kind, stream), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				r := gin.New()
				r.Use(OpsErrorLoggerMiddleware(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)))
				r.POST("/v1/messages", func(c *gin.Context) {
					if stream {
						c.Header("Content-Type", "text/event-stream")
						_, err := c.Writer.WriteString(":\n\n")
						require.NoError(t, err)
						c.Writer.Flush()
					}
					h := &OpenAIGatewayHandler{}
					switch kind {
					case "anthropic":
						h.anthropicStreamingAwareError(c, 503, "overloaded_error", "No available accounts", stream)
					case "implicit_anthropic":
						h.anthropicErrorResponse(c, 503, "overloaded_error", "No available accounts")
					case "openai":
						h.errorResponse(c, 503, "overloaded_error", "No available accounts")
					case "queue":
						h.handleConcurrencyError(c, &WaitQueueFullError{SlotType: "user"}, "user", stream)
					}
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
				var row *service.OpsInsertErrorLogInput
				if len(opsErrorLogQueue) > 0 {
					row = (<-opsErrorLogQueue).entry
				}
				logMonitorRow(t, row)
				if (kind == "openai" || kind == "implicit_anthropic") && stream {
					require.Nil(t, row)
					return
				}
				require.NotNil(t, row)
				if kind == "anthropic" {
					require.Contains(t, row.ErrorBody, `"type":"overloaded_error"`)
				}
				if kind == "queue" || kind == "openai" {
					require.NotContains(t, row.ErrorBody, `"type":"error"`)
				}
			})
		}
	}
}
