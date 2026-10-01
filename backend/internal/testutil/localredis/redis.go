//go:build integration

// Package localredis enables opt-in integration tests against a temporary local
// Redis instance. Callers retain responsibility for isolating their test keys.
package localredis

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func OptionsFromEnv() (*redis.Options, error) {
	raw := strings.TrimSpace(os.Getenv("SUB2API_TEST_REDIS_URL"))
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "redis" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return nil, fmt.Errorf("SUB2API_TEST_REDIS_URL must select a loopback redis:// URL")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil || opts.DB <= 0 {
		return nil, fmt.Errorf("SUB2API_TEST_REDIS_URL must select a nonzero test database")
	}
	return opts, nil
}

// Open returns nil when local mode is not requested, allowing the caller to use
// its container harness. It never clears the selected database.
func Open(t *testing.T, ctx context.Context) *redis.Client {
	t.Helper()
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opts == nil {
		return nil
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		t.Fatalf("local Redis test connection: %v", err)
	}
	return client
}
