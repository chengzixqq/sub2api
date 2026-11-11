package service

import (
	"fmt"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
)

const serviceTestTimezoneEnv = "SUB2API_SERVICE_TEST_TIMEZONE"

func TestMain(m *testing.M) {
	// Gin mode and the application timezone are process globals. Initialize them
	// before tests start so parallel requests and background timers only read them.
	gin.SetMode(gin.TestMode)
	tz := os.Getenv(serviceTestTimezoneEnv)
	if tz == "" {
		tz = "UTC"
	}
	if err := timezone.Init(tz); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
