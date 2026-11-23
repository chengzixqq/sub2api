package service

import "bytes"

// The limit needs an upper bound across the body. Thinking only needs candidate
// keys inside messages. Both are decided by the same cache_control anchor walk.
type cacheControlCandidates struct {
	literalKeys int
	thinking    bool
	escapedName bool
}

func scanCacheControl(body []byte, messages jsonRange) cacheControlCandidates {
	if hasAlignmentNameEscape(body) {
		return cacheControlCandidates{thinking: true, escapedName: true}
	}
	var found cacheControlCandidates
	const prefix, anchor = `"cache`, `_control"`
	for offset := 0; offset < len(body); {
		i := bytes.Index(body[offset:], []byte(anchor))
		if i < 0 {
			break
		}
		i += offset
		offset = i + len(anchor)
		if !bytes.HasSuffix(body[:i], []byte(prefix)) {
			continue
		}
		found.literalKeys++
		// The exact count beyond the limit is determined by the structured
		// check. Stop inspecting block prefixes once that check is required.
		if found.literalKeys > maxCacheControlBlocks {
			found.thinking = true
			break
		}
		inMessages := !messages.exists() || (messages.start <= i && i < messages.end)
		if inMessages && !found.thinking && !insideNonThinkingBlock(body[:i-len(prefix)]) {
			found.thinking = true
		}
	}
	if found.thinking {
		window := body
		if messages.exists() {
			window = body[messages.start:messages.end]
		}
		found.thinking = containsThinkingLiteral(window)
	}
	return found
}

// Search at a less common suffix so ordinary type/text keys do not make
// the full-body fallback repeatedly compare the beginning of "thinking".
func containsThinkingLiteral(body []byte) bool {
	const prefix, suffix = "thi", "nking"
	for {
		i := bytes.Index(body, []byte(suffix))
		if i < 0 {
			return false
		}
		if bytes.HasSuffix(body[:i], []byte(prefix)) {
			return true
		}
		body = body[i+len(suffix):]
	}
}

// A literal type-first prefix can only introduce an actual object in valid
// JSON. If that object still contains this key, gjson will read its first
// type. Nested objects and ambiguous boundaries fall back to the full check.
func insideNonThinkingBlock(before []byte) bool {
	// Skip a preceding string value in one step so braces inside tool output
	// cannot be mistaken for the boundary of its containing object.
	if bytes.HasSuffix(before, []byte(`",`)) {
		end := len(before) - 2
		for end > 0 {
			quote := bytes.LastIndexByte(before[:end], '"')
			if quote < 0 {
				break
			}
			start := quote
			for start > 0 && before[start-1] == '\\' {
				start--
			}
			if (quote-start)%2 == 0 {
				if quote > 0 && before[quote-1] == ':' && !bytes.HasSuffix(before[:quote], []byte(`{"type":`)) {
					before = before[:quote]
				}
				break
			}
			end = start
		}
	}
	start := bytes.LastIndexByte(before, '{')
	if start < 0 {
		return false
	}
	tail := before[start:]
	if !bytes.HasPrefix(tail, []byte(`{"type":"text"`)) && !bytes.HasPrefix(tail, []byte(`{"type":"tool_result"`)) {
		return false
	}
	return bytes.IndexByte(tail, '}') < 0
}

// Encoded quotes are string content. They cannot delimit a JSON member or
// complete any of the names tested here, all of which contain only letters
// and underscores. A literal backslash preceding u must have odd parity.
func hasAlignmentNameEscape(body []byte) bool {
	i := bytes.Index(body, []byte(`\u00`))
	if i < 0 {
		return false
	}
	for ; i+5 < len(body); i++ {
		if body[i] != '\\' || body[i+1] != 'u' || body[i+2] != '0' || body[i+3] != '0' {
			continue
		}
		hi, lo := hexNibble(body[i+4]), hexNibble(body[i+5])
		ch := hi<<4 | lo
		if hi >= 0 && lo >= 0 && (ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '_') {
			start := i
			for start > 0 && body[start-1] == '\\' {
				start--
			}
			if (i-start)%2 == 0 {
				return true
			}
		}
		i += 5
	}
	return false
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}
