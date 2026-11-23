//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type claudeCustomizationHandlerRepo struct {
	values map[string]string
}

func (r *claudeCustomizationHandlerRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *claudeCustomizationHandlerRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}
func (r *claudeCustomizationHandlerRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *claudeCustomizationHandlerRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *claudeCustomizationHandlerRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *claudeCustomizationHandlerRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *claudeCustomizationHandlerRepo) Delete(context.Context, string) error { return nil }

func claudeCustomizationRequest(t *testing.T, method string, body any, params gin.Params, handle func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, "/api/v1/admin/customization", reader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	handle(c)
	return rec
}

func TestSettingHandler_ClaudeCustomizationCacheControlLimitMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &claudeCustomizationHandlerRepo{values: map[string]string{}}
	h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	rec := claudeCustomizationRequest(t, http.MethodGet, nil, nil, h.GetClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "null", gjson.Get(rec.Body.String(), "data.hits").Raw)
	require.Equal(t, "trim", gjson.Get(rec.Body.String(), "data.global.cache_control_limit_mode").String())
	require.Equal(t, "trim", gjson.Get(rec.Body.String(), "data.defaults.cache_control_limit_mode").String())

	policy := service.DefaultClaudeCustomizationSettings()
	policy.Preset = service.ClaudePresetCustom
	policy.CacheControlLimitMode = "passthrough"
	rec = claudeCustomizationRequest(t, http.MethodPut, policy, nil, h.UpdateClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "passthrough", gjson.Get(rec.Body.String(), "data.global.cache_control_limit_mode").String())
	stored := repo.values[service.SettingKeyClaudeCustomization]
	require.Equal(t, "passthrough", gjson.Get(stored, "cache_control_limit_mode").String())

	policy.CacheControlLimitMode = "drop"
	rec = claudeCustomizationRequest(t, http.MethodPut, policy, nil, h.UpdateClaudeCustomization)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "cache_control_limit_mode")
	require.Equal(t, stored, repo.values[service.SettingKeyClaudeCustomization], "an invalid value must not be stored")

	rec = claudeCustomizationRequest(t, http.MethodPost, map[string]any{"preset": "magic"}, nil, h.ApplyClaudeCustomizationPreset)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "passthrough", gjson.Get(rec.Body.String(), "data.global.cache_control_limit_mode").String())
	rec = claudeCustomizationRequest(t, http.MethodPost, map[string]any{"preset": "official"}, nil, h.ApplyClaudeCustomizationPreset)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "passthrough", gjson.Get(rec.Body.String(), "data.global.cache_control_limit_mode").String())
	rec = claudeCustomizationRequest(t, http.MethodPost, nil, nil, h.ResetClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "trim", gjson.Get(rec.Body.String(), "data.global.cache_control_limit_mode").String())

	// A stored policy from before this field existed reads back as trim.
	repo.values[service.SettingKeyClaudeCustomization] = `{"preset":"official","fallback_policy":"strict","beta_policy_mode":"official_strict","unknown_beta_action":"filter"}`
	rec = claudeCustomizationRequest(t, http.MethodGet, nil, nil, h.GetClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "trim", gjson.Get(rec.Body.String(), "data.global.cache_control_limit_mode").String())
}

// claudeOverrideAdminService persists UpdateAccountExtra so the handler's
// read-after-write response reflects the stored override.
type claudeOverrideAdminService struct {
	*stubAdminService
	account *service.Account
	updates []map[string]any
}

func (s *claudeOverrideAdminService) GetAccount(context.Context, int64) (*service.Account, error) {
	copied := *s.account
	return &copied, nil
}

func (s *claudeOverrideAdminService) UpdateAccountExtra(_ context.Context, _ int64, updates map[string]any) error {
	s.updates = append(s.updates, updates)
	extra := map[string]any{}
	for key, value := range s.account.Extra {
		extra[key] = value
	}
	for key, value := range updates {
		if key == service.AccountClaudeCustomizationExtraKey {
			merged, _ := extra[key].(map[string]any)
			if merged == nil {
				merged = map[string]any{}
			}
			for k, v := range value.(map[string]any) {
				if v == nil {
					delete(merged, k)
				} else {
					merged[k] = v
				}
			}
			extra[key] = merged
		} else {
			extra[key] = value
		}
	}
	s.account.Extra = extra
	return nil
}

