//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Count the actual wire slots independently of the production path collector.
func batchFiveWireSlots(body []byte) int {
	root := gjson.ParseBytes(body)
	count := 0
	add := func(cc gjson.Result) {
		if cc.Exists() && cc.Type != gjson.Null {
			count++
		}
	}
	add(root.Get("cache_control"))
	root.Get("system").ForEach(func(_, block gjson.Result) bool { add(block.Get("cache_control")); return true })
	root.Get("tools").ForEach(func(_, block gjson.Result) bool { add(block.Get("cache_control")); return true })
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool { add(block.Get("cache_control")); return true })
		return true
	})
	return count
}

type batchFiveSettingRepo struct{ *fallbackPolicySettingRepo }

func (r *batchFiveSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string)
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func batchFiveForward(t *testing.T, body []byte, accountType string, mimic, count bool, mode string, rewrite bool) []byte {
	t.Helper()
	response := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":1}}`
	path := "/v1/messages"
	if count {
		path += "/count_tokens"
		response = `{"input_tokens":42}`
	}
	up := &fallbackSequenceUpstream{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}}}
	svc := fallbackNativeGatewayForTest(t, up, nil)
	settings := DefaultClaudeCustomizationSettings()
	settings.Preset = ClaudePresetCustom
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	var policy map[string]any
	require.NoError(t, json.Unmarshal(raw, &policy))
	policy["cache_control_limit_mode"] = mode
	raw, err = json.Marshal(policy)
	require.NoError(t, err)
	svc.settingService = NewSettingService(&batchFiveSettingRepo{&fallbackPolicySettingRepo{values: map[string]string{SettingKeyClaudeCustomization: string(raw), SettingKeyRewriteMessageCacheControl: fmt.Sprint(rewrite)}}}, svc.cfg)
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
	account := fallbackNativeAccountForTest(false)
	account.Type = accountType
	account.Credentials["access_token"] = "test-oauth-token"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("User-Agent", "third-party-client/1.0")
	if !mimic {
		c.Request = c.Request.WithContext(SetClaudeCodeClient(c.Request.Context(), true))
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	if count {
		err = svc.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	} else {
		_, err = svc.Forward(c.Request.Context(), c, account, parsed)
	}
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, up.bodies, 1)
	return up.bodies[0]
}

func batchFiveCacheBody(top string, blocks int) []byte {
	var content []map[string]any
	for i := 0; i < blocks; i++ {
		content = append(content, map[string]any{"type": "text", "text": fmt.Sprintf("block-%d", i), "cache_control": map[string]string{"type": "ephemeral"}})
	}
	content = append(content, map[string]any{"type": "text", "text": "last"})
	body := map[string]any{
		"model": "claude-sonnet-4-5", "max_tokens": 16,
		"system":   []map[string]string{{"type": "text", "text": "client instructions"}},
		"tools":    []map[string]any{{"name": "lookup", "description": "lookup", "input_schema": map[string]string{"type": "object"}}},
		"messages": []map[string]any{{"role": "user", "content": content}},
	}
	if top == "object" {
		body["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	if top == "null" {
		body["cache_control"] = nil
	}
	raw, _ := json.Marshal(body)
	return raw
}

func TestBatchFiveTrimTopLevelSlots(t *testing.T) {
	for _, tc := range []struct{ name, tools string }{
		{"null", `{"name":"a","cache_control":null}`},
		{"deferred", `{"name":"a","defer_loading":true,"cache_control":{"type":"ephemeral"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"cache_control":{"type":"ephemeral"},"tools":[` + tc.tools + `,{"name":"last","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"content":[{"type":"text","cache_control":{}},{"type":"text","cache_control":{}}]}]}`)
			require.Equal(t, stripDeferredToolCacheControl(body), stripDeferredToolCacheControl(enforceCacheControlLimit(body)))
		})
	}
	for _, top := range []string{"missing", "null", "object"} {
		for blocks := 0; blocks <= 6; blocks++ {
			for _, mimic := range []bool{false, true} {
				for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
					for _, count := range []bool{false, true} {
						id := fmt.Sprintf("%s/blocks=%d/mimic=%t/%s/count=%t", top, blocks, mimic, accountType, count)
						t.Run(id, func(t *testing.T) {
							body := batchFiveCacheBody(top, blocks)
							sent := batchFiveForward(t, body, accountType, mimic, count, "trim", false)
							row, err := json.Marshal(map[string]any{"id": id, "sent": string(sent), "slots": batchFiveWireSlots(sent)})
							require.NoError(t, err)
							t.Logf("CACHE %s", row)
							// Non-mimic count_tokens reserves a top slot only for exactly four client blocks.
							trim := !count || (mimic && accountType != AccountTypeAPIKey)
							if trim {
								require.LessOrEqual(t, batchFiveWireSlots(sent), 4, "%s", sent)
							} else {
								expected := blocks
								if top == "object" && blocks != 4 {
									expected++
								}
								require.Equal(t, expected, batchFiveWireSlots(sent))
							}
							require.Equal(t, gjson.GetBytes(body, "cache_control").Raw, gjson.GetBytes(sent, "cache_control").Raw)
						})
					}
				}
			}
		}
	}
}
