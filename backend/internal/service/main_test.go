package service

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
)

const serviceTestTimezoneEnv = "SUB2API_SERVICE_TEST_TIMEZONE"
const serviceIsolatedTestEnv = "SUB2API_ISOLATED_SERVICE_TEST"

// runServiceTestInFreshProcess isolates tests whose observations include
// process-global state, such as time.Local or runtime allocation counters.
// A running service timer from another test must not affect those assertions.
func runServiceTestInFreshProcess(t *testing.T, env ...string) bool {
	t.Helper()
	if os.Getenv(serviceIsolatedTestEnv) == t.Name() {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, serviceIsolatedTestEnv+"="+t.Name())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated test subprocess: %v\n%s", err, output)
	}
	return true
}

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
