package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type alignmentHitStore struct{ client *redis.Client }

func NewAlignmentHitStore(client *redis.Client) service.AlignmentHitStore {
	return &alignmentHitStore{client: client}
}

func (s *alignmentHitStore) IncrBy(ctx context.Context, batch map[string]int64, ttl time.Duration) error {
	_, err := s.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for key, n := range batch {
			p.IncrBy(ctx, key, n)
			p.Expire(ctx, key, ttl)
		}
		return nil
	})
	return err
}

func (s *alignmentHitStore) Read(ctx context.Context, keys []string) ([]int64, error) {
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(keys))
	for i, v := range values {
		if v == nil {
			continue
		}
		raw, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected alignment hit count type %T", v)
		}
		out[i], err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
