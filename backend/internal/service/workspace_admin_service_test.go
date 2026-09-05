package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// adminRepoStub 是 WorkspaceAdminRepository 的测试替身。
//
// 记录 UpsertGrant 与 Update 的入参 —— 区间校验类断言的核心是
// 「被拒时有没有落库」，只看返回的 error 判不出来。
type adminRepoStub struct {
	lastUpsert *WorkspaceGrantInput
	upsertCals int
	lastUpdate *WorkspaceUpdateInput
	updateCals int
	// addedMembers 计数支撑「被拒时不落库」类断言。
	addedMembers int
}

func (r *adminRepoStub) Create(_ context.Context, name, desc string, perms WorkspacePermissions) (*Workspace, error) {
	return &Workspace{ID: 1, Name: name, Description: desc, Permissions: perms}, nil
}

func (r *adminRepoStub) Update(_ context.Context, id int64, in WorkspaceUpdateInput) (*Workspace, error) {
	r.updateCals++
	snapshot := in
	r.lastUpdate = &snapshot
	return &Workspace{
		ID:                id,
		SettlementRateMin: in.SettlementRateMin,
		SettlementRateMax: in.SettlementRateMax,
	}, nil
}
func (r *adminRepoStub) Delete(_ context.Context, _ int64) error { return nil }
func (r *adminRepoStub) ListMembers(_ context.Context, _ int64) ([]*WorkspaceMember, error) {
	return nil, nil
}

func (r *adminRepoStub) AddMember(_ context.Context, wsID, userID int64) (*WorkspaceMember, error) {
	r.addedMembers++
	return &WorkspaceMember{WorkspaceID: wsID, UserID: userID}, nil
}
func (r *adminRepoStub) RemoveMember(_ context.Context, _, _ int64) error { return nil }

func (r *adminRepoStub) UpsertGrant(_ context.Context, in WorkspaceGrantInput) (*WorkspaceGroupGrant, error) {
	r.upsertCals++
	snapshot := in
	r.lastUpsert = &snapshot
	return &WorkspaceGroupGrant{
		WorkspaceID:  in.WorkspaceID,
		GroupID:      in.GroupID,
		BasePriority: in.BasePriority,
		Enabled:      in.Enabled,
	}, nil
}

func (r *adminRepoStub) DeleteGrant(_ context.Context, _, _ int64) error { return nil }

// workspaceRepoStub 是只关心 GetByID 的 WorkspaceRepository 替身。
//
// getCalls 支撑「不碰区间就不回表」这条断言 —— 少查一次库的行为
// 只能靠调用计数观察，返回值上看不出来。
//
// 其余方法返回零值/not found：区间校验链路若走到它们即为实现漂移。
type workspaceRepoStub struct {
	workspace *Workspace
	err       error
	getCalls  int
}

func (r *workspaceRepoStub) GetByID(_ context.Context, _ int64) (*Workspace, error) {
	r.getCalls++
	if r.err != nil {
		return nil, r.err
	}
	return r.workspace, nil
}

