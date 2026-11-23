//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestContextFilterMissingBeta(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, mode := range []string{"strip", "reject"} {
			for _, scope := range []string{BetaPolicyScopeAll, BetaPolicyScopeAPIKey} {
				for _, token := range []string{matrixCM, matrixCompact} {
					for _, client := range []string{"", matrixCM, matrixCompact, matrixBoth} {
						for _, field := range []bool{false, true} {
							name := fmt.Sprintf("%s/%s/%s/%s/client=%s/field=%t", path, mode, scope, token, client, field)
							t.Run(name, func(t *testing.T) { checkContextFilter(t, path, mode, scope, token, client, field) })
						}
					}
				}
			}
		}
	}
}

func TestContextFilterSecondHeaderLine(t *testing.T) {
	policies := []struct {
		name   string
		scope  string
		strict bool
	}{
		{name: "all", scope: BetaPolicyScopeAll},
		{name: "apikey", scope: BetaPolicyScopeAPIKey},
		{name: "strict", scope: BetaPolicyScopeAll, strict: true},
	}
	bodies := []struct {
		name  string
		field string
	}{
		{name: "empty_object", field: `"context_management":{}`},
		{name: "edits", field: `"context_management":{"edits":[{"type":"clear_thinking_20251015"}]}`},
		{name: "null", field: `"context_management":null`},
		{name: "array", field: `"context_management":[]`},
		{name: "string", field: `"context_management":"value"`},
		{name: "escaped_key", field: `"context_managem\u0065nt":{}`},
	}
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, mode := range []string{"strip", "reject"} {
			for _, policy := range policies {
				for _, body := range bodies {
					name := fmt.Sprintf("%s/%s/%s/%s", path, mode, policy.name, body.name)
					t.Run(name, func(t *testing.T) {
						checkContextFilterSecondHeaderLine(t, path, mode, policy.scope, policy.strict, body.field)
					})
				}
			}
		}
	}
}

func checkContextFilterSecondHeaderLine(t *testing.T, path, mode, scope string, strict bool, field string) {
	t.Helper()
	upstream := &betaRouteUpstream{}
	upstream.resp = nativeAnthropicBufferedResponse()
	svc := fallbackNativeGatewayForTest(t, upstream, nil)
	repo := &betaMatrixRepo{}
	svc.settingService = NewSettingService(repo, svc.cfg)
	rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: scope, BetaToken: matrixCM, Action: BetaPolicyActionFilter}}})
	require.NoError(t, err)
	require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
	account := fallbackNativeAccountForTest(false)
	fields := map[string]any{}
	if mode == "reject" {
		fields["context_management_missing_beta"] = mode
	}
	if strict {
		fields["beta_policy_mode"] = ClaudeBetaOfficialStrict
		fields["unknown_beta_action"] = ClaudeUnknownBetaFilter
	}
	account.Extra[AccountClaudeCustomizationExtraKey] = fields
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Add("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	c.Request.Header.Add("Anthropic-Beta", matrixCM)
	body := `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],` + field + `}`
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	if path == "/v1/messages" {
		_, err = svc.Forward(t.Context(), c, account, parsed)
	} else {
		err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
	}
	id := rec.Header().Get("request-id")
	response := rec.Body.String()
	if id != "" {
		response = strings.ReplaceAll(response, id, "REQUEST_ID")
	}
	var beta []string
	if upstream.lastReq != nil {
		beta = upstream.lastReq.Header["anthropic-beta"]
	}
	t.Logf("%s status=%d calls=%d reads=%d beta=%q sent=%q received=%q content-type=%q", t.Name(), rec.Code, upstream.calls, repo.reads, beta, upstream.lastBody, response, rec.Header().Get("Content-Type"))
	if mode == "reject" {
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.ErrorIs(t, err, ErrLocalAnthropicRejected)
		require.Zero(t, upstream.calls)
		require.Regexp(t, officialIDPattern, id)
		require.Equal(t, `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"},"request_id":"`+id+`"}`, rec.Body.String())
		return
	}
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, upstream.calls)
	require.NotContains(t, strings.Join(beta, ","), matrixCM)
	require.False(t, gjson.GetBytes(upstream.lastBody, "context_management").Exists())
}

func checkContextFilter(t *testing.T, path, mode, scope, token, client string, field bool) {
	upstream := &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
	svc := fallbackNativeGatewayForTest(t, upstream, nil)
	repo := &fallbackPolicySettingRepo{}
	svc.settingService = NewSettingService(repo, svc.cfg)
	rules, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{Scope: scope, BetaToken: token, Action: BetaPolicyActionFilter}}})
	require.NoError(t, err)
	require.NoError(t, repo.Set(t.Context(), SettingKeyBetaPolicySettings, string(rules)))
	a := fallbackNativeAccountForTest(false)
	a.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{"context_management_missing_beta": mode}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", path, nil)
	if client != "" {
		c.Request.Header.Set("Anthropic-Beta", client)
	}
	body := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]`
	if field {
		body += `,"context_management":{}`
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body+"}")), PlatformAnthropic)
	require.NoError(t, err)
	if path == "/v1/messages" {
		_, err = svc.Forward(t.Context(), c, a, parsed)
	} else {
		err = svc.ForwardCountTokens(t.Context(), c, a, parsed)
	}
	id := rec.Header().Get("request-id")
	response := rec.Body.String()
	if id != "" {
		response = strings.ReplaceAll(response, id, "REQUEST_ID")
	}
	header := ""
	if upstream.lastReq != nil {
		header = getHeaderRaw(upstream.lastReq.Header, "anthropic-beta")
	}
	t.Logf("FILTER %s %d header=%q sent=%q received=%q", t.Name(), rec.Code, header, upstream.lastBody, response)
	if mode == "reject" && field && client == "" {
		require.ErrorIs(t, err, ErrLocalAnthropicRejected)
		require.Nil(t, upstream.lastReq)
		require.Equal(t, 400, rec.Code)
		require.Regexp(t, officialIDPattern, id)
		require.Equal(t, `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"},"request_id":"REQUEST_ID"}`, response)
		return
	}
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, 200, rec.Code)
	require.False(t, containsBetaToken(header, token))
	require.Equal(t, field && hasContextManagementBeta(header), gjson.GetBytes(upstream.lastBody, "context_management").Exists())
}
