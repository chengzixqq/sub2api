//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// G06: user / group / account writes accept an optional Idempotency-Key. Without a key they behave exactly as before
// (the Web sends none); with a key a retry replays the first response and the write happens once.

func installMemoryIdempotency(t *testing.T) {
	t.Helper()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
}

func optionalWriteRequest(router *gin.Engine, method, path, body, key string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func responseDataID(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	var envelope struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return envelope.Data.ID
}

func responseReason(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return envelope.Reason
}

func requireNoIdempotencyHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for name := range rec.Header() {
		require.NotContains(t, http.CanonicalHeaderKey(name), "X-Idempotency", "a keyless request must not get idempotency headers")
	}
}

// assertKeyedReplay sends the same keyed request twice: the first runs, the second replays it.
func assertKeyedReplay(t *testing.T, router *gin.Engine, method, path, body, key string) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
	t.Helper()
	first := optionalWriteRequest(router, method, path, body, key)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))
	require.NotEmpty(t, first.Header().Get("X-Idempotency-Expires-At"))

	replay := optionalWriteRequest(router, method, path, body, key)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.NotEmpty(t, replay.Header().Get("X-Idempotency-Expires-At"))
	return first, replay
}

func TestKeyedExecutionTimeoutOnlyAppliesWithKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	contextWithKey := func(key string, set bool) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		if set {
			c.Request.Header.Set("Idempotency-Key", key)
		}
		return c
	}

	require.Zero(t, keyedExecutionTimeout(contextWithKey("", false), adminWriteExecutionTimeout))
	require.Zero(t, keyedExecutionTimeout(contextWithKey("   ", true), adminWriteExecutionTimeout))
	require.Equal(t, adminWriteExecutionTimeout, keyedExecutionTimeout(contextWithKey("phone-retry", true), adminWriteExecutionTimeout))
	require.Equal(t, 30*time.Second, adminWriteExecutionTimeout)
	require.Equal(t, 60*time.Second, adminBulkWriteExecutionTimeout)
	require.Equal(t, 120*time.Second, adminBatchRefreshExecutionTimeout)
}

func TestUserCreate_IdempotencyKeyReplaysSingleCreate(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	installMemoryIdempotency(t)
	body := `{"email":"g06@example.com","password":"secret-1","username":"g06"}`

	first, replay := assertKeyedReplay(t, router, http.MethodPost, "/api/v1/admin/users", body, "user-create-retry")

	require.Equal(t, 1, adminSvc.createUserCalls, "the keyed retry must not create a second user")
	require.Equal(t, responseDataID(t, first), responseDataID(t, replay))
}

func TestUserCreate_SameKeyDifferentBodyConflicts(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	installMemoryIdempotency(t)

	first := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/users", `{"email":"a@example.com","password":"secret-1"}`, "user-create-conflict")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/users", `{"email":"b@example.com","password":"secret-1"}`, "user-create-conflict")

	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", responseReason(t, second))
	require.Equal(t, 1, adminSvc.createUserCalls)
}

// The password is not part of the request fingerprint: the stored hash must not help an offline password guess.
func TestUserCreate_IdempotencyPayloadOmitsPassword(t *testing.T) {
	payload := createUserIdempotencyPayload(CreateUserRequest{Email: "a@example.com", Password: "secret-password-1"})

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-password-1")
	require.Contains(t, string(raw), "a@example.com")

	router, adminSvc := setupAdminRouter()
	installMemoryIdempotency(t)
	first := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/users", `{"email":"a@example.com","password":"secret-1"}`, "user-create-password")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	replay := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/users", `{"email":"a@example.com","password":"secret-2"}`, "user-create-password")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 1, adminSvc.createUserCalls)
}

func TestUserCreate_WithoutKeyUnchanged(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	installMemoryIdempotency(t)
	body := `{"email":"web@example.com","password":"secret-1"}`

	for i := 0; i < 2; i++ {
		rec := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/users", body, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		requireNoIdempotencyHeaders(t, rec)
		require.Equal(t, int64(100), responseDataID(t, rec))
	}
	require.Equal(t, 2, adminSvc.createUserCalls, "keyless callers run every time")
}

