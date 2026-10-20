package admin

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

// windowCostSampleConcurrency bounds the per-account window-cost queries
// (PostgreSQL aggregates) one sample may run at the same time.
const windowCostSampleConcurrency = 10

// AccountRuntimeBatchRequest is the body of POST /api/v1/admin/accounts/runtime/batch.
type AccountRuntimeBatchRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

// AccountRuntimeItem is one account's live sample.
//
// A counter that could not be read is never reported as 0: concurrency is
// null with concurrency_available=false, the other metrics are omitted
// (they are also omitted when they do not apply to the account).
type AccountRuntimeItem struct {
	CurrentConcurrency     *int       `json:"current_concurrency"`
	ProbeConcurrency       *int       `json:"probe_concurrency"`
	ConcurrencyAvailable   bool       `json:"concurrency_available"`
	CurrentRPM             *int       `json:"current_rpm,omitempty"`
	SchedulingRPM          *int       `json:"scheduling_rpm,omitempty"`
	ActiveSessions         *int       `json:"active_sessions,omitempty"`
	CurrentWindowCost      *float64   `json:"current_window_cost,omitempty"`
	Status                 string     `json:"status"`
	Schedulable            bool       `json:"schedulable"`
	RateLimitResetAt       *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil          *time.Time `json:"overload_until"`
	TempUnschedulableUntil *time.Time `json:"temp_unschedulable_until"`
}

type accountRuntimeBatchPayload struct {
	// SampledAt is when the counters were read (RFC3339, UTC); compare the
	// *_until / *_reset_at times against it rather than the device clock.
	SampledAt string                        `json:"sampled_at"`
	Items     map[string]AccountRuntimeItem `json:"items"`
}

// accountRuntimeSample is one batched read of the live counters for a set of
// accounts. An account missing from a map means its value is unknown (read
// failed, counter not wired, or not applicable) and must not be shown as 0.
type accountRuntimeSample struct {
	concurrency    map[int64]service.AccountConcurrencyBreakdown
	windowCosts    map[int64]float64
	activeSessions map[int64]int
	schedulingRPM  map[int64]int
	rollingRPM     map[int64]int
}

func (s accountRuntimeSample) concurrencyFor(accountID int64) (service.AccountConcurrencyBreakdown, bool) {
	breakdown, ok := s.concurrency[accountID]
	return breakdown, ok
}

// applyTo copies the sample onto a list or detail row. Unknown concurrency
// stays 0 there (the Web reads an int) with ConcurrencyAvailable=false.
func (s accountRuntimeSample) applyTo(item *AccountWithConcurrency, accountID int64) {
	if breakdown, ok := s.concurrencyFor(accountID); ok {
		item.CurrentConcurrency = breakdown.Current
		item.ProbeConcurrency = breakdown.Probe
		item.ConcurrencyAvailable = true
	}
	item.CurrentWindowCost = sampledValue(s.windowCosts, accountID)
	item.ActiveSessions = sampledValue(s.activeSessions, accountID)
	item.SchedulingRPM = sampledValue(s.schedulingRPM, accountID)
	item.CurrentRPM = sampledValue(s.rollingRPM, accountID)
}

func sampledValue[T any](values map[int64]T, accountID int64) *T {
	value, ok := values[accountID]
	if !ok {
		return nil
	}
	return &value
}

// sampleRuntime reads the live counters for accounts with one batched call per
// counter. Shared by the account list and runtime/batch so both report the
// same values; per-account failures leave that account out of the map.
func (h *AccountHandler) sampleRuntime(ctx context.Context, accounts []*service.Account, includeWindowCost bool) accountRuntimeSample {
	var sample accountRuntimeSample
	accountIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	if len(accountIDs) == 0 {
		return sample
	}

	// 并发数（Redis ZCARD，极低开销）。读取失败时整张表为空，各账号都是“不可用”。
	if h.concurrencyService != nil {
		if counts, err := h.concurrencyService.GetAccountConcurrencyBreakdownBatch(ctx, accountIDs); err == nil {
			sample.concurrency = counts
		}
	}

	// 一次 Redis 批量读取，不依赖账号类型或是否配置 RPM 限额。
	// 读取失败的账号不在返回 map 中，current_rpm 保持缺省而非伪造为零。
	if h.rpmCache != nil {
		sample.schedulingRPM, _ = h.rpmCache.GetRPMBatch(ctx, accountIDs)
		if rolling, ok := h.rpmCache.(service.RollingRPMCache); ok {
			sample.rollingRPM, _ = rolling.GetRollingRPMBatch(ctx, accountIDs)
		}
	}

	sample.activeSessions = h.sampleActiveSessions(ctx, accounts)
	if includeWindowCost {
		sample.windowCosts = h.sampleWindowCosts(ctx, accounts)
	}
	return sample
}

