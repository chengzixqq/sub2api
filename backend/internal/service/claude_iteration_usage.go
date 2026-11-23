package service

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
)

// ClaudeIterationUsage is copy-on-write: forwarding/failure snapshots may share
// this pointer, but observing another SSE frame never changes an earlier copy.
type ClaudeIterationUsage struct {
	// Settlement-only snapshot before compactions and message TTL recovery.
	originalExecutor *UsageTokens
	Iterations       []ClaudeUsageIteration
	Present          bool
	Invalid          bool
	Terminal         bool
	AdvisorSeen      bool
}

type ClaudeUsageIteration struct {
	Invalid bool
	Type    string
	Model   string
	Usage   UsageTokens
}

func cloneClaudeIterations(src *ClaudeIterationUsage) *ClaudeIterationUsage {
	if src == nil {
		return &ClaudeIterationUsage{}
	}
	dst := *src
	dst.Iterations = append([]ClaudeUsageIteration(nil), src.Iterations...)
	return &dst
}

func (u ClaudeUsage) Clone() ClaudeUsage {
	if u.IterationUsage != nil {
		u.IterationUsage = cloneClaudeIterations(u.IterationUsage)
	}
	return u
}

func (u ClaudeUsage) HasAdvisorUsage() bool {
	return u.hasIterationTokens("advisor_message")
}

func (u ClaudeUsage) HasCompactionUsage() bool {
	return u.hasIterationTokens("compaction")
}

func (u ClaudeUsage) hasIterationTokens(typ string) bool {
	if u.IterationUsage == nil {
		return false
	}
	for _, entry := range u.IterationUsage.Iterations {
		if !entry.Invalid && entry.Type == typ && (entry.Usage.InputTokens > 0 || entry.Usage.OutputTokens > 0 || entry.Usage.CacheCreationTokens > 0 || entry.Usage.CacheReadTokens > 0) {
			return true
		}
	}
	return false
}

var compactionOverlapLogLimit = rate.NewLimiter(rate.Every(time.Minute), 1)

// Anthropic's top-level executor usage already includes message iterations.
// Add only separately metered compactions, on a copy used for settlement.
func (r *ForwardResult) withCompactionUsage(ctx context.Context) *ForwardResult {
	if !r.Usage.HasCompactionUsage() {
		return r
	}
	// A compatible provider may already include compacted usage in its total.
	// Keep settlement unchanged while collecting evidence of that convention.
	if claudeIterationsMatchAggregate(r.Usage, true) && compactionOverlapLogLimit.Allow() {
		slog.InfoContext(ctx, "billing.compaction_usage_overlap", "request_id", r.RequestID,
			"input_tokens", r.Usage.InputTokens, "output_tokens", r.Usage.OutputTokens)
	}
	bill := *r
	if err := addCompactionUsage(&bill.Usage); err != nil {
		slog.ErrorContext(ctx, "billing.compaction_usage_fallback", "request_id", r.RequestID, "error", err)
		bill.Usage = r.Usage
		bill.Usage.IterationUsage = nil // Settle the original top-level counters only.
	} else {
		state := *bill.Usage.IterationUsage
		original := r.Usage.billingUsageTokens()
		state.originalExecutor = &original
		bill.Usage.IterationUsage = &state
	}
	return &bill
}

func addCompactionUsage(u *ClaudeUsage) error {
	// All downstream sums, including input-to-cache transfers, must fit in int.
	remaining, ok := remainingClaudeTokens(math.MaxInt, claudeUsageTokens(*u))
	if !ok {
		return errors.New("invalid top-level usage in Claude compaction response")
	}
	fillCompactionMessageCacheTTL(u)
	for _, entry := range u.IterationUsage.Iterations {
		if entry.Invalid || entry.Type != "compaction" {
			continue
		}
		t := entry.Usage
		remaining, ok = remainingClaudeTokens(remaining, t)
		if !ok {
			return errors.New("compaction usage exceeds representable token total")
		}
		if t.CacheCreation5mTokens > math.MaxInt-u.CacheCreation5mTokens || t.CacheCreation1hTokens > math.MaxInt-u.CacheCreation1hTokens {
			return errors.New("compaction cache TTL usage exceeds representable token total")
		}
		u.InputTokens += t.InputTokens
		u.OutputTokens += t.OutputTokens
		u.CacheCreationInputTokens += t.CacheCreationTokens
		u.CacheReadInputTokens += t.CacheReadTokens
		u.CacheCreation5mTokens += t.CacheCreation5mTokens
		u.CacheCreation1hTokens += t.CacheCreation1hTokens
	}
	return nil
}