func TestUserUpdate_IdempotencyKeyReplaysWithoutConcurrency(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	installMemoryIdempotency(t)

	first, replay := assertKeyedReplay(t, router, http.MethodPut, "/api/v1/admin/users/1", `{"notes":"x"}`, "user-update-retry")

	require.Equal(t, 1, adminSvc.updateUserCalls, "the keyed retry must not update again")
	require.Equal(t, responseDataID(t, first), responseDataID(t, replay))

	noKey := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/1", `{"notes":"x"}`, "")
	require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
	requireNoIdempotencyHeaders(t, noKey)
	require.Equal(t, 2, adminSvc.updateUserCalls)
}

// The new password is not part of the PUT /admin/users/:id fingerprint either (G06 sends every keyed update through
// the coordinator): only whether a password is set counts, so the stored hash cannot help an offline password guess.
func TestUserUpdate_IdempotencyFingerprintOmitsPassword(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	repo := newMemoryIdempotencyRepoStub()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	body := func(password string) string {
		return `{"email":"u@example.com","password":"` + password + `","role":"user","concurrency":3}`
	}

	first := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/5", body("Abc12345"), "user-update-password-a")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, "Abc12345", adminSvc.lastUpdateUserInput.Password, "the service still receives the new password")
	second := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/5", body("Xyz98765"), "user-update-password-b")
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "Xyz98765", adminSvc.lastUpdateUserInput.Password)

	fingerprints := map[string]string{}
	for _, record := range repo.data {
		fingerprints[record.IdempotencyKeyHash] = record.RequestFingerprint
	}
	require.Len(t, fingerprints, 2)
	fingerprintA := fingerprints[service.HashIdempotencyKey("user-update-password-a")]
	require.NotEmpty(t, fingerprintA)
	require.Equal(t, fingerprintA, fingerprints[service.HashIdempotencyKey("user-update-password-b")],
		"the stored fingerprint must not depend on the password value")
	plaintext, err := service.BuildIdempotencyFingerprint(http.MethodPut, "/api/v1/admin/users/:id", "admin:0", struct {
		UserID int64             `json:"user_id"`
		Body   UpdateUserRequest `json:"body"`
	}{UserID: 5, Body: UpdateUserRequest{Email: "u@example.com", Password: "Abc12345", Role: "user", Concurrency: intPtr(3)}})
	require.NoError(t, err)
	require.NotEqual(t, plaintext, fingerprintA)

	// Same key, another password: a replay of the first update (as for POST /admin/users).
	replay := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/5", body("Other-111"), "user-update-password-a")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 2, adminSvc.updateUserCalls)

	// Same key without the password is a different request.
	conflict := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/5", `{"email":"u@example.com","role":"user","concurrency":3}`, "user-update-password-a")
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", responseReason(t, conflict))
}

// Without a password the payload marshals exactly like the pre-G06 {user_id, body} struct, so records stored by the
// Web's keyed concurrency edits keep replaying across the upgrade; the adjustment action id does not see the password.
func TestUserUpdateIdempotencyPayloadCompatibleAndRedacted(t *testing.T) {
	concurrency := 4
	req := UpdateUserRequest{Email: "u@example.com", Role: "user", Concurrency: &concurrency, AdjustmentNotes: "n"}
	legacy, err := json.Marshal(struct {
		UserID int64             `json:"user_id"`
		Body   UpdateUserRequest `json:"body"`
	}{UserID: 9, Body: req})
	require.NoError(t, err)
	current, err := json.Marshal(newUserUpdateIdempotencyPayload(9, req))
	require.NoError(t, err)
	require.JSONEq(t, string(legacy), string(current))
	require.Equal(t, string(legacy), string(current))

	withPassword := req
	withPassword.Password = "Abc12345"
	raw, err := json.Marshal(newUserUpdateIdempotencyPayload(9, withPassword))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "Abc12345")
	require.Contains(t, string(raw), `"password_set":true`)
	require.Equal(t, "Abc12345", withPassword.Password, "building the payload must not change the request")

	gin.SetMode(gin.TestMode)
	actionID := func(password string) string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/9", nil)
		c.Request.Header.Set("Idempotency-Key", "same-key")
		r := req
		r.Password = password
		id, derived := adminAdjustmentActionID(c, "admin.users.update", newUserUpdateIdempotencyPayload(9, r))
		require.True(t, derived)
		return id.String()
	}
	require.Equal(t, actionID("Abc12345"), actionID("Xyz98765"))
}

