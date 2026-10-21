//go:build unit

package repository

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fakeRepoForAdapter struct {
	upsertCalledWith   []UserPlatformQuotaRecord
	upsertCalledUserID int64
	upsertErr          error
	resetCalledWith    [4]any // userID, platform, window, newStart
	resetErr           error

	// 单平台方法
	oneUserID     int64
	oneRecord     UserPlatformQuotaRecord
	onePlatform   string
	oneCurrent    *UserPlatformQuotaRecord
	onePrevious   *UserPlatformQuotaRecord
	oneErr        error
	deletePrev    *UserPlatformQuotaRecord
	deleteErr     error
	restoreRecord *UserPlatformQuotaRecord
	restoreErr    error
}

func (f *fakeRepoForAdapter) UpsertOneForUser(_ context.Context, userID int64, rec UserPlatformQuotaRecord) (*UserPlatformQuotaRecord, *UserPlatformQuotaRecord, error) {
	f.oneUserID = userID
	f.oneRecord = rec
	return f.oneCurrent, f.onePrevious, f.oneErr
}
func (f *fakeRepoForAdapter) SoftDeleteOne(_ context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	f.oneUserID = userID
	f.onePlatform = platform
	return f.deletePrev, f.deleteErr
}
func (f *fakeRepoForAdapter) RestoreLatestDeleted(_ context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	f.oneUserID = userID
	f.onePlatform = platform
	return f.restoreRecord, f.restoreErr
}

func (f *fakeRepoForAdapter) BulkInsertInitial(_ context.Context, _ []UserPlatformQuotaRecord) error {
	return nil
}
func (f *fakeRepoForAdapter) GetByUserPlatform(_ context.Context, _ int64, _ string) (*UserPlatformQuotaRecord, error) {
	return nil, nil
}
func (f *fakeRepoForAdapter) ListByUser(_ context.Context, _ int64) ([]UserPlatformQuotaRecord, error) {
	return nil, nil
}
func (f *fakeRepoForAdapter) IncrementUsageWithReset(_ context.Context, _ int64, _ string, _ float64, _ time.Time) error {
	return nil
}
func (f *fakeRepoForAdapter) ResetExpiredWindow(_ context.Context, userID int64, platform string, window string, newStart time.Time) error {
	f.resetCalledWith = [4]any{userID, platform, window, newStart}
	return f.resetErr
}
func (f *fakeRepoForAdapter) UpsertForUser(_ context.Context, userID int64, records []UserPlatformQuotaRecord) error {
	f.upsertCalledUserID = userID
	f.upsertCalledWith = records
	return f.upsertErr
}
func (f *fakeRepoForAdapter) BatchSnapshotUsage(_ context.Context, _ []UserPlatformQuotaSnapshot, _ time.Time) error {
	return nil
}

func TestGenericAdapter_UpsertForUser_ForwardsRecords(t *testing.T) {
	fake := &fakeRepoForAdapter{}
	adapter := NewUserPlatformQuotaServiceAdapter(fake)

	err := adapter.UpsertForUser(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fake.upsertCalledUserID != 42 {
		t.Errorf("forwarded userID = %d, want 42", fake.upsertCalledUserID)
	}
}

func TestGenericAdapter_UpsertForUser_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	fake := &fakeRepoForAdapter{upsertErr: wantErr}
	adapter := NewUserPlatformQuotaServiceAdapter(fake)

	err := adapter.UpsertForUser(context.Background(), 1, nil)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected %v, got %v", wantErr, err)
	}
}

func TestGenericAdapter_ResetExpiredWindow_ForwardsAllParams(t *testing.T) {
	fake := &fakeRepoForAdapter{}
	adapter := NewUserPlatformQuotaServiceAdapter(fake)

	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	if err := adapter.ResetExpiredWindow(context.Background(), 7, "openai", "weekly", now); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fake.resetCalledWith[0].(int64) != 7 ||
		fake.resetCalledWith[1].(string) != "openai" ||
		fake.resetCalledWith[2].(string) != "weekly" ||
		!fake.resetCalledWith[3].(time.Time).Equal(now) {
		t.Errorf("forwarded params mismatch: %+v", fake.resetCalledWith)
	}
}

