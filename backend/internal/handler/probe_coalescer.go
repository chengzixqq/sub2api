package handler

// Probe coalescing deliberately lives at the handler layer.  By the time a
// handler calls BeginProbe, authentication, model/group validation, user
// concurrency and billing eligibility have already run.  That keeps a
// synthetic response from becoming an authentication or quota bypass while
// allowing it to skip only the upstream account slot.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ProbeCoalescingMode string

const (
	ProbeCoalescingOff    ProbeCoalescingMode = "off"
	ProbeCoalescingShadow ProbeCoalescingMode = "shadow"
	ProbeCoalescingActive ProbeCoalescingMode = "active"
)

const (
	defaultProbeWindow       = 60 * time.Second
	defaultProbeLeaderLimit  = 8 * time.Second
	defaultProbeAttemptLimit = 8
)

type probeProtocol string

const (
	probeProtocolAnthropic probeProtocol = "anthropic_messages"
	probeProtocolChat      probeProtocol = "openai_chat"
	probeProtocolResponse  probeProtocol = "openai_responses"
	probeProtocolGemini    probeProtocol = "gemini_generate_content"
)

// ProbeParseRejectReason is intentionally a small, bounded vocabulary.  It
// is used for sampled diagnostics only; request bodies and credentials are
// never logged.  Keeping the vocabulary bounded also makes it safe to export
// as a metric label in deployments that wire the counters to a metrics sink.
type ProbeParseRejectReason string

const (
	probeRejectInvalidJSON     ProbeParseRejectReason = "invalid_json"
	probeRejectUnsupportedPath ProbeParseRejectReason = "unsupported_path"
	probeRejectMissingModel    ProbeParseRejectReason = "missing_model"
	probeRejectBodyShape       ProbeParseRejectReason = "body_shape"
	probeRejectMessageShape    ProbeParseRejectReason = "message_shape"
	probeRejectPrompt          ProbeParseRejectReason = "prompt_mismatch"
	probeRejectStreaming       ProbeParseRejectReason = "streaming"
	probeRejectInputTokens     ProbeParseRejectReason = "input_tokens_endpoint"
	probeRejectTokenEstimate   ProbeParseRejectReason = "token_estimate"
	probeRejectGeminiAction    ProbeParseRejectReason = "gemini_action"
	probeRejectModelMismatch   ProbeParseRejectReason = "model_mismatch"
	probeRejectTokenLimit      ProbeParseRejectReason = "token_limit"
)

var probeParseRejectCounters sync.Map // map[ProbeParseRejectReason]*atomic.Uint64

// recordProbeParseReject increments a bounded diagnostic counter and emits a
// low-rate structured event.  The event contains only a route class and the
// reason, never a model, body, API key, or user prompt.
func recordProbeParseReject(path string, reason ProbeParseRejectReason) {
	if reason == "" {
		reason = probeRejectBodyShape
	}
	value, _ := probeParseRejectCounters.LoadOrStore(reason, &atomic.Uint64{})
	counter, ok := value.(*atomic.Uint64)
	if !ok {
		return
	}
	count := counter.Add(1)
	// Keep the first sample and then one event per 64 rejects per reason.  The
	// global logger sampler remains an additional safety net in production.
	if count != 1 && count%64 != 0 {
		return
	}
	logger.L().Info("probe.parse_rejected",
		zap.String("reason", string(reason)),
		zap.String("route", probeRouteClass(path)),
		zap.Uint64("count", count),
	)
}

func looksLikeChannelMonitorProbe(path string, body []byte) bool {
	// Keep diagnostics focused on the small, fixed-shape monitor requests while
	// still catching probes whose arithmetic prompt was altered by a downstream
	// wrapper. Never inspect or log the body itself; this is only a cheap gate.
	if len(body) > 8<<10 {
		return false
	}
	if strings.Contains(path, "/v1beta/models/") {
		return bytes.Contains(body, []byte("Calculate and respond with ONLY")) ||
			bytes.Contains(body, []byte("Q: 3 + 5"))
	}
	return bytes.Contains(body, []byte("Calculate and respond with ONLY")) ||
		bytes.Contains(body, []byte("Q: 3 + 5"))
}

// ProbeParseRejectSnapshot returns a copy suitable for diagnostics/tests.
// The returned map is detached from the live counters.
func ProbeParseRejectSnapshot() map[ProbeParseRejectReason]uint64 {
	snapshot := make(map[ProbeParseRejectReason]uint64)
	probeParseRejectCounters.Range(func(key, value any) bool {
		reason, ok := key.(ProbeParseRejectReason)
		counter, counterOK := value.(*atomic.Uint64)
		if ok && counterOK {
			snapshot[reason] = counter.Load()
		}
		return true
	})
	return snapshot
}

// ProbeCandidate is intentionally small and contains no account or pricing
// state.  Those values are supplied by the handler only after a real leader
// has been selected.
type ProbeCandidate struct {
	Protocol     probeProtocol
	Model        string
	Prompt       string
	Expected     string
	InputTokens  int
	OutputTokens int
	Key          string
}

func (p ProbeCandidate) valid() bool {
	return p.Protocol != "" && strings.TrimSpace(p.Model) != "" &&
		strings.TrimSpace(p.Prompt) != "" && strings.TrimSpace(p.Expected) != "" &&
		p.InputTokens > 0 && p.OutputTokens > 0
}

type probeEntry struct {
	key          string
	bucket       int64
	active       bool
	attempts     int
	deadline     time.Time
	done         chan struct{}
	cancel       context.CancelFunc
	expiry       *time.Timer
	billingReady bool
	billingErr   error
	result       probeResult
	healthyTo    time.Time
}

type probeResult struct {
	success         bool
	leaderRequestID string
	leaderAccount   *service.Account
}

type ProbeResolution struct {
	Candidate       ProbeCandidate
	Synthetic       bool
	Leader          bool
	LeaderRequestID string
	LeaderAccount   *service.Account
	Lease           *ProbeLease
}

type ProbeCoalescerConfig struct {
	Mode          ProbeCoalescingMode
	Window        time.Duration
	LeaderTimeout time.Duration
	AttemptBudget int
}

func (c ProbeCoalescerConfig) normalized() ProbeCoalescerConfig {
	if c.Mode != ProbeCoalescingOff && c.Mode != ProbeCoalescingShadow && c.Mode != ProbeCoalescingActive {
		c.Mode = ProbeCoalescingShadow
	}
	if c.Window <= 0 {
		c.Window = defaultProbeWindow
	}
	if c.LeaderTimeout <= 0 {
		c.LeaderTimeout = defaultProbeLeaderLimit
	}
	if c.AttemptBudget <= 0 {
		c.AttemptBudget = defaultProbeAttemptLimit
	}
	return c
}

