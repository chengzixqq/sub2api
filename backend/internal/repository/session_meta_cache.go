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

// Session metadata: session_meta:{sid} is a hash of the fields below (times in
// Unix milliseconds); user_sessions:{user_id} is a sorted set of sids scored by
// last_seen_at.
const (
	sessionMetaKeyPrefix  = "session_meta:"
	userSessionsKeyPrefix = "user_sessions:"
)

var _ service.SessionMetaCache = (*refreshTokenCache)(nil)

func sessionMetaKey(sessionID string) string {
	return sessionMetaKeyPrefix + sessionID
}

func userSessionsKey(userID int64) string {
	return fmt.Sprintf("%s%d", userSessionsKeyPrefix, userID)
}

func (c *refreshTokenCache) SaveSessionMeta(ctx context.Context, meta *service.SessionMeta, ttl time.Duration) error {
	key := sessionMetaKey(meta.ID)
	lastSeen := meta.LastSeenAt.UnixMilli()
	pipe := c.rdb.TxPipeline()
	pipe.HSetNX(ctx, key, "created_at", meta.CreatedAt.UnixMilli())
	if meta.ClientType != "" {
		pipe.HSetNX(ctx, key, "client_type", meta.ClientType)
	}
	if meta.DeviceName != "" {
		pipe.HSetNX(ctx, key, "device_name", meta.DeviceName)
	}
	fields := []any{"user_id", meta.UserID, "last_seen_at", lastSeen, "expires_at", meta.ExpiresAt.UnixMilli()}
	if meta.IP != "" {
		fields = append(fields, "ip", meta.IP)
	}
	if meta.UserAgent != "" {
		fields = append(fields, "ua", meta.UserAgent)
	}
	pipe.HSet(ctx, key, fields...)
	pipe.Expire(ctx, key, ttl)
	pipe.ZAdd(ctx, userSessionsKey(meta.UserID), redis.Z{Score: float64(lastSeen), Member: meta.ID})
	pipe.Expire(ctx, userSessionsKey(meta.UserID), ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) GetSessionMeta(ctx context.Context, sessionID string) (*service.SessionMeta, error) {
	values, err := c.rdb.HGetAll(ctx, sessionMetaKey(sessionID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, service.ErrSessionNotFound
	}
	return parseSessionMeta(sessionID, values), nil
}

func (c *refreshTokenCache) ListUserSessions(ctx context.Context, userID int64, limit int) ([]*service.SessionMeta, error) {
	ids, err := c.rdb.ZRevRange(ctx, userSessionsKey(userID), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	pipe := c.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGetAll(ctx, sessionMetaKey(id))
	}
	if len(ids) > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
	}
	sessions := make([]*service.SessionMeta, 0, len(ids))
	var stale []any
	for i, cmd := range cmds {
		meta := parseSessionMeta(ids[i], cmd.Val())
		if len(cmd.Val()) == 0 || meta.UserID != userID {
			stale = append(stale, ids[i])
			continue
		}
		sessions = append(sessions, meta)
	}
	if len(stale) > 0 {
		// Expired sessions leave their id behind; drop it.
		_ = c.rdb.ZRem(ctx, userSessionsKey(userID), stale...).Err()
	}
	return sessions, nil
}

func (c *refreshTokenCache) deleteSessionMeta(ctx context.Context, sessionID string) error {
	userID, err := c.rdb.HGet(ctx, sessionMetaKey(sessionID), "user_id").Int64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	}
	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, sessionMetaKey(sessionID))
	pipe.ZRem(ctx, userSessionsKey(userID), sessionID)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) deleteUserSessionMetas(ctx context.Context, userID int64) error {
	ids, err := c.rdb.ZRange(ctx, userSessionsKey(userID), 0, -1).Result()
	if err != nil {
		return err
	}
	pipe := c.rdb.Pipeline()
	for _, id := range ids {
		pipe.Del(ctx, sessionMetaKey(id))
	}
	pipe.Del(ctx, userSessionsKey(userID))
	_, err = pipe.Exec(ctx)
	return err
}

func parseSessionMeta(sessionID string, values map[string]string) *service.SessionMeta {
	userID, _ := strconv.ParseInt(values["user_id"], 10, 64)
	return &service.SessionMeta{
		ID:         sessionID,
		UserID:     userID,
		DeviceName: values["device_name"],
		ClientType: values["client_type"],
		UserAgent:  values["ua"],
		IP:         values["ip"],
		CreatedAt:  parseUnixMilli(values["created_at"]),
		LastSeenAt: parseUnixMilli(values["last_seen_at"]),
		ExpiresAt:  parseUnixMilli(values["expires_at"]),
	}
}

func parseUnixMilli(raw string) time.Time {
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