// message_delta may update cache_creation_input_tokens while leaving only
// message_start's TTL breakdown at the top level. Recover missing detail from
// message iterations once their aggregate matches the final executor usage.
func fillCompactionMessageCacheTTL(u *ClaudeUsage) {
	if u.CacheCreation5mTokens < 0 || u.CacheCreation1hTokens < 0 ||
		u.CacheCreation5mTokens >= u.CacheCreationInputTokens-u.CacheCreation1hTokens {
		return
	}
	raw := *u
	raw.CacheCreation5mTokens, raw.CacheCreation1hTokens = 0, 0
	if !claudeExecutorIterationsComplete(raw) {
		return
	}
	var short, long int
	for _, entry := range u.IterationUsage.Iterations {
		if entry.Type == "message" {
			short += entry.Usage.CacheCreation5mTokens
			long += entry.Usage.CacheCreation1hTokens
		}
	}
	if short >= u.CacheCreation5mTokens && long >= u.CacheCreation1hTokens {
		u.CacheCreation5mTokens, u.CacheCreation1hTokens = short, long
	}
}

func remainingClaudeTokens(limit int, t UsageTokens) (int, bool) {
	for _, count := range [...]int{t.InputTokens, t.OutputTokens, t.CacheCreationTokens, t.CacheReadTokens} {
		if count < 0 || count > limit {
			return 0, false
		}
		limit -= count
	}
	return limit, true
}

// transformExecutorIterations preserves advisor token buckets and snapshots.
func transformExecutorIterations(u *ClaudeUsage, transform func(string, *ClaudeUsage)) {
	if u == nil || u.IterationUsage == nil {
		return
	}
	state := cloneClaudeIterations(u.IterationUsage)
	for i := range state.Iterations {
		entry := state.Iterations[i]
		if entry.Invalid || (entry.Type != "message" && entry.Type != "compaction") {
			continue
		}
		executor := claudeUsageFromTokens(entry.Usage)
		transform(entry.Type, &executor)
		state.Iterations[i].Usage = claudeUsageTokens(executor)
	}
	u.IterationUsage = state
}

func observeClaudeIterationEvent(data string, usage *ClaudeUsage) {
	if usage == nil || data == "" {
		return
	}
	event := gjson.Parse(data)
	node := event.Get("usage")
	if event.Get("type").String() == "message_start" {
		node = event.Get("message.usage")
	}
	// Keep TTL detail in compatibility paths whose typed usage schema only
	// retains the aggregate cache counter.
	if cache := node.Get("cache_creation"); cache.IsObject() {
		for _, field := range []struct {
			name string
			dst  *int
		}{{"ephemeral_5m_input_tokens", &usage.CacheCreation5mTokens}, {"ephemeral_1h_input_tokens", &usage.CacheCreation1hTokens}} {
			v := cache.Get(field.name)
			if n, err := strconv.ParseInt(v.Raw, 10, strconv.IntSize); v.Type == gjson.Number && err == nil && n >= 0 {
				*field.dst = int(n)
			}
		}
	}
	observeClaudeIterationUsage(node, usage)
	typ := event.Get("type").String()
	advisorSeen := typ == "content_block_start" && event.Get("content_block.type").String() == "advisor_tool_result"
	if typ == "message" {
		event.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "advisor_tool_result" {
				advisorSeen = true
			}
			return true
		})
	}
	if usage.IterationUsage != nil || advisorSeen {
		state := cloneClaudeIterations(usage.IterationUsage)
		state.AdvisorSeen = state.AdvisorSeen || advisorSeen
		if typ == "message_stop" || (typ == "message" && event.Get("stop_reason").Type == gjson.String) {
			state.Terminal = true
		}
		usage.IterationUsage = state
	}
}

func observeClaudeIterationUsage(node gjson.Result, usage *ClaudeUsage) {
	items := node.Get("iterations")
	if !items.Exists() || items.Type == gjson.Null {
		return
	}
	state := cloneClaudeIterations(usage.IterationUsage)
	state.Present = true
	if !items.IsArray() {
		state.Invalid = true
		usage.IterationUsage = state
		return
	}
	// Keep original array positions, including invalid entries. A malformed
	// executor must not erase a fully validated advisor later in the snapshot.
	next := append([]ClaudeUsageIteration(nil), state.Iterations...)
	incoming := items.Array()
	if len(incoming) < len(next) {
		state.Invalid = true
	}
	for i, item := range incoming {
		entry := ClaudeUsageIteration{Type: item.Get("type").String(), Model: strings.TrimSpace(item.Get("model").String())}
		if entry.Type == "advisor_message" {
			state.AdvisorSeen = true
		}
		valid := item.IsObject() && entry.Type != ""
		fields := []struct {
			name     string
			dst      *int
			required bool
		}{
			{"input_tokens", &entry.Usage.InputTokens, true}, {"output_tokens", &entry.Usage.OutputTokens, true},
			{"cache_creation_input_tokens", &entry.Usage.CacheCreationTokens, false}, {"cache_read_input_tokens", &entry.Usage.CacheReadTokens, false},
			{"cache_creation.ephemeral_5m_input_tokens", &entry.Usage.CacheCreation5mTokens, false}, {"cache_creation.ephemeral_1h_input_tokens", &entry.Usage.CacheCreation1hTokens, false},
		}
		for _, f := range fields {
			v := item.Get(f.name)
			if !v.Exists() && !f.required {
				continue
			}
			n, err := strconv.ParseInt(v.Raw, 10, strconv.IntSize)
			if v.Type != gjson.Number || err != nil || n < 0 {
				valid = false
				continue
			}
			*f.dst = int(n)
		}
		if entry.Usage.CacheCreation5mTokens > math.MaxInt-entry.Usage.CacheCreation1hTokens {
			valid = false
		} else {
			cacheTotal := entry.Usage.CacheCreation5mTokens + entry.Usage.CacheCreation1hTokens
			if !item.Get("cache_creation_input_tokens").Exists() {
				entry.Usage.CacheCreationTokens = cacheTotal
			}
			if cacheTotal > entry.Usage.CacheCreationTokens {
				valid = false
			}
		}
		if _, ok := remainingClaudeTokens(math.MaxInt, entry.Usage); !ok {
			valid = false
		}
		if i < len(state.Iterations) && !state.Iterations[i].Invalid {
			prev := state.Iterations[i]
			if entry.Type != prev.Type || (entry.Type == "advisor_message" && entry.Model != prev.Model) {
				valid = false
			}
			before, after := prev.Usage, entry.Usage
			if after.InputTokens < before.InputTokens || after.OutputTokens < before.OutputTokens || after.CacheCreationTokens < before.CacheCreationTokens ||
				after.CacheReadTokens < before.CacheReadTokens || after.CacheCreation5mTokens < before.CacheCreation5mTokens || after.CacheCreation1hTokens < before.CacheCreation1hTokens {
				valid = false
			}
		}
		if !valid {
			state.Invalid = true
			if i < len(state.Iterations) && !state.Iterations[i].Invalid {
				continue
			}
			entry.Invalid = true
		}
		if i < len(next) {
			next[i] = entry
		} else {
			next = append(next, entry)
		}
	}
	state.Iterations = next
	usage.IterationUsage = state
}

