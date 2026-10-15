package service

import (
	"context"
	"time"
)

// RPMCache RPM 计数器缓存接口
// 统计所有上游账号当前自然分钟成功转发的请求数。
// Anthropic OAuth/SetupToken 账号还可用该计数执行原有 RPM 调度限制。
type RPMCache interface {
	// IncrementRPM 原子递增并返回当前分钟的计数
	// 使用 Redis 服务器时间确定 minute key，避免多实例时钟偏差
	IncrementRPM(ctx context.Context, accountID int64) (count int, err error)

	// GetRPM 获取当前分钟的 RPM 计数
	GetRPM(ctx context.Context, accountID int64) (count int, err error)

	// GetRPMBatch 批量获取多个账号的 RPM 计数（使用 Pipeline）。
	// 可同时返回有效部分结果和错误；失败的 key 不在 map 中，未使用的分钟返回 0。
	GetRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

// RollingRPMCache reports successful forwards in (Redis time - 60s, Redis time].
// It is separate from RPMCache so display windows cannot alter scheduling limits.
type RollingRPMCache interface {
	GetRollingRPM(ctx context.Context, accountID int64) (int, error)
	GetRollingRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
	GetTotalRollingRPM(ctx context.Context) (int, error)
}

// ScopedRollingRPMCache keeps user/key views on the same successful-forward
// event stream without exposing other users' traffic.
type ScopedRollingRPMCache interface {
	GetUserRollingRPM(ctx context.Context, userID int64) (int, error)
	GetAPIKeyRollingRPM(ctx context.Context, apiKeyID int64) (int, error)
}

// incrementAccountRPM shares the same bounded, best-effort counter write across
// gateway protocols. Request cancellation remains authoritative; a disconnected
// request may not be counted, and cache failures must never fail a forward.
func incrementAccountRPM(ctx context.Context, cache RPMCache, accountID int64) error {
	if cache == nil || accountID <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := cache.IncrementRPM(ctx, accountID)
	return err
}

// SetRPMCache is called during handler composition, before requests are served.
func (s *OpenAIGatewayService) SetRPMCache(cache RPMCache) {
	s.rpmCache = cache
}

func (s *OpenAIGatewayService) IncrementAccountRPM(ctx context.Context, accountID int64) error {
	return incrementAccountRPM(ctx, s.rpmCache, accountID)
}
