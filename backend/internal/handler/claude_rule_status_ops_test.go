//go:build unit

package handler

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeRuleStatusOps(t *testing.T) {
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformAntigravity, service.PlatformGemini, service.PlatformOpenAI} {
		for _, pair := range [][2]int{{429, 400}, {529, 400}, {503, 400}, {403, 400}, {500, 529}} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d_to_%d/stream=%t", platform, pair[0], pair[1], stream), func(t *testing.T) {
					withOpsFilterSettings(t, func(t *testing.T, ops *service.OpsService) {
						setupOpsErrorLogTestQueue(t, 2)
						r := gin.New()
						r.Use(OpsErrorLoggerMiddleware(ops))
						r.POST("/v1/messages", func(c *gin.Context) {
							rule := newBodyLimitPassthroughRule(pair[1], "rule message")
							rule.Platforms = []string{platform}
							rule.ErrorCodes = []int{pair[0]}
							rule.Keywords = []string{"rule input"}
							rules := service.NewErrorPassthroughService(&errorPassthroughRepoStub{rules: []*model.ErrorPassthroughRule{rule}}, nil)
							service.BindErrorPassthroughService(c, rules)
							if stream {
								c.Header("Content-Type", "text/event-stream")
								_, err := c.Writer.WriteString(":\n\n")
								require.NoError(t, err)
								c.Writer.Flush()
							}
							body := []byte(fmt.Sprintf(`{"error":{"status_code":%d,"message":"rule input"}}`, pair[0]))
							fail := &service.UpstreamFailoverError{StatusCode: pair[0], ResponseBody: body}
							switch platform {
							case service.PlatformAntigravity:
								require.Error(t, (&service.AntigravityGatewayService{}).WriteMappedClaudeError(c, &service.Account{ID: 1, Platform: platform}, pair[0], "", body, nil))
							case service.PlatformOpenAI:
								fail.StreamFailedFrame = &service.OpenAIStreamFailedFrame{Platform: platform, Payload: body, Message: "rule input"}
								(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, fail, stream)
							default:
								(&GatewayHandler{errorPassthroughService: rules}).handleFailoverExhausted(c, fail, platform, stream)
							}
						})
						w := httptest.NewRecorder()
						r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
						require.Len(t, opsErrorLogQueue, 1, w.Body.String())
						row := (<-opsErrorLogQueue).entry
						logMonitorRow(t, row)
						if stream && platform == service.PlatformAntigravity {
							require.Equal(t, 200, row.StatusCode)
							require.Empty(t, row.ErrorBody)
							require.Equal(t, pair[0], *row.UpstreamStatusCode)
						} else {
							require.Contains(t, row.ErrorBody, `"type":"upstream_error"`)
						}

						expected := `{"type":"error","error":{"type":"upstream_error","message":"rule message"}}`
						if stream {
							require.Equal(t, 200, w.Code)
							require.True(t, strings.HasSuffix(w.Body.String(), "event: error\ndata: "+expected+"\n\n"), w.Body.String())
						} else {
							require.Equal(t, pair[1], w.Code)
							require.Equal(t, expected, w.Body.String())
						}
					})
				})
			}
		}
	}
}