// ProbeCoalescer is a process-local single-flight cache.  Redis-backed
// coordination can be layered behind this API later; when a process cannot
// coordinate, callers safely fall back to real upstream traffic.
type ProbeCoalescer struct {
	mu      sync.Mutex
	entries map[string]*probeEntry
	config  atomic.Value // ProbeCoalescerConfig
	now     func() time.Time

	settingsMu      sync.Mutex
	settingsService *service.SettingService
	settingsAt      time.Time
}

func NewProbeCoalescer(cfg ProbeCoalescerConfig) *ProbeCoalescer {
	p := &ProbeCoalescer{entries: make(map[string]*probeEntry), now: time.Now}
	p.config.Store(cfg.normalized())
	return p
}

var defaultProbeCoalescer = NewProbeCoalescer(ProbeCoalescerConfig{Mode: ProbeCoalescingShadow})
var defaultProbeSettings atomic.Pointer[service.SettingService]

// ErrProbeAttemptBudgetExhausted tells callers to stop rather than starting
// another real upstream probe after the bounded retry budget is consumed.
var ErrProbeAttemptBudgetExhausted = errors.New("probe attempt budget exhausted")

func DefaultProbeCoalescer() *ProbeCoalescer { return defaultProbeCoalescer }

func RegisterProbeSettingsService(settings *service.SettingService) {
	defaultProbeSettings.Store(settings)
}

func syncProbeCoalescerSettings(p *ProbeCoalescer, settings *service.SettingService, ctx context.Context) {
	if settings == nil {
		settings = defaultProbeSettings.Load()
	}
	if p == nil || settings == nil {
		return
	}
	now := p.now()
	p.settingsMu.Lock()
	if p.settingsService == settings && now.Sub(p.settingsAt) < 5*time.Second {
		p.settingsMu.Unlock()
		return
	}
	p.settingsService = settings
	p.settingsAt = now
	runtime := settings.GetProbeCoalescingRuntime(ctx)
	p.settingsMu.Unlock()
	mode := ProbeCoalescingMode(runtime.Mode)
	p.SetConfig(ProbeCoalescerConfig{Mode: mode, Window: runtime.Window(), LeaderTimeout: runtime.LeaderTimeout(), AttemptBudget: runtime.AttemptBudget})
}

func (p *ProbeCoalescer) Config() ProbeCoalescerConfig {
	if p == nil {
		return ProbeCoalescerConfig{Mode: ProbeCoalescingOff}.normalized()
	}
	cfg, ok := p.config.Load().(ProbeCoalescerConfig)
	if !ok {
		return ProbeCoalescerConfig{Mode: ProbeCoalescingOff}.normalized()
	}
	return cfg
}

func (p *ProbeCoalescer) SetConfig(cfg ProbeCoalescerConfig) {
	if p == nil {
		return
	}
	p.config.Store(cfg.normalized())
}

