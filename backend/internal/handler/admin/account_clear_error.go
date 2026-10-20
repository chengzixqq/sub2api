package admin

import (
	"sort"
	"sync"
)

// ClearAccountErrorRequest is the optional body of
// POST /admin/accounts/:id/clear-error.
//
// Without a body, or with preserve_inactive omitted / false, every account is
// reactivated (the historical behavior). preserve_inactive=true keeps the
// status of accounts that are neither active nor error and only clears their
// runtime limits; the returned account's status tells the client which
// happened. Old servers silently ignore the field, so clients must check the
// accounts.clear_error_preserve_inactive capability first.
type ClearAccountErrorRequest struct {
	PreserveInactive bool `json:"preserve_inactive"`
}

// BatchClearAccountErrorRequest is the body of
// POST /admin/accounts/batch-clear-error.
type BatchClearAccountErrorRequest struct {
	AccountIDs       []int64 `json:"account_ids"`
	PreserveInactive bool    `json:"preserve_inactive"`
}

// skippedInactiveIDs collects, concurrency-safely, the accounts a batch clear
// left inactive. It always renders as a JSON array, never null.
type skippedInactiveIDs struct {
	mu  sync.Mutex
	ids []int64
}

func newSkippedInactiveIDs() *skippedInactiveIDs {
	return &skippedInactiveIDs{ids: make([]int64, 0)}
}

func (s *skippedInactiveIDs) add(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, id)
}

// sorted returns the collected IDs in ascending order.
func (s *skippedInactiveIDs) sorted() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append(make([]int64, 0, len(s.ids)), s.ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
