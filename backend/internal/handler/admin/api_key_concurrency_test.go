package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// keyConcurrencyCacheStub answers only the API key slot reads; ConcurrencyService
// type-asserts the cache to APIKeyConcurrencyCache, so all three methods exist.
type keyConcurrencyCacheStub struct {
	service.ConcurrencyCache
	counts map[int64]int
	err    error
	calls  [][]int64
}

func (s *keyConcurrencyCacheStub) TrackAPIKeySlot(context.Context, int64, string) error { return nil }

func (s *keyConcurrencyCacheStub) ReleaseAPIKeySlot(context.Context, int64, string) error { return nil }

func (s *keyConcurrencyCacheStub) GetAPIKeyConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	s.calls = append(s.calls, append([]int64(nil), ids...))
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = s.counts[id]
	}
	return out, nil
}

var _ service.APIKeyConcurrencyCache = (*keyConcurrencyCacheStub)(nil)

const keyConcurrencySecret = "sk-abcdefghijklmnopqrstuv3f9a"

func keyConcurrencyAdminService() *stubAdminService {
	svc := newStubAdminService()
	svc.apiKeys = []service.APIKey{
		{ID: 4087, UserID: 1, Key: keyConcurrencySecret, Name: "脚本", Status: service.StatusActive},
		{ID: 4088, UserID: 1, Key: "sk-zyxwvutsrqponmlkjihgfe7c1d", Name: "备用", Status: service.StatusActive},
	}
	return svc
}

func newKeyConcurrencyRouter(svc *stubAdminService, cs *service.ConcurrencyService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/users/:id/api-keys", NewUserHandler(svc, cs, nil, nil, nil, nil, nil).GetUserAPIKeys)
	r.GET("/groups/:id/api-keys", NewGroupHandlerWithConfig(svc, nil, nil, cs, nil).GetGroupAPIKeys)
	r.GET("/api-keys/:id", ProvideAdminAPIKeyHandler(svc, nil, cs).Get)
	return r
}

type keyConcurrencyItem struct {
	ID                 int64  `json:"id"`
	Key                string `json:"key"`
	CurrentConcurrency int    `json:"current_concurrency"`
}

func keyConcurrencyListByID(t *testing.T, rec *httptest.ResponseRecorder) map[int64]keyConcurrencyItem {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items []keyConcurrencyItem `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	out := make(map[int64]keyConcurrencyItem, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		out[item.ID] = item
	}
	require.Len(t, out, 2, "the list keeps every key")
	return out
}

func keyConcurrencyGet(t *testing.T, rec *httptest.ResponseRecorder) keyConcurrencyItem {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data keyConcurrencyItem `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data
}