// Begin creates either the first leader, a follower waiting for a leader, or
// a bypass decision.  Shadow mode intentionally never changes the response.
func (p *ProbeCoalescer) Begin(ctx context.Context, candidate ProbeCandidate, requestID string) *ProbeLease {
	if p == nil || !candidate.valid() {
		return &ProbeLease{coalescer: p, candidate: candidate, role: probeBypass}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := p.Config()
	if cfg.Mode != ProbeCoalescingActive {
		role := probeShadow
		if cfg.Mode == ProbeCoalescingOff {
			role = probeBypass
		}
		return &ProbeLease{coalescer: p, candidate: candidate, role: role}
	}
	now := p.now()
	bucket := now.UnixNano() / cfg.Window.Nanoseconds()
	key := candidate.Key
	if key == "" {
		key = candidate.Model + ":" + string(candidate.Protocol)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now, cfg.Window)
	attempts := 1
	if entry := p.entries[key]; entry != nil && entry.bucket == bucket {
		if !entry.active && entry.healthyTo.After(now) && entry.result.success {
			return &ProbeLease{coalescer: p, candidate: candidate, role: probeFollower, entry: entry, baseCtx: ctx}
		}
		if entry.active {
			return &ProbeLease{coalescer: p, candidate: candidate, role: probeFollower, entry: entry, baseCtx: ctx}
		}
		// A failed entry is replaced by the next request, subject to the
		// per-window attempt budget.
		if entry.attempts >= cfg.AttemptBudget {
			return &ProbeLease{coalescer: p, candidate: candidate, role: probeExhausted, entry: entry, baseCtx: ctx}
		}
		attempts = entry.attempts + 1
	}
	entry := &probeEntry{
		key:      key,
		bucket:   bucket,
		active:   true,
		attempts: attempts,
		deadline: now.Add(cfg.LeaderTimeout),
		done:     make(chan struct{}),
	}
	leaderCtx, cancel := context.WithCancel(ctx)
	entry.cancel = cancel
	p.entries[key] = entry
	p.armExpiryLocked(entry, cfg.LeaderTimeout)
	return &ProbeLease{coalescer: p, candidate: candidate, role: probeLeader, entry: entry, baseCtx: ctx, leaderCtx: leaderCtx}
}

// armExpiryLocked bounds a leader even when no follower is waiting on it. The
// callback only marks the exact current entry failed; a later replacement
// cannot be affected by a stale timer.
func (p *ProbeCoalescer) armExpiryLocked(entry *probeEntry, timeout time.Duration) {
	if p == nil || entry == nil || timeout <= 0 {
		return
	}
	entry.expiry = time.AfterFunc(timeout, func() {
		p.expireEntry(entry)
	})
}

func (p *ProbeCoalescer) expireEntry(entry *probeEntry) {
	if p == nil || entry == nil {
		return
	}
	var cancel context.CancelFunc
	p.mu.Lock()
	if entry.active && p.entries[entry.key] == entry {
		entry.active = false
		entry.result = probeResult{}
		cancel = entry.cancel
		close(entry.done)
	}
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *ProbeCoalescer) pruneLocked(now time.Time, window time.Duration) {
	for key, entry := range p.entries {
		if entry == nil {
			delete(p.entries, key)
			continue
		}
		// Failed attempts have a zero healthyTo; retain them for the current
		// bucket so AttemptBudget can actually bound retries. Successful
		// entries expire by healthyTo, and old buckets are removed regardless.
		healthExpired := !entry.active && !entry.healthyTo.IsZero() && entry.healthyTo.Before(now.Add(-window))
		if healthExpired || entry.bucket < (now.UnixNano()/window.Nanoseconds())-2 {
			delete(p.entries, key)
		}
	}
}

type probeRole uint8

const (
	probeBypass probeRole = iota
	probeShadow
	probeLeader
	probeFollower
	probeExhausted
)

type ProbeLease struct {
	coalescer *ProbeCoalescer
	candidate ProbeCandidate
	role      probeRole
	entry     *probeEntry
	finished  atomic.Bool
	baseCtx   context.Context
	leaderCtx context.Context
}

func (l *ProbeLease) IsBypass() bool    { return l == nil || l.role == probeBypass }
func (l *ProbeLease) IsShadow() bool    { return l != nil && l.role == probeShadow }
func (l *ProbeLease) IsLeader() bool    { return l != nil && l.role == probeLeader }
func (l *ProbeLease) IsFollower() bool  { return l != nil && l.role == probeFollower }
func (l *ProbeLease) IsExhausted() bool { return l != nil && l.role == probeExhausted }
func (l *ProbeLease) Candidate() ProbeCandidate {
	if l == nil {
		return ProbeCandidate{}
	}
	return l.candidate
}

// LeaderContext is the cancellable context bound to the real upstream probe.
// It is canceled when the lease finishes or when a follower takes over after
// the leader deadline, preventing an expired leader from overlapping the next
// real attempt.
func (l *ProbeLease) LeaderContext() context.Context {
	if l == nil || !l.IsLeader() {
		return nil
	}
	if l.leaderCtx != nil {
		return l.leaderCtx
	}
	return context.Background()
}

// MarkBillingReady records that the leader's provider-cost usage row and user
// billing have completed. A healthy result is never published without this
// barrier. Calls arriving after timeout/failover are ignored.
func (l *ProbeLease) MarkBillingReady(err error) {
	if l == nil || l.coalescer == nil || l.entry == nil || !l.IsLeader() {
		return
	}
	l.coalescer.mu.Lock()
	defer l.coalescer.mu.Unlock()
	if !l.entry.active {
		return
	}
	l.entry.billingReady = true
	l.entry.billingErr = err
}

// Resolve waits for a successful health result.  If the current leader fails,
// exactly one waiter is promoted to leader.  A follower that cannot be safely
// billed should call Promote instead of returning a free response.
func (l *ProbeLease) Resolve(ctx context.Context) (ProbeResolution, error) {
	if l == nil {
		return ProbeResolution{}, errors.New("nil probe lease")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if l.role == probeBypass || l.role == probeShadow {
		return ProbeResolution{Candidate: l.candidate}, nil
	}
	for {
		entry := l.entry
		if entry == nil {
			return ProbeResolution{Candidate: l.candidate}, errors.New("probe entry missing")
		}
		// A leader that never returns must not pin every follower until the
		// request context expires. Mark the attempt failed once its deadline
		// elapses; the first waiter then owns the next attempt.
		remaining := time.Until(entry.deadline)
		if remaining < 0 {
			remaining = 0
		}
		timer := time.NewTimer(remaining)
		select {
		case <-entry.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			var cancel context.CancelFunc
			var expiry *time.Timer
			l.coalescer.mu.Lock()
			if entry.active && l.coalescer.entries[entry.key] == entry {
				entry.active = false
				entry.result = probeResult{}
				cancel = entry.cancel
				expiry = entry.expiry
				close(entry.done)
			}
			l.coalescer.mu.Unlock()
			if expiry != nil {
				expiry.Stop()
			}
			if cancel != nil {
				cancel()
			}
		case <-ctx.Done():
			return ProbeResolution{}, ctx.Err()
		}
		l.coalescer.mu.Lock()
		result := entry.result
		cfg := l.coalescer.Config()
		now := l.coalescer.now()
		if current := l.coalescer.entries[entry.key]; current != nil && current != entry {
			// Another waiter may already have replaced this attempt (including
			// after a billing failure). Always follow the current entry rather
			// than allowing stale waiters to create parallel leaders.
			l.entry = current
			l.coalescer.mu.Unlock()
			continue
		}
		if result.success && entry.healthyTo.After(now) {
			l.coalescer.mu.Unlock()
			return ProbeResolution{Candidate: l.candidate, Synthetic: true, LeaderRequestID: result.leaderRequestID, LeaderAccount: result.leaderAccount}, nil
		}
		// Failed leader: atomically claim the next attempt.
		if entry.attempts >= cfg.AttemptBudget {
			l.coalescer.mu.Unlock()
			return ProbeResolution{Candidate: l.candidate}, ErrProbeAttemptBudgetExhausted
		}
		bucket := entry.bucket
		key := entry.key
		baseCtx := l.baseCtx
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		leaderCtx, cancel := context.WithCancel(baseCtx)
		newEntry := &probeEntry{key: key, bucket: bucket, active: true, attempts: entry.attempts + 1, deadline: now.Add(cfg.LeaderTimeout), done: make(chan struct{}), cancel: cancel}
		l.coalescer.entries[key] = newEntry
		l.coalescer.armExpiryLocked(newEntry, cfg.LeaderTimeout)
		l.coalescer.mu.Unlock()
		l.entry = newEntry
		l.role = probeLeader
		l.leaderCtx = leaderCtx
		return ProbeResolution{Candidate: l.candidate, Leader: true, Lease: l}, nil
	}
}

func (l *ProbeLease) Promote() bool {
	if l == nil || l.coalescer == nil || l.role != probeFollower || l.entry == nil {
		return false
	}
	l.coalescer.mu.Lock()
	defer l.coalescer.mu.Unlock()
	entry := l.entry
	if entry.active {
		return false
	}
	if current := l.coalescer.entries[entry.key]; current != nil && current != entry {
		return false
	}
	cfg := l.coalescer.Config()
	now := l.coalescer.now()
	if entry.attempts >= cfg.AttemptBudget {
		return false
	}
	baseCtx := l.baseCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	leaderCtx, cancel := context.WithCancel(baseCtx)
	newEntry := &probeEntry{
		key:      entry.key,
		bucket:   entry.bucket,
		active:   true,
		attempts: entry.attempts + 1,
		deadline: now.Add(cfg.LeaderTimeout),
		done:     make(chan struct{}),
		cancel:   cancel,
	}
	l.coalescer.entries[entry.key] = newEntry
	l.coalescer.armExpiryLocked(newEntry, cfg.LeaderTimeout)
	l.entry = newEntry
	l.role = probeLeader
	l.leaderCtx = leaderCtx
	l.finished.Store(false)
	return true
}

func (l *ProbeLease) Finish(success bool, requestID string, account *service.Account) {
	if l == nil || l.coalescer == nil || l.entry == nil || !l.IsLeader() || !l.finished.CompareAndSwap(false, true) {
		return
	}
	l.coalescer.mu.Lock()
	entry := l.entry
	if !entry.active {
		l.coalescer.mu.Unlock()
		return
	}
	if success && (account == nil || !entry.billingReady || entry.billingErr != nil) {
		// A valid upstream body without a durable provider-cost/user billing row
		// is not a reusable health result. Followers must take over normally.
		success = false
	}
	account = probeAccountSnapshot(account)
	entry.active = false
	entry.result = probeResult{success: success, leaderRequestID: requestID, leaderAccount: account}
	if success {
		entry.healthyTo = l.coalescer.now().Add(l.coalescer.Config().Window)
	}
	cancel := entry.cancel
	expiry := entry.expiry
	close(entry.done)
	l.coalescer.mu.Unlock()
	if expiry != nil {
		expiry.Stop()
	}
	if cancel != nil {
		cancel()
	}
}

// probeArithmeticRE accepts the exact monitor few-shot template, allowing
// only insignificant line/space differences and operands in the same range
// as the monitor generator.  This is intentionally stricter than a generic
// "contains arithmetic" heuristic to prevent real user requests being mocked.
var probeArithmeticRE = regexp.MustCompile(`(?s)^Calculate\s+and\s+respond\s+with\s+ONLY\s+the\s+number,\s+nothing\s+else\.\s*Q:\s*3\s*\+\s*5\s*=\s*\?\s*A:\s*8\s*Q:\s*12\s*-\s*7\s*=\s*\?\s*A:\s*5\s*Q:\s*(\d{1,2})\s*([+-])\s*(\d{1,2})\s*=\s*\?\s*A:\s*$`)
var probeNumberTokenRE = regexp.MustCompile(`-?\d+`)

func parseProbePrompt(prompt string) (expected string, ok bool) {
	prompt = strings.ReplaceAll(prompt, "\r\n", "\n")
	prompt = strings.TrimSpace(prompt)
	m := probeArithmeticRE.FindStringSubmatch(prompt)
	if len(m) != 4 {
		return "", false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[3])
	if a < 1 || a > 50 || b < 1 || b > 50 {
		return "", false
	}
	if m[2] == "+" {
		return strconv.Itoa(a + b), true
	}
	if a < b {
		return "", false
	}
	return strconv.Itoa(a - b), true
}

func parseStrictProbeBody(path string, body []byte) (ProbeCandidate, bool) {
	if !looksLikeChannelMonitorProbe(path, body) {
		return ProbeCandidate{}, false
	}
	candidate, ok := parseStrictProbeBodyDetailed(path, body)
	if !ok {
		reason := classifyProbeParseReject(path, body)
		recordProbeParseReject(path, reason)
	}
	return candidate, ok
}

// parseStrictProbeBodyDetailed recognizes only the exact challenge templates
// emitted by channel_monitor_checker.go. Gemini is slightly different from
// the OpenAI/Anthropic protocols: its model is carried in the URL, not JSON.
func parseStrictProbeBodyDetailed(path string, body []byte) (ProbeCandidate, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return ProbeCandidate{}, false
	}
	if strings.Contains(path, "input_tokens") {
		return ProbeCandidate{}, false
	}
	protocol := probeProtocol("")
	var prompt string
	var ok bool
	model := ""
	if strings.Contains(path, "/v1beta/models/") {
		var actionOK bool
		model, actionOK = geminiProbeModelFromPath(path)
		if !actionOK {
			return ProbeCandidate{}, false
		}
		if !exactKeys(raw, "contents", "generationConfig") {
			return ProbeCandidate{}, false
		}
		var generation map[string]json.RawMessage
		if json.Unmarshal(raw["generationConfig"], &generation) != nil ||
			!exactKeys(generation, "maxOutputTokens") || !validProbeTokenLimit(rawInt(generation, "maxOutputTokens")) {
			return ProbeCandidate{}, false
		}
		prompt, ok = singleGeminiText(raw["contents"])
		protocol = probeProtocolGemini
	} else {
		var modelOK bool
		model, modelOK = rawString(raw, "model")
		if !modelOK || strings.TrimSpace(model) == "" {
			return ProbeCandidate{}, false
		}
		switch {
		case strings.Contains(path, "/chat/completions"):
			if !strictChatProbeKeys(raw) || !validProbeTokenLimit(rawInt(raw, "max_tokens")) || (rawHas(raw, "stream") && rawBool(raw, "stream")) {
				return ProbeCandidate{}, false
			}
			prompt, ok = singleUserText(raw["messages"])
			protocol = probeProtocolChat
		case strings.Contains(path, "/responses"):
			if !strictResponseProbeKeys(raw) || !validProbeTokenLimit(rawInt(raw, "max_output_tokens")) || (rawHas(raw, "stream") && rawBool(raw, "stream")) {
				return ProbeCandidate{}, false
			}
			instructions, iok := rawString(raw, "instructions")
			prompt, ok = rawString(raw, "input")
			if !iok || instructions != "You are a channel health-check endpoint. Answer the arithmetic challenge exactly and briefly." {
				return ProbeCandidate{}, false
			}
			protocol = probeProtocolResponse
		case strings.Contains(path, "/messages"):
			if !strictAnthropicProbeKeys(raw) || !validProbeTokenLimit(rawInt(raw, "max_tokens")) || (rawHas(raw, "stream") && rawBool(raw, "stream")) {
				return ProbeCandidate{}, false
			}
			prompt, ok = singleUserText(raw["messages"])
			protocol = probeProtocolAnthropic
		default:
			return ProbeCandidate{}, false
		}
	}
	if !ok {
		return ProbeCandidate{}, false
	}
	expected, ok := parseProbePrompt(prompt)
	if !ok {
		return ProbeCandidate{}, false
	}
	platform := service.PlatformOpenAI
	switch protocol {
	case probeProtocolAnthropic:
		platform = service.PlatformAnthropic
	case probeProtocolGemini:
		platform = service.PlatformGemini
	}
	// Reuse the billing path's estimator so Unicode and future estimator
	// corrections apply equally to synthetic probe charges. Gemini's estimator
	// expects a native contents envelope, so wrap the extracted text before
	// estimating rather than passing a bare string (which correctly estimates
	// to zero for a malformed native request).
	inputTokens := estimateProbeTextTokens(protocol, platform, prompt)
	outputTokens := estimateProbeTextTokens(protocol, platform, expected)
	if inputTokens <= 0 || outputTokens <= 0 {
		return ProbeCandidate{}, false
	}
	return ProbeCandidate{Protocol: protocol, Model: model, Prompt: prompt, Expected: expected, InputTokens: inputTokens, OutputTokens: outputTokens}, true
}

func estimateProbeTextTokens(protocol probeProtocol, platform, text string) int {
	if protocol != probeProtocolGemini {
		return service.EstimateFailurePromptTokens(platform, []byte(text))
	}
	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{{"parts": []map[string]string{{"text": text}}}},
	})
	if err != nil {
		return 0
	}
	return service.EstimateFailurePromptTokens(platform, body)
}