func TestAccountHandler_ClaudeCustomizationCacheControlLimitModeOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &claudeCustomizationHandlerRepo{values: map[string]string{}}
	settings := service.NewSettingService(repo, &config.Config{})
	admin := &claudeOverrideAdminService{
		stubAdminService: newStubAdminService(),
		account:          &service.Account{ID: 7, Name: "claude", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Extra: map[string]any{}},
	}
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetClaudeCustomizationService(settings)
	params := gin.Params{{Key: "id", Value: "7"}}

	// No override: the account inherits the global trim.
	rec := claudeCustomizationRequest(t, http.MethodGet, nil, params, h.GetClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "null", gjson.Get(rec.Body.String(), "data.hits").Raw)
	require.Equal(t, "trim", gjson.Get(rec.Body.String(), "data.effective.cache_control_limit_mode").String())
	require.False(t, gjson.Get(rec.Body.String(), "data.overrides.cache_control_limit_mode").Exists())

	rec = claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{"cache_control_limit_mode": "passthrough"}}, params, h.UpdateClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "passthrough", gjson.Get(rec.Body.String(), "data.overrides.cache_control_limit_mode").String())
	require.Equal(t, "passthrough", gjson.Get(rec.Body.String(), "data.effective.cache_control_limit_mode").String())
	require.Equal(t, "trim", gjson.Get(rec.Body.String(), "data.global.cache_control_limit_mode").String())
	require.Equal(t, map[string]any{"cache_control_limit_mode": "passthrough"}, admin.updates[0][service.AccountClaudeCustomizationExtraKey])

	rec = claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{"url_redaction_enabled": true}}, params, h.UpdateClaudeCustomization)
	require.Equal(t, "passthrough", gjson.Get(rec.Body.String(), "data.overrides.cache_control_limit_mode").String())
	for _, invalid := range []any{"drop", 1} {
		rec = claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{"cache_control_limit_mode": invalid}}, params, h.UpdateClaudeCustomization)
		require.Equal(t, http.StatusBadRequest, rec.Code, "%v", invalid)
	}
	require.Len(t, admin.updates, 2, "rejected overrides must not be written")

	// Removing the key restores inheritance; the global mode then applies.
	policy := service.DefaultClaudeCustomizationSettings()
	policy.Preset = service.ClaudePresetCustom
	policy.CacheControlLimitMode = "passthrough"
	require.NoError(t, settings.SetClaudeCustomizationSettings(context.Background(), policy))
	rec = claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{"cache_control_limit_mode": "trim"}}, params, h.UpdateClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "trim", gjson.Get(rec.Body.String(), "data.effective.cache_control_limit_mode").String())
	rec = claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{"cache_control_limit_mode": nil}}, params, h.UpdateClaudeCustomization)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "passthrough", gjson.Get(rec.Body.String(), "data.effective.cache_control_limit_mode").String())
	require.False(t, gjson.Get(rec.Body.String(), "data.overrides.cache_control_limit_mode").Exists())
}

func TestCustomizationInvalidFields(t *testing.T) {
	repo := &claudeCustomizationHandlerRepo{values: map[string]string{service.SettingKeyClaudeCustomization: `{"request_id_mode":17,"thinking_cache_control":"reject","cache_control_limit_mode":"invalid"}`}}
	handler := NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)
	rec := claudeCustomizationRequest(t, http.MethodGet, nil, nil, handler.GetClaudeCustomization)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "reject", gjson.Get(rec.Body.String(), "data.global.thinking_cache_control").String())
	require.Len(t, gjson.Get(rec.Body.String(), "data.invalid_fields").Array(), 2)
}

type deniedCustomizationAccount struct{ *claudeOverrideAdminService }

func (*deniedCustomizationAccount) GetAccount(context.Context, int64) (*service.Account, error) {
	return nil, service.ErrAccountNotFound
}
func TestCustomizationRequiresScopedAccountBeforeWrite(t *testing.T) {
	admin := &deniedCustomizationAccount{&claudeOverrideAdminService{account: &service.Account{}}}
	handler := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec := claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{}}, gin.Params{{Key: "id", Value: "7"}}, handler.UpdateClaudeCustomization)
	require.Equal(t, 404, rec.Code)
	require.Empty(t, admin.updates)
}

