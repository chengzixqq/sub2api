package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memoryProxyLatencyCache struct {
	items   map[int64]*ProxyLatencyInfo
	deleted []int64
}

func newMemoryProxyLatencyCache() *memoryProxyLatencyCache {
	return &memoryProxyLatencyCache{items: map[int64]*ProxyLatencyInfo{}}
}

func (c *memoryProxyLatencyCache) GetProxyLatencies(_ context.Context, ids []int64) (map[int64]*ProxyLatencyInfo, error) {
	out := map[int64]*ProxyLatencyInfo{}
	for _, id := range ids {
		if info, ok := c.items[id]; ok {
			copied := *info
			out[id] = &copied
		}
	}
	return out, nil
}

func (c *memoryProxyLatencyCache) SetProxyLatency(_ context.Context, id int64, info *ProxyLatencyInfo) error {
	copied := *info
	c.items[id] = &copied
	return nil
}

func (c *memoryProxyLatencyCache) DeleteProxyLatency(_ context.Context, id int64) error {
	delete(c.items, id)
	c.deleted = append(c.deleted, id)
	return nil
}

type fakeProxyProber struct{ err error }

func (p fakeProxyProber) ProbeProxy(context.Context, string) (*ProxyExitInfo, int64, error) {
	if p.err != nil {
		return nil, 0, p.err
	}
	return &ProxyExitInfo{IP: "203.0.113.24", Country: "Hong Kong"}, 182, nil
}

type proxyLatencyRepoStub struct {
	ProxyRepository
	proxy *Proxy
}

func (r *proxyLatencyRepoStub) GetByIDScoped(context.Context, int64) (*Proxy, error) {
	copied := *r.proxy
	return &copied, nil
}

func TestTestProxy_ReturnsCheckedAtAndListExposesIt(t *testing.T) {
	cache := newMemoryProxyLatencyCache()
	svc := &adminServiceImpl{
		proxyRepo:         &proxyLatencyRepoStub{proxy: &Proxy{ID: 1, Protocol: "socks5", Host: "203.0.113.24", Port: 1080}},
		proxyProber:       fakeProxyProber{},
		proxyLatencyCache: cache,
	}
	before := time.Now().Unix()

	result, err := svc.TestProxy(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.GreaterOrEqual(t, result.CheckedAt, before)

	proxies := []ProxyWithAccountCount{{Proxy: Proxy{ID: 1}}, {Proxy: Proxy{ID: 2}}}
	svc.attachProxyLatency(context.Background(), proxies)
	require.NotNil(t, proxies[0].LatencyCheckedAt)
	require.Equal(t, result.CheckedAt, *proxies[0].LatencyCheckedAt)
	require.Nil(t, proxies[1].LatencyCheckedAt, "never probed")

	svc.proxyProber = fakeProxyProber{err: errors.New("dial failed")}
	failed, err := svc.TestProxy(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, failed.Success)
	require.NotZero(t, failed.CheckedAt, "failures carry checked_at too")
}
