package service

import (
	"context"
	"sort"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const maxGroupMemberChange = 200

// GroupMemberAdd adds one account to a group; Priority <= 0 means the default 50.
type GroupMemberAdd struct {
	AccountID int64 `json:"account_id"`
	Priority  int   `json:"priority,omitempty"`
}

// GroupMemberRemoved reports a removed membership with its priority, so the
// client can undo by adding it back with the same priority.
type GroupMemberRemoved struct {
	AccountID int64
	Priority  int
}

// GroupMemberChange is what the repository actually changed.
type GroupMemberChange struct {
	Added   []int64
	Removed []GroupMemberRemoved
}

// GroupMemberRepository changes group memberships row by row.
type GroupMemberRepository interface {
	UpdateGroupMembers(ctx context.Context, groupID int64, adds []GroupMemberAdd, removeIDs []int64) (*GroupMemberChange, error)
}

type UpdateGroupMembersInput struct {
	Add                   []GroupMemberAdd
	Remove                []int64
	SkipMixedChannelCheck bool
}

// GroupMemberResult is one per-account outcome: added, removed, unchanged or failed.
type GroupMemberResult struct {
	AccountID int64             `json:"account_id"`
	Action    string            `json:"action"`
	Status    string            `json:"status"`
	Priority  *int              `json:"priority,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Message   string            `json:"message,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

type GroupMemberCounts struct {
	AccountCount            int64 `json:"account_count"`
	ActiveAccountCount      int64 `json:"active_account_count"`
	RateLimitedAccountCount int64 `json:"rate_limited_account_count"`
}

type UpdateGroupMembersResult struct {
	GroupID   int64               `json:"group_id"`
	Added     int                 `json:"added"`
	Removed   int                 `json:"removed"`
	Unchanged int                 `json:"unchanged"`
	Failed    int                 `json:"failed"`
	Results   []GroupMemberResult `json:"results"`
	Counts    *GroupMemberCounts  `json:"counts,omitempty"`
}

var (
	errEmptyMemberChange  = infraerrors.BadRequest("EMPTY_MEMBER_CHANGE", "add or remove at least one account")
	errTooManyAccounts    = infraerrors.BadRequest("TOO_MANY_ACCOUNTS", "add and remove may list at most 200 accounts in total")
	errAccountInBothLists = infraerrors.BadRequest("ACCOUNT_IN_BOTH_LISTS", "an account cannot be both added and removed")
)

const (
	groupMemberActionAdd       = "add"
	groupMemberActionRemove    = "remove"
	groupMemberStatusAdded     = "added"
	groupMemberStatusRemoved   = "removed"
	groupMemberStatusUnchanged = "unchanged"
	groupMemberStatusFailed    = "failed"
)

// normalizeGroupMemberInput dedupes the lists and validates their shape.
func normalizeGroupMemberInput(in UpdateGroupMembersInput) ([]GroupMemberAdd, []int64, error) {
	adds := make([]GroupMemberAdd, 0, len(in.Add))
	seenAdd := map[int64]bool{}
	for _, add := range in.Add {
		if add.AccountID <= 0 {
			return nil, nil, infraerrors.BadRequest("INVALID_ACCOUNT_ID", "account_id must be positive")
		}
		if seenAdd[add.AccountID] {
			continue
		}
		seenAdd[add.AccountID] = true
		adds = append(adds, add)
	}
	removes := make([]int64, 0, len(in.Remove))
	seenRemove := map[int64]bool{}
	for _, id := range in.Remove {
		if id <= 0 {
			return nil, nil, infraerrors.BadRequest("INVALID_ACCOUNT_ID", "account_id must be positive")
		}
		if seenAdd[id] {
			return nil, nil, errAccountInBothLists
		}
		if seenRemove[id] {
			continue
		}
		seenRemove[id] = true
		removes = append(removes, id)
	}
	if len(adds)+len(removes) == 0 {
		return nil, nil, errEmptyMemberChange
	}
	if len(adds)+len(removes) > maxGroupMemberChange {
		return nil, nil, errTooManyAccounts
	}
	return adds, removes, nil
}

// UpdateGroupMembers adds / removes accounts of one group. Each account is
// validated on its own (missing → ACCOUNT_NOT_FOUND, mixed channel without
// confirmation → MIXED_CHANNEL_RISK) and failures do not stop the others; the
// valid changes are applied in one transaction.
func (s *adminServiceImpl) UpdateGroupMembers(ctx context.Context, groupID int64, in UpdateGroupMembersInput) (*UpdateGroupMembersResult, error) {
	if groupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_GROUP_ID", "invalid group id")
	}
	adds, removes, err := normalizeGroupMemberInput(in)
	if err != nil {
		return nil, err
	}
	if err := s.requireGroupAccess(ctx, groupID); err != nil {
		return nil, err
	}
	if err := s.validateGroupIDsExist(ctx, []int64{groupID}); err != nil {
		return nil, err
	}
	if err := s.ValidateAccountGroupBindings(ctx, []int64{groupID}); err != nil {
		return nil, err
	}
	repo, ok := s.groupRepo.(GroupMemberRepository)
	if !ok {
		return nil, infraerrors.InternalServer("GROUP_REPOSITORY_UNSUPPORTED", "group repository cannot update members")
	}

	result := &UpdateGroupMembersResult{GroupID: groupID, Results: []GroupMemberResult{}}
	validAdds, err := s.validateGroupMemberAdds(ctx, groupID, adds, in.SkipMixedChannelCheck, result)
	if err != nil {
		return nil, err
	}
	validRemoves, err := s.validateGroupMemberRemoves(ctx, removes, result)
	if err != nil {
		return nil, err
	}
	if len(validAdds)+len(validRemoves) > 0 {
		change, err := repo.UpdateGroupMembers(ctx, groupID, validAdds, validRemoves)
		if err != nil {
			return nil, err
		}
		appendGroupMemberChangeResults(result, validAdds, validRemoves, change)
	}
	sort.SliceStable(result.Results, func(i, j int) bool { return result.Results[i].Action < result.Results[j].Action })
	for _, r := range result.Results {
		switch r.Status {
		case groupMemberStatusAdded:
			result.Added++
		case groupMemberStatusRemoved:
			result.Removed++
		case groupMemberStatusUnchanged:
			result.Unchanged++
		case groupMemberStatusFailed:
			result.Failed++
		}
	}
	if counts, err := s.groupMemberCounts(ctx, groupID); err == nil {
		result.Counts = counts
	}
	return result, nil
}

func (s *adminServiceImpl) validateGroupMemberAdds(ctx context.Context, groupID int64, adds []GroupMemberAdd, skipMixed bool, result *UpdateGroupMembersResult) ([]GroupMemberAdd, error) {
	if len(adds) == 0 {
		return nil, nil
	}
	accounts, err := s.loadScopedAccounts(ctx, groupMemberAddIDs(adds))
	if err != nil {
		return nil, err
	}
	var groupPlatforms map[string]bool
	if !skipMixed {
		members, err := s.accountRepo.ListByGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		groupPlatforms = map[string]bool{}
		for _, m := range members {
			if p := getAccountPlatform(m.Platform); p != "" {
				groupPlatforms[p] = true
			}
		}
	}
	valid := make([]GroupMemberAdd, 0, len(adds))
	for _, add := range adds {
		account, ok := accounts[add.AccountID]
		if !ok {
			result.Results = append(result.Results, groupMemberFailed(add.AccountID, groupMemberActionAdd, "ACCOUNT_NOT_FOUND", "account not found", nil))
			continue
		}
		if !skipMixed {
			if current := getAccountPlatform(account.Platform); current != "" {
				if other := mixedChannelOther(groupPlatforms, current); other != "" {
					mixed := &MixedChannelError{GroupID: groupID, CurrentPlatform: current, OtherPlatform: other}
					result.Results = append(result.Results, groupMemberFailed(add.AccountID, groupMemberActionAdd, "MIXED_CHANNEL_RISK", mixed.Error(),
						map[string]string{"current_platform": current, "other_platform": other}))
					continue
				}
			}
		}
		valid = append(valid, add)
	}
	return valid, nil
}

func (s *adminServiceImpl) validateGroupMemberRemoves(ctx context.Context, removes []int64, result *UpdateGroupMembersResult) ([]int64, error) {
	if len(removes) == 0 {
		return nil, nil
	}
	accounts, err := s.loadScopedAccounts(ctx, removes)
	if err != nil {
		return nil, err
	}
	valid := make([]int64, 0, len(removes))
	for _, id := range removes {
		if _, ok := accounts[id]; !ok {
			result.Results = append(result.Results, groupMemberFailed(id, groupMemberActionRemove, "ACCOUNT_NOT_FOUND", "account not found", nil))
			continue
		}
		valid = append(valid, id)
	}
	return valid, nil
}

// loadScopedAccounts returns the requested accounts the caller may manage.
func (s *adminServiceImpl) loadScopedAccounts(ctx context.Context, ids []int64) (map[int64]*Account, error) {
	scoped, err := s.scopedAccountIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, scoped)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*Account, len(accounts))
	for _, a := range accounts {
		if a != nil {
			out[a.ID] = a
		}
	}
	return out, nil
}

