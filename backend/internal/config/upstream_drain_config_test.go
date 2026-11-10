package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLoadHTTPDrainDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, ordinary, image   string
		wantOrdinary, wantImage int
	}{
		{"defaults", "", "", 180, 900},
		{"independent", "45", "1200", 45, 1200},
		{"disabled", "0", "0", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("GATEWAY_UPSTREAM_DRAIN_TIMEOUT_SECONDS", tc.ordinary)
			t.Setenv("GATEWAY_IMAGE_UPSTREAM_DRAIN_TIMEOUT_SECONDS", tc.image)
			t.Setenv("GATEWAY_STREAM_DATA_INTERVAL_TIMEOUT", "600")
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, tc.wantOrdinary, cfg.Gateway.UpstreamDrainTimeoutSeconds)
			require.Equal(t, tc.wantImage, cfg.Gateway.ImageUpstreamDrainTimeoutSeconds)
			require.Equal(t, 600, cfg.Gateway.StreamDataIntervalTimeout, "retain custom idle ceiling")
		})
	}
}
func TestLoadHTTPDrainRejectsInvalidDurations(t *testing.T) {
	for _, key := range []string{"GATEWAY_UPSTREAM_DRAIN_TIMEOUT_SECONDS", "GATEWAY_IMAGE_UPSTREAM_DRAIN_TIMEOUT_SECONDS"} {
		for _, value := range []string{"-1", "9223372037"} {
			t.Run(key+value, func(t *testing.T) {
				resetViperWithJWTSecret(t)
				t.Setenv(key, value)
				_, err := Load()
				require.Error(t, err)
			})
		}
	}
}