// The step-up gate stays in front of the idempotency wrapper: a keyed promotion without step-up never runs.
func TestUserUpdate_KeyedPromotionStillRequiresStepUp(t *testing.T) {
	router, adminSvc := setupRoleStepUpRouter(t)
	installMemoryIdempotency(t)

	rec := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/users/1", `{"role":"admin"}`, "promote-retry")

	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	require.Zero(t, adminSvc.updateUserCalls)
}

func TestGroupWrites_IdempotencyKeyReplays(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		calls  func(*stubAdminService) int
	}{
		{"create", http.MethodPost, "/api/v1/admin/groups", `{"name":"t-grp","platform":"anthropic"}`, func(s *stubAdminService) int { return len(s.createdGroups) }},
		{"update", http.MethodPut, "/api/v1/admin/groups/2", `{"name":"renamed"}`, func(s *stubAdminService) int { return len(s.updatedGroups) }},
		{"delete", http.MethodDelete, "/api/v1/admin/groups/2", "", func(s *stubAdminService) int { return len(s.deletedGroupIDs) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, adminSvc := setupAdminRouter()
			installMemoryIdempotency(t)

			first, replay := assertKeyedReplay(t, router, tc.method, tc.path, tc.body, "group-"+tc.name+"-retry")

			require.Equal(t, 1, tc.calls(adminSvc), "the keyed retry must not repeat the write")
			if tc.method == http.MethodDelete {
				require.JSONEq(t, first.Body.String(), replay.Body.String())
				require.Contains(t, replay.Body.String(), "Group deleted successfully")
			} else {
				require.Equal(t, responseDataID(t, first), responseDataID(t, replay))
			}

			noKey := optionalWriteRequest(router, tc.method, tc.path, tc.body, "")
			require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
			requireNoIdempotencyHeaders(t, noKey)
			require.Equal(t, 2, tc.calls(adminSvc))
		})
	}
}

// optionalLimitField has only unexported fields; without its own MarshalJSON every limit fingerprints as {} and a
// retry that only changes a limit would replay the old one instead of conflicting.
func TestGroupCreate_SameKeyDifferentDailyLimitConflicts(t *testing.T) {
	router, adminSvc := setupAdminRouter()
	installMemoryIdempotency(t)

	first := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/groups", `{"name":"limits","daily_limit_usd":5}`, "group-limit")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/groups", `{"name":"limits","daily_limit_usd":6}`, "group-limit")

	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", responseReason(t, second))
	require.Len(t, adminSvc.createdGroups, 1)
}

