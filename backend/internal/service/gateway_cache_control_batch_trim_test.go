//go:build unit

package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchTrimMatchesSequentialDeletes(t *testing.T) {
	// The roots are intentionally out of priority order. Include first,
	// middle, last, escaped and null members in different blocks.
	body := []byte(`{ "messages" : [{"content":[
	 { "cache_control" : null , "type":"text" },
	 { "type":"text", "cache\u005fcontrol":{}, "other":1 },
	 { "type":"text" , "cache_control":{},"other":false },
	 { "cache_control":{} }, { "type":"text","cache_control":{} }
	]}], "system":[{"type":"text", "cache_control":{}}],
	"tools":[{"name":"a","cache_control":true},{"cache_control":{},"name":"b"}] }`)
	_, messages, tools, system := collectCacheControlPaths(body, cacheControlLimitPlan{})
	for remove := 1; remove <= len(messages)+len(tools)+len(system); remove++ {
		want := body
		priority := []string{tools[1], tools[0]}
		priority = append(priority, messages...)
		priority = append(priority, system...)
		for _, path := range priority[:remove] {
			var ok bool
			want, ok = deleteJSONPathBytes(want, path)
			require.True(t, ok)
		}
		got, remaining, changed := trimCacheControlPaths(body, remove, tools, messages, system)
		require.True(t, changed)
		require.Zero(t, remaining)
		require.Equal(t, string(want), string(got))
	}
}

// gjson reads a compound path through duplicate members until it finds the
// leaf. sjson updates the first matching member at each level.
func TestCacheControlBatchDuplicateMembers(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"content":[{"cache_control":{"type":"ephemeral"},"cache_control":{"ttl":"1h"}}]}]}`,
		`{"tools":[{"cache_control":{"type":"ephemeral"}}],"tools":{"0":{"cache_control":{"ttl":"1h"}}}}`,
		`{"messages":[{"content":[{"cache_control":{"type":"ephemeral"}}],"content":[{"cache_control":{"ttl":"1h"}}]}]}`,
		`{"cache_control":{"type":"ephemeral"},"cache_control":{"ttl":"1h"}}`,
	} {
		got := normalizeCacheControlTTLOrder([]byte(body), false)
		want := legacyNormalizeCacheControlTTLOrder([]byte(body))
		require.Equal(t, string(want), string(got))
		require.Contains(t, string(got), `"type":"ephemeral","ttl":"1h"`)
	}
}

func TestDeleteSelectedCacheControlMembers(t *testing.T) {
	for _, count := range []int{1, 5} {
		for _, keys := range []string{
			`"cache_control":{}`,
			`"cache_control":null, "cache\u005fcontrol":{}`,
			`"cache\u005fcontrol":{}, "cache_control":false, "cache_control":7`,
		} {
			t.Run(fmt.Sprintf("%d/%s", count, keys), func(t *testing.T) {
				var blocks, paths, want []string
				for i := range count {
					blocks = append(blocks, `{ "name":"\u0061", `+keys+`, "other" : true }`)
					paths = append(paths, fmt.Sprintf("tools.%d.cache_control", i))
					want = append(want, `{ "name":"\u0061", "other" : true }`)
				}
				unselected := `{"cache_control":0,"cache_control":1}`
				body := `{"tools":[` + strings.Join(append(blocks, unselected), ",") + `]}`
				expected := `{"tools":[` + strings.Join(append(want, unselected), ",") + `]}`
				require.Equal(t, expected, string(deleteCacheControlPaths([]byte(body), paths)))
			})
		}
	}
}