func claudeUsageTokens(u ClaudeUsage) UsageTokens {
	return UsageTokens{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheCreationTokens: u.CacheCreationInputTokens,
		CacheReadTokens: u.CacheReadInputTokens, CacheCreation5mTokens: u.CacheCreation5mTokens, CacheCreation1hTokens: u.CacheCreation1hTokens,
		CacheCreation30mTokens: u.CacheCreation30mTokens, CacheCreation12hTokens: u.CacheCreation12hTokens,
		ImageInputTokens: u.ImageInputTokens, ImageCacheReadTokens: u.ImageCacheReadTokens, ImageOutputTokens: u.ImageOutputTokens}
}

func claudeUsageFromTokens(u UsageTokens) ClaudeUsage {
	return ClaudeUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheCreationInputTokens: u.CacheCreationTokens,
		CacheReadInputTokens: u.CacheReadTokens, CacheCreation5mTokens: u.CacheCreation5mTokens, CacheCreation1hTokens: u.CacheCreation1hTokens,
		CacheCreation30mTokens: u.CacheCreation30mTokens, CacheCreation12hTokens: u.CacheCreation12hTokens}
}

// Check the raw Anthropic top-level usage against message iterations only.
func claudeExecutorIterationsComplete(u ClaudeUsage) bool {
	return claudeIterationsMatchAggregate(u, false)
}

// The settlement copy includes compactions in the aggregate used for pricing.
func claudeIterationsMatchAggregate(u ClaudeUsage, includesCompactions bool) bool {
	s := u.IterationUsage
	if s == nil || !s.Present || s.Invalid || !s.Terminal {
		return false
	}
	var sums [8]int
	limits := [8]int{u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.CacheCreation5mTokens, u.CacheCreation1hTokens, u.CacheCreation30mTokens, u.CacheCreation12hTokens}
	checkClaudeTTL := u.CacheCreation5mTokens > 0 || u.CacheCreation1hTokens > 0
	checkOpenAITTL := u.CacheCreation30mTokens > 0 || u.CacheCreation12hTokens > 0
	if !checkClaudeTTL {
		limits[4], limits[5] = u.CacheCreationInputTokens, u.CacheCreationInputTokens
	}
	if !checkOpenAITTL {
		limits[6], limits[7] = u.CacheCreationInputTokens, u.CacheCreationInputTokens
	}
	hasBillableCompaction := u.HasCompactionUsage()
	for _, entry := range s.Iterations {
		// With no separately metered compaction, keep the legacy advisor
		// completeness rule: only message and advisor entries certify a split.
		if entry.Type == "compaction" && !hasBillableCompaction {
			return false
		}
		if entry.Type == "advisor_message" || (entry.Type == "compaction" && !includesCompactions) {
			continue
		}
		if entry.Invalid || (entry.Type != "message" && entry.Type != "compaction") {
			return false
		}
		t := entry.Usage
		values := [8]int{t.InputTokens, t.OutputTokens, t.CacheCreationTokens, t.CacheReadTokens, t.CacheCreation5mTokens, t.CacheCreation1hTokens, t.CacheCreation30mTokens, t.CacheCreation12hTokens}
		for i, value := range values {
			if value < 0 || limits[i] < sums[i] || value > limits[i]-sums[i] {
				return false
			}
			sums[i] += value
		}
	}
	if !checkClaudeTTL {
		sums[4], sums[5] = limits[4], limits[5]
	}
	if !checkOpenAITTL {
		sums[6], sums[7] = limits[6], limits[7]
	}
	return sums == limits
}
