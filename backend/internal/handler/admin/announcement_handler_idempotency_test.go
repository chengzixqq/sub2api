//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// H07: POST /admin/announcements accepts an optional Idempotency-Key. Without a key it creates every time (the Web
// sends none); with a key a timed-out phone retry replays the first announcement instead of creating a duplicate.

const announcementCreatePath = "/api/v1/admin/announcements"

type announcementCreateRepoStub struct {
	service.AnnouncementRepository
	mu       sync.Mutex
	created  []service.Announcement
	onCreate func(ctx context.Context)
}

func (r *announcementCreateRepoStub) Create(ctx context.Context, a *service.Announcement) error {
	if r.onCreate != nil {
		r.onCreate(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a.ID = int64(len(r.created) + 1)
	now := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC).Add(time.Duration(a.ID) * time.Second)
	a.CreatedAt = now
	a.UpdatedAt = now
	r.created = append(r.created, *a)
	return nil
}

func (r *announcementCreateRepoStub) createCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.created)
}

const testAdminIDHeader = "X-Test-Admin-ID"

func newAnnouncementCreateRouter(repo *announcementCreateRepoStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAnnouncementHandler(service.NewAnnouncementService(repo, nil, nil, nil))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		adminID := int64(77)
		if raw := c.GetHeader(testAdminIDHeader); raw != "" {
			adminID, _ = strconv.ParseInt(raw, 10, 64)
		}
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: adminID})
		c.Next()
	})
	router.POST(announcementCreatePath, h.Create)
	return router
}

