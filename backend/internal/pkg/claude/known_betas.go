package claude

// Sources checked 2026-11-20. Entries also include historical client opt-ins.
// Bedrock: internal/service/bedrock_request.go (allowed names and translations).
// docs: https://platform.claude.com/docs/en/api/beta-headers
// SDKs: AnthropicBetaParam and AnthropicBeta, respectively:
// https://raw.githubusercontent.com/anthropics/anthropic-sdk-python/main/src/anthropic/types/anthropic_beta_param.py
// https://raw.githubusercontent.com/anthropics/anthropic-sdk-typescript/main/src/resources/beta/beta.ts
// The first sixteen entries are the named-beta registry and beta constants in
// Claude Code 2.1.328 / claude-agent-sdk 0.3.328. Recognized names include
// endpoint-specific opt-ins such as ccr_byoc and environments.
var knownBetas = map[string]struct{}{
	"ccr-byoc-2025-07-29":                {}, // Claude Code named beta: ccr_byoc
	"environments-2025-11-01":            {}, // Claude Code named beta: environments
	"mcp-servers-2025-12-04":             {}, // Claude Code named beta: mcp_servers
	"program-use-case-ids-2026-09-17":    {}, // Claude Code named beta: program_use_case_ids
	"afk-mode-2026-01-31":                {}, // Claude Code beta constant (auto mode)
	"auto-mode-classifier-2026-07-16":    {}, // Claude Code beta constant (auto-mode-classifier)
	"cache-keepalive-2026-09-03":         {}, // Claude Code beta constant (cache-keepalive)
	"claude-code-side-query-2026-09-29":  {}, // Claude Code beta constant (claude-code-side-query)
	"context-hint-2026-04-09":            {}, // Claude Code beta constant (context-hint)
	"prompt-caching-evict-2026-05-12":    {}, // Claude Code beta constant (prompt-caching-evict)
	"resumption-2026-10-08":              {}, // Claude Code beta constant (resumption)
	"structured-outputs-2025-12-15":      {}, // Claude Code beta constant (structured-outputs)
	"thinking-resumption-2026-07-17":     {}, // Claude Code beta constant (thinking-resumption)
	"timing-2026-09-09":                  {}, // Claude Code beta constant (timing)
	"tool-provenance-2026-10-26":         {}, // Claude Code beta constant (tool-provenance)
	"web-search-results-only-2026-11-15": {}, // Claude Code beta constant (web-search-results-only)
	"code-execution-2025-08-25":          {}, // Legacy opt-ins: https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool#tool-versions
	"dangerous-tool-use-2026-09-03":      {}, // LiteLLM #44435, https://docs.litellm.ai/blog/claude-code-server-side-auto-mode
	"max-tokens-3-5-sonnet-2024-07-15":   {}, // internal/service/claude_code_detection_test.go
	"memory-2025-08-18":                  {}, // Header used in agent-router fixtures: https://github.com/theagentrouter/agent-router/commit/2f608bca91a5bc00cfb9685c0bd2cdfe609e24ad
	"search-results-2025-06-09":          {}, // Legacy header: https://platform.claude.com/docs/en/release-notes/api
	"structured-outputs-2025-09-17":      {}, // anthropic-sdk-python v0.73.0: src/anthropic/resources/beta/messages/messages.py
	"tools-2024-04-04":                   {}, // anthropic-sdk-python v0.25.0: src/anthropic/resources/beta/tools/messages.py
	"tools-2024-05-16":                   {}, // anthropic-sdk-python v0.26.0: src/anthropic/resources/beta/tools/messages.py
	"web-fetch-2025-09-10":               {}, // Historical client header: https://github.com/GROUPEABU/IA-ASSISTANT-/commit/cd82f377aa74a8bdc5faf3138043d4736bf47794
	"web-search-2025-03-05":              {}, // Client header: https://github.com/s1ms1ms1m/casalingo/commit/bee8d9a09fc76dcbe6f9feed2d5f99381d60339c

	"admin-user-sign-out-2026-10-08":              {}, // SDKs
	"advanced-tool-use-2025-11-20":                {}, // Bedrock
	"advisor-tool-2026-03-01":                     {}, // SDKs
	"agent-memory-2026-07-22":                     {}, // docs, SDKs
	"analytics-usage-cost-2026-08-24":             {}, // SDKs
	"cache-diagnosis-2026-04-07":                  {}, // SDKs
	"ce-claude-code-managed-settings-2026-10-06":  {}, // SDKs
	"ce-plugins-2026-09-01":                       {}, // SDKs
	"ce-user-management-2026-07-13":               {}, // SDKs
	BetaClaudeCode:                                {}, // constants.go
	"code-execution-2025-05-22":                   {}, // SDKs
	BetaCompact:                                   {}, // constants.go, Bedrock, SDKs
	"compact-2026-09-04":                          {}, // SDKs
	"computer-use-2024-10-22":                     {}, // SDKs
	"computer-use-2025-01-24":                     {}, // Bedrock, SDKs
	"computer-use-2025-11-24":                     {}, // Bedrock, SDKs
	BetaContext1M:                                 {}, // constants.go, Bedrock, SDKs
	BetaContextManagement:                         {}, // constants.go, Bedrock, docs, SDKs
	"dev-full-thinking-2025-05-14":                {}, // SDKs
	"dreaming-2026-04-21":                         {}, // SDKs
	BetaEffort:                                    {}, // constants.go
	"experimental-claude-tag-2026-10-21":          {}, // SDKs
	BetaExtendedCacheTTL:                          {}, // constants.go, SDKs
	BetaFallbackCreditLegacy:                      {}, // constants.go, SDKs
	BetaFallbackCredit:                            {}, // constants.go, SDKs
	BetaFastMode:                                  {}, // constants.go, SDKs
	"files-api-2025-04-14":                        {}, // SDKs
	BetaFineGrainedToolStreaming:                  {}, // constants.go, Bedrock
	BetaInlineTools:                               {}, // constants.go, SDKs
	BetaInterleavedThinking:                       {}, // constants.go, SDKs
	"managed-agents-2026-04-01":                   {}, // docs, SDKs
	"mcp-client-2025-04-04":                       {}, // SDKs
	"mcp-client-2025-11-20":                       {}, // SDKs
	"mcp-client-2026-09-15":                       {}, // SDKs
	"mcp-tunnels-2026-06-22":                      {}, // docs, SDKs
	"message-batches-2024-09-24":                  {}, // SDKs
	"message-threads-2026-08-12":                  {}, // SDKs
	BetaMidConversationOutputConfig:               {}, // constants.go, SDKs
	"mid-conversation-system-clear-at-2026-08-21": {}, // SDKs
	BetaMidConversationToolChanges:                {}, // constants.go, SDKs
	"model-context-window-exceeded-2025-08-26":    {}, // SDKs
	BetaOAuth:                             {}, // constants.go
	"output-128k-2025-02-19":              {}, // SDKs
	"output-300k-2026-03-24":              {}, // SDKs
	"pdfs-2024-09-25":                     {}, // SDKs
	BetaPerTurnControl:                    {}, // constants.go
	"prompt-caching-2024-07-31":           {}, // SDKs
	BetaPromptCachingScope:                {}, // constants.go
	BetaRedactThinking:                    {}, // constants.go
	"server-side-fallback-2026-06-01":     {}, // SDKs
	BetaServerSideFallback:                {}, // constants.go, SDKs
	"skills-2025-10-02":                   {}, // SDKs
	"spend-limit-reads-2026-09-26":        {}, // SDKs
	BetaStructuredOutputs:                 {}, // constants.go, SDKs
	"task-budgets-2026-03-13":             {}, // SDKs
	"telemetry-destinations-2026-08-11":   {}, // SDKs
	BetaThinkingBindingControls:           {}, // constants.go, SDKs
	"thinking-display-updates-2026-08-18": {}, // SDKs
	"thinking-token-count-2026-05-13":     {}, // SDKs
	BetaTokenCounting:                     {}, // constants.go, SDKs
	"token-efficient-tools-2025-02-19":    {}, // SDKs
	"tool-examples-2025-10-29":            {}, // Bedrock
	"tool-search-tool-2025-10-19":         {}, // Bedrock
	"user-profiles-2026-03-24":            {}, // SDKs
	"user-profiles-2026-08-18":            {}, // SDKs
	"user-profiles-2026-09-04":            {}, // SDKs
	"user-profiles-billing-2026-10-01":    {}, // SDKs
	"webhooks-2026-10-07":                 {}, // SDKs
}

// IsKnownBeta reports whether the exact, case-sensitive beta name is recognized.
func IsKnownBeta(token string) bool {
	_, ok := knownBetas[token]
	return ok
}
