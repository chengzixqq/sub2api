package service

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/tidwall/gjson"
)

type cacheControlSection int

// Logical TTL order. JSON members may occur in any physical order.
const (
	cacheTools cacheControlSection = iota
	cacheSystem
	cacheMessages
	cacheTop
)

type cacheControlBlock struct {
	block, cc, leaf gjson.Result
	index, content  int
	replacement     []byte
}

// Each list contains disjoint ranges of the original request in wire order.
type cacheControlBatch [4][]cacheControlBlock

// A nested gjson path can match a later duplicate member if the first one
// lacks its leaf. sjson writes through the first member. Keep the lookup
// separate from the target so duplicate keys keep their original wire bytes.
type cacheControlReader struct {
	batch    cacheControlBatch
	leafPath string
	fallback [3]map[[2]int]gjson.Result
}

func (r *cacheControlReader) block(section cacheControlSection, block gjson.Result, i, j int, primary bool) {
	if primary {
		cc := block.Get("cache_control")
		if cc.Exists() {
			b := cacheControlBlock{block: block, cc: cc, index: i, content: j}
			if r.leafPath != "" {
				b.leaf = block.Get(r.leafPath)
			}
			r.batch[section] = append(r.batch[section], b)
		}
		return
	}
	if r.leafPath == "" {
		return
	}
	leaf := block.Get(r.leafPath)
	if leaf.Exists() {
		if r.fallback[section] == nil {
			r.fallback[section] = make(map[[2]int]gjson.Result)
		}
		key := [2]int{i, j}
		if _, exists := r.fallback[section][key]; !exists {
			r.fallback[section][key] = leaf
		}
	}
}

// Only arrays produce breakpoint candidates. A duplicate ancestor can also
// be an object whose decimal member names satisfy gjson's indexed path.
func walkCacheControlIndices(value gjson.Result, primary bool, visit func(int, gjson.Result)) {
	if value.IsArray() {
		value.ForEach(func(i, v gjson.Result) bool {
			visit(int(i.Int()), v)
			return true
		})
	} else if !primary && value.IsObject() {
		value.ForEach(func(k, v gjson.Result) bool {
			name := k.String()
			if i, err := strconv.Atoi(name); err == nil && i >= 0 && strconv.Itoa(i) == name {
				visit(i, v)
			}
			return true
		})
	}
}

func (r *cacheControlReader) section(section cacheControlSection, value gjson.Result, primary bool) {
	if !primary && r.leafPath == "" {
		return
	}
	walkCacheControlIndices(value, primary, func(i int, item gjson.Result) {
		if section != cacheMessages {
			r.block(section, item, i, 0, primary)
			return
		}
		readContent := func(content gjson.Result, first bool) {
			walkCacheControlIndices(content, first, func(j int, block gjson.Result) { r.block(section, block, i, j, first) })
		}
		if r.leafPath == "" {
			readContent(item.Get("content"), primary)
			return
		}
		first := primary
		item.ForEach(func(key, content gjson.Result) bool {
			if key.Str == "content" {
				readContent(content, first)
				first = false
			}
			return true
		})
	})
}

func readCacheControlBatch(body []byte, key string) cacheControlBatch {
	root := gjson.Parse(openAIWSPayloadStringView(body))
	var r cacheControlReader
	if key != "" {
		r.leafPath = "cache_control." + key
	}
	var seen [3]bool
	root.ForEach(func(name, value gjson.Result) bool {
		var section cacheControlSection
		switch name.Str {
		case "tools":
			section = cacheTools
		case "system":
			section = cacheSystem
		case "messages":
			section = cacheMessages
		default:
			return true
		}
		r.section(section, value, !seen[section])
		seen[section] = true
		return true
	})
	for section, lookup := range r.fallback {
		if len(lookup) == 0 {
			continue
		}
		for i := range r.batch[section] {
			b := &r.batch[section][i]
			if !b.leaf.Exists() {
				b.leaf = lookup[[2]int{b.index, b.content}]
			}
		}
	}
	if cc := root.Get("cache_control"); cc.Exists() {
		b := cacheControlBlock{block: cc, cc: cc}
		if key != "" {
			b.leaf = root.Get(r.leafPath)
		}
		r.batch[cacheTop] = []cacheControlBlock{b}
	}
	return r.batch
}

// Edits inside cache_control use the value's own range, so long tool
// descriptions or message text are copied only when assembling the result.
func (b *cacheControlBlock) setField(body []byte, key string, value any) bool {
	next, ok := setJSONValueBytes(body[b.cc.Index:b.cc.Index+len(b.cc.Raw)], key, value)
	if ok {
		b.block = b.cc
		b.replacement = next
	}
	return ok
}

func (b *cacheControlBlock) deleteField(body []byte, key string) {
	b.replacement, _ = deleteJSONPathBytes(body[b.block.Index:b.block.Index+len(b.block.Raw)], key)
}

func (batch *cacheControlBatch) apply(body []byte) []byte {
	// Sort at most four sections. Array order inside each section is already
	// correct even when deletion priority runs backwards.
	var sections [][]cacheControlBlock
	size := len(body)
	for _, blocks := range batch {
		changed := false
		for i := range blocks {
			b := &blocks[i]
			if b.replacement != nil {
				changed = true
				size += len(b.replacement) - len(b.block.Raw)
			}
		}
		if changed {
			sections = append(sections, blocks)
		}
	}
	if len(sections) == 0 {
		return body
	}
	slices.SortFunc(sections, func(a, b []cacheControlBlock) int {
		return cmp.Compare(a[0].block.Index, b[0].block.Index)
	})
	out := make([]byte, 0, size)
	cursor := 0
	for _, blocks := range sections {
		for i := range blocks {
			b := &blocks[i]
			if b.replacement == nil {
				continue
			}
			out = append(out, body[cursor:b.block.Index]...)
			out = append(out, b.replacement...)
			cursor = b.block.Index + len(b.block.Raw)
		}
	}
	return append(out, body[cursor:]...)
}

// Tools have no thinking-block restriction. System thinking is always stripped.
// Preserved message thinking fields do not participate in the limit or TTL order.
func (b *cacheControlBlock) participates(section cacheControlSection, preserveThinking bool) bool {
	switch section {
	case cacheSystem:
		return b.block.Get("type").String() != "thinking"
	case cacheMessages:
		kind := b.block.Get("type").String()
		return kind != "thinking" && (!preserveThinking || !isThinkingCacheControlBlock(kind))
	default:
		return true
	}
}
