//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Reference predicates copied from the earlier implementation. Keep their early returns as
// well as their gjson semantics so a shortcut cannot change observable results.
func referenceFirstThinkingCacheControlPath(body []byte) (msgIndex, contentIndex int, blockType string, ok bool) {
	if !bytes.Contains(body, cacheControlKeyBytes) && !bytes.Contains(body, jsonUnicodeEscapeBytes) {
		return
	}
	messages := gjson.Get(openAIWSPayloadStringView(body), "messages")
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(i, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(j, block gjson.Result) bool {
			cc := block.Get("cache_control")
			if cc.Exists() && cc.Type != gjson.Null {
				kind := block.Get("type").String()
				if isThinkingCacheControlBlock(kind) {
					msgIndex, contentIndex, blockType, ok = int(i.Int()), int(j.Int()), strings.Clone(kind), true
					return false
				}
			}
			return true
		})
		return !ok
	})
	return
}

func referenceCountClientCacheControlBreakpoints(body []byte) int {
	if !bytes.Contains(body, jsonUnicodeEscapeBytes) && bytes.Count(body, cacheControlKeyBytes) <= maxCacheControlBlocks {
		return 0
	}
	root := gjson.Parse(openAIWSPayloadStringView(body))
	count := 0
	if cc := root.Get("cache_control"); cc.Exists() && cc.Type != gjson.Null {
		count++
	}
	countBlocks := func(blocks gjson.Result) {
		if blocks.IsArray() {
			blocks.ForEach(func(_, block gjson.Result) bool {
				switch block.Get("type").String() {
				case "thinking", "redacted_thinking":
				default:
					if cc := block.Get("cache_control"); cc.Exists() && cc.Type != gjson.Null {
						count++
					}
				}
				return true
			})
		}
	}
	countBlocks(root.Get("system"))
	if messages := root.Get("messages"); messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			countBlocks(message.Get("content"))
			return true
		})
	}
	if tools := root.Get("tools"); tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			if cc := tool.Get("cache_control"); cc.Exists() && cc.Type != gjson.Null {
				count++
			}
			return true
		})
	}
	return count
}

func referenceMissingContextManagementBeta(header http.Header, body []byte) bool {
	return !hasContextManagementBeta(anthropicBetaHeader(header)) && gjson.GetBytes(body, "context_management").Exists()
}

// Compare the shortcut's actual return value as well as the production
// overflow decision. BASE sometimes returns a sub-limit count for escaped JSON.
func compareAlignmentPredicates(t *testing.T, body []byte, beta string) {
	t.Helper()
	header := http.Header{"Anthropic-Beta": {beta}}
	i, j, kind, hit := referenceFirstThinkingCacheControlPath(body)
	expected := referenceCountClientCacheControlBreakpoints(body)
	if count := countClientCacheControlBreakpoints(body); count != expected {
		t.Fatalf("count %d, BASE %d; body %s", count, expected, body)
	}
	if got, want := MissingContextManagementBeta(header, body), referenceMissingContextManagementBeta(header, body); got != want {
		t.Fatalf("missing beta %t, BASE %t; body %s", got, want, body)
	}
	msg := gjson.GetBytes(body, "messages")
	window := missingJSONRange()
	var raw []byte
	if msg.IsArray() {
		window = rangeFromResult(msg)
		if window.exists() {
			raw = body[window.start:window.end]
		}
	}
	// Exercise both the queue predicate and the shared Forward/count_tokens scan.
	for _, bounds := range []jsonRange{missingJSONRange(), window} {
		candidates := scanCacheControl(body, bounds)
		if !candidates.mayOverflow() && expected > maxCacheControlBlocks {
			t.Fatalf("missed overflow %d; body %s", expected, body)
		}
		var mi, ci int
		var blockType string
		var ok bool
		if candidates.thinking {
			if bounds.exists() {
				mi, ci, blockType, ok = findThinkingCacheControlPath(body, raw)
			} else {
				mi, ci, blockType, ok = firstThinkingCacheControlPath(body, nil)
			}
		}
		if mi != i || ci != j || blockType != kind || ok != hit {
			t.Fatalf("thinking (%d,%d,%q,%t), BASE (%d,%d,%q,%t); body %s", mi, ci, blockType, ok, i, j, kind, hit, body)
		}
	}
}

var alignmentDiffBlocks = []string{
	`"{\u0022type\u0022:\u0022thinking\u0022,\u0022cache_control\u0022:{}}"`,
	`"{\"type\":\"thinking\",\"cache_control\u0022:{}}"`,
	`{"type":"text","text":"say \"cache_control\": and thinking","cache_control":null}`,
	`{"type":"thinking","cache_control":{}}`,
	`{"type":"redacted_thinking","cache_control":[]}`,
	`{"cache_control":"ephemeral", "type" : "thinking"}`,
	`{"type":"th\u0069nking","cach\u0065_control":{"type":"ephemeral"}}`,
	`{"typ\u0065":"redacted_thinking","cache_control":true}`,
	`{"type":"text","text":"\u001b[0m \u00e9 \u4e2d","cache_control":{}}`,
	`{"type":"text","type":"thinking","cache_control":null,"cache_control":[]}`,
	`{"type":"thinking","type":"text","cache_control":"x"}`,
	`{"type":"tool_result","content":[{"type":"thinking","cache_control":{}}]}`,
	`{"type":"tool_result","content":"{\"type\":\"thinking\",\"cache_control\":true}"}`,
	`{"meta":{"type":"text"},"cache_control":{},"type":"thinking"}`,
	`{"type":"text","text":"} {","cache_control":[]}`,
	`{"type":"text","text":"cache_control"}`,
	`null`, `"cache_control"`, `[]`,
}

