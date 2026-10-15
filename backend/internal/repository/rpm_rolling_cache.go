package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const rollingRPMGlobalKey = "rpm:rolling:{rpm60}:all"

// Both keys share a hash tag, allowing atomic account/global updates on Redis
// Cluster. UUID members preserve concurrent forwards with identical timestamps.
var recordRollingRPMScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
for i = 1, #KEYS do
  local kind = redis.call('TYPE', KEYS[i]).ok
  if kind ~= 'none' and kind ~= 'zset' then
    return redis.error_reply('WRONGTYPE rolling RPM requires sorted sets')
  end
end
for i = 1, #KEYS do
  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now - 60000000)
  redis.call('ZADD', KEYS[i], now, ARGV[1])
  redis.call('EXPIRE', KEYS[i], 120)
end
return 1
`)

var _ service.RollingRPMCache = (*RPMCacheImpl)(nil)
var _ service.ScopedRollingRPMCache = (*RPMCacheImpl)(nil)

func rollingRPMAccountKey(accountID int64) string {
	return "rpm:rolling:{rpm60}:account:" + strconv.FormatInt(accountID, 10)
}

func (c *RPMCacheImpl) recordRollingRPM(ctx context.Context, accountID int64) error {
	keys := []string{rollingRPMAccountKey(accountID), rollingRPMGlobalKey}
	if userID, ok := ctx.Value(ctxkey.UserID).(int64); ok && userID > 0 {
		keys = append(keys, "rpm:rolling:{rpm60}:user:"+strconv.FormatInt(userID, 10))
	}
	if apiKeyID, ok := ctx.Value(ctxkey.APIKeyID).(int64); ok && apiKeyID > 0 {
		keys = append(keys, "rpm:rolling:{rpm60}:key:"+strconv.FormatInt(apiKeyID, 10))
	}
	return recordRollingRPMScript.Run(ctx, c.rdb, keys, uuid.NewString()).Err()
}

func (c *RPMCacheImpl) rollingRPMBounds(ctx context.Context) (string, string, error) {
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return "", "", fmt.Errorf("rolling rpm TIME: %w", err)
	}
	return "(" + strconv.FormatInt(now.UnixMicro()-60_000_000, 10), strconv.FormatInt(now.UnixMicro(), 10), nil
}

func (c *RPMCacheImpl) GetRollingRPM(ctx context.Context, accountID int64) (int, error) {
	counts, err := c.GetRollingRPMBatch(ctx, []int64{accountID})
	return counts[accountID], err
}

func (c *RPMCacheImpl) GetRollingRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	if len(accountIDs) == 0 {
		return map[int64]int{}, nil
	}
	min, max, err := c.rollingRPMBounds(ctx)
	if err != nil {
		return nil, err
	}
	pipe := c.rdb.Pipeline()
	commands := make(map[int64]*redis.IntCmd, len(accountIDs))
	for _, id := range accountIDs {
		commands[id] = pipe.ZCount(ctx, rollingRPMAccountKey(id), min, max)
	}
	_, pipelineErr := pipe.Exec(ctx)
	counts := make(map[int64]int, len(commands))
	var readErrors []error
	for id, command := range commands {
		if count, err := command.Result(); err == nil {
			counts[id] = int(count)
		} else {
			readErrors = append(readErrors, fmt.Errorf("account %d: %w", id, err))
		}
	}
	if err := errors.Join(append(readErrors, pipelineErr)...); err != nil {
		return counts, fmt.Errorf("rolling rpm batch: %w", err)
	}
	return counts, nil
}

func (c *RPMCacheImpl) GetTotalRollingRPM(ctx context.Context) (int, error) {
	return c.getRollingRPMKey(ctx, rollingRPMGlobalKey)
}

func (c *RPMCacheImpl) GetUserRollingRPM(ctx context.Context, userID int64) (int, error) {
	return c.getRollingRPMKey(ctx, "rpm:rolling:{rpm60}:user:"+strconv.FormatInt(userID, 10))
}

func (c *RPMCacheImpl) GetAPIKeyRollingRPM(ctx context.Context, apiKeyID int64) (int, error) {
	return c.getRollingRPMKey(ctx, "rpm:rolling:{rpm60}:key:"+strconv.FormatInt(apiKeyID, 10))
}

func (c *RPMCacheImpl) getRollingRPMKey(ctx context.Context, key string) (int, error) {
	min, max, err := c.rollingRPMBounds(ctx)
	if err != nil {
		return 0, err
	}
	count, err := c.rdb.ZCount(ctx, key, min, max).Result()
	if err != nil {
		return 0, fmt.Errorf("rolling rpm total: %w", err)
	}
	return int(count), nil
}