func TestOldAccountClientPreservesAlignmentFields(t *testing.T) {
	stored := map[string]any{}
	patch := map[string]any{}
	for _, field := range service.ClaudeCustomizationFields() {
		if field.Group == service.ClaudeFieldGroupOfficialAlignment {
			stored[field.Key] = field.OfficialValue
		} else {
			patch[field.Key] = field.Default
		}
	}
	admin := &claudeOverrideAdminService{account: &service.Account{Type: service.AccountTypeAPIKey, Extra: map[string]any{service.AccountClaudeCustomizationExtraKey: stored}}}
	handler := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetClaudeCustomizationService(service.NewSettingService(nil, nil))
	rec := claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": patch}, gin.Params{{Key: "id", Value: "7"}}, handler.UpdateClaudeCustomization)
	require.Equal(t, 200, rec.Code)
	for _, field := range service.ClaudeCustomizationFields() {
		if field.Group == service.ClaudeFieldGroupOfficialAlignment {
			require.Equal(t, field.OfficialValue, gjson.Get(rec.Body.String(), "data.overrides."+field.Key).Value())
		}
	}
	admin.account.Type = service.AccountTypeOAuth
	rec = claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{"context_management_missing_beta": "reject"}}, gin.Params{{Key: "id", Value: "7"}}, handler.UpdateClaudeCustomization)
	require.Equal(t, 400, rec.Code)
	require.Len(t, admin.updates, 1)
	rec = claudeCustomizationRequest(t, http.MethodGet, nil, gin.Params{{Key: "id", Value: "7"}}, handler.GetClaudeCustomization)
	require.Contains(t, gjson.Get(rec.Body.String(), "data.ignored_overrides").Raw, "context_management_missing_beta")
	require.Equal(t, "strip", gjson.Get(rec.Body.String(), "data.effective.context_management_missing_beta").String())
}

func (r *claudeCustomizationHandlerRepo) RepairClaudeCustomizationField(_ context.Context, key string) error {
	var obj map[string]any
	if err := json.Unmarshal([]byte(r.values[service.SettingKeyClaudeCustomization]), &obj); err != nil {
		return err
	}
	if key == "preset" {
		obj[key] = "custom"
	} else {
		delete(obj, key)
	}
	b, err := json.Marshal(obj)
	r.values[service.SettingKeyClaudeCustomization] = string(b)
	return err
}

func TestGlobalFieldResetAndPresetFallback(t *testing.T) {
	repo := &claudeCustomizationHandlerRepo{values: map[string]string{service.SettingKeyClaudeCustomization: `{"preset":"garbled","fallback_policy":"strict","request_id_mode":"bad"}`}}
	svc := service.NewSettingService(repo, nil)
	h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/customization/reset/:field", h.ResetClaudeCustomizationField)
	for _, key := range []string{"unknown_field", "request_id_mode"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("POST", "/customization/reset/"+key, nil))
		if key == "unknown_field" {
			require.Equal(t, 400, rec.Code)
			continue
		}
		require.Equal(t, 200, rec.Code)
		require.Equal(t, "custom", gjson.Get(rec.Body.String(), "data.global.preset").Str)
		require.Equal(t, "strict", gjson.Get(rec.Body.String(), "data.global.fallback_policy").Str)
		require.False(t, gjson.Get(repo.values[service.SettingKeyClaudeCustomization], key).Exists())
	}
	repo.values[service.SettingKeyClaudeCustomization] = `{"preset":"garbled","fallback_policy":"strict","beta_policy_mode":"client_passthrough","metadata_passthrough":false}`
	before, invalid, err := svc.ClaudeCustomizationWithInvalidFields(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"preset"}, invalid)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest("POST", "/customization/reset/preset", nil))
	require.Equal(t, 200, resp.Code)
	after, invalid, err := svc.ClaudeCustomizationWithInvalidFields(t.Context())
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Equal(t, before, after)
	require.Equal(t, "custom", gjson.Get(repo.values[service.SettingKeyClaudeCustomization], "preset").Str)
	repo.values[service.SettingKeyClaudeCustomization] = "{"
	rec := claudeCustomizationRequest(t, "GET", nil, nil, h.GetClaudeCustomization)
	require.Equal(t, "CLAUDE_CUSTOMIZATION_CORRUPT", gjson.Get(rec.Body.String(), "reason").Str)
	rec = claudeCustomizationRequest(t, "POST", nil, nil, h.ResetClaudeCustomization)
	require.Equal(t, 200, rec.Code)
}
