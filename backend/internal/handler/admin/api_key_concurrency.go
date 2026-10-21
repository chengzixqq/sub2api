package admin

import (
	"context"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// apiKeyConcurrencyCounts reads the live request count of every key on one page
// in a single batch, from the same Redis slots the user-side GET /keys reads.
// The counts are best-effort: a nil service, an empty page or a read error all
// leave every key at 0 (ConcurrencyService already logs and zeroes Redis
// failures), and the list itself never fails because of it.
func apiKeyConcurrencyCounts(ctx context.Context, cs *service.ConcurrencyService, keys []service.APIKey) map[int64]int {
	if cs == nil || len(keys) == 0 {
		return map[int64]int{}
	}
	ids := make([]int64, 0, len(keys))
	for i := range keys {
		if keys[i].ID > 0 {
			ids = append(ids, keys[i].ID)
		}
	}
	if len(ids) == 0 {
		return map[int64]int{}
	}
	counts, err := cs.GetAPIKeyConcurrencyBatch(ctx, ids)
	if err != nil {
		slog.Warn("admin_api_key_concurrency_unavailable", "count", len(ids), "error", err)
		return map[int64]int{}
	}
	return counts
}
