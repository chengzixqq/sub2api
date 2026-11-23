// Catalogue checked 2026-11-20 against https://platform.claude.com/docs/en/models/overview,
// https://platform.claude.com/docs/en/about-claude/model-deprecations, each model
// overview cited below, https://www.anthropic.com/news/claude-opus-4-1 and
// https://www.anthropic.com/news/claude-4. created_at uses the documented
// release day at 00:00:00Z. Token limits use decimal K=1000 and M=1000000.
// Complete per-model capabilities are unpublished, so capabilities is null.
// Each known line names the documented product family.
package claude

// OfficialModel is the Models API representation in the example response's key order.
type OfficialModel struct {
	ID             string    `json:"id"`
	Capabilities   *struct{} `json:"capabilities"`
	CreatedAt      string    `json:"created_at"`
	DeprecatedAt   *string   `json:"deprecated_at"`
	DisplayName    string    `json:"display_name"`
	Lifecycle      string    `json:"lifecycle"`
	Line           *string   `json:"line"`
	MaxInputTokens *int      `json:"max_input_tokens"`
	MaxTokens      *int      `json:"max_tokens"`
	RetiresAt      *string   `json:"retires_at"`
	Type           string    `json:"type"`
}

type catalogEntry struct {
	name, released, line           string
	input, output                  int
	lifecycle, deprecated, retires string
}

var officialCatalog = map[string]catalogEntry{
	// https://platform.claude.com/docs/en/models/fable-5-2/overview
	"claude-fable-5-2": {name: "Claude Fable 5.2", released: "2026-11-09", line: "fable", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/fable-5-1/overview
	"claude-fable-5-1": {name: "Claude Fable 5.1", released: "2026-09-01", line: "fable", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/fable-5/overview
	"claude-fable-5": {name: "Claude Fable 5", released: "2026-06-09", line: "fable", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/mythos-5-2/overview
	"claude-mythos-5-2": {name: "Claude Mythos 5.2", released: "2026-11-09", line: "mythos", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/opus-5-5/overview
	"claude-opus-5-5": {name: "Claude Opus 5.5", released: "2026-09-22", line: "opus", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/opus-5/overview
	"claude-opus-5": {name: "Claude Opus 5", released: "2026-07-24", line: "opus", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/opus-4-8/overview
	"claude-opus-4-8": {name: "Claude Opus 4.8", released: "2026-05-28", line: "opus", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/opus-4-7/overview
	"claude-opus-4-7": {name: "Claude Opus 4.7", released: "2026-04-16", line: "opus", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/opus-4-6/overview
	"claude-opus-4-6": {name: "Claude Opus 4.6", released: "2026-02-05", line: "opus", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/opus-4-5/overview
	"claude-opus-4-5-20251101": {name: "Claude Opus 4.5", released: "2025-11-24", line: "opus", input: 200_000, output: 64_000},
	// Release: https://www.anthropic.com/news/claude-opus-4-1
	// Deprecation / retirement dates: https://platform.claude.com/docs/en/about-claude/model-deprecations
	// 200K input / 32K output: https://platform.claude.com/docs/en/models/overview
	"claude-opus-4-1-20250805": {name: "Claude Opus 4.1", released: "2025-08-05", line: "opus", input: 200_000, output: 32_000, lifecycle: "retired", deprecated: "2026-06-05", retires: "2026-08-05"},
	// Release date: Introducing Claude 4 (2025-05-22), https://www.anthropic.com/news/claude-4
	// The news page does not mention this ID; its name/ID mapping and deprecation / retirement dates come from
	// https://platform.claude.com/docs/en/about-claude/model-deprecations
	// 200K input / 32K output: https://platform.claude.com/docs/en/models/overview
	"claude-opus-4-20250514": {name: "Claude Opus 4", released: "2025-05-22", line: "opus", input: 200_000, output: 32_000, lifecycle: "retired", deprecated: "2026-04-14", retires: "2026-06-15"},
	// https://platform.claude.com/docs/en/models/sonnet-5-5/overview
	"claude-sonnet-5-5": {name: "Claude Sonnet 5.5", released: "2026-09-28", line: "sonnet", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/sonnet-5/overview
	"claude-sonnet-5": {name: "Claude Sonnet 5", released: "2026-06-30", line: "sonnet", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/sonnet-4-6/overview
	"claude-sonnet-4-6": {name: "Claude Sonnet 4.6", released: "2026-02-17", line: "sonnet", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/sonnet-4-5/overview
	"claude-sonnet-4-5-20250929": {name: "Claude Sonnet 4.5", released: "2025-09-29", line: "sonnet", input: 200_000, output: 64_000, lifecycle: "deprecated", deprecated: "2026-09-30", retires: "2026-11-30"},
	// https://platform.claude.com/docs/en/models/haiku-5-5/overview
	"claude-haiku-5-5": {name: "Claude Haiku 5.5", released: "2026-10-07", line: "haiku", input: 1_000_000, output: 128_000},
	// https://platform.claude.com/docs/en/models/haiku-4-5/overview
	"claude-haiku-4-5-20251001": {name: "Claude Haiku 4.5", released: "2025-10-15", line: "haiku", input: 200_000, output: 64_000},
}

// Aliases come from the Model IDs table in each snapshot's overview.
var catalogAliases = map[string]string{
	"claude-opus-4-5":   "claude-opus-4-5-20251101",
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
}

// OfficialModelInfo keeps the group's routable ID, including aliases and private
// models. The boolean reports whether documented metadata was available.
func OfficialModelInfo(id string) (OfficialModel, bool) {
	info := OfficialModel{ID: id, DisplayName: id, CreatedAt: "1970-01-01T00:00:00Z", Lifecycle: "active", Type: "model"}
	key := id
	if snapshot, ok := catalogAliases[id]; ok {
		key = snapshot
	}
	entry, ok := officialCatalog[key]
	if !ok {
		return info, false
	}
	const midnight = "T00:00:00Z"
	info.DisplayName = entry.name
	info.CreatedAt = entry.released + midnight
	info.Line = &entry.line
	info.MaxInputTokens = &entry.input
	info.MaxTokens = &entry.output
	if entry.lifecycle != "" {
		info.Lifecycle = entry.lifecycle
		deprecated, retires := entry.deprecated+midnight, entry.retires+midnight
		info.DeprecatedAt, info.RetiresAt = &deprecated, &retires
	}
	return info, true
}
