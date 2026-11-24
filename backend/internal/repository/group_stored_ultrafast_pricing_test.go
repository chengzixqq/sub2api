package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// storedGroupUltrafastPricingJSON 模拟旧备份恢复出的 groups.model_pricing：第一条带分组
// 不支持的 ultrafast_multiplier，第二条是普通合法条目。分组 API 会拒绝这样的入参，
// 但整库恢复不经过分组 API。
const storedGroupUltrafastPricingJSON = `[
	{"platform":"openai","models":["gpt-5.5"],"billing_mode":"token","input_price":0.000002,"output_price":0.000008,"cache_read_price":0.0000002,"fast_multiplier":1.5,"ultrafast_multiplier":2.5,"reasoning_effort_multipliers":{"high":1.2},"intervals":null},
	{"platform":"openai","models":["gpt-5"],"billing_mode":"token","input_price":0.000001,"output_price":0.000004,"ultrafast_multiplier":null,"intervals":[]}
]`

// storedGroupPricingWithoutUltrafast 是旧价卡原样解码、只清空倍率后的期望值。
func storedGroupPricingWithoutUltrafast(t *testing.T) []service.ChannelModelPricing {
	t.Helper()
	var want []service.ChannelModelPricing
	require.NoError(t, json.Unmarshal([]byte(storedGroupUltrafastPricingJSON), &want))
	require.Len(t, want, 2)
	require.NotNil(t, want[0].UltrafastMultiplier, "fixture must carry the stale multiplier")
	want[0].UltrafastMultiplier = nil
	return want
}

// requireStoredGroupPricingDropsUltrafast 检查实际写入 model_pricing 的字节：
// 不再有倍率值，其余字段与其他条目和旧价卡逐项一致。
func requireStoredGroupPricingDropsUltrafast(t *testing.T, raw []byte) {
	t.Helper()
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(raw, &entries))
	require.Len(t, entries, 2)
	for i, entry := range entries {
		require.Nil(t, entry["ultrafast_multiplier"], "entry %d", i)
	}
	var stored []service.ChannelModelPricing
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Equal(t, storedGroupPricingWithoutUltrafast(t), stored)
}

// storedGroupPricingRepo 让真实的分组服务流程跑在真实的行解码（groupEntityToService）上，
// 写入时与 createGroupRecord / groupRepository.Update 相同，把整卡 json.Marshal 后存入
// model_pricing。未覆盖的方法落到 nil 接口上，意外调用会直接失败。
type storedGroupPricingRepo struct {
	service.AdminGroupRepository
	rows   map[int64]*dbent.Group
	nextID int64
	writes int
}

func newStoredGroupPricingRepo(sourceID int64) *storedGroupPricingRepo {
	description := "restored from an old backup"
	return &storedGroupPricingRepo{
		rows: map[int64]*dbent.Group{sourceID: {
			ID:               sourceID,
			Name:             "restored-group",
			Description:      &description,
			Platform:         service.PlatformOpenAI,
			Status:           service.StatusActive,
			SubscriptionType: service.SubscriptionTypeStandard,
			RateMultiplier:   1,
			ModelPricing:     []byte(storedGroupUltrafastPricingJSON),
		}},
		nextID: sourceID,
	}
}

func (r *storedGroupPricingRepo) GetByID(_ context.Context, id int64) (*service.Group, error) {
	row, ok := r.rows[id]
	if !ok {
		return nil, service.ErrGroupNotFound
	}
	return groupEntityToService(row), nil
}

func (r *storedGroupPricingRepo) persist(group *service.Group) error {
	modelPricing, err := json.Marshal(group.ModelPricing)
	if err != nil {
		return err
	}
	description := group.Description
	r.rows[group.ID] = &dbent.Group{
		ID:               group.ID,
		Name:             group.Name,
		Description:      &description,
		Platform:         group.Platform,
		Status:           group.Status,
		SubscriptionType: group.SubscriptionType,
		RateMultiplier:   group.RateMultiplier,
		ModelPricing:     modelPricing,
	}
	r.writes++
	return nil
}

func (r *storedGroupPricingRepo) Update(_ context.Context, group *service.Group) error {
	if _, ok := r.rows[group.ID]; !ok {
		return service.ErrGroupNotFound
	}
	return r.persist(group)
}

func (r *storedGroupPricingRepo) CreateFromSource(_ context.Context, group *service.Group, sourceGroupID int64) error {
	if _, ok := r.rows[sourceGroupID]; !ok {
		return service.ErrGroupNotFound
	}
	r.nextID++
	group.ID = r.nextID
	return r.persist(group)
}

