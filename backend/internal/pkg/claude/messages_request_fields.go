package claude

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// UnknownTopLevelRequestFields returns extra fields in their JSON wire order.
// Sources checked 2026-11-20: https://platform.claude.com/docs/en/api/messages
// and https://platform.claude.com/docs/en/api/beta/messages/create.
func UnknownTopLevelRequestFields(body []byte, betaHeader string) []string {
	var unknown []string
	gjson.ParseBytes(body).ForEach(func(key, _ gjson.Result) bool {
		if !knownMessagesRequestField(key.String(), betaHeader) {
			unknown = append(unknown, key.String())
		}
		return true
	})
	return unknown
}

func knownMessagesRequestField(key, beta string) bool {
	switch key {
	case "model", "messages", "max_tokens", "cache_control", "container", "diagnostics",
		"inference_geo", "metadata", "output_config", "service_tier", "speed", "stop_sequences",
		"stream", "system", "thinking", "tool_choice", "tools":
		return true
	case "temperature", "top_k", "top_p":
		// Observed upstream behaviour, 2026-11-20: opus-4-7 and opus-5-5 report
		// "`X` is deprecated for this model." With context_management and no
		// context beta, its schema error comes first. These keys pass schema validation.
		return true
	case "context_management":
		return hasMessagesBeta(beta, BetaContextManagement, BetaCompact)
	case "fallbacks":
		// Claude Code list-form fallbacks and SDK AnthropicBeta.
		return hasMessagesBeta(beta, BetaServerSideFallback, "server-side-fallback-2026-06-01")
	case "fallback_credit_token":
		return hasMessagesBeta(beta, BetaServerSideFallback, BetaFallbackCredit, BetaFallbackCreditLegacy)
	case "mcp_servers":
		return hasMessagesBeta(beta, "mcp-client-2025-04-04", "mcp-client-2025-11-20", "mcp-client-2026-09-15")
	case "output_format":
		return hasMessagesBeta(beta, "structured-outputs-2025-11-13")
	case "compaction":
		return hasMessagesBeta(beta, "compact-2026-09-04")
	case "billing_policy":
		return hasMessagesBeta(beta, "user-profiles-billing-2026-10-01")
	case "thread":
		return hasMessagesBeta(beta, "message-threads-2026-08-12")
	default:
		return false
	}
}

func hasMessagesBeta(header string, allowed ...string) bool {
	for token := range strings.SplitSeq(header, ",") {
		if slices.Contains(allowed, strings.TrimSpace(token)) {
			return true
		}
	}
	return false
}
