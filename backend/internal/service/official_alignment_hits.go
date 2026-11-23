package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

var alignmentHitFields = [...]string{
	ClaudeFieldContextManagementMissingBeta, ClaudeFieldCacheControlLimitMode,
	ClaudeFieldUnknownBetaHeader, ClaudeFieldThinkingCacheControl,
}

type AlignmentHitStore interface {
	IncrBy(ctx context.Context, batch map[string]int64, ttl time.Duration) error
	// Read returns counts in key order. Missing buckets contribute zero.
	Read(ctx context.Context, keys []string) ([]int64, error)
}

type AlignmentHitCounts struct {
	Last24h int64 `json:"last_24h"`
	Last7d  int64 `json:"last_7d"`
}

type alignmentHitBucket struct {
	field     string
	accountID int64
	hour      int64
}

func alignmentHitKey(field, scope string, hour int64) string {
	return "official_align:hits:" + field + ":" + scope + ":" + time.Unix(hour*3600, 0).UTC().Format("2006010215")
}

type alignmentHitLog struct {
	field, mode, path     string
	accountID, suppressed int64
}

type hitLogLimit struct {
	window     atomic.Int64 // UTC second * 16 + number admitted in that second (0..10)
	suppressed atomic.Int64
}

type AlignmentHitRecorder struct {
	store   AlignmentHitStore
	pending sync.Map // alignmentHitBucket -> *atomic.Int64; -1 means claimed by flush
	// Only admin reads and background writes coordinate Redis and pending state.
	storeMu             sync.Mutex
	limits              [len(alignmentHitFields)]hitLogLimit
	logs                chan alignmentHitLog
	warnAt              atomic.Int64
	startOnce, stopOnce sync.Once
	stop, done          chan struct{}
}

func NewAlignmentHitRecorder(store AlignmentHitStore) *AlignmentHitRecorder {
	return &AlignmentHitRecorder{store: store, logs: make(chan alignmentHitLog, 40), stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *SettingService) AlignmentHits() *AlignmentHitRecorder {
	if s == nil {
		return nil
	}
	return s.alignmentHits
}

func (r *AlignmentHitRecorder) Record(c *gin.Context, field string, accountID int64, mode string) {
	if r == nil || c == nil || c.Request == nil {
		return
	}
	i := slices.Index(alignmentHitFields[:], field)
	if i < 0 {
		return
	}
	marker := "official_align:counted:" + field
	if _, seen := c.Get(marker); seen {
		return
	}
	c.Set(marker, true)
	now := time.Now().Unix()
	key := alignmentHitBucket{field: field, accountID: accountID, hour: now / 3600}
	for {
		value, found := r.pending.Load(key)
		if !found {
			fresh := new(atomic.Int64)
			fresh.Store(1)
			if value, found = r.pending.LoadOrStore(key, fresh); !found {
				break
			}
		}
		counter, ok := value.(*atomic.Int64)
		if !ok {
			return
		}
		count := counter.Load()
		if count < 0 {
			r.pending.CompareAndDelete(key, counter)
			continue
		}
		if counter.CompareAndSwap(count, count+1) {
			break
		}
	}
	var path string
	if c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	r.queueLog(&r.limits[i], now, alignmentHitLog{field: field, mode: mode, accountID: accountID, path: path})
}

// Neither a slow log sink nor a slow Redis flush can stall a request.
func (r *AlignmentHitRecorder) queueLog(limit *hitLogLimit, second int64, entry alignmentHitLog) {
	for {
		old := limit.window.Load()
		next := old + 1
		if old/16 < second {
			next = second*16 + 1
		} else if old%16 >= 10 {
			limit.suppressed.Add(1)
			return
		}
		if limit.window.CompareAndSwap(old, next) {
			break
		}
	}
	entry.suppressed = limit.suppressed.Swap(0)
	select {
	case r.logs <- entry:
	default:
		limit.suppressed.Add(entry.suppressed + 1)
	}
}

func (r *AlignmentHitRecorder) Start() {
	if r != nil {
		r.startOnce.Do(func() { go r.run() })
	}
}

func (r *AlignmentHitRecorder) Stop() {
	if r == nil {
		return
	}
	r.Start()
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
}

func (r *AlignmentHitRecorder) recoverWorker() {
	if v := recover(); v != nil && shouldLogClaudePolicyFailure(&r.warnAt) {
		slog.Warn("alignment hit worker panicked", "panic", v, "stack", string(debug.Stack()))
	}
}

func (r *AlignmentHitRecorder) writeLog(entry alignmentHitLog) {
	defer r.recoverWorker()
	slog.Info("official alignment switch hit", "alignment_switch", entry.field, "account_id", entry.accountID,
		"mode", entry.mode, "request_path", entry.path, "suppressed", entry.suppressed)
}

func (r *AlignmentHitRecorder) run() {
	defer close(r.done)
	defer r.recoverWorker()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case entry := <-r.logs:
			r.writeLog(entry)
		case <-ticker.C:
			r.flush()
		case <-r.stop:
			r.flush()
			for len(r.logs) > 0 {
				r.writeLog(<-r.logs)
			}
			return
		}
	}
}

