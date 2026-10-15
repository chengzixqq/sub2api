//go:build embed

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type htmlLoadProvider struct {
	load func(context.Context) (any, error)
}

func (p htmlLoadProvider) GetPublicSettingsForInjection(ctx context.Context) (any, error) {
	return p.load(ctx)
}

func htmlLoadRouter(t *testing.T, provider htmlLoadProvider) (*FrontendServer, http.Handler) {
	t.Helper()
	s, err := NewFrontendServer(provider)
	require.NoError(t, err)
	r := gin.New()
	r.Use(s.Middleware())
	return s, r
}

func readHTML(r http.Handler, ctx context.Context) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard", nil).WithContext(ctx))
	return w
}

func TestFrontendHTMLLoad_ColdRequestsShareSettings(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	_, r := htmlLoadRouter(t, htmlLoadProvider{load: func(ctx context.Context) (any, error) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return map[string]any{"site_name": "Shared settings"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}})
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := readHTML(r, context.Background())
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Shared settings") {
				t.Errorf("unexpected HTML response: status=%d", w.Code)
			}
		}()
	}
	<-entered
	// Keep the first read in flight while the concurrent requests join it.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	t.Logf("24 simultaneous cold HTML requests: settings reads=%d", calls.Load())
	require.EqualValues(t, 1, calls.Load())
}

func TestFrontendHTMLLoad_InvalidationRejectsStalePublication(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	s, r := htmlLoadRouter(t, htmlLoadProvider{load: func(ctx context.Context) (any, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
				return map[string]any{"site_name": "Old settings"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return map[string]any{"site_name": "Updated settings"}, nil
	}})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- readHTML(r, context.Background()) }()
	<-entered
	s.InvalidateCache()
	updated := readHTML(r, context.Background())
	close(release)
	old := <-done
	final := readHTML(r, context.Background())
	t.Logf("new request updated=%t; old in-flight response updated=%t; final cache updated=%t",
		strings.Contains(updated.Body.String(), "Updated settings"),
		strings.Contains(old.Body.String(), "Updated settings"),
		strings.Contains(final.Body.String(), "Updated settings"))
	require.Contains(t, updated.Body.String(), "Updated settings")
	require.Contains(t, old.Body.String(), "Updated settings")
	require.Contains(t, final.Body.String(), "Updated settings")
}

func TestFrontendHTMLLoad_CancelledWaiterDoesNotCancelSharedRead(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	_, r := htmlLoadRouter(t, htmlLoadProvider{load: func(ctx context.Context) (any, error) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return map[string]any{"site_name": "Surviving request"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- readHTML(r, ctx) }()
	<-entered
	cancel()
	<-firstDone
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { secondDone <- readHTML(r, context.Background()) }()
	close(release)
	second := <-secondDone
	require.Contains(t, second.Body.String(), "Surviving request")
	require.EqualValues(t, 1, calls.Load())
}
