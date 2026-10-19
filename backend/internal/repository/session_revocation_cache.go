package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// SetUserTokensValidAfter stores "<unix ms>" or "<unix ms>:<except sid>".
func (c *refreshTokenCache) SetUserTokensValidAfter(ctx context.Context, userID int64, validAfter time.Time, exceptSessionID string, ttl time.Duration) error {
	value := strconv.FormatInt(validAfter.UnixMilli(), 10)
	if exceptSessionID != "" {
		value += ":" + exceptSessionID
	}
	return c.rdb.Set(ctx, userTokensValidAfterKey(userID), value, ttl).Err()
}

func (c *refreshTokenCache) GetSessionRevocation(ctx context.Context, sessionID string, userID int64) (service.SessionRevocation, error) {
	pipe := c.rdb.Pipeline()
	var sidCmd *redis.StringCmd
	if sessionID != "" {
		sidCmd = pipe.Get(ctx, revokedSessionKey(sessionID))
	}
	afterCmd := pipe.Get(ctx, userTokensValidAfterKey(userID))
	// Exec reports only the first failing command, which may be a plain miss;
	// each command is checked below.
	_, _ = pipe.Exec(ctx)

	var rev service.SessionRevocation
	if sidCmd != nil {
		switch err := sidCmd.Err(); {
		case err == nil:
			rev.Revoked = true
		case !errors.Is(err, redis.Nil):
			return service.SessionRevocation{}, err
		}
	}
	raw, err := afterCmd.Result()
	switch {
	case errors.Is(err, redis.Nil):
		return rev, nil
	case err != nil:
		return service.SessionRevocation{}, err
	}
	msPart, except, _ := strings.Cut(raw, ":")
	if ms, err := strconv.ParseInt(msPart, 10, 64); err == nil {
		rev.ValidAfter = time.UnixMilli(ms)
		rev.ExceptSessionID = except
	}
	return rev, nil
}