func geminiProbeModelFromPath(path string) (string, bool) {
	const prefix = "/v1beta/models/"
	idx := strings.Index(path, prefix)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimPrefix(path[idx+len(prefix):], "/")
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 || colon == len(rest)-1 {
		return "", false
	}
	model := strings.TrimSpace(rest[:colon])
	action := strings.TrimSpace(rest[colon+1:])
	if action != "generateContent" || !service.IsSafeGeminiModelPathSegment(model) {
		return "", false
	}
	return model, true
}

func singleGeminiText(raw json.RawMessage) (string, bool) {
	var contents []json.RawMessage
	if json.Unmarshal(raw, &contents) != nil || len(contents) != 1 {
		return "", false
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(contents[0], &content) != nil {
		return "", false
	}
	// The monitor emits only parts. A canonical optional role=user is accepted
	// for compatibility, while all other fields remain intentionally rejected.
	if len(content) == 2 {
		if _, ok := content["role"]; !ok {
			return "", false
		}
		role, roleOK := rawString(content, "role")
		if !roleOK || role != "user" {
			return "", false
		}
	} else if len(content) != 1 {
		return "", false
	}
	partsRaw, ok := content["parts"]
	if !ok {
		return "", false
	}
	var parts []json.RawMessage
	if json.Unmarshal(partsRaw, &parts) != nil || len(parts) != 1 {
		return "", false
	}
	var part map[string]json.RawMessage
	if json.Unmarshal(parts[0], &part) != nil || !exactKeys(part, "text") {
		return "", false
	}
	text, ok := rawString(part, "text")
	return text, ok && strings.TrimSpace(text) != ""
}

func probeRouteClass(path string) string {
	switch {
	case strings.Contains(path, "/v1beta/models/"):
		return "gemini_v1beta"
	case strings.Contains(path, "/chat/completions"):
		return "openai_chat"
	case strings.Contains(path, "/responses"):
		return "openai_responses"
	case strings.Contains(path, "/messages"):
		return "anthropic_messages"
	default:
		return "other"
	}
}

func classifyProbeParseReject(path string, body []byte) ProbeParseRejectReason {
	if !json.Valid(body) {
		return probeRejectInvalidJSON
	}
	if strings.Contains(path, "input_tokens") {
		return probeRejectInputTokens
	}
	if strings.Contains(path, "/v1beta/models/") {
		if strings.Contains(path, ":streamGenerateContent") {
			return probeRejectStreaming
		}
		if _, ok := geminiProbeModelFromPath(path); !ok {
			return probeRejectGeminiAction
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(body, &raw) != nil || !exactKeys(raw, "contents", "generationConfig") {
			return probeRejectBodyShape
		}
		var generation map[string]json.RawMessage
		if json.Unmarshal(raw["generationConfig"], &generation) == nil &&
			exactKeys(generation, "maxOutputTokens") &&
			!validProbeTokenLimit(rawInt(generation, "maxOutputTokens")) {
			return probeRejectTokenLimit
		}
		return probeRejectPrompt
	}
	if !strings.Contains(path, "/chat/completions") && !strings.Contains(path, "/responses") && !strings.Contains(path, "/messages") {
		return probeRejectUnsupportedPath
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return probeRejectInvalidJSON
	}
	if _, ok := raw["model"]; !ok {
		return probeRejectMissingModel
	}
	switch {
	case strings.Contains(path, "/chat/completions"):
		if rawHas(raw, "stream") && rawBool(raw, "stream") {
			return probeRejectStreaming
		}
		if !strictChatProbeKeys(raw) {
			return probeRejectBodyShape
		}
		if !validProbeTokenLimit(rawInt(raw, "max_tokens")) {
			return probeRejectTokenLimit
		}
		if _, ok := singleUserText(raw["messages"]); !ok {
			return probeRejectMessageShape
		}
	case strings.Contains(path, "/responses"):
		if rawHas(raw, "stream") && rawBool(raw, "stream") {
			return probeRejectStreaming
		}
		if !strictResponseProbeKeys(raw) {
			return probeRejectBodyShape
		}
		if !validProbeTokenLimit(rawInt(raw, "max_output_tokens")) {
			return probeRejectTokenLimit
		}
	case strings.Contains(path, "/messages"):
		if !strictAnthropicProbeKeys(raw) {
			return probeRejectBodyShape
		}
		if !validProbeTokenLimit(rawInt(raw, "max_tokens")) {
			return probeRejectTokenLimit
		}
		if _, ok := singleUserText(raw["messages"]); !ok {
			return probeRejectMessageShape
		}
	default:
		return probeRejectUnsupportedPath
	}
	return probeRejectPrompt
}

func exactKeys(raw map[string]json.RawMessage, keys ...string) bool {
	if len(raw) != len(keys) {
		return false
	}
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	for k := range raw {
		if _, ok := want[k]; !ok {
			return false
		}
	}
	return true
}

// strictChatProbeKeys accepts both canonical bodies emitted by the monitor:
// the OpenAI adapter omits stream for its non-streaming request, while the
// other OpenAI-compatible adapters explicitly send stream:false.  No other
// optional fields are accepted, so ordinary tool/multimodal requests cannot
// enter the synthetic path accidentally.
func strictChatProbeKeys(raw map[string]json.RawMessage) bool {
	return exactKeys(raw, "model", "messages", "max_tokens") ||
		exactKeys(raw, "model", "messages", "max_tokens", "stream")
}

func strictAnthropicProbeKeys(raw map[string]json.RawMessage) bool {
	if exactKeys(raw, "model", "messages", "max_tokens") ||
		exactKeys(raw, "model", "messages", "max_tokens", "stream") {
		return true
	}
	return isClaudeCodeProbeBody(raw)
}

func strictResponseProbeKeys(raw map[string]json.RawMessage) bool {
	return exactKeys(raw, "model", "instructions", "input", "max_output_tokens") ||
		exactKeys(raw, "model", "instructions", "input", "max_output_tokens", "stream")
}

// validProbeTokenLimit accepts the default 50-token request and the built-in
// low-token templates (currently 20).  The arithmetic prompt is still parsed
// strictly, so this does not turn arbitrary low-budget requests into probes.
func validProbeTokenLimit(n int) bool {
	return n >= 1 && n <= 50
}

const claudeCodeProbeSystemText = "You are Claude Code, Anthropic's official CLI for Claude."

// isClaudeCodeProbeBody permits only the fixed, seeded Claude Code monitor
// metadata. Arbitrary system/metadata fields remain outside the coalesced
// path because they can represent a real user request.
func isClaudeCodeProbeBody(raw map[string]json.RawMessage) bool {
	baseKeys := map[string]struct{}{
		"model": {}, "messages": {}, "max_tokens": {},
	}
	if _, ok := raw["stream"]; ok {
		baseKeys["stream"] = struct{}{}
	}
	if _, ok := raw["system"]; !ok {
		return false
	}
	if _, ok := raw["metadata"]; !ok {
		return false
	}
	if len(raw) != len(baseKeys)+2 {
		return false
	}
	for key := range raw {
		if _, ok := baseKeys[key]; !ok && key != "system" && key != "metadata" {
			return false
		}
	}

	var system []map[string]json.RawMessage
	if json.Unmarshal(raw["system"], &system) != nil || len(system) != 1 ||
		!exactKeys(system[0], "type", "text") {
		return false
	}
	systemType, typeOK := rawString(system[0], "type")
	systemText, textOK := rawString(system[0], "text")
	if !typeOK || systemType != "text" || !textOK || systemText != claudeCodeProbeSystemText {
		return false
	}

	var metadata map[string]json.RawMessage
	if json.Unmarshal(raw["metadata"], &metadata) != nil || !exactKeys(metadata, "user_id") {
		return false
	}
	userID, ok := rawString(metadata, "user_id")
	return ok && strings.HasPrefix(userID, "user_") &&
		strings.Contains(userID, "_account_") && strings.Contains(userID, "_session_")
}

func rawHas(raw map[string]json.RawMessage, key string) bool {
	_, ok := raw[key]
	return ok
}

func rawString(raw map[string]json.RawMessage, key string) (string, bool) {
	var v string
	b, ok := raw[key]
	if !ok || json.Unmarshal(b, &v) != nil {
		return "", false
	}
	return v, true
}

func rawInt(raw map[string]json.RawMessage, key string) int {
	var v int
	if json.Unmarshal(raw[key], &v) != nil {
		return 0
	}
	return v
}

func rawBool(raw map[string]json.RawMessage, key string) bool {
	var v bool
	if json.Unmarshal(raw[key], &v) != nil {
		return true
	}
	return v
}

func singleUserText(raw json.RawMessage) (string, bool) {
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &messages) != nil || len(messages) != 1 || messages[0].Role != "user" {
		return "", false
	}
	var text string
	if json.Unmarshal(messages[0].Content, &text) != nil {
		return "", false
	}
	return text, strings.TrimSpace(text) != ""
}

func freshProbeID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + hex.EncodeToString(b[:])
}

