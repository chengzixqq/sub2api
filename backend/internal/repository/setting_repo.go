package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// SetContentModerationConfig holds live group rows until the setting is written.
// Deletion takes FOR UPDATE on the group before cleaning this setting, so both
// operations follow the same lock order: groups, then settings.
func (r *settingRepository) SetContentModerationConfig(ctx context.Context, value string, groupIDs []int64) error {
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, ent.ErrTxStarted) {
		return err
	}
	client := r.client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		client = tx.Client()
	}
	if err := lockLiveGroups(ctx, client, groupIDs); err != nil {
		return err
	}
	now := time.Now()
	if err := client.Setting.Create().SetKey(service.SettingKeyContentModerationConfig).
		SetValue(value).SetUpdatedAt(now).OnConflictColumns(setting.FieldKey).
		UpdateNewValues().Exec(ctx); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

// ListClaudeAlignmentOverrides projects only the sparse customization object.
// Malformed legacy values are ignored just as they are by the policy resolver.
func (r *settingRepository) ListClaudeAlignmentOverrides(ctx context.Context, accountTypes []string) ([]service.ClaudeAlignmentOverrides, error) {
	rows, err := r.client.QueryContext(ctx, `
		SELECT id, type, platform, extra -> $1::text, schedulable, dispatch_scope,
           extra @> '{"mixed_scheduling":true}', ARRAY(SELECT group_id FROM account_groups WHERE account_id = accounts.id)
        FROM accounts
		WHERE deleted_at IS NULL AND platform = ANY($2) AND type = ANY($3) AND status = $4
		  AND extra ? $1::text AND jsonb_typeof(extra -> $1::text) = 'object'`,
		service.AccountClaudeCustomizationExtraKey, pq.Array([]string{service.PlatformAnthropic, service.PlatformAntigravity}), pq.Array(accountTypes), service.StatusActive)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []service.ClaudeAlignmentOverrides
	for rows.Next() {
		var account service.ClaudeAlignmentOverrides
		var raw []byte
		if err := rows.Scan(&account.AccountID, &account.AccountType, &account.Platform, &raw, &account.Schedulable, &account.DispatchScope, &account.Mixed, pq.Array(&account.GroupIDs)); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &account.Fields); err != nil {
			return nil, err
		}
		result = append(result, account)
	}
	return result, rows.Err()
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

// ReplaceExistingAdminAPIKey 原子轮换仍存在的旧版管理员密钥。
// 受影响行数为零表示密钥已被删除或不存在，绝不能通过 upsert 重新创建。
func (r *settingRepository) ReplaceExistingAdminAPIKey(ctx context.Context, value string) (bool, error) {
	n, err := r.client.Setting.Update().
		Where(setting.KeyEQ(service.SettingKeyAdminAPIKey), setting.ValueNEQ("")).
		SetValue(value).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}

func (r *settingRepository) RepairClaudeCustomizationField(ctx context.Context, key string) error {
	query := `UPDATE settings SET value=(value::jsonb - $2::text)::text,updated_at=NOW() WHERE key=$1`
	if key == "preset" {
		// Resetting a missing preset would activate magic and replace other stored values.
		query = `UPDATE settings SET value=jsonb_set(value::jsonb, ARRAY[$2::text], '"custom"'::jsonb)::text,updated_at=NOW() WHERE key=$1`
	}
	_, err := r.client.ExecContext(ctx, query, service.SettingKeyClaudeCustomization, key)
	return err
}

func (r *settingRepository) ClaudeAlignmentRouting(ctx context.Context) (*service.ClaudeAlignmentRouting, error) {
	rows, err := r.client.QueryContext(ctx, `SELECT id,platform,claude_code_only,fallback_group_id FROM groups WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	routing := &service.ClaudeAlignmentRouting{Groups: map[int64]*service.Group{}, Policies: map[[2]int64]*service.AccountDispatchPolicy{}}
	for rows.Next() {
		var g service.Group
		if err := rows.Scan(&g.ID, &g.Platform, &g.ClaudeCodeOnly, &g.FallbackGroupID); err != nil {
			return nil, err
		}
		routing.Groups[g.ID] = &g
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Share the dispatch reader's authorization joins and member/extra eligibility.
	grants, err := r.client.QueryContext(ctx, fmt.Sprintf(accountDispatchPolicySelect, "TRUE"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = grants.Close() }()
	policies, err := scanAccountDispatchPolicies(grants)
	if err != nil {
		return nil, err
	}
	for i := range policies {
		policy := &policies[i]
		routing.Policies[[2]int64{policy.UserID, policy.GroupID}] = policy
	}
	return routing, nil
}