func randomAlignmentBody(r *rand.Rand, id int) []byte {
	blocks := func() string {
		items := make([]string, r.Intn(8))
		for i := range items {
			items[i] = alignmentDiffBlocks[r.Intn(len(alignmentDiffBlocks))]
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	messages := make([]string, r.Intn(4))
	for i := range messages {
		messages[i] = `{"content":` + blocks() + `,"role":"assistant"}`
	}
	msg := "[" + strings.Join(messages, ",") + "]"
	if r.Intn(10) == 0 {
		msg = `{"content":` + blocks() + `}`
	}
	fields := []string{`"max_tokens":` + strconv.Itoa(id+1), `"messages":` + msg, `"system":` + blocks(), `"tools":` + blocks()}
	for _, optional := range []string{`"messages":null`, `"cache_control":{}`, `"context_management":null`, `"context_managem\u0065nt":[]`, `"metadata":{"sample":"\\\"cache_control\\\":"}`} {
		if r.Intn(3) == 0 {
			fields = append(fields, optional)
		}
	}
	r.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
	body := []byte("{" + strings.Join(fields, ",") + "}")
	if id%8 == 0 {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err != nil {
			panic(err)
		}
		return pretty.Bytes()
	}
	return body
}

func TestAlignmentPredicatesMatchBase(t *testing.T) {
	r := rand.New(rand.NewSource(59))
	betas := []string{"", "some-beta", "compact-2026-01-12", "context-management-2025-06-27"}
	for n := range 200_000 {
		compareAlignmentPredicates(t, randomAlignmentBody(r, n), betas[r.Intn(len(betas))])
	}
}

func FuzzAlignmentPredicatesMatchBase(f *testing.F) {
	for _, block := range alignmentDiffBlocks {
		f.Add([]byte(`{"messages":[{"content":[`+block+`]}]}`), "")
	}
	f.Add([]byte(`{ "context_managem\u0065nt" : null, "messages": {}, "messages": [] }`), "compact-2026-01-12")
	f.Add([]byte(`{"context_management":{},"messages":null}`), "")
	f.Add([]byte(`{"messages":[{"content":[{"type":"tool_result","content":"{\u0022type\u0022:\u0022thinking\u0022,\u0022cache_control\u0022:{}}"}]}]}`), "")
	f.Fuzz(func(t *testing.T, body []byte, beta string) {
		if json.Valid(body) {
			compareAlignmentPredicates(t, body, beta)
		}
	})
}

func TestSkipUnrelatedEncodedQuotesAndToolResult(t *testing.T) {
	body := []byte(`{"messages":[{"content":[{"type":"tool_result","content":"call(\u0022a\u0022)","cache_control":{}}]}]}`)
	got := scanCacheControl(body, missingJSONRange())
	if got.escapedName || got.thinking {
		t.Fatalf("unnecessary full scan: %+v", got)
	}
}

func TestAlignmentCandidateKeys(t *testing.T) {
	for _, block := range []string{
		`{"type":"tool_result","content":"{\u0022type\u0022:\u0022thinking\u0022}","cache_control":{}}`,
		`{"type":"text","text":"` + strings.Repeat("history", 80) + `","cache_control":{}}`,
	} {
		body := []byte(`{"messages":[{"content":[{"type":"thinking"},` + block + `]}]}`)
		compareAlignmentPredicates(t, body, "")
		if v := scanCacheControl(body, missingJSONRange()); v.escapedName || v.thinking {
			t.Errorf("unneeded candidate parse %+v", v)
		}
	}
}

func TestCacheControlCandidateCountSaturates(t *testing.T) {
	block := `{"type":"text","text":"hello","cache_control":{}}`
	blocks := []string{block, block, block, block, block, block}
	body := []byte(`{"messages":[{"content":[` + strings.Join(blocks, ",") + `]}]}`)
	candidates := scanCacheControl(body, missingJSONRange())
	if candidates.literalKeys != maxCacheControlBlocks+1 || candidates.thinking {
		t.Fatalf("unexpected overflow candidates %+v", candidates)
	}
	compareAlignmentPredicates(t, body, "")
}

func BenchmarkCacheControlScanText(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20} {
		body := []byte(`{"messages":[{"content":[{"type":"text","text":"` + strings.Repeat("a", size) + `","cache_control":{}}]}]}`)
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				scanCacheControl(body, missingJSONRange())
			}
		})
	}
}

func TestThinkingLiteralSuffix(t *testing.T) {
	for text, want := range map[string]bool{"banking": false, "nkingthinking": true, "thinking": true, "think": false, "redacted_thinking": true} {
		if got := containsThinkingLiteral([]byte(text)); got != want {
			t.Errorf("containsThinkingLiteral(%q) = %t, want %t", text, got, want)
		}
	}
}

func BenchmarkCacheControlScanDistinctBlocks(b *testing.B) {
	const block = `{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}`
	blocks := strings.Repeat(block+",", 99_999) + block
	body := []byte(`{"messages":[{"content":[` + blocks + `]}]}`)
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		scanCacheControl(body, missingJSONRange())
	}
}
