//go:build unit

package service

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestClientMarkerCollisionPreservesBreakpoints(t *testing.T) {
	for _, mode := range []string{ClaudeCacheControlLimitPassthrough, ClaudeAlignReject} {
		for _, mimic := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mimic=%t", mode, mimic), func(t *testing.T) {
				body := batchFiveCacheBody("missing", 3)
				body = []byte(strings.Replace(string(body), `"cache_control":{`, `"cache_control":{"__sub2api_client_cache_control__":"client",`, 1))
				plan, _, _ := planCacheControlLimit(body, mode, "strip", mimic, false)
				if mimic {
					require.Equal(t, cacheControlTrimInjectedOnly, plan.scope)
				}
				sent := batchFiveForward(t, body, AccountTypeOAuth, mimic, false, mode, false)
				require.LessOrEqual(t, batchFiveWireSlots(sent), 4)
				require.Contains(t, string(sent), `"__sub2api_client_cache_control__":"client"`)
				for _, block := range gjson.GetBytes(sent, "messages").Array() {
					for _, item := range block.Get("content").Array() {
						if strings.HasPrefix(item.Get("text").Str, "block-") {
							require.True(t, item.Get("cache_control").Exists())
						}
					}
				}
			})
		}
	}
}
