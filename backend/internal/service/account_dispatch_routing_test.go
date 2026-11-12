//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type accountDispatchTestRepo struct {
	AccountRepository
	policy   *AccountDispatchPolicy
	readErr  error
	accounts []*Account
	userID   int64
	groupID  int64
}

func (r *accountDispatchTestRepo) GetAccountDispatchPolicy(_ context.Context, userID, groupID int64) (*AccountDispatchPolicy, error) {
	r.userID, r.groupID = userID, groupID
	return r.policy, r.readErr
}

func (r *accountDispatchTestRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	wanted := make(map[int64]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	var result []*Account
	for _, account := range r.accounts {
		if wanted[account.ID] {
			result = append(result, account)
		}
	}
	return result, nil
}

func (r *accountDispatchTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for _, account := range r.accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return nil, ErrNoAvailableAccounts
}

func accountDispatchTestAccount(id int64, groupIDs []int64, scope string) Account {
	return Account{
		ID: id, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true,
		GroupIDs: groupIDs, DispatchScope: scope,
	}
}

func accountDispatchTestContext(userID int64) context.Context {
	return context.WithValue(context.Background(), ctxkey.UserID, userID)
}

func TestAccountDispatchCandidates_DefaultAndAppend(t *testing.T) {
	groupID := int64(10)
	shared := accountDispatchTestAccount(1, []int64{groupID}, DispatchScopeShared)
	private := accountDispatchTestAccount(2, []int64{groupID}, DispatchScopeAuthorizedOnly)
	extra := accountDispatchTestAccount(3, []int64{20}, DispatchScopeAuthorizedOnly)
	repo := &accountDispatchTestRepo{accounts: []*Account{&shared, &private, &extra}}
	base := []Account{shared, private}

	ctx, err := withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, base, false)
	if err != nil || len(got) != 1 || got[0].ID != shared.ID {
		t.Fatalf("default candidates = %v, %v; want only shared", got, err)
	}

	repo.policy = &AccountDispatchPolicy{Mode: AccountDispatchModeAppend, Accounts: []AccountDispatchGrant{
		{AccountID: private.ID, Origin: AccountDispatchOriginMember},
		{AccountID: extra.ID, Origin: AccountDispatchOriginExtra},
	}}
	ctx, err = withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID)
	if err != nil {
		t.Fatal(err)
	}
	got, err = accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, base, false)
	if err != nil || len(got) != 3 || got[0].ID != shared.ID || got[1].ID != private.ID || got[2].ID != extra.ID {
		t.Fatalf("append candidates = %v, %v; want shared, private, extra", got, err)
	}
	if repo.userID != 100 || repo.groupID != groupID {
		t.Fatalf("policy loaded for user=%d group=%d", repo.userID, repo.groupID)
	}
}

func TestAccountDispatchCandidates_ReplaceAndRevocation(t *testing.T) {
	groupID := int64(10)
	fallbackID := int64(20)
	shared := accountDispatchTestAccount(1, []int64{groupID}, DispatchScopeShared)
	member := accountDispatchTestAccount(2, []int64{groupID}, DispatchScopeShared)
	extra := accountDispatchTestAccount(3, []int64{fallbackID}, DispatchScopeAuthorizedOnly)
	repo := &accountDispatchTestRepo{
		policy: &AccountDispatchPolicy{Mode: AccountDispatchModeReplace, Accounts: []AccountDispatchGrant{
			{AccountID: member.ID, Origin: AccountDispatchOriginMember},
			{AccountID: extra.ID, Origin: AccountDispatchOriginExtra},
		}},
		accounts: []*Account{&shared, &member, &extra},
	}
	ctx, err := withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, []Account{shared, member}, false)
	if err != nil || len(got) != 2 || got[0].ID != member.ID || got[1].ID != extra.ID {
		t.Fatalf("replace candidates = %v, %v; want member and extra", got, err)
	}
	if accountDispatchAllows(ctx, &shared, &fallbackID, false) {
		t.Fatal("fallback or stale sticky account bypassed replace policy")
	}
	if !accountDispatchAllows(ctx, &extra, &fallbackID, false) {
		t.Fatal("explicit extra account denied on fallback")
	}

	member.GroupIDs = nil
	got, err = accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, []Account{shared}, false)
	if err != nil || len(got) != 1 || got[0].ID != extra.ID {
		t.Fatalf("removed member candidates = %v, %v; want only extra", got, err)
	}

	repo.policy = &AccountDispatchPolicy{Mode: AccountDispatchModeReplace}
	ctx, err = withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID)
	if err != nil {
		t.Fatal(err)
	}
	got, err = accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, []Account{shared}, false)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty replace candidates = %v, %v; want none", got, err)
	}

	repo.policy = nil
	ctx, err = withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID)
	if err != nil {
		t.Fatal(err)
	}
	got, err = accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, []Account{shared}, false)
	if err != nil || len(got) != 1 || got[0].ID != shared.ID {
		t.Fatalf("restored defaults = %v, %v; want shared", got, err)
	}
}

