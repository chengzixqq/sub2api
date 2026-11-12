package service

import (
	"os"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const accountDispatchWritesEnv = "SUB2API_ACCOUNT_DISPATCH_WRITES_ENABLED"

func requireAccountDispatchWritesEnabled() error {
	value := strings.TrimSpace(os.Getenv(accountDispatchWritesEnv))
	if value == "1" || strings.EqualFold(value, "true") {
		return nil
	}
	return infraerrors.ServiceUnavailable(
		"ACCOUNT_DISPATCH_WRITES_DISABLED",
		"account dispatch policy writes are disabled until all gateway instances are upgraded",
	)
}
