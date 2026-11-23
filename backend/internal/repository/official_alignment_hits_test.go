//go:build unit

package repository

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAlignmentHitRedisFlushExpiryAndFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	defer client.Close()
	hits := service.NewAlignmentHitRecorder(NewAlignmentHitStore(client))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	hits.Start()
	hits.Record(c, service.ClaudeFieldUnknownBetaHeader, 7, "reject")
	hits.Stop()
	require.Len(t, mr.Keys(), 2)
	for _, scope := range []string{"all", "7"} {
		key := "official_align:hits:unknown_beta_header:" + scope + ":" + time.Now().UTC().Format("2006010215")
		n, err := mr.Get(key)
		require.NoError(t, err)
		require.Equal(t, "1", n)
		require.Equal(t, 8*24*time.Hour, mr.TTL(key))
	}
	got, err := hits.Summary(t.Context(), nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, got[service.ClaudeFieldUnknownBetaHeader].Last7d)
	mr.Close()
	hits = service.NewAlignmentHitRecorder(NewAlignmentHitStore(client))
	hits.Start()
	hits.Record(c, service.ClaudeFieldThinkingCacheControl, 7, "strip")
	hits.Stop()
	got, err = hits.Summary(t.Context(), nil)
	require.Error(t, err)
	require.Nil(t, got)
}