func TestOptionalLimitFieldMarshalJSONDistinguishesTriState(t *testing.T) {
	var req UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"daily_limit_usd":null,"weekly_limit_usd":5}`), &req))

	raw, err := json.Marshal(req)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))

	require.Contains(t, fields, "monthly_limit_usd")
	require.Nil(t, fields["monthly_limit_usd"], "omitted: no change")
	require.Equal(t, -1.0, fields["daily_limit_usd"], "explicit null: unlimited")
	require.Equal(t, 5.0, fields["weekly_limit_usd"])
}

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// opaqueJSONStructs lists struct types reachable from t that json.Marshal turns into {} (no exported field and no
// value-receiver MarshalJSON): such a field drops out of the idempotency fingerprint.
func opaqueJSONStructs(t reflect.Type, path string, seen map[reflect.Type]bool) []string {
	if t.Implements(jsonMarshalerType) {
		return nil
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return opaqueJSONStructs(t.Elem(), path+"[]", seen)
	case reflect.Struct:
	default:
		return nil
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	var found []string
	exported := 0
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() || field.Tag.Get("json") == "-" {
			continue
		}
		exported++
		found = append(found, opaqueJSONStructs(field.Type, path+"."+field.Name, seen)...)
	}
	if exported == 0 {
		found = append(found, path+" ("+t.String()+")")
	}
	return found
}

func TestOptionalIdempotentRequestPayloadsHaveNoOpaqueFields(t *testing.T) {
	payloads := []any{
		CreateUserRequest{},
		UpdateUserRequest{},
		userUpdateIdempotencyPayload{},
		CreateGroupRequest{},
		UpdateGroupRequest{},
		UpdateAccountRequest{},
		SetSchedulableRequest{},
		BulkUpdateAccountsRequest{},
		CreateAnnouncementRequest{},
	}
	for _, payload := range payloads {
		typ := reflect.TypeOf(payload)
		require.Empty(t, opaqueJSONStructs(typ, typ.Name(), map[reflect.Type]bool{}),
			"these fields marshal to {} and would not be part of the idempotency fingerprint")
	}
}

func newOptionalWriteAccountRouter(admin service.AdminService, grok service.GrokOAuthTokenService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAccountHandler(admin, nil, nil, nil, nil, grok, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.PUT("/api/v1/admin/accounts/:id", h.Update)
	router.POST("/api/v1/admin/accounts/:id/schedulable", h.SetSchedulable)
	router.POST("/api/v1/admin/accounts/bulk-update", h.BulkUpdate)
	router.POST("/api/v1/admin/accounts/:id/refresh", h.Refresh)
	router.POST("/api/v1/admin/accounts/batch-refresh", h.BatchRefresh)
	return router
}

func TestAccountUpdate_IdempotencyKeyReplays(t *testing.T) {
	adminSvc := newStubAdminService()
	router := newOptionalWriteAccountRouter(adminSvc, nil)
	installMemoryIdempotency(t)

	first, replay := assertKeyedReplay(t, router, http.MethodPut, "/api/v1/admin/accounts/3", `{"name":"renamed"}`, "account-update-retry")

	require.Equal(t, 1, adminSvc.updateAccountCalls, "the keyed retry must not update again")
	require.Equal(t, int64(3), responseDataID(t, first))
	require.Equal(t, responseDataID(t, first), responseDataID(t, replay))

	noKey := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/accounts/3", `{"name":"renamed"}`, "")
	require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
	requireNoIdempotencyHeaders(t, noKey)
	require.Equal(t, 2, adminSvc.updateAccountCalls)
}

func TestAccountUpdate_MixedChannelBodyUnchangedWithAndWithoutKey(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.updateAccountErr = newTestMixedChannelError()
	router := newOptionalWriteAccountRouter(adminSvc, nil)
	installMemoryIdempotency(t)
	body := `{"group_ids":[27]}`

	assertMixedChannelConflictBody(t, optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/accounts/3", body, ""))
	assertMixedChannelConflictBody(t, optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/accounts/3", body, "mixed-update"))

	// The keyed failure is recorded as retryable: an immediate retry with the same key backs off; confirming the risk
	// changes the body, so the client must use a new key.
	retry := optionalWriteRequest(router, http.MethodPut, "/api/v1/admin/accounts/3", body, "mixed-update")
	require.Equal(t, http.StatusConflict, retry.Code, retry.Body.String())
	require.Equal(t, "IDEMPOTENCY_RETRY_BACKOFF", responseReason(t, retry))
	require.NotEmpty(t, retry.Header().Get("Retry-After"))

	bulkSvc := newStubAdminService()
	bulkSvc.bulkUpdateAccountErr = newTestMixedChannelError()
	bulkRouter := newOptionalWriteAccountRouter(bulkSvc, nil)
	bulkBody := `{"account_ids":[1,2,3],"group_ids":[27]}`
	assertMixedChannelConflictBody(t, optionalWriteRequest(bulkRouter, http.MethodPost, "/api/v1/admin/accounts/bulk-update", bulkBody, ""))
	assertMixedChannelConflictBody(t, optionalWriteRequest(bulkRouter, http.MethodPost, "/api/v1/admin/accounts/bulk-update", bulkBody, "mixed-bulk"))
}

func TestAccountSetSchedulableAndBulkUpdate_IdempotencyKeyReplays(t *testing.T) {
	adminSvc := newStubAdminService()
	router := newOptionalWriteAccountRouter(adminSvc, nil)
	installMemoryIdempotency(t)

	first, replay := assertKeyedReplay(t, router, http.MethodPost, "/api/v1/admin/accounts/3/schedulable", `{"schedulable":false}`, "schedulable-retry")
	require.Equal(t, 1, adminSvc.setSchedulableCalls)
	require.Equal(t, responseDataID(t, first), responseDataID(t, replay))

	_, bulkReplay := assertKeyedReplay(t, router, http.MethodPost, "/api/v1/admin/accounts/bulk-update", `{"account_ids":[1,2],"schedulable":true}`, "bulk-retry")
	require.Equal(t, 1, adminSvc.bulkUpdateCalls)
	require.Contains(t, bulkReplay.Body.String(), `"success":2`)

	noKey := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/accounts/bulk-update", `{"account_ids":[1,2],"schedulable":true}`, "")
	require.Equal(t, http.StatusOK, noKey.Code, noKey.Body.String())
	requireNoIdempotencyHeaders(t, noKey)
	require.Equal(t, 2, adminSvc.bulkUpdateCalls)

	empty := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/accounts/bulk-update", `{"account_ids":[1]}`, "bulk-empty")
	require.Equal(t, http.StatusBadRequest, empty.Code, empty.Body.String())
	require.Contains(t, empty.Body.String(), "No updates provided")
}

func TestAccountRefresh_IdempotencyKeyReplaysSingleUpstreamRefresh(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = &service.Account{
		ID:          3,
		Name:        "grok",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh"},
	}
	grokOAuth := &grokRefreshOAuthStub{info: &service.GrokTokenInfo{AccessToken: "new", RefreshToken: "new-refresh", ExpiresAt: 1_800_000_000}}
	router := newOptionalWriteAccountRouter(adminSvc, grokOAuth)
	installMemoryIdempotency(t)

	first, replay := assertKeyedReplay(t, router, http.MethodPost, "/api/v1/admin/accounts/3/refresh", "", "refresh-retry")
	require.Equal(t, 1, grokOAuth.calls, "a keyed retry must not refresh the upstream token again")
	require.Equal(t, responseDataID(t, first), responseDataID(t, replay))

	for i := 0; i < 2; i++ {
		rec := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/accounts/3/refresh", "", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		requireNoIdempotencyHeaders(t, rec)
	}
	require.Equal(t, 3, grokOAuth.calls)
}

func TestAccountBatchRefresh_IdempotencyKeyReplays(t *testing.T) {
	adminSvc := newStubAdminService()
	router := newOptionalWriteAccountRouter(adminSvc, nil)
	installMemoryIdempotency(t)

	_, replay := assertKeyedReplay(t, router, http.MethodPost, "/api/v1/admin/accounts/batch-refresh", `{"account_ids":[11,12]}`, "batch-refresh-retry")
	require.Equal(t, 1, adminSvc.getAccountsByIDsCalls, "the keyed retry must not refresh again")
	var envelope struct {
		Data struct {
			Total   int `json:"total"`
			Success int `json:"success"`
			Failed  int `json:"failed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &envelope))
	require.Equal(t, 2, envelope.Data.Total)
	require.Zero(t, envelope.Data.Success)
	require.Equal(t, 2, envelope.Data.Failed, "non-OAuth accounts cannot be refreshed")

	empty := optionalWriteRequest(router, http.MethodPost, "/api/v1/admin/accounts/batch-refresh", `{"account_ids":[]}`, "batch-refresh-empty")
	require.Equal(t, http.StatusBadRequest, empty.Code, empty.Body.String())
	require.Contains(t, empty.Body.String(), "account_ids is required")
}