func (p ProbeCandidate) syntheticBody() ([]byte, string) {
	switch p.Protocol {
	case probeProtocolAnthropic:
		body := map[string]any{"id": freshProbeID("msg_"), "type": "message", "role": "assistant", "model": p.Model, "content": []map[string]any{{"type": "text", "text": p.Expected}}, "stop_reason": "end_turn", "stop_sequence": nil, "usage": map[string]int{"input_tokens": p.InputTokens, "output_tokens": p.OutputTokens}}
		b, _ := json.Marshal(body)
		return b, "application/json"
	case probeProtocolChat:
		body := map[string]any{"id": freshProbeID("chatcmpl-"), "object": "chat.completion", "created": time.Now().Unix(), "model": p.Model, "choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": p.Expected}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": p.InputTokens, "completion_tokens": p.OutputTokens, "total_tokens": p.InputTokens + p.OutputTokens}}
		b, _ := json.Marshal(body)
		return b, "application/json"
	case probeProtocolGemini:
		body := map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]string{{"text": p.Expected}},
					"role":  "model",
				},
				"finishReason": "STOP",
				"index":        0,
			}},
			"usageMetadata": map[string]int{
				"promptTokenCount":     p.InputTokens,
				"candidatesTokenCount": p.OutputTokens,
				"totalTokenCount":      p.InputTokens + p.OutputTokens,
			},
		}
		b, _ := json.Marshal(body)
		return b, "application/json"
	default:
		messageID := freshProbeID("msg_")
		body := map[string]any{"id": freshProbeID("resp_"), "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": p.Model, "output": []map[string]any{{"id": messageID, "type": "message", "status": "completed", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": p.Expected, "annotations": []any{}}}}}, "output_text": p.Expected, "usage": map[string]any{"input_tokens": p.InputTokens, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens": p.OutputTokens, "output_tokens_details": map[string]int{"reasoning_tokens": 0}, "total_tokens": p.InputTokens + p.OutputTokens}}
		b, _ := json.Marshal(body)
		return b, "application/json"
	}
}

