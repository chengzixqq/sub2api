package service

import (
	"strconv"
	"strings"
)

// rawJSONView borrows a value from validated JSON. Field presence and short
// protocol identifiers can be checked without unescaping arbitrary text.
type rawJSONView string

func (v rawJSONView) hasValue() bool {
	return v != "" && v != "null" && v != `""`
}

func (v rawJSONView) hasString() bool {
	return len(v) > 2 && v[0] == '"'
}

// All protocol keys and type names compared here are short ASCII identifiers.
// Match escaped spellings without allocating storage for arbitrary wire text.
func (v rawJSONView) identifier(buf *[32]byte) string {
	if len(v) < 2 || v[0] != '"' {
		return ""
	}
	s := string(v[1 : len(v)-1])
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	n := 0
	for len(s) > 0 {
		ch, _, tail, err := strconv.UnquoteChar(s, '"')
		if err != nil || ch > 127 || n == len(buf) {
			return ""
		}
		buf[n] = byte(ch)
		n++
		s = tail
	}
	return openAIWSPayloadStringView(buf[:n])
}

// valueEnd also handles primitive array elements. The WS scanner's primitive
// branch is for object members, whose delimiters are comma and closing brace.
func (v rawJSONView) valueEnd(i int) int {
	switch v[i] {
	case '{', '[', '"':
		return skipOpenAIWSJSONValue([]byte(v), i)
	}
	end := strings.IndexAny(string(v[i:]), ",]}")
	if end < 0 {
		return len(v)
	}
	return i + end
}

func (v rawJSONView) field(path string) rawJSONView {
	if len(v) == 0 || v[0] != '{' {
		return ""
	}
	name, rest, nested := strings.Cut(path, ".")
	var scratch [32]byte
	for i := skipOpenAIWSJSONSpace([]byte(v), 1); v[i] != '}'; {
		end := scanOpenAIWSJSONString([]byte(v), i)
		match := v[i:end].identifier(&scratch) == name
		colon := skipOpenAIWSJSONSpace([]byte(v), end)
		start := skipOpenAIWSJSONSpace([]byte(v), colon+1)
		end = v.valueEnd(start)
		if match {
			item := rawJSONView(strings.TrimSpace(string(v[start:end])))
			if !nested {
				return item
			}
			// A compound gjson path can match a later duplicate parent when
			// the first parent lacks its leaf.
			if leaf := item.field(rest); leaf != "" {
				return leaf
			}
		}
		i = skipOpenAIWSJSONSpace([]byte(v), end)
		if v[i] == ',' {
			i = skipOpenAIWSJSONSpace([]byte(v), i+1)
		}
	}
	return ""
}

// hasNonNullFieldAfter looks for a matching same-level member after a known
// value in this object. Offset is relative to this view and points just past
// that value. The shortest possible suffix is ,"key":0}.
func (v rawJSONView) hasNonNullFieldAfter(offset int, key string) bool {
	if len(v)-offset < len(key)+6 {
		return false
	}
	var scratch [32]byte
	for i := skipOpenAIWSJSONSpace([]byte(v), offset); v[i] == ','; {
		name := skipOpenAIWSJSONSpace([]byte(v), i+1)
		end := scanOpenAIWSJSONString([]byte(v), name)
		colon := skipOpenAIWSJSONSpace([]byte(v), end)
		start := skipOpenAIWSJSONSpace([]byte(v), colon+1)
		if v[start] != 'n' && v[name:end].identifier(&scratch) == key {
			return true
		}
		i = skipOpenAIWSJSONSpace([]byte(v), v.valueEnd(start))
	}
	return false
}

// Compatible upstreams sometimes supply one object in an array field.
func (v rawJSONView) any(hasContent func(rawJSONView) bool) bool {
	if len(v) == 0 || v[0] != '[' {
		return v != "" && v != "null" && hasContent(v)
	}
	for i := skipOpenAIWSJSONSpace([]byte(v), 1); v[i] != ']'; {
		end := v.valueEnd(i)
		if hasContent(rawJSONView(strings.TrimSpace(string(v[i:end])))) {
			return true
		}
		i = skipOpenAIWSJSONSpace([]byte(v), end)
		if v[i] == ',' {
			i = skipOpenAIWSJSONSpace([]byte(v), i+1)
		}
	}
	return false
}