func TestGenericAdapter_SingleQuotaMethodsWrapSentinels(t *testing.T) {
	ctx := context.Background()
	daily := 20.0
	oldDaily := 10.0
	start := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	fake := &fakeRepoForAdapter{
		oneCurrent:  &UserPlatformQuotaRecord{UserID: 42, Platform: "anthropic", DailyLimitUSD: &daily, DailyUsageUSD: 1.5, DailyWindowStart: &start},
		onePrevious: &UserPlatformQuotaRecord{UserID: 42, Platform: "anthropic", DailyLimitUSD: &oldDaily, DailyUsageUSD: 1.5},
	}
	adapter := NewUserPlatformQuotaServiceAdapter(fake)

	current, previous, err := adapter.UpsertOneForUser(ctx, 42, service.UserPlatformQuotaRecord{Platform: "anthropic", DailyLimitUSD: &daily})
	require.NoError(t, err)
	require.Equal(t, int64(42), fake.oneUserID)
	require.Equal(t, int64(42), fake.oneRecord.UserID, "the adapter stamps the user id on the record")
	require.Equal(t, "anthropic", fake.oneRecord.Platform)
	require.Same(t, &daily, fake.oneRecord.DailyLimitUSD)
	require.Nil(t, fake.oneRecord.WeeklyLimitUSD)
	require.Equal(t, service.UserPlatformQuotaRecord{UserID: 42, Platform: "anthropic", DailyLimitUSD: &daily, DailyUsageUSD: 1.5, DailyWindowStart: &start}, *current)
	require.Equal(t, 10.0, *previous.DailyLimitUSD)

	fake.oneCurrent, fake.onePrevious = nil, nil
	current, previous, err = adapter.UpsertOneForUser(ctx, 42, service.UserPlatformQuotaRecord{Platform: "anthropic"})
	require.NoError(t, err)
	require.Nil(t, current, "nil records stay nil")
	require.Nil(t, previous)

	fake.deleteErr = ErrUserPlatformQuotaNotFound
	_, err = adapter.SoftDeleteOne(ctx, 7, "zhipu")
	require.ErrorIs(t, err, service.ErrUserPlatformQuotaNotFound)
	require.ErrorIs(t, err, ErrUserPlatformQuotaNotFound)
	require.Equal(t, "USER_PLATFORM_QUOTA_NOT_FOUND", infraerrors.Reason(err))
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))
	require.Equal(t, "zhipu", fake.onePlatform)

	fake.restoreErr = ErrUserPlatformQuotaActiveExists
	_, err = adapter.RestoreLatestDeleted(ctx, 7, "zhipu")
	require.ErrorIs(t, err, service.ErrUserPlatformQuotaActiveExists)
	require.Equal(t, "USER_PLATFORM_QUOTA_ACTIVE_EXISTS", infraerrors.Reason(err))
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))

	fake.restoreErr = ErrUserPlatformQuotaNotFound
	_, err = adapter.RestoreLatestDeleted(ctx, 7, "zhipu")
	require.ErrorIs(t, err, service.ErrUserPlatformQuotaNotFound)

	other := errors.New("db down")
	fake.oneErr = other
	_, _, err = adapter.UpsertOneForUser(ctx, 7, service.UserPlatformQuotaRecord{Platform: "zhipu", DailyLimitUSD: &daily})
	require.ErrorIs(t, err, other)
	require.NotErrorIs(t, err, service.ErrUserPlatformQuotaNotFound)

	fake.deleteErr, fake.deletePrev = nil, &UserPlatformQuotaRecord{UserID: 7, Platform: "zhipu", DailyLimitUSD: &oldDaily}
	prev, err := adapter.SoftDeleteOne(ctx, 7, "zhipu")
	require.NoError(t, err)
	require.Equal(t, 10.0, *prev.DailyLimitUSD)
	fake.restoreErr, fake.restoreRecord = nil, &UserPlatformQuotaRecord{UserID: 7, Platform: "zhipu", DailyUsageUSD: 3}
	restored, err := adapter.RestoreLatestDeleted(ctx, 7, "zhipu")
	require.NoError(t, err)
	require.Equal(t, 3.0, restored.DailyUsageUSD)
}

func TestGenericAdapter_ResetExpiredWindow_PropagatesError(t *testing.T) {
	wantErr := errors.New("not found")
	fake := &fakeRepoForAdapter{resetErr: wantErr}
	adapter := NewUserPlatformQuotaServiceAdapter(fake)

	err := adapter.ResetExpiredWindow(context.Background(), 1, "a", "daily", time.Now())
	if !errors.Is(err, wantErr) {
		t.Errorf("expected %v, got %v", wantErr, err)
	}
}
