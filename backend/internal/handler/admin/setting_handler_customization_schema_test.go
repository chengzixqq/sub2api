//go:build unit

package admin

import (
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

func TestSettingHandler_CustomizationSchema(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/admin/settings/customization/schema", (&SettingHandler{}).GetClaudeCustomizationSchema)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/settings/customization/schema", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var response struct{ Data []map[string]any }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	want := []struct {
		key, group        string
		options           []string
		initial, official any
		risk              string
	}{
		{"fallback_policy", "compatibility", []string{"native_passthrough", "strict", "fable_native_passthrough"}, "native_passthrough", "strict", ""},
		{"beta_policy_mode", "compatibility", []string{"capability_aware", "official_strict", "client_passthrough"}, "capability_aware", "official_strict", ""},
		{"unknown_beta_action", "compatibility", []string{"pass_on_native_only", "filter", "pass"}, "pass_on_native_only", "filter", ""},
		{"thinking_prefilter_enabled", "thinking", nil, false, true, ""},
		{"thinking_signature_retry_enabled", "thinking", nil, false, false, ""},
		{"thinking_tool_downgrade_retry_enabled", "thinking", nil, false, false, ""},
		{"fingerprint_unification", "privacy", nil, true, true, ""},
		{"metadata_passthrough", "privacy", nil, true, false, ""},
		{"url_redaction_enabled", "privacy", nil, true, true, ""},
		{"context_management_missing_beta", "official_alignment", []string{"strip", "reject"}, "strip", "reject", "medium"},
		{"request_id_mode", "official_alignment", []string{"passthrough", "fill"}, "passthrough", "fill", "low"},
		{"cache_control_limit_mode", "official_alignment", []string{"trim", "passthrough", "reject"}, "trim", "reject", "medium"},
		{"unknown_beta_header", "official_alignment", []string{"passthrough", "reject"}, "passthrough", "reject", "high"},
		{"models_catalog", "official_alignment", []string{"synthetic", "official"}, "synthetic", "official", "none"},
		{"upstream_header_passthrough", "official_alignment", []string{"off", "on"}, "off", "on", "low"},
		{"thinking_cache_control", "official_alignment", []string{"strip", "passthrough", "reject"}, "strip", "reject", "medium"},
		{"bedrock_cc_context_management", "official_alignment", []string{"strip", "passthrough"}, "strip", "passthrough", "medium"},
	}
	require.Len(t, response.Data, len(want))
	for i, f := range want {
		got := response.Data[i]
		require.Len(t, got, 12)
		if f.key == "context_management_missing_beta" {
			require.Equal(t, []any{"apikey"}, got["account_types"])
		}
		require.Equal(t, f.group == "official_alignment" && f.official == "reject", got["hit_stats"])
		require.Equal(t, f.key, got["key"])
		require.Equal(t, f.group, got["group"])
		require.Equal(t, f.initial, got["default"])
		require.Equal(t, f.official, got["official_value"])
		require.Equal(t, f.group != "official_alignment", got["preset_managed"])
		require.Equal(t, f.key != "models_catalog", got["account_overridable"])
		require.Equal(t, f.key == "models_catalog", got["global_only"])
		require.Contains(t, []string{"none", "low", "medium", "high"}, got["risk"])
		if f.risk != "" {
			require.Equal(t, f.risk, got["risk"])
		}
		require.Contains(t, got, "options")
		if f.options == nil {
			require.Equal(t, "bool", got["kind"])
			require.Empty(t, got["options"])
		} else {
			require.Equal(t, "enum", got["kind"])
			options := make([]any, len(f.options))
			for j, v := range f.options {
				options[j] = v
			}
			require.Equal(t, options, got["options"])
		}
	}
}

func TestSettingHandler_ClaudeCustomizationRejectsInvalidGlobalAlignment(t *testing.T) {
	repo := &claudeCustomizationHandlerRepo{values: map[string]string{}}
	h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)
	cfg := service.DefaultClaudeCustomizationSettings()
	cfg.RequestIDMode = "invalid"
	rec := claudeCustomizationRequest(t, http.MethodPut, cfg, nil, h.UpdateClaudeCustomization)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "request_id_mode")
	require.Empty(t, repo.values)
}

func TestAccountHandler_ClaudeCustomizationFields_RejectInvalidAndGlobalOnly(t *testing.T) {
	admin := &claudeOverrideAdminService{stubAdminService: newStubAdminService(), account: &service.Account{ID: 7, Type: service.AccountTypeAPIKey}}
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for key, value := range map[string]string{"thinking_cache_control": "drop", "models_catalog": "official"} {
		rec := claudeCustomizationRequest(t, http.MethodPut, map[string]any{"overrides": map[string]any{key: value}}, gin.Params{{Key: "id", Value: "7"}}, h.UpdateClaudeCustomization)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), key)
	}
	require.Empty(t, admin.updates)
}

func TestSettingHandler_BedrockCCContextManagementPresets(t *testing.T) {
	for _, mode := range []string{"strip", "passthrough"} {
		for _, preset := range []string{"official", "magic", "custom"} {
			t.Run(mode+"/"+preset, func(t *testing.T) {
				repo := &claudeCustomizationHandlerRepo{values: map[string]string{
					service.SettingKeyClaudeCustomization: `{"preset":"magic","bedrock_cc_context_management":"` + mode + `"}`,
				}}
				h := NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)
				rec := claudeCustomizationRequest(t, http.MethodPost, map[string]any{"preset": preset}, nil, h.ApplyClaudeCustomizationPreset)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, mode, gjson.Get(rec.Body.String(), "data.global.bedrock_cc_context_management").String())
				// An older client saves an unrelated field, omitting the new switch.
				raw, err := json.Marshal(service.DefaultClaudeCustomizationSettings())
				require.NoError(t, err)
				var update map[string]any
				require.NoError(t, json.Unmarshal(raw, &update))
				delete(update, "bedrock_cc_context_management")
				update["preset"], update["metadata_passthrough"] = "custom", false
				rec = claudeCustomizationRequest(t, http.MethodPut, update, nil, h.UpdateClaudeCustomization)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, mode, gjson.Get(rec.Body.String(), "data.global.bedrock_cc_context_management").String())
			})
		}
	}
}