func newStoredGroupPricingAdminService(repo *storedGroupPricingRepo) service.AdminService {
	return service.NewAdminService(
		nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func TestGroupEntityToService_DropsStoredGroupUltrafastMultiplier(t *testing.T) {
	row := &dbent.Group{
		ID:           31,
		Name:         "restored-group",
		Platform:     service.PlatformOpenAI,
		Status:       service.StatusActive,
		ModelPricing: []byte(storedGroupUltrafastPricingJSON),
	}

	got := groupEntityToService(row)
	require.NotNil(t, got)
	require.Equal(t, storedGroupPricingWithoutUltrafast(t), got.ModelPricing)
	require.JSONEq(t, storedGroupUltrafastPricingJSON, string(row.ModelPricing), "decoding must not touch the stored row")

	// Create / CreateFromSource / Update 都把解码后的整卡原样序列化写回。
	written, err := json.Marshal(got.ModelPricing)
	require.NoError(t, err)
	requireStoredGroupPricingDropsUltrafast(t, written)

	// 没有该值的普通价卡逐字段保持解码结果。
	const plain = `[{"platform":"openai","models":["gpt-5"],"billing_mode":"token","input_price":0.000001,"intervals":[]}]`
	var want []service.ChannelModelPricing
	require.NoError(t, json.Unmarshal([]byte(plain), &want))
	require.Equal(t, want, groupEntityToService(&dbent.Group{ID: 32, ModelPricing: []byte(plain)}).ModelPricing)
}

// 管理员只改其他字段时，服务层会把载入的整卡回写；旧倍率不能随之写回。显式携带数值的
// 价卡仍按现有 normalizer 返回 400 且不写库；显式提交不含倍率的价卡照常保存。
func TestGroupStoredUltrafast_AdminUpdateOfOtherFieldsDoesNotRewriteIt(t *testing.T) {
	const groupID = int64(41)
	ctx := service.WithScope(context.Background(), service.AdminScope())

	repo := newStoredGroupPricingRepo(groupID)
	svc := newStoredGroupPricingAdminService(repo)
	description := "renamed by admin"
	updated, err := svc.UpdateGroup(ctx, groupID, &service.UpdateGroupInput{Description: &description})
	require.NoError(t, err)
	require.Equal(t, 1, repo.writes)
	require.Equal(t, storedGroupPricingWithoutUltrafast(t), updated.ModelPricing)
	require.Equal(t, description, *repo.rows[groupID].Description)
	requireStoredGroupPricingDropsUltrafast(t, repo.rows[groupID].ModelPricing)

	rejectRepo := newStoredGroupPricingRepo(groupID)
	rejectSvc := newStoredGroupPricingAdminService(rejectRepo)
	explicit := 1.0
	withMultiplier := []service.ChannelModelPricing{{
		Models:              []string{"gpt-5.5"},
		BillingMode:         service.BillingModeToken,
		InputPrice:          &explicit,
		OutputPrice:         &explicit,
		UltrafastMultiplier: &explicit,
	}}
	_, err = rejectSvc.UpdateGroup(ctx, groupID, &service.UpdateGroupInput{ModelPricing: &withMultiplier})
	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, int32(http.StatusBadRequest), appErr.Code)
	require.Equal(t, "GROUP_MODEL_ULTRAFAST_MULTIPLIER_UNSUPPORTED", appErr.Reason)
	require.Zero(t, rejectRepo.writes)
	require.JSONEq(t, storedGroupUltrafastPricingJSON, string(rejectRepo.rows[groupID].ModelPricing))

	repairRepo := newStoredGroupPricingRepo(groupID)
	repairSvc := newStoredGroupPricingAdminService(repairRepo)
	input := 0.000003
	output := 0.000009
	plainCard := []service.ChannelModelPricing{{
		Models:      []string{"gpt-5.5"},
		BillingMode: service.BillingModeToken,
		InputPrice:  &input,
		OutputPrice: &output,
	}}
	repaired, err := repairSvc.UpdateGroup(ctx, groupID, &service.UpdateGroupInput{ModelPricing: &plainCard})
	require.NoError(t, err)
	require.Equal(t, 1, repairRepo.writes)
	require.Len(t, repaired.ModelPricing, 1)
	require.Nil(t, repaired.ModelPricing[0].UltrafastMultiplier)
	var stored []service.ChannelModelPricing
	require.NoError(t, json.Unmarshal(repairRepo.rows[groupID].ModelPricing, &stored))
	require.Len(t, stored, 1)
	require.Nil(t, stored[0].UltrafastMultiplier)
	require.Equal(t, input, *stored[0].InputPrice)
	require.Equal(t, output, *stored[0].OutputPrice)
}

// GroupService.Update 同样整卡回写载入的分组。
func TestGroupStoredUltrafast_GroupServiceUpdateDoesNotRewriteIt(t *testing.T) {
	const groupID = int64(42)
	repo := newStoredGroupPricingRepo(groupID)
	description := "renamed through group service"

	updated, err := service.NewGroupService(repo, nil).Update(context.Background(), groupID, service.UpdateGroupRequest{Description: &description})
	require.NoError(t, err)
	require.Equal(t, 1, repo.writes)
	require.Equal(t, storedGroupPricingWithoutUltrafast(t), updated.ModelPricing)
	require.Equal(t, description, *repo.rows[groupID].Description)
	requireStoredGroupPricingDropsUltrafast(t, repo.rows[groupID].ModelPricing)
}

// 复制源组新建副本时，副本价卡不能带上旧倍率；复制照常成功，源行保持原样。
func TestGroupStoredUltrafast_DuplicateDoesNotCopyIt(t *testing.T) {
	const sourceID = int64(43)
	repo := newStoredGroupPricingRepo(sourceID)
	svc := newStoredGroupPricingAdminService(repo)

	duplicate, err := svc.DuplicateGroup(service.WithScope(context.Background(), service.AdminScope()), sourceID, "admin:1", "")
	require.NoError(t, err)
	require.NotNil(t, duplicate)
	require.NotEqual(t, sourceID, duplicate.ID)
	require.Equal(t, 1, repo.writes)
	require.Equal(t, storedGroupPricingWithoutUltrafast(t), duplicate.ModelPricing)
	require.Contains(t, repo.rows, duplicate.ID)
	requireStoredGroupPricingDropsUltrafast(t, repo.rows[duplicate.ID].ModelPricing)
	require.JSONEq(t, storedGroupUltrafastPricingJSON, string(repo.rows[sourceID].ModelPricing))
}
