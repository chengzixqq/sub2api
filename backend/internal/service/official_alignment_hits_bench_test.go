//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func alignmentBenchmarkBody(b *testing.B, output string, ansi, hit bool) []byte {
	b.Helper()
	var history strings.Builder
	for i := range 20 {
		result := output
		if ansi && i == 10 {
			result += "\x1b[0m"
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			b.Fatal(err)
		}
		history.WriteString(`{"role":"assistant","content":[{"type":"thinking","thinking":"consider the output","signature":"opaque"},{"type":"text","text":"done"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"id","content":`)
		history.Write(encoded)
		history.WriteString(`}]},`)
	}
	last := `{"type":"text","text":"continue","cache_control":{}}`
	if hit {
		last += `,{"type":"thinking","thinking":"current","cache_control":{}}`
	}
	return []byte(`{"model":"claude-opus-4-6","max_tokens":1024,"system":[{"type":"text","text":"instructions","cache_control":{}},{"type":"text","text":"project","cache_control":{}}],"tools":[{"name":"lookup","input_schema":{"type":"object"},"cache_control":{}}],"messages":[` + history.String() + `{"role":"user","content":[` + last + `]}]}`)
}

func BenchmarkDefaultAlignmentChecks(b *testing.B) {
	const source = "func read(path string) error {\n\tdata, err := os.ReadFile(path)\n\tif err != nil { return fmt.Errorf(\"read %q: %w\", path, err) }\n\treturn json.Unmarshal(data, &result)\n}\n"
	const quotes = `case "a", "b", "c": fmt.Println("x", "y", "z")`
	for _, tc := range []struct {
		name, output string
		ansi         bool
	}{
		{"UnicodeJSONTypeKeys", strings.Repeat(`{"type":"object","cache_control":"none"}`, 220), false},
		{"ToolResultTailWithThinkingHistory", strings.Repeat(source, 56), false},
		{"ToolResultTailLongText", strings.Repeat(source, 56), false},
		{"Plain", strings.Repeat("sample output ", 720), false},
		{"GoSource", strings.Repeat(source, 56), false},
		{"ANSI", strings.Repeat(source, 56), true},
		{"UnicodeQuotes", strings.Repeat(quotes, 172), false},
		{"ToolResultTail", strings.Repeat(source, 56), false},
		{"QuoteDense", strings.Repeat(quotes, 172), false},
	} {
		body := alignmentBenchmarkBody(b, tc.output, tc.ansi, false)
		if tc.name == "UnicodeQuotes" || tc.name == "UnicodeJSONTypeKeys" {
			body = bytes.ReplaceAll(body, []byte(`\"`), []byte(`\u0022`))
		}
		if tc.name == "ToolResultTail" {
			body = bytes.ReplaceAll(body, []byte(`{"type":"thinking","thinking":"consider the output","signature":"opaque"},`), nil)
			body = bytes.ReplaceAll(body, []byte(`"type":"text","text":"continue"`), []byte(`"type":"tool_result","content":"done"`))
		}
		if tc.name == "ToolResultTailLongText" {
			body = bytes.ReplaceAll(body, []byte(`"text":"continue"`), []byte(`"text":"`+strings.Repeat("a", 400)+`"`))
		}
		if tc.name == "ToolResultTailWithThinkingHistory" {
			body = bytes.ReplaceAll(body, []byte(`"type":"text","text":"continue"`), []byte(`"type":"tool_result","content":"done"`))
		}
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			b.Run(tc.name+"/"+accountType, func(b *testing.B) {
				benchmarkAlignmentChecks(b, body, accountType)
			})
		}
	}
}

func BenchmarkAlignmentThinkingHit(b *testing.B) {
	benchmarkAlignmentChecks(b, alignmentBenchmarkBody(b, "done", false, true), AccountTypeAPIKey)
}

func benchmarkAlignmentChecks(b *testing.B, body []byte, accountType string) {
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	if err != nil {
		b.Fatal(err)
	}
	s := NewSettingService(&queueAlignmentSettings{}, nil)
	s.alignmentHits = NewAlignmentHitRecorder(&memoryHitStore{})
	s.alignmentSnapshot(b.Context())
	policy := DefaultClaudeCustomizationSettings()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14")
	account := fallbackNativeAccountForTest(false)
	account.Type = accountType
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		c.Keys = nil // Each request gets its own Record deduplication markers.
		if err := s.checkUnknownBeta(b.Context(), c, account, policy); err != nil {
			b.Fatal(err)
		}
		if err := s.alignmentHits.checkCacheControls(c, account, policy.ThinkingCacheControl, policy.CacheControlLimitMode, parsed); err != nil {
			b.Fatal(err)
		}
		if _, _, err := s.alignmentHits.applyContextManagementMissingBeta(c, account, policy, c.Request.Header, body); err != nil {
			b.Fatal(err)
		}
	}
}