func TestAccountDispatchCandidates_RejectsUnavailableAndWrongPlatform(t *testing.T) {
	groupID := int64(10)
	wrongPlatform := accountDispatchTestAccount(1, []int64{20}, DispatchScopeAuthorizedOnly)
	wrongPlatform.Platform = PlatformAnthropic
	inactive := accountDispatchTestAccount(2, []int64{20}, DispatchScopeAuthorizedOnly)
	inactive.Status = "inactive"
	repo := &accountDispatchTestRepo{
		policy: &AccountDispatchPolicy{Mode: AccountDispatchModeReplace, Accounts: []AccountDispatchGrant{
			{AccountID: wrongPlatform.ID, Origin: AccountDispatchOriginExtra},
			{AccountID: inactive.ID, Origin: AccountDispatchOriginExtra},
		}},
		accounts: []*Account{&wrongPlatform, &inactive},
	}
	ctx, err := withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, nil, false)
	if err != nil || len(got) != 0 {
		t.Fatalf("unavailable candidates = %v, %v; want none", got, err)
	}
}

func TestAccountDispatchSelection_ReadFailureIsFatal(t *testing.T) {
	groupID := int64(10)
	repo := &accountDispatchTestRepo{readErr: errors.New("database unavailable")}
	if _, err := withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID); err == nil {
		t.Fatal("policy read failure fell back to shared accounts")
	}
	if _, err := withAccountDispatchSelection(accountDispatchTestContext(100), struct{}{}, &groupID); err == nil {
		t.Fatal("missing policy reader fell back to shared accounts")
	}
}

func TestEnsureAccountDispatchPolicy_RejectsLookupFailure(t *testing.T) {
	groupID := int64(10)
	repo := &accountDispatchTestRepo{readErr: errors.New("database unavailable")}
	svc := &GatewayService{accountRepo: repo}
	if err := svc.EnsureAccountDispatchPolicy(context.Background(), 100, &groupID); err == nil {
		t.Fatal("model list preflight accepted an unavailable policy")
	}
	if repo.userID != 100 || repo.groupID != groupID {
		t.Fatalf("preflight loaded user=%d group=%d", repo.userID, repo.groupID)
	}
}

func TestAccountDispatchSelection_UsesOriginalGroupAfterFallback(t *testing.T) {
	originalID, fallbackID := int64(10), int64(20)
	ctx := context.WithValue(accountDispatchTestContext(100), ctxkey.AccountDispatchGroupID, originalID)
	ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: fallbackID, Platform: PlatformOpenAI, Hydrated: true})
	repo := &accountDispatchTestRepo{policy: &AccountDispatchPolicy{Mode: AccountDispatchModeReplace}}
	selectedCtx, err := withAccountDispatchSelection(ctx, repo, &fallbackID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.groupID != originalID {
		t.Fatalf("loaded fallback policy group=%d; want original=%d", repo.groupID, originalID)
	}
	fallback := accountDispatchTestAccount(1, []int64{fallbackID}, DispatchScopeShared)
	if accountDispatchAllows(selectedCtx, &fallback, &fallbackID, true) {
		t.Fatal("handler fallback escaped original replace policy")
	}
}

func TestAccountDispatchTurn_RevocationBlocksNextTurn(t *testing.T) {
	groupID := int64(10)
	account := accountDispatchTestAccount(1, nil, DispatchScopeAuthorizedOnly)
	repo := &accountDispatchTestRepo{
		policy: &AccountDispatchPolicy{Mode: AccountDispatchModeReplace, Accounts: []AccountDispatchGrant{
			{AccountID: account.ID, Origin: AccountDispatchOriginExtra},
		}},
		accounts: []*Account{&account},
	}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx, err := withAccountDispatchSelection(accountDispatchTestContext(100), repo, &groupID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckAccountDispatchForTurn(ctx, 100, &groupID, account.ID); err != nil {
		t.Fatalf("first authorized turn rejected: %v", err)
	}
	repo.policy = &AccountDispatchPolicy{Mode: AccountDispatchModeReplace}
	if err := svc.CheckAccountDispatchForTurn(ctx, 100, &groupID, account.ID); err == nil {
		t.Fatal("second turn reused revoked grant from connection context")
	}
	account.DispatchScope = DispatchScopeShared
	account.GroupIDs = []int64{groupID}
	repo.policy = nil
	if err := svc.CheckAccountDispatchForTurn(ctx, 100, &groupID, account.ID); err != nil {
		t.Fatalf("shared account rejected: %v", err)
	}
	account.DispatchScope = DispatchScopeAuthorizedOnly
	if err := svc.CheckAccountDispatchForTurn(ctx, 100, &groupID, account.ID); err == nil {
		t.Fatal("second turn reused account after shared to private transition")
	}
}