// channelID is optional for compatibility with callers that do not resolve a
// channel before probe admission.  A group is currently bound to at most one
// channel, so including the resolved ID prevents a just-reassigned channel
// from inheriting the previous channel's short-lived health result.
func probeCandidateForRequest(c *gin.Context, body []byte, model string, groupID int64, target string, channelID ...int64) (ProbeCandidate, bool) {
	if c == nil || c.Request == nil || c.Request.Method != http.MethodPost {
		return ProbeCandidate{}, false
	}
	// A missing group is not a stable authorization pool. Do not let
	// malformed/legacy requests fall into the shared group-0 namespace.
	if groupID <= 0 {
		return ProbeCandidate{}, false
	}
	candidate, ok := parseStrictProbeBody(c.Request.URL.Path, body)
	if !ok {
		return ProbeCandidate{}, false
	}
	if strings.TrimSpace(model) != "" && candidate.Model != model {
		if looksLikeChannelMonitorProbe(c.Request.URL.Path, body) {
			recordProbeParseReject(c.Request.URL.Path, probeRejectModelMismatch)
		}
		return ProbeCandidate{}, false
	}
	resolvedChannelID := int64(0)
	if len(channelID) > 0 {
		resolvedChannelID = channelID[0]
	}
	workspaceID := int64(0)
	// Gateway routes normally do not carry an admin workspace scope.  When a
	// caller does provide one (for example a vendor-scoped gateway entrypoint),
	// include it so a shared group cannot reuse health state across workspaces.
	// Unrestricted/admin scope intentionally stays zero because it is global.
	if scope, ok := service.ScopeFromContext(c.Request.Context()); ok && !scope.Unrestricted && scope.WorkspaceID > 0 {
		workspaceID = scope.WorkspaceID
	}
	// Keep health state isolated by authorization pool, resolved provider and
	// channel.  The effective account/workspace set is not available until
	// account selection, so this remains a conservative pre-selection scope.
	candidate.Key = strings.Join([]string{
		strconv.FormatInt(groupID, 10),
		strconv.FormatInt(workspaceID, 10),
		strings.TrimSpace(target),
		strconv.FormatInt(resolvedChannelID, 10),
		string(candidate.Protocol),
		candidate.Model,
	}, "|")
	return candidate, true
}