type groupUpdateHookAdminService struct {
	*stubAdminService
	onUpdate func(ctx context.Context)
}

func (s *groupUpdateHookAdminService) UpdateGroup(ctx context.Context, id int64, input *service.UpdateGroupInput) (*service.Group, error) {
	if s.onUpdate != nil {
		s.onUpdate(ctx)
	}
	return s.stubAdminService.UpdateGroup(ctx, id, input)
}

func newGroupUpdateHookRouter(svc *groupUpdateHookAdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/api/v1/admin/groups/:id", NewGroupHandler(svc, nil, nil).Update)
	return router
}

func sendGroupUpdate(router *gin.Engine, ctx context.Context, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/groups/2", bytes.NewBufferString(`{"name":"renamed"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// A keyed write keeps running and stores its result when the phone drops the connection, so the retry replays it.
func TestKeyedOptionalWrite_ClientDisconnectStillCommitsAndReplays(t *testing.T) {
	repo := &ctxCheckingIdempotencyRepo{memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub()}
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	svc := &groupUpdateHookAdminService{stubAdminService: newStubAdminService()}
	router := newGroupUpdateHookRouter(svc)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	var execErr error
	var hasDeadline bool
	svc.onUpdate = func(ctx context.Context) {
		cancelRequest() // the client goes away while the write is running
		execErr = ctx.Err()
		_, hasDeadline = ctx.Deadline()
	}

	first := sendGroupUpdate(router, requestCtx, "group-update-dropped")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.NoError(t, execErr, "an accepted keyed write must not be interrupted by the client")
	require.True(t, hasDeadline, "a keyed write is bounded by its execution timeout")

	svc.onUpdate = nil
	retry := sendGroupUpdate(router, context.Background(), "group-update-dropped")
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	require.Equal(t, "true", retry.Header().Get("X-Idempotency-Replayed"))
	require.Len(t, svc.updatedGroups, 1)
}

// Without a key nothing changes for the Web: the write stays on the request context with no extra deadline.
func TestKeylessOptionalWrite_StaysOnRequestContext(t *testing.T) {
	installMemoryIdempotency(t)
	svc := &groupUpdateHookAdminService{stubAdminService: newStubAdminService()}
	router := newGroupUpdateHookRouter(svc)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	var execErr error
	var hasDeadline bool
	svc.onUpdate = func(ctx context.Context) {
		cancelRequest()
		execErr = ctx.Err()
		_, hasDeadline = ctx.Deadline()
	}

	rec := sendGroupUpdate(router, requestCtx, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Error(t, execErr, "a keyless write must still follow the client's request context")
	require.False(t, hasDeadline)
	requireNoIdempotencyHeaders(t, rec)
}

// TestOptionalKeyedWriteHandlersUseKeyedHelpers keeps the G06 writes (and the H07 announcement create) on the helpers
// that only detach keyed requests.
// The money-write helpers detach every request (changing the Web path) and the required ones reject keyless calls.
func TestOptionalKeyedWriteHandlersUseKeyedHelpers(t *testing.T) {
	targets := map[string][]string{
		"user_handler.go":         {"UserHandler.Create", "UserHandler.Update"},
		"group_handler.go":        {"GroupHandler.Create", "GroupHandler.Update", "GroupHandler.Delete"},
		"account_handler.go":      {"AccountHandler.Update", "AccountHandler.SetSchedulable", "AccountHandler.BulkUpdate", "AccountHandler.Refresh", "AccountHandler.BatchRefresh"},
		"announcement_handler.go": {"AnnouncementHandler.Create"},
	}

	found := 0
	fset := token.NewFileSet()
	for file, methods := range targets {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		wanted := make(map[string]bool, len(methods))
		for _, method := range methods {
			wanted[method] = true
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			name := receiverTypeName(fn) + "." + fn.Name.Name
			if !wanted[name] {
				continue
			}
			found++
			usesKeyedHelper := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				switch ident.Name {
				case "executeAdminOptionalWrite", "executeAdminOptionalWriteJSON":
					usesKeyedHelper = true
				case "executeAdminIdempotent", "executeAdminIdempotentJSON", "executeAdminIdempotentWithTimeout",
					"executeAdminIdempotentJSONWithTimeout", "executeAdminMoneyWriteJSON", "executeAdminOptionalMoneyWriteJSON",
					"executeAdminOptionalBatchMoneyWrite":
					t.Errorf("%s calls %s; use executeAdminOptionalWrite / executeAdminOptionalWriteJSON", name, ident.Name)
				}
				return true
			})
			require.True(t, usesKeyedHelper, "%s must use executeAdminOptionalWrite or executeAdminOptionalWriteJSON", name)
		}
	}
	require.Equal(t, 11, found, "every guarded handler method must still exist")
}