// sampleActiveSessions 只查启用了会话数限制的 Anthropic OAuth / Setup Token 账号（Redis ZCARD，低开销）。
func (h *AccountHandler) sampleActiveSessions(ctx context.Context, accounts []*service.Account) map[int64]int {
	if h.sessionLimitCache == nil {
		return nil
	}
	accountIDs := make([]int64, 0)
	idleTimeouts := make(map[int64]time.Duration)
	for _, account := range accounts {
		if account == nil || !account.IsAnthropicOAuthOrSetupToken() || account.GetMaxSessions() <= 0 {
			continue
		}
		accountIDs = append(accountIDs, account.ID)
		idleTimeouts[account.ID] = time.Duration(account.GetSessionIdleTimeoutMinutes()) * time.Minute
	}
	if len(accountIDs) == 0 {
		return nil
	}
	sessions, _ := h.sessionLimitCache.GetActiveSessionCountBatch(ctx, accountIDs, idleTimeouts)
	return sessions
}

// sampleWindowCosts 只查启用了窗口费用限制的 Anthropic OAuth / Setup Token 账号
// （每个账号一条 PostgreSQL 聚合，并发受限）；单个账号失败只缺该账号。
func (h *AccountHandler) sampleWindowCosts(ctx context.Context, accounts []*service.Account) map[int64]float64 {
	if h.accountUsageService == nil {
		return nil
	}
	targets := make([]*service.Account, 0)
	for _, account := range accounts {
		if account != nil && account.IsAnthropicOAuthOrSetupToken() && account.GetWindowCostLimit() > 0 {
			targets = append(targets, account)
		}
	}
	if len(targets) == 0 {
		return nil
	}

	costs := make(map[int64]float64, len(targets))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(windowCostSampleConcurrency)
	for _, account := range targets {
		g.Go(func() error {
			// 统一的窗口开始时间计算逻辑（考虑窗口过期情况）
			stats, err := h.accountUsageService.GetAccountWindowStats(gctx, account.ID, account.GetCurrentWindowStartTime())
			if err == nil && stats != nil {
				mu.Lock()
				costs[account.ID] = stats.StandardCost // 使用标准费用
				mu.Unlock()
			}
			return nil // 允许部分失败
		})
	}
	_ = g.Wait()
	return costs
}

// GetBatchRuntime samples live counters and scheduling state for the given accounts.
// POST /api/v1/admin/accounts/runtime/batch?include_window_cost=0 (optional; default 1)
func (h *AccountHandler) GetBatchRuntime(c *gin.Context) {
	// 只读轮询（App 自动刷新约每 30 秒一次），与只读 GET 一样不写审计日志。
	middleware.SkipAudit(c)

	var req AccountRuntimeBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	accountIDs := normalizeInt64IDList(req.AccountIDs)
	if len(accountIDs) > service.MaxAccountRuntimeBatchIDs {
		response.ErrorFrom(c, service.ErrTooManyRuntimeAccountIDs)
		return
	}
	includeWindowCost := parseBoolQueryWithDefault(c.Query("include_window_cost"), true)

	sampledAt := time.Now().UTC()
	items, err := h.collectAccountRuntime(c.Request.Context(), accountIDs, includeWindowCost)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, accountRuntimeBatchPayload{SampledAt: sampledAt.Format(time.RFC3339), Items: items})
}

// collectAccountRuntime narrows accountIDs to the caller's workspace before
// reading anything, then samples the accounts that are left. Out-of-scope and
// unknown IDs are simply absent, so the response is not an existence probe.
func (h *AccountHandler) collectAccountRuntime(ctx context.Context, accountIDs []int64, includeWindowCost bool) (map[string]AccountRuntimeItem, error) {
	items := map[string]AccountRuntimeItem{}
	if len(accountIDs) == 0 {
		return items, nil
	}
	scopedIDs, err := h.adminService.FilterAccountIDsByScope(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	if len(scopedIDs) == 0 {
		return items, nil
	}
	// GetAccountsByIDs re-checks the workspace of every loaded account.
	loaded, err := h.adminService.GetAccountsByIDs(ctx, scopedIDs)
	if err != nil {
		return nil, err
	}
	accounts := make([]*service.Account, 0, len(loaded))
	for _, account := range loaded {
		if account != nil {
			accounts = append(accounts, account)
		}
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })

	sample := h.sampleRuntime(ctx, accounts, includeWindowCost)
	for _, account := range accounts {
		items[strconv.FormatInt(account.ID, 10)] = newAccountRuntimeItem(account, sample)
	}
	return items, nil
}

func newAccountRuntimeItem(account *service.Account, sample accountRuntimeSample) AccountRuntimeItem {
	item := AccountRuntimeItem{
		Status:                 account.Status,
		Schedulable:            account.Schedulable,
		RateLimitResetAt:       utcTimePtr(account.RateLimitResetAt),
		OverloadUntil:          utcTimePtr(account.OverloadUntil),
		TempUnschedulableUntil: utcTimePtr(account.TempUnschedulableUntil),
		CurrentWindowCost:      sampledValue(sample.windowCosts, account.ID),
		ActiveSessions:         sampledValue(sample.activeSessions, account.ID),
		SchedulingRPM:          sampledValue(sample.schedulingRPM, account.ID),
		CurrentRPM:             sampledValue(sample.rollingRPM, account.ID),
	}
	if breakdown, ok := sample.concurrencyFor(account.ID); ok {
		current, probe := breakdown.Current, breakdown.Probe
		item.CurrentConcurrency = &current
		item.ProbeConcurrency = &probe
		item.ConcurrencyAvailable = true
	}
	return item
}

func utcTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}