func (r *AlignmentHitRecorder) flush() {
	defer r.recoverWorker()
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	batch := make(map[string]int64)
	r.rangePending(func(key alignmentHitBucket, counter *atomic.Int64) {
		n := counter.Swap(-1)
		r.pending.CompareAndDelete(key, counter)
		batch[alignmentHitKey(key.field, strconv.FormatInt(key.accountID, 10), key.hour)] += n
		batch[alignmentHitKey(key.field, "all", key.hour)] += n
	})
	if len(batch) == 0 || r.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.store.IncrBy(ctx, batch, 8*24*time.Hour); err != nil && shouldLogClaudePolicyFailure(&r.warnAt) {
		slog.Warn("alignment hit batch dropped", "error", err)
	}
}

func (r *AlignmentHitRecorder) rangePending(visit func(alignmentHitBucket, *atomic.Int64)) {
	r.pending.Range(func(k, v any) bool {
		key, validKey := k.(alignmentHitBucket)
		counter, validValue := v.(*atomic.Int64)
		if validKey && validValue {
			visit(key, counter)
		}
		return true
	})
}

func (r *AlignmentHitRecorder) Summary(ctx context.Context, accountID *int64) (map[string]AlignmentHitCounts, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("alignment hit store unavailable")
	}
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	const hours = 7 * 24
	now := time.Now().Unix() / 3600
	scope := "all"
	if accountID != nil {
		scope = strconv.FormatInt(*accountID, 10)
	}
	keys := make([]string, 0, len(alignmentHitFields)*hours)
	for _, field := range alignmentHitFields {
		for age := range int64(hours) {
			keys = append(keys, alignmentHitKey(field, scope, now-age))
		}
	}
	values, err := r.store.Read(ctx, keys)
	if err != nil {
		return nil, err
	}
	if len(values) != len(keys) {
		return nil, fmt.Errorf("alignment hit store returned %d counts for %d keys", len(values), len(keys))
	}
	result := make(map[string]AlignmentHitCounts, len(alignmentHitFields))
	add := func(field string, age, n int64) {
		counts := result[field]
		counts.Last7d += n
		if age < 24 {
			counts.Last24h += n
		}
		result[field] = counts
	}
	for i, n := range values {
		add(alignmentHitFields[i/hours], int64(i%hours), n)
	}
	r.rangePending(func(key alignmentHitBucket, counter *atomic.Int64) {
		age := now - key.hour
		if age >= 0 && age < hours && (accountID == nil || key.accountID == *accountID) {
			add(key.field, age, counter.Load())
		}
	})
	return result, nil
}
