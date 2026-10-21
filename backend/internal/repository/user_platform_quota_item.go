package repository

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/userplatformquota"
)

// 单平台读写（后端适配清单 E07）：整表 UpsertForUser 之外，只动一个平台的限额，
// 其他平台的行、用量与窗口都不碰。三个方法都在事务里先锁住该平台的活跃行作为 previous，
// 写入仍走原生 SQL（不经过 ent 的软删 hook / 拦截器），条件里显式带 deleted_at。

// UpsertOneForUser 替换 rec.Platform 的三档限额（缺失 = 不限，不是合并补丁）：
//   - 至少配置一档：UPDATE 活跃行（保留 usage / window_start），没有活跃行时 INSERT（用量从 0 起算，
//     不复活软删的历史行，那是 RestoreLatestDeleted 的事）；
//   - 三档全空：软删活跃行；没有活跃行时什么也不做。current 为 nil。
func (r *userPlatformQuotaRepository) UpsertOneForUser(ctx context.Context, userID int64, rec UserPlatformQuotaRecord) (*UserPlatformQuotaRecord, *UserPlatformQuotaRecord, error) {
	rec.UserID = userID
	if err := validateQuotaPlatforms([]UserPlatformQuotaRecord{rec}); err != nil {
		return nil, nil, err
	}
	var current, previous *UserPlatformQuotaRecord
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		var err error
		previous, err = lockActiveQuotaRow(txCtx, txClient, userID, rec.Platform)
		if err != nil {
			return err
		}
		now := time.Now()
		if !rec.HasAnyLimit() {
			if previous == nil {
				return nil
			}
			return softDeleteActiveQuotaRow(txCtx, txClient, userID, rec.Platform, now)
		}
		affected, err := updateLimitsRow(txCtx, txClient, userID, rec, now)
		if err != nil {
			return err
		}
		if affected == 0 {
			if err := insertLimitsRow(txCtx, txClient, userID, rec, now); err != nil {
				return err
			}
		}
		current, err = readActiveQuotaRow(txCtx, txClient, userID, rec.Platform)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("user_platform_quotas: row for user %d platform %q missing after upsert", userID, rec.Platform)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return current, previous, nil
}

// SoftDeleteOne 软删一个平台的活跃行并返回删除前的记录；没有活跃行返回 ErrUserPlatformQuotaNotFound。
func (r *userPlatformQuotaRepository) SoftDeleteOne(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	if err := validateQuotaPlatforms([]UserPlatformQuotaRecord{{Platform: platform}}); err != nil {
		return nil, err
	}
	var previous *UserPlatformQuotaRecord
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		var err error
		previous, err = lockActiveQuotaRow(txCtx, txClient, userID, platform)
		if err != nil {
			return err
		}
		if previous == nil {
			return ErrUserPlatformQuotaNotFound
		}
		return softDeleteActiveQuotaRow(txCtx, txClient, userID, platform, time.Now())
	})
	if err != nil {
		return nil, err
	}
	return previous, nil
}

// RestoreLatestDeleted 把该平台 deleted_at 最新的软删行（同时间取 id 大的）恢复为活跃，
// 限额、用量与窗口起点原样保留；过期窗口在读取与计费时按既有规则自动归零。
// 已有活跃行返回 ErrUserPlatformQuotaActiveExists（含并发恢复撞上部分唯一索引的情况），
// 没有软删行返回 ErrUserPlatformQuotaNotFound。
func (r *userPlatformQuotaRepository) RestoreLatestDeleted(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	if err := validateQuotaPlatforms([]UserPlatformQuotaRecord{{Platform: platform}}); err != nil {
		return nil, err
	}
	var restored *UserPlatformQuotaRecord
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		active, err := lockActiveQuotaRow(txCtx, txClient, userID, platform)
		if err != nil {
			return err
		}
		if active != nil {
			return ErrUserPlatformQuotaActiveExists
		}
		const query = `UPDATE user_platform_quotas SET deleted_at = NULL, updated_at = $3
			WHERE id = (
				SELECT id FROM user_platform_quotas
				WHERE user_id = $1 AND platform = $2 AND deleted_at IS NOT NULL
				ORDER BY deleted_at DESC, id DESC
				LIMIT 1
			)`
		res, err := txClient.ExecContext(txCtx, query, userID, platform, time.Now())
		if err != nil {
			if isUniqueConstraintViolation(err) {
				return fmt.Errorf("%w: %w", ErrUserPlatformQuotaActiveExists, err)
			}
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrUserPlatformQuotaNotFound
		}
		restored, err = readActiveQuotaRow(txCtx, txClient, userID, platform)
		if err != nil {
			return err
		}
		if restored == nil {
			return fmt.Errorf("user_platform_quotas: row for user %d platform %q missing after restore", userID, platform)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return restored, nil
}

// lockActiveQuotaRow 在事务里 SELECT ... FOR UPDATE 读出 (user, platform) 的活跃行，没有时返回 (nil, nil)。
func lockActiveQuotaRow(ctx context.Context, client *dbent.Client, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	entity, err := client.UserPlatformQuota.Query().
		Where(
			userplatformquota.UserIDEQ(userID),
			userplatformquota.PlatformEQ(platform),
			userplatformquota.DeletedAtIsNil(),
		).
		ForUpdate().
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return entQuotaToRecord(entity), nil
}

// readActiveQuotaRow 读出 (user, platform) 的活跃行，没有时返回 (nil, nil)。
func readActiveQuotaRow(ctx context.Context, client *dbent.Client, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	entity, err := client.UserPlatformQuota.Query().
		Where(
			userplatformquota.UserIDEQ(userID),
			userplatformquota.PlatformEQ(platform),
			userplatformquota.DeletedAtIsNil(),
		).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return entQuotaToRecord(entity), nil
}

// softDeleteActiveQuotaRow 软删 (user, platform) 的活跃行；与 softDeleteMissingPlatforms 同样只写 deleted_at / updated_at。
func softDeleteActiveQuotaRow(ctx context.Context, client *dbent.Client, userID int64, platform string, now time.Time) error {
	const query = `UPDATE user_platform_quotas SET deleted_at = $3, updated_at = $3
		WHERE user_id = $1 AND platform = $2 AND deleted_at IS NULL`
	_, err := client.ExecContext(ctx, query, userID, platform, now)
	return err
}
