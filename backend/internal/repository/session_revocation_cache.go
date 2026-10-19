package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	revokedSessionKeyPrefix       = "revoked_sid:"
	userTokensValidAfterKeyPrefix = "user_tokens_valid_after:"
)

var _ service.SessionRevocationCache = (*refreshTokenCache)(nil)

func revokedSessionKey(sessionID string) string {
	return revokedSessionKeyPrefix + sessionID
}

func userTokensValidAfterKey(userID int64) string {
	return fmt.Sprintf("%s%d", userTokensValidAfterKeyPrefix, userID)
}

func (c *refreshTokenCache) RevokeSession(ctx context.Context, sessionID string, ttl time.Duration) error {
	return c.rdb.Set(ctx, revokedSessionKey(sessionID), "1", ttl).Err()
}

// SetUserTokensValidAfter stores the point in Unix milliseconds.
func (c *refreshTokenCache) SetUserTokensValidAfter(ctx context.Context, userID int64, validAfter time.Time, ttl time.Duration) error {
	return c.rdb.Set(ctx, userTokensValidAfterKey(userID), strconv.FormatInt(validAfter.UnixMilli(), 10), ttl).Err()
}

func (c *refreshTokenCache) GetSessionRevocation(ctx context.Context, sessionID string, userID int64) (bool, time.Time, error) {
	pipe := c.rdb.Pipeline()
	var sidCmd *redis.StringCmd
	if sessionID != "" {
		sidCmd = pipe.Get(ctx, revokedSessionKey(sessionID))
	}
	afterCmd := pipe.Get(ctx, userTokensValidAfterKey(userID))
	// Exec reports only the first failing command, which may be a plain miss;
	// each command is checked below.
	_, _ = pipe.Exec(ctx)

	revoked := false
	if sidCmd != nil {
		switch err := sidCmd.Err(); {
		case err == nil:
			revoked = true
		case !errors.Is(err, redis.Nil):
			return false, time.Time{}, err
		}
	}
	var validAfter time.Time
	switch ms, err := afterCmd.Int64(); {
	case err == nil:
		validAfter = time.UnixMilli(ms)
	case !errors.Is(err, redis.Nil):
		return false, time.Time{}, err
	}
	return revoked, validAfter, nil
}