func serveKeyConcurrency(r *gin.Engine, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAdminAPIKeyLists_FillCurrentConcurrency(t *testing.T) {
	svc := keyConcurrencyAdminService()
	cache := &keyConcurrencyCacheStub{counts: map[int64]int{4087: 2}}
	r := newKeyConcurrencyRouter(svc, service.NewConcurrencyService(cache))

	for _, path := range []string{"/users/1/api-keys", "/groups/2/api-keys"} {
		items := keyConcurrencyListByID(t, serveKeyConcurrency(r, path))
		require.Equal(t, 2, items[4087].CurrentConcurrency, path)
		require.Equal(t, 0, items[4088].CurrentConcurrency, path)

		rec := serveKeyConcurrency(r, path+"?key_view=masked")
		require.NotContains(t, rec.Body.String(), keyConcurrencySecret, path)
		items = keyConcurrencyListByID(t, rec)
		require.Equal(t, 2, items[4087].CurrentConcurrency, path+" masked")
		require.Empty(t, items[4087].Key, path+" masked")
	}
	for i := range svc.apiKeys {
		require.Zero(t, svc.apiKeys[i].CurrentConcurrency, "handlers fill the response, not the service structs")
	}
}

func TestAdminAPIKeyGet_FillsCurrentConcurrency(t *testing.T) {
	cache := &keyConcurrencyCacheStub{counts: map[int64]int{4087: 2}}
	r := newKeyConcurrencyRouter(keyConcurrencyAdminService(), service.NewConcurrencyService(cache))

	require.Equal(t, 2, keyConcurrencyGet(t, serveKeyConcurrency(r, "/api-keys/4087")).CurrentConcurrency)

	rec := serveKeyConcurrency(r, "/api-keys/4087?key_view=masked")
	require.NotContains(t, rec.Body.String(), keyConcurrencySecret)
	require.Equal(t, 2, keyConcurrencyGet(t, rec).CurrentConcurrency)

	require.Equal(t, 0, keyConcurrencyGet(t, serveKeyConcurrency(r, "/api-keys/4088")).CurrentConcurrency)
	require.Equal(t, []int64{4088}, cache.calls[len(cache.calls)-1])
	require.Equal(t, http.StatusNotFound, serveKeyConcurrency(r, "/api-keys/999").Code)
}

func TestAdminAPIKeyLists_ConcurrencyBatchedOncePerPage(t *testing.T) {
	for _, path := range []string{"/users/1/api-keys", "/groups/2/api-keys"} {
		cache := &keyConcurrencyCacheStub{counts: map[int64]int{4087: 1, 4088: 3}}
		r := newKeyConcurrencyRouter(keyConcurrencyAdminService(), service.NewConcurrencyService(cache))

		items := keyConcurrencyListByID(t, serveKeyConcurrency(r, path))
		require.Equal(t, 1, items[4087].CurrentConcurrency, path)
		require.Equal(t, 3, items[4088].CurrentConcurrency, path)
		require.Len(t, cache.calls, 1, path+": one batch read per page, not one per key")
		require.ElementsMatch(t, []int64{4087, 4088}, cache.calls[0], path)
	}
}

func TestAdminAPIKeyLists_EmptyPageSkipsConcurrencyRead(t *testing.T) {
	svc := keyConcurrencyAdminService()
	svc.apiKeys = nil
	cache := &keyConcurrencyCacheStub{counts: map[int64]int{4087: 2}}
	r := newKeyConcurrencyRouter(svc, service.NewConcurrencyService(cache))

	rec := serveKeyConcurrency(r, "/users/1/api-keys")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"items":[]`)
	require.Empty(t, cache.calls)
}

func TestAdminAPIKeyLists_ConcurrencyReadFailureKeepsZero(t *testing.T) {
	cache := &keyConcurrencyCacheStub{counts: map[int64]int{4087: 2}, err: errors.New("redis down")}
	r := newKeyConcurrencyRouter(keyConcurrencyAdminService(), service.NewConcurrencyService(cache))

	for _, path := range []string{"/users/1/api-keys", "/groups/2/api-keys"} {
		items := keyConcurrencyListByID(t, serveKeyConcurrency(r, path))
		require.Equal(t, 0, items[4087].CurrentConcurrency, path)
		require.Equal(t, 0, items[4088].CurrentConcurrency, path)
	}
	require.Equal(t, 0, keyConcurrencyGet(t, serveKeyConcurrency(r, "/api-keys/4087")).CurrentConcurrency)
	require.Len(t, cache.calls, 3, "every endpoint tried the read")
}

func TestAdminAPIKeyLists_NilConcurrencyServiceKeepsZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := keyConcurrencyAdminService()
	r := gin.New()
	r.GET("/users/:id/api-keys", NewUserHandler(svc, nil, nil, nil, nil, nil, nil).GetUserAPIKeys)
	r.GET("/groups/:id/api-keys", NewGroupHandler(svc, nil, nil).GetGroupAPIKeys)
	r.GET("/api-keys/:id", NewAdminAPIKeyHandler(svc, nil).Get)
	r.GET("/provided/api-keys/:id", ProvideAdminAPIKeyHandler(svc, nil, nil).Get)

	for _, path := range []string{"/users/1/api-keys", "/groups/2/api-keys"} {
		items := keyConcurrencyListByID(t, serveKeyConcurrency(r, path))
		require.Equal(t, 0, items[4087].CurrentConcurrency, path)
	}
	for _, path := range []string{"/api-keys/4087", "/provided/api-keys/4087"} {
		require.Equal(t, 0, keyConcurrencyGet(t, serveKeyConcurrency(r, path)).CurrentConcurrency, path)
	}
}