func (r *workspaceRepoStub) List(_ context.Context) ([]*Workspace, error) { return nil, nil }
func (r *workspaceRepoStub) GetByUserID(_ context.Context, _ int64) (*Workspace, error) {
	return nil, domain.ErrWorkspaceMemberNotFound
}
func (r *workspaceRepoStub) ListGrantsByWorkspace(_ context.Context, _ int64) ([]*WorkspaceGroupGrant, error) {
	return nil, nil
}
func (r *workspaceRepoStub) GetGrant(_ context.Context, _, _ int64) (*WorkspaceGroupGrant, error) {
	return nil, domain.ErrWorkspaceGrantNotFound
}
func (r *workspaceRepoStub) CountGrantsByGroup(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// newRangeSvc 组装一个只为结算区间校验服务的 WorkspaceAdminService。
//
// readRepo 返回库里那条现存工作区 —— 「只改一端」的校验
// 必须能读到另一端的旧值，否则倒挂区间会被放过。
func newRangeSvc(current *Workspace) (*WorkspaceAdminService, *adminRepoStub) {
	write := &adminRepoStub{}
	read := &workspaceRepoStub{workspace: current}
	return NewWorkspaceAdminService(write, read, NewWorkspaceService(read, nil), nil, nil), write
}

// TestUpdateSettlementRangeAcceptsValid 锁定合法区间可落库。
func TestUpdateSettlementRangeAcceptsValid(t *testing.T) {
	svc, write := newRangeSvc(&Workspace{ID: 7})

	got, err := svc.Update(context.Background(), 7, WorkspaceUpdateInput{
		SettlementRateMin: ptrFloat64(0.05),
		SettlementRateMax: ptrFloat64(0.06),
	})
	if err != nil {
		t.Fatalf("合法区间应通过，得到 err=%v", err)
	}
	if got.SettlementRateMax == nil || *got.SettlementRateMax != 0.06 {
		t.Errorf("上限未写入 0.06，得到 %v", got.SettlementRateMax)
	}
	if write.updateCals != 1 {
		t.Errorf("合法区间应落库一次，实得 %d 次", write.updateCals)
	}
}

// TestUpdateSettlementRangeRejectsInverted 锁定 min > max 被拒。
//
// 倒挂区间会让账号侧的校验永远无法通过：任何倍率都同时越上限、跌下限，
// 供应商从此改不动自己的结算倍率，且报错指向倍率本身而非这份配置。
func TestUpdateSettlementRangeRejectsInverted(t *testing.T) {
	svc, write := newRangeSvc(&Workspace{ID: 7})

	_, err := svc.Update(context.Background(), 7, WorkspaceUpdateInput{
		SettlementRateMin: ptrFloat64(0.06),
		SettlementRateMax: ptrFloat64(0.05),
	})
	if err != domain.ErrWorkspaceInvalidSettlementRange {
		t.Errorf("倒挂区间应返回 ErrWorkspaceInvalidSettlementRange，得到 %v", err)
	}
	if write.updateCals != 0 {
		t.Error("被拒的区间不得落库")
	}
}

// TestUpdateSettlementRangeMergesExistingBound 锁定只改一端时合着旧值校验。
//
// 这是本组最容易漏的一条：只改上限时，下限来自库里的旧值，
// 单看入参那一端永远合法，于是 min=0.06 / max=0.05 的矛盾区间会被放过。
func TestUpdateSettlementRangeMergesExistingBound(t *testing.T) {
	svc, write := newRangeSvc(&Workspace{ID: 7, SettlementRateMin: ptrFloat64(0.06)})

	_, err := svc.Update(context.Background(), 7, WorkspaceUpdateInput{
		SettlementRateMax: ptrFloat64(0.05),
	})
	if err != domain.ErrWorkspaceInvalidSettlementRange {
		t.Errorf("新上限低于库里的下限应被拒，得到 %v", err)
	}
	if write.updateCals != 0 {
		t.Error("被拒的区间不得落库")
	}
}

// TestUpdateSettlementRangeRejectsNegative 锁定负边界被拒。
//
// 负倍率会让用量产生负成本，把结算金额倒扣回去。
func TestUpdateSettlementRangeRejectsNegative(t *testing.T) {
	for _, in := range []WorkspaceUpdateInput{
		{SettlementRateMin: ptrFloat64(-0.1)},
		{SettlementRateMax: ptrFloat64(-0.1)},
	} {
		svc, write := newRangeSvc(&Workspace{ID: 7})
		if _, err := svc.Update(context.Background(), 7, in); err != domain.ErrWorkspaceInvalidCostRate {
			t.Errorf("负边界应返回 ErrWorkspaceInvalidCostRate，得到 %v", err)
		}
		if write.updateCals != 0 {
			t.Error("被拒的区间不得落库")
		}
	}
}

// TestUpdateSettlementRangeZeroClearsBound 锁定「指向 0」是清除而非非法值。
//
// 这是 WorkspaceUpdateInput 三态语义里最易踩的一档：nil 表示本次不改，
// 指向 0 表示撤掉该端的限制。若把 0 直接送进正数校验就会被判非法，
// 站长从此无法取消已设的上限 —— 只能改成别的数，撤不掉。
func TestUpdateSettlementRangeZeroClearsBound(t *testing.T) {
	svc, write := newRangeSvc(&Workspace{
		ID:                7,
		SettlementRateMin: ptrFloat64(0.05),
		SettlementRateMax: ptrFloat64(0.06),
	})

	if _, err := svc.Update(context.Background(), 7, WorkspaceUpdateInput{
		SettlementRateMax: ptrFloat64(0),
	}); err != nil {
		t.Fatalf("指向 0 应视为清除上限而放行，得到 %v", err)
	}
	if write.updateCals != 1 {
		t.Errorf("清除上限应落库一次，实得 %d 次", write.updateCals)
	}
}

// TestUpdateWithoutRangeSkipsLookup 锁定不碰区间时不多查一次库。
//
// Update 是改名、改状态、改权限的公用入口。若无条件回表读现存值，
// 每次改名都会多一次查询；这条同时守住「两端都没提交就不必合并旧值」。
func TestUpdateWithoutRangeSkipsLookup(t *testing.T) {
	write := &adminRepoStub{}
	read := &workspaceRepoStub{workspace: &Workspace{ID: 7}}
	svc := NewWorkspaceAdminService(write, read, NewWorkspaceService(read, nil), nil, nil)

	name := "改个名"
	if _, err := svc.Update(context.Background(), 7, WorkspaceUpdateInput{Name: &name}); err != nil {
		t.Fatalf("只改名应通过，得到 %v", err)
	}
	if read.getCalls != 0 {
		t.Errorf("未提交区间时不应回表，实得 %d 次", read.getCalls)
	}
}

// TestValidateSettlementRangeBoundaries 直接锁定区间校验的边界语义。
//
// nil 的含义分两层：Max 为 nil 表示「不开放自助调价」，
// Min 为 nil 表示「不设下限」。两者都不该被当成 0 参与比较。
func TestValidateSettlementRangeBoundaries(t *testing.T) {
	if err := validateSettlementRange(nil, nil); err != nil {
		t.Errorf("两端均未设应放行（不开放自助调价），得到 %v", err)
	}
	if err := validateSettlementRange(nil, ptrFloat64(0.06)); err != nil {
		t.Errorf("只设上限应放行（下限视为 0），得到 %v", err)
	}
	if err := validateSettlementRange(ptrFloat64(0.05), nil); err != nil {
		t.Errorf("只设下限应放行，得到 %v", err)
	}
	// 相等是闭区间的合法端点：站长借此把倍率钉死在一个值上。
	if err := validateSettlementRange(ptrFloat64(0.05), ptrFloat64(0.05)); err != nil {
		t.Errorf("上下限相等应放行（钉死单一倍率），得到 %v", err)
	}
}