func (s *adminServiceImpl) groupMemberCounts(ctx context.Context, groupID int64) (*GroupMemberCounts, error) {
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return &GroupMemberCounts{AccountCount: group.AccountCount, ActiveAccountCount: group.ActiveAccountCount, RateLimitedAccountCount: group.RateLimitedAccountCount}, nil
}

func appendGroupMemberChangeResults(result *UpdateGroupMembersResult, adds []GroupMemberAdd, removes []int64, change *GroupMemberChange) {
	added := map[int64]bool{}
	for _, id := range change.Added {
		added[id] = true
	}
	for _, add := range adds {
		r := GroupMemberResult{AccountID: add.AccountID, Action: groupMemberActionAdd, Status: groupMemberStatusUnchanged}
		if added[add.AccountID] {
			priority := add.Priority
			if priority <= 0 {
				priority = 50
			}
			r.Status, r.Priority = groupMemberStatusAdded, &priority
		}
		result.Results = append(result.Results, r)
	}
	removed := map[int64]int{}
	for _, r := range change.Removed {
		removed[r.AccountID] = r.Priority
	}
	for _, id := range removes {
		r := GroupMemberResult{AccountID: id, Action: groupMemberActionRemove, Status: groupMemberStatusUnchanged}
		if priority, ok := removed[id]; ok {
			p := priority
			r.Status, r.Priority = groupMemberStatusRemoved, &p
		}
		result.Results = append(result.Results, r)
	}
}

func groupMemberFailed(accountID int64, action, reason, message string, details map[string]string) GroupMemberResult {
	return GroupMemberResult{AccountID: accountID, Action: action, Status: groupMemberStatusFailed, Reason: reason, Message: message, Details: details}
}

func groupMemberAddIDs(adds []GroupMemberAdd) []int64 {
	ids := make([]int64, len(adds))
	for i, add := range adds {
		ids[i] = add.AccountID
	}
	return ids
}

// mixedChannelOther returns a platform already in the group that would mix
// with current, or "".
func mixedChannelOther(groupPlatforms map[string]bool, current string) string {
	for platform := range groupPlatforms {
		if platform != current {
			return platform
		}
	}
	return ""
}
