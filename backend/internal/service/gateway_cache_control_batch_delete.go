package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// deleteCacheControlPaths preserves the original sjson member deletion,
// including key order, escape spellings and whitespace.
func deleteCacheControlPaths(body []byte, paths []string) []byte {
	if len(paths) <= maxCacheControlBlocks {
		for _, path := range paths {
			body = deleteCacheControlMembers(body, path)
		}
		return body
	}
	selected := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		selected[path] = struct{}{}
	}
	batch := readCacheControlBatch(body, "")
	for section := cacheTools; section < cacheTop; section++ {
		blocks := batch[section]
		for i := range blocks {
			b := &blocks[i]
			var path string
			switch section {
			case cacheTools:
				path = fmt.Sprintf("tools.%d.cache_control", b.index)
			case cacheSystem:
				path = fmt.Sprintf("system.%d.cache_control", b.index)
			case cacheMessages:
				path = fmt.Sprintf("messages.%d.content.%d.cache_control", b.index, b.content)
			}
			if _, ok := selected[path]; ok {
				block := body[b.block.Index : b.block.Index+len(b.block.Raw)]
				b.replacement = deleteCacheControlMembers(block, "cache_control")
			}
		}
	}
	return batch.apply(body)
}

func deleteCacheControlMembers(body []byte, path string) []byte {
	for {
		next, ok := deleteJSONPathBytes(body, path)
		// sjson may leave the bytes intact when a duplicate ancestor shadows
		// the path. Only repeat a deletion that actually shortened the body.
		if !ok || len(next) >= len(body) {
			return body
		}
		body = next
		if !cacheControlMemberRemains(body, path) {
			return body
		}
	}
}

func cacheControlMemberRemains(body []byte, path string) bool {
	view := openAIWSPayloadStringView(body)
	if !strings.Contains(view, `\`) || !gjson.Valid(view) {
		return gjson.Get(view, path).Exists()
	}
	// gjson unescapes even unrelated key names while searching. Walk the
	// same first-parent indices as sjson without allocating decoded names.
	value := rawJSONView(strings.TrimSpace(view))
	for key := range strings.SplitSeq(path, ".") {
		if len(value) == 0 {
			return false
		}
		if value[0] != '[' {
			value = value.field(key)
			continue
		}
		index, err := strconv.Atoi(key)
		if err != nil || index < 0 {
			return false
		}
		var element rawJSONView
		value.any(func(item rawJSONView) bool {
			if index == 0 {
				element = item
				return true
			}
			index--
			return false
		})
		value = element
	}
	return value != ""
}
