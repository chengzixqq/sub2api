package handler

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorImages_UsesTerminalMetadataWithoutCapturingPayload(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	result := &service.OpenAIForwardResult{Model: "gpt-image-1", ImageCount: 1}
	observeMonitorImageResult(c, result, nil)
	got, ok := monitorImageResult(c)
	require.True(t, ok)
	require.Equal(t, "success", got.Outcome)
	require.True(t, got.TerminalSeen)
	observeMonitorImageResult(c, result, errors.New("stream interrupted"))
	got, _ = monitorImageResult(c)
	require.Equal(t, "unknown", got.Outcome)
	require.True(t, got.OutputSeen)
	require.False(t, got.TerminalSeen)
	w := &channelMonitorCaptureWriter{ResponseWriter: c.Writer, protocol: "images"}
	w.observe(make([]byte, channelMonitorCaptureLimit*2))
	require.Nil(t, w.parser)
}