func sendAnnouncementCreate(router *gin.Engine, ctx context.Context, body, key string, adminID int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, announcementCreatePath, bytes.NewBufferString(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if adminID > 0 {
		req.Header.Set(testAdminIDHeader, strconv.FormatInt(adminID, 10))
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func installIdempotencyRepo(t *testing.T, repo service.IdempotencyRepository, observeOnly bool) {
	t.Helper()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = observeOnly
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
}

func TestAdminAnnouncementCreate_SameKeyReplaysWithoutCreatingTwice(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installMemoryIdempotency(t)

	first, replay := assertKeyedReplay(t, router, http.MethodPost, announcementCreatePath, `{"title":"t","content":"c"}`, "notice-create-retry")

	require.Equal(t, 1, repo.createCalls(), "the keyed retry must not create a second announcement")
	require.Equal(t, responseDataID(t, first), responseDataID(t, replay))
	require.JSONEq(t, first.Body.String(), replay.Body.String(), "the replay returns the first announcement unchanged")
	var envelope struct {
		Data struct {
			Title      string `json:"title"`
			Content    string `json:"content"`
			Status     string `json:"status"`
			NotifyMode string `json:"notify_mode"`
			CreatedBy  int64  `json:"created_by"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &envelope))
	require.Equal(t, "t", envelope.Data.Title)
	require.Equal(t, "c", envelope.Data.Content)
	require.Equal(t, service.AnnouncementStatusDraft, envelope.Data.Status)
	require.Equal(t, service.AnnouncementNotifyModeSilent, envelope.Data.NotifyMode)
	require.Equal(t, int64(77), envelope.Data.CreatedBy)
}

// Keys enforced (observe_only=false) must not break the Web, which never sends a key: keyless requests create every
// time, get no idempotency headers and stay on the request context.
func TestAdminAnnouncementCreate_WithoutKeyCreatesEachTimeEvenWhenKeysEnforced(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installIdempotencyRepo(t, newMemoryIdempotencyRepoStub(), false)
	var hasDeadline bool
	repo.onCreate = func(ctx context.Context) { _, hasDeadline = ctx.Deadline() }
	body := `{"title":"web","content":"c"}`

	first := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, body, "")
	second := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, body, "")

	for _, rec := range []*httptest.ResponseRecorder{first, second} {
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
		requireNoIdempotencyHeaders(t, rec)
	}
	require.NotEqual(t, responseDataID(t, first), responseDataID(t, second))
	require.Equal(t, 2, repo.createCalls(), "keyless callers create every time")
	require.False(t, hasDeadline, "a keyless create keeps running on the request context without an extra deadline")
}

func TestAdminAnnouncementCreate_SameKeyDifferentBodyConflicts(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installMemoryIdempotency(t)

	first := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, `{"title":"t","content":"c"}`, "notice-conflict")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, `{"title":"t2","content":"c"}`, "notice-conflict")

	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", responseReason(t, second))
	require.Equal(t, 1, repo.createCalls())
}

func TestAdminAnnouncementCreate_InvalidKeyRejectedBeforeCreate(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installMemoryIdempotency(t)

	for _, key := range []string{strings.Repeat("k", 129), "has space"} {
		rec := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, `{"title":"t","content":"c"}`, key)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, "IDEMPOTENCY_KEY_INVALID", responseReason(t, rec))
	}
	require.Zero(t, repo.createCalls())
}

// A keyed create fails closed when the idempotency store is down; the Web (no key) is not affected.
func TestAdminAnnouncementCreate_StoreUnavailable(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installIdempotencyRepo(t, storeUnavailableRepoStub{}, true)

	keyed := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, `{"title":"t","content":"c"}`, "notice-store-down")
	require.Equal(t, http.StatusServiceUnavailable, keyed.Code, keyed.Body.String())
	require.Equal(t, "IDEMPOTENCY_STORE_UNAVAILABLE", responseReason(t, keyed))
	require.Zero(t, repo.createCalls())

	keyless := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, `{"title":"t","content":"c"}`, "")
	require.Equal(t, http.StatusOK, keyless.Code, keyless.Body.String())
	require.Equal(t, 1, repo.createCalls())
}

// The phone drops the connection while the keyed create runs: the create still commits and its result is stored, so
// the retry with the same key replays it instead of returning IDEMPOTENCY_IN_PROGRESS for 24 hours.
func TestAdminAnnouncementCreate_ClientDisconnectStillCommitsAndReplays(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installIdempotencyRepo(t, &ctxCheckingIdempotencyRepo{memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub()}, true)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	var execErr error
	var hasDeadline bool
	repo.onCreate = func(ctx context.Context) {
		cancelRequest()
		execErr = ctx.Err()
		_, hasDeadline = ctx.Deadline()
	}
	body := `{"title":"t","content":"c","notify_mode":"popup","status":"active"}`

	first := sendAnnouncementCreate(router, requestCtx, body, "notice-dropped", 0)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.NoError(t, execErr, "an accepted keyed create must not be interrupted by the client")
	require.True(t, hasDeadline, "a keyed create is bounded by its execution timeout")

	repo.onCreate = nil
	retry := sendAnnouncementCreate(router, context.Background(), body, "notice-dropped", 0)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	require.Equal(t, "true", retry.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, responseDataID(t, first), responseDataID(t, retry))
	require.Equal(t, 1, repo.createCalls(), "a popup announcement must reach users once")
}

// The fingerprint includes the acting admin: another admin reusing the key conflicts and never sees the first result.
func TestAdminAnnouncementCreate_OtherAdminSameKeyGetsConflictNotReplay(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installMemoryIdempotency(t)
	body := `{"title":"secret plan","content":"c"}`

	first := sendAnnouncementCreate(router, context.Background(), body, "shared-key", 77)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	other := sendAnnouncementCreate(router, context.Background(), body, "shared-key", 78)

	require.Equal(t, http.StatusConflict, other.Code, other.Body.String())
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", responseReason(t, other))
	require.NotContains(t, other.Body.String(), "secret plan")
	require.Empty(t, other.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 1, repo.createCalls())
}

// Request binding stays in front of the idempotency wrapper: a malformed body is rejected without claiming the key,
// so the corrected request with the same key creates normally.
func TestAdminAnnouncementCreate_BindErrorDoesNotClaimKey(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installMemoryIdempotency(t)

	bad := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, `{"title":"t"}`, "notice-bind")
	require.Equal(t, http.StatusBadRequest, bad.Code, bad.Body.String())
	require.Contains(t, bad.Body.String(), "Invalid request")

	fixed := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, `{"title":"t","content":"c"}`, "notice-bind")
	require.Equal(t, http.StatusOK, fixed.Code, fixed.Body.String())
	require.Empty(t, fixed.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 1, repo.createCalls())
}

// A keyed business validation failure is not replayed: the error comes back as before and the key backs off for 5
// seconds, after which the same request may be retried with the same key.
func TestAdminAnnouncementCreate_KeyedValidationErrorBacksOff(t *testing.T) {
	repo := &announcementCreateRepoStub{}
	router := newAnnouncementCreateRouter(repo)
	installMemoryIdempotency(t)
	body := `{"title":"` + strings.Repeat("t", 201) + `","content":"c"}`

	first := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, body, "notice-invalid")
	require.Equal(t, http.StatusBadRequest, first.Code, first.Body.String())
	require.Equal(t, "ANNOUNCEMENT_TITLE_INVALID", responseReason(t, first))

	retry := optionalWriteRequest(router, http.MethodPost, announcementCreatePath, body, "notice-invalid")
	require.Equal(t, http.StatusConflict, retry.Code, retry.Body.String())
	require.Equal(t, "IDEMPOTENCY_RETRY_BACKOFF", responseReason(t, retry))
	require.NotEmpty(t, retry.Header().Get("Retry-After"))
	require.Zero(t, repo.createCalls())
}