type probeBillFunc func(context.Context, ProbeCandidate, *service.Account, string) error

// resolveProbeFollower returns handled=true when it wrote a synthetic response
// or a terminal billing error.  handled=false means the caller must continue
// the ordinary account-selection path (including when this follower was
// promoted to a leader after a failed leader).
func resolveProbeFollower(c *gin.Context, lease *ProbeLease, bill probeBillFunc) (handled bool, promoted bool, err error) {
	if lease == nil {
		return false, false, nil
	}
	if lease.IsExhausted() {
		return true, false, ErrProbeAttemptBudgetExhausted
	}
	if !lease.IsFollower() {
		return false, false, nil
	}
	resolution, resolveErr := lease.Resolve(c.Request.Context())
	if resolveErr != nil {
		// A disconnected follower must not fall through into account selection
		// and create a replacement upstream probe after its client is gone.
		if errors.Is(resolveErr, context.Canceled) || errors.Is(resolveErr, context.DeadlineExceeded) {
			return true, false, nil
		}
		if errors.Is(resolveErr, ErrProbeAttemptBudgetExhausted) {
			return true, false, resolveErr
		}
		if resolution.Leader || lease.IsLeader() {
			return false, true, nil
		}
		return true, false, resolveErr
	}
	if !resolution.Synthetic {
		if resolution.Leader || lease.IsLeader() {
			return false, true, nil
		}
		return false, false, nil
	}
	if resolution.LeaderAccount == nil || bill == nil {
		if lease.Promote() {
			return false, true, nil
		}
		return true, false, errors.New("probe coalescer has no billable leader snapshot")
	}
	if err := bill(c.Request.Context(), resolution.Candidate, resolution.LeaderAccount, resolution.LeaderRequestID); err != nil {
		// Only an unresolved price is safe to retry against the real upstream.
		// Billing, dependency, balance, and persistence errors are terminal: a
		// promotion after a debit could issue a second billable request.
		if errors.Is(err, service.ErrSyntheticProbePricingUnavailable) && lease.Promote() {
			return false, true, nil
		}
		return true, false, err
	}
	body, contentType := resolution.Candidate.syntheticBody()
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(body)
	return true, false, nil
}

func probeResolutionError(err error) (status int, code, message string) {
	if errors.Is(err, ErrProbeAttemptBudgetExhausted) {
		return http.StatusServiceUnavailable, "probe_unavailable", "Probe attempt budget exhausted"
	}
	if errors.Is(err, service.ErrSyntheticProbePricingUnavailable) {
		return http.StatusServiceUnavailable, "probe_unavailable", "Probe pricing is temporarily unavailable"
	}
	if errors.Is(err, service.ErrSyntheticProbeBillingUnavailable) ||
		errors.Is(err, service.ErrSyntheticProbeUsagePersistence) {
		return http.StatusServiceUnavailable, "probe_billing_unavailable", "Probe billing is temporarily unavailable"
	}
	return http.StatusPaymentRequired, "billing_error", err.Error()
}

