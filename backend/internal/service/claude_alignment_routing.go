package service

import (
	"context"
	"slices"
)

// ClaudeAlignmentRouting is an immutable projection used only to decide whether
// to send queue pings. Actual dispatch still rechecks live authorization.
type ClaudeAlignmentRouting struct {
	Groups   map[int64]*Group
	Policies map[[2]int64]*AccountDispatchPolicy
	accounts map[int64]ClaudeAlignmentOverrides
}
type alignmentRoutingReader interface {
	ClaudeAlignmentRouting(context.Context) (*ClaudeAlignmentRouting, error)
}

func (r *ClaudeAlignmentRouting) GetAccountDispatchPolicy(_ context.Context, userID, groupID int64) (*AccountDispatchPolicy, error) {
	return r.Policies[[2]int64{userID, groupID}], nil
}

func (r *ClaudeAlignmentRouting) containsCandidate(dispatch *accountDispatchSelection, ids []int64, pools []alignmentPool, allGroups bool) bool {
	for _, id := range ids {
		a := r.accounts[id]
		if !a.Schedulable {
			continue
		}
		var origin string
		replace := false
		if dispatch != nil && dispatch.policy != nil {
			origin = dispatch.grants[id]
			if origin == AccountDispatchOriginMember && !slices.Contains(a.GroupIDs, dispatch.requestGroupID) {
				origin = ""
			}
			replace = dispatch.policy.Mode == AccountDispatchModeReplace
		}
		for _, pool := range pools {
			if a.Platform != pool.platform || pool.mixedOnly && !a.Mixed {
				continue
			}
			if origin == AccountDispatchOriginMember || origin == AccountDispatchOriginExtra && !pool.mixedOnly {
				return true
			}
			if replace || a.DispatchScope == DispatchScopeAuthorizedOnly {
				continue
			}
			if allGroups || pool.groupID == nil && len(a.GroupIDs) == 0 || pool.groupID != nil && slices.Contains(a.GroupIDs, *pool.groupID) {
				return true
			}
		}
	}
	return false
}
