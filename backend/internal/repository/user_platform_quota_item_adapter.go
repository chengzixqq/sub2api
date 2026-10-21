package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 单平台方法的 service 适配（后端适配清单 E07）。具体 adapter 与通用 adapter 共用下面的转换：
// 记录转换 nil 安全，repository sentinel 包装成带稳定 reason 的 service sentinel（写法同 ResetExpiredWindow）。

func (a *userPlatformQuotaServiceAdapter) UpsertOneForUser(ctx context.Context, userID int64, rec service.UserPlatformQuotaRecord) (*service.UserPlatformQuotaRecord, *service.UserPlatformQuotaRecord, error) {
	return upsertOneForUserViaRepo(ctx, a.inner, userID, rec)
}

func (a *userPlatformQuotaServiceAdapter) SoftDeleteOne(ctx context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	return softDeleteOneViaRepo(ctx, a.inner, userID, platform)
}

func (a *userPlatformQuotaServiceAdapter) RestoreLatestDeleted(ctx context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	return restoreLatestDeletedViaRepo(ctx, a.inner, userID, platform)
}

func (a *genericUserPlatformQuotaAdapter) UpsertOneForUser(ctx context.Context, userID int64, rec service.UserPlatformQuotaRecord) (*service.UserPlatformQuotaRecord, *service.UserPlatformQuotaRecord, error) {
	return upsertOneForUserViaRepo(ctx, a.inner, userID, rec)
}

func (a *genericUserPlatformQuotaAdapter) SoftDeleteOne(ctx context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	return softDeleteOneViaRepo(ctx, a.inner, userID, platform)
}

func (a *genericUserPlatformQuotaAdapter) RestoreLatestDeleted(ctx context.Context, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	return restoreLatestDeletedViaRepo(ctx, a.inner, userID, platform)
}

func upsertOneForUserViaRepo(ctx context.Context, repo UserPlatformQuotaRepository, userID int64, rec service.UserPlatformQuotaRecord) (*service.UserPlatformQuotaRecord, *service.UserPlatformQuotaRecord, error) {
	repoRec := toRepoRecords([]service.UserPlatformQuotaRecord{rec})[0]
	repoRec.UserID = userID
	current, previous, err := repo.UpsertOneForUser(ctx, userID, repoRec)
	if err != nil {
		return nil, nil, wrapSingleQuotaError(err)
	}
	return toServiceRecordOrNil(current), toServiceRecordOrNil(previous), nil
}

func softDeleteOneViaRepo(ctx context.Context, repo UserPlatformQuotaRepository, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	previous, err := repo.SoftDeleteOne(ctx, userID, platform)
	if err != nil {
		return nil, wrapSingleQuotaError(err)
	}
	return toServiceRecordOrNil(previous), nil
}

func restoreLatestDeletedViaRepo(ctx context.Context, repo UserPlatformQuotaRepository, userID int64, platform string) (*service.UserPlatformQuotaRecord, error) {
	restored, err := repo.RestoreLatestDeleted(ctx, userID, platform)
	if err != nil {
		return nil, wrapSingleQuotaError(err)
	}
	return toServiceRecordOrNil(restored), nil
}

// wrapSingleQuotaError 把 repository sentinel 包装成 service sentinel，两层 errors.Is 都成立。
func wrapSingleQuotaError(err error) error {
	switch {
	case errors.Is(err, ErrUserPlatformQuotaActiveExists):
		return fmt.Errorf("%w: %w", service.ErrUserPlatformQuotaActiveExists, err)
	case errors.Is(err, ErrUserPlatformQuotaNotFound):
		return fmt.Errorf("%w: %w", service.ErrUserPlatformQuotaNotFound, err)
	default:
		return err
	}
}

func toServiceRecordOrNil(rec *UserPlatformQuotaRecord) *service.UserPlatformQuotaRecord {
	if rec == nil {
		return nil
	}
	return toServiceRecord(rec)
}