func requestIDForProbe(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return "probe:" + freshProbeID("")
	}
	if ctx := c.Request.Context(); ctx != nil {
		if v, ok := ctx.Value(probeRequestIDKey{}).(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	base := ""
	// RequestLogger accepts X-Request-ID for operational correlation, so its
	// context value is not automatically a trusted idempotency key. Reuse it
	// only when the request did not bring that header from the client; otherwise
	// create a fresh server-side namespace value for this HTTP request.
	if strings.TrimSpace(c.Request.Header.Get("X-Request-ID")) == "" {
		if v, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok {
			base = strings.TrimSpace(v)
		}
	}
	if base == "" {
		base = freshProbeID("")
	}
	id := "probe:" + base
	if len(id) > 64 {
		// usage_logs.request_id is VARCHAR(64). Keep the durable billing key
		// within that contract; unusually long correlation IDs get a fresh
		// server-generated value rather than a truncated/colliding key.
		id = "probe:" + freshProbeID("")
	}
	c.Request = c.Request.WithContext(withProbeRequestID(c.Request.Context(), id))
	return id
}

// prepareProbeAdmission marks a recognized probe in the request context so a
// real upstream probe can be split out in account-concurrency reporting. The
// marker is internal bookkeeping only; it does not change billing IDs,
// idempotency, or the response path. A coalescer request ID is allocated only
// in active mode, where the leader context must carry it through Begin.
func prepareProbeAdmission(c *gin.Context, coalescer *ProbeCoalescer) (context.Context, string) {
	if c == nil || c.Request == nil {
		return context.Background(), ""
	}
	// Mark the request before creating the leader context. Active leaders derive
	// their cancellable context inside Begin; marking afterward would be lost
	// when installProbeLeader replaces c.Request with that derived context.
	ctx := service.WithProbeAccountConcurrency(c.Request.Context(), true)
	c.Request = c.Request.WithContext(ctx)
	if coalescer == nil || coalescer.Config().Mode != ProbeCoalescingActive {
		return ctx, ""
	}
	id := requestIDForProbe(c)
	return c.Request.Context(), id
}

// markProbeAccountConcurrency marks only requests that will make a real
// upstream call. Active followers return before account selection and remain
// unmarked; shadow/off requests continue through the normal upstream path and
// are intentionally counted as probe leaders for the admin breakdown.
func markProbeAccountConcurrency(c *gin.Context, lease *ProbeLease, candidateDetected bool) {
	if c == nil || c.Request == nil || !candidateDetected {
		return
	}
	if lease != nil && lease.IsFollower() {
		return
	}
	c.Request = c.Request.WithContext(service.WithProbeAccountConcurrency(c.Request.Context(), true))
}

const probeLeaderAccountContextKey = "sub2api.probe_leader_account"

func setProbeLeaderAccount(c *gin.Context, account *service.Account) {
	if c != nil && account != nil {
		// The scheduler may reuse or refresh its Account object after the
		// upstream call. Keep the billing/audit snapshot stable for followers.
		c.Set(probeLeaderAccountContextKey, probeAccountSnapshot(account))
	}
}

func getProbeLeaderAccount(c *gin.Context) *service.Account {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(probeLeaderAccountContextKey); ok {
		if account, ok := value.(*service.Account); ok {
			return account
		}
	}
	return nil
}

func installProbeLeader(c *gin.Context, lease *ProbeLease, requestID string) func() {
	if lease == nil || !lease.IsLeader() {
		return func() {}
	}
	bindProbeLeaderContext(c, lease)
	capture, original := installProbeCapture(c)
	if capture == nil || original == nil {
		return func() { lease.Finish(false, requestID, nil) }
	}
	return func() {
		c.Writer = original
		account := getProbeLeaderAccount(c)
		lease.Finish(validateProbeResponse(lease.Candidate(), capture.status, capture.buf.Bytes()), requestID, account)
		capture.CopyTo(original)
	}
}

// bindProbeLeaderContext replaces the request context only for a real leader.
// The derived context retains all request values (including ProbeRequestID)
// but is canceled when the coalescer deadline/finalization closes the lease.
func bindProbeLeaderContext(c *gin.Context, lease *ProbeLease) {
	if c == nil || c.Request == nil || lease == nil || !lease.IsLeader() {
		return
	}
	if ctx := lease.LeaderContext(); ctx != nil {
		c.Request = c.Request.WithContext(ctx)
	}
}

const probeLeaderBillingTimeout = 10 * time.Second

// runProbeLeaderUsageTask executes the provider-cost/user billing task inline
// for a probe leader. The normal worker pool is intentionally bypassed here:
// its drop/sample overflow policies are valid for ordinary traffic but cannot
// be allowed to publish a reusable health result before billing is durable.
// parent cancellation is detached while preserving context values, so a client
// disconnect does not abandon the leader's money event.
func runProbeLeaderUsageTask(parent context.Context, lease *ProbeLease, task func(context.Context) error, publishHealthy bool) error {
	if lease == nil || !lease.IsLeader() {
		return nil
	}
	if task == nil {
		err := errors.New("probe leader usage task is nil")
		if publishHealthy {
			lease.MarkBillingReady(err)
		}
		return err
	}
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	base = context.WithValue(base, ctxkey.ProbeUsagePersistenceRequired, true)
	ctx, cancel := context.WithTimeout(base, probeLeaderBillingTimeout)
	defer cancel()
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("probe leader usage task panic: %v", recovered)
			}
		}()
		err = task(ctx)
	}()
	if publishHealthy {
		lease.MarkBillingReady(err)
	}
	return err
}

// probeCaptureWriter buffers only non-streaming leader responses.  It lets us
// validate the arithmetic answer before publishing health and avoids replaying
// a leader's IDs/body to followers.
type probeCaptureWriter struct {
	gin.ResponseWriter
	status int
	buf    bytes.Buffer
	wrote  bool
}

func (w *probeCaptureWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
}
func (w *probeCaptureWriter) WriteHeaderNow() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
}
func (w *probeCaptureWriter) Write(b []byte) (int, error)       { w.WriteHeaderNow(); return w.buf.Write(b) }
func (w *probeCaptureWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *probeCaptureWriter) Flush()                            { w.WriteHeaderNow() }
func (w *probeCaptureWriter) CopyTo(dst gin.ResponseWriter) {
	for k, vals := range w.Header() {
		dst.Header()[k] = append([]string(nil), vals...)
	}
	if w.wrote {
		dst.WriteHeader(w.status)
	}
	_, _ = dst.Write(w.buf.Bytes())
}

func installProbeCapture(c *gin.Context) (*probeCaptureWriter, gin.ResponseWriter) {
	if c == nil || c.Writer == nil {
		return nil, nil
	}
	original := c.Writer
	w := &probeCaptureWriter{ResponseWriter: original}
	c.Writer = w
	return w, original
}

func validateProbeResponse(candidate ProbeCandidate, status int, body []byte) bool {
	if status < 200 || status >= 300 || len(body) == 0 {
		return false
	}
	var text string
	switch candidate.Protocol {
	case probeProtocolAnthropic:
		var v struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(body, &v) != nil {
			return false
		}
		for _, x := range v.Content {
			if x.Type == "text" {
				text += x.Text
			}
		}
	case probeProtocolChat:
		var v struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &v) != nil || len(v.Choices) == 0 {
			return false
		}
		text = v.Choices[0].Message.Content
	case probeProtocolGemini:
		var v struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if json.Unmarshal(body, &v) != nil || len(v.Candidates) == 0 {
			return false
		}
		for _, part := range v.Candidates[0].Content.Parts {
			text += part.Text
		}
	default:
		var v struct {
			OutputText string `json:"output_text"`
			Output     []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if json.Unmarshal(body, &v) != nil {
			return false
		}
		text = v.OutputText
		if strings.TrimSpace(text) == "" {
			var parts []string
			for _, item := range v.Output {
				if item.Type != "" && item.Type != "message" {
					continue
				}
				for _, content := range item.Content {
					if content.Type == "output_text" || content.Type == "text" {
						parts = append(parts, content.Text)
					}
				}
			}
			text = strings.Join(parts, "\n")
		}
	}
	for _, token := range probeNumberTokenRE.FindAllString(text, -1) {
		if token == candidate.Expected {
			return true
		}
	}
	return false
}

func optionalGroupID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

type probeRequestIDKey struct{}

func withProbeRequestID(ctx context.Context, id string) context.Context {
	ctx = context.WithValue(ctx, probeRequestIDKey{}, id)
	return context.WithValue(ctx, ctxkey.ProbeRequestID, id)
}
