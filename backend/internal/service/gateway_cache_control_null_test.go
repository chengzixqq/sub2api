//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var batchFiveCacheValues = []struct{ name, raw string }{
	{"missing", ""}, {"null", "null"}, {"ephemeral", `{"type":"ephemeral"}`}, {"one_hour", `{"type":"ephemeral","ttl":"1h"}`},
}

func batchFiveSetTarget(t *testing.T, body []byte, target, raw string) ([]byte, string) {
	t.Helper()
	path := "tools.0.cache_control"
	if target == "message" {
		path = fmt.Sprintf("messages.0.content.%d.cache_control", len(gjson.GetBytes(body, "messages.0.content").Array())-1)
	}
	if raw != "" {
		var err error
		body, err = sjson.SetRawBytes(body, path, []byte(raw))
		require.NoError(t, err)
	}
	return body, path
}

func batchFiveInject(t *testing.T, body []byte, target string) []byte {
	t.Helper()
	if target == "tool" {
		return applyToolsLastCacheBreakpoint(body)
	}
	msg := gjson.GetBytes(body, "messages.0")
	return injectCacheControlOnLastContentBlock(body, 0, &msg, false)
}

func TestBatchFiveNullCacheInjection(t *testing.T) {
	for _, target := range []string{"tool", "message"} {
		for _, value := range batchFiveCacheValues {
			t.Run(target+"/"+value.name, func(t *testing.T) {
				body, path := batchFiveSetTarget(t, batchFiveCacheBody("missing", 0), target, value.raw)
				sent := batchFiveInject(t, body, target)
				row, err := json.Marshal(map[string]string{"id": target + "/" + value.name, "sent": string(sent)})
				require.NoError(t, err)
				t.Logf("INJECT %s", row)
				cc := gjson.GetBytes(sent, path)
				require.Equal(t, "ephemeral", cc.Get("type").String())
				ttl := claude.DefaultCacheControlTTL
				if value.name == "one_hour" {
					ttl = "1h"
				}
				require.Equal(t, ttl, cc.Get("ttl").String())
			})
		}
	}
}

// Null has no client marker. After injection it must be reclaimable in all
// modes, including when automatic caching occupies one of the four slots.
func TestBatchFiveNullInjectionIsReclaimable(t *testing.T) {
	for _, target := range []string{"tool", "message"} {
		for _, mode := range []string{"trim", "passthrough", "reject"} {
			for _, top := range []string{"missing", "object"} {
				for _, raw := range []string{"", "null"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", target, mode, top, raw), func(t *testing.T) {
						blocks := 4
						if top == "object" {
							blocks--
						}
						body, path := batchFiveSetTarget(t, batchFiveCacheBody(top, blocks), target, raw)
						plan, planned, _ := planCacheControlLimit(body, mode, "strip", true)
						if mode != "trim" {
							require.Equal(t, cacheControlTrimInjectedOnly, plan.scope)
						}
						injected := batchFiveInject(t, planned, target)
						require.Equal(t, "ephemeral", gjson.GetBytes(injected, path+".type").String())
						require.Equal(t, claude.DefaultCacheControlTTL, gjson.GetBytes(injected, path+".ttl").String())
						sent := plan.apply(injected)
						require.Equal(t, 4, batchFiveWireSlots(sent), "%s", sent)
						start := 0
						if mode == "trim" && target == "message" {
							// Historical trim removes the earliest message anchor.
							require.False(t, gjson.GetBytes(sent, "messages.0.content.0.cache_control").Exists())
							require.Equal(t, "ephemeral", gjson.GetBytes(sent, path+".type").String())
							start = 1
						} else {
							require.False(t, gjson.GetBytes(sent, path).Exists(), "%s", sent)
						}
						for i := start; i < blocks; i++ {
							require.Equal(t, "ephemeral", gjson.GetBytes(sent, fmt.Sprintf("messages.0.content.%d.cache_control.type", i)).String())
						}
						require.NotContains(t, string(sent), cacheControlClientMarkerKey)
					})
				}
			}
		}
	}
}

func TestBatchFiveNullCacheWire(t *testing.T) {
	for _, target := range []string{"tool", "message"} {
		for _, value := range batchFiveCacheValues {
			for _, mode := range []string{"trim", "passthrough", "reject"} {
				for _, count := range []bool{false, true} {
					for _, blocks := range []int{0, 2} {
						id := fmt.Sprintf("%s/%s/%s/count=%t/blocks=%d", target, value.name, mode, count, blocks)
						t.Run(id, func(t *testing.T) {
							body, _ := batchFiveSetTarget(t, batchFiveCacheBody("missing", blocks), target, value.raw)
							// Opt-in message rewriting clears the old anchors first;
							// the direct injector test above covers the null snapshot.
							sent := batchFiveForward(t, body, AccountTypeOAuth, true, count, mode, target == "message")
							row, err := json.Marshal(map[string]any{"id": id, "sent": string(sent), "slots": batchFiveWireSlots(sent)})
							require.NoError(t, err)
							t.Logf("NULLWIRE %s", row)
							path := "tools.0.cache_control"
							if target == "message" {
								messages := gjson.GetBytes(sent, "messages").Array()
								mi := len(messages) - 1
								ci := len(messages[mi].Get("content").Array()) - 1
								path = fmt.Sprintf("messages.%d.content.%d.cache_control", mi, ci)
							}
							require.Equal(t, "ephemeral", gjson.GetBytes(sent, path+".type").String(), "%s", sent)
							require.NotEmpty(t, gjson.GetBytes(sent, path+".ttl").String())
							require.LessOrEqual(t, batchFiveWireSlots(sent), 4, "%s", sent)
							require.NotContains(t, string(sent), cacheControlClientMarkerKey)
						})
					}
				}
			}
		}
	}
}

func TestBatchFiveTTLRewritesKeepNull(t *testing.T) {
	body := []byte(`{"cache_control":null,"system":[{"type":"text","text":"s","cache_control":null}],"tools":[{"name":"t","cache_control":null}],"messages":[{"role":"user","content":[{"type":"text","text":"m","cache_control":null}]}]}`)
	require.Equal(t, body, normalizeCacheControlTTLOrder(body, false))
	require.Equal(t, body, forceEphemeralCacheControlTTL(body, "5m"))
	require.Equal(t, body, forceEphemeralCacheControlTTL(body, "1h"))
}
