package service

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOpenAIImageDrainTimeoutIsIndependentFromIdleAndOrdinaryDrain(t *testing.T) {
	require.Equal(t, 900*time.Second, (&OpenAIGatewayService{}).openAIImageDrainTimeout())
	for _, seconds := range []int{0, 45, 1200} {
		s := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
			ImageUpstreamDrainTimeoutSeconds: seconds,
			UpstreamDrainTimeoutSeconds:      17,
			ImageStreamDataIntervalTimeout:   600,
		}}}
		require.Equal(t, time.Duration(seconds)*time.Second, s.openAIImageDrainTimeout())
	}
}
