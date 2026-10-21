//go:build integration

package repository

import (
	"context"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newGuardCode creates a code with a random value (codes are unique across the shared test database).
func (s *RedeemCodeRepoSuite) newGuardCode(codeType, status string, value float64, usedBy *int64) *service.RedeemCode {
	code := &service.RedeemCode{Code: "DG-" + uuid.NewString()[:13], Type: codeType, Value: value, Status: status, UsedBy: usedBy}
	if usedBy != nil {
		usedAt := time.Now()
		code.UsedAt = &usedAt
	}
	s.Require().NoError(s.repo.Create(s.ctx, code))
	return code
}

// missingCodeID returns the id of a code that existed and was removed, so it cannot belong to anyone else.
func (s *RedeemCodeRepoSuite) missingCodeID() int64 {
	gone := s.newGuardCode(service.RedeemTypeBalance, service.StatusUnused, 1, nil)
	s.Require().NoError(s.repo.Delete(s.ctx, gone.ID))
	return gone.ID
}

func (s *RedeemCodeRepoSuite) requireCodeExists(id int64) {
	_, err := s.repo.GetByID(s.ctx, id)
	s.Require().NoError(err, "code %d must still exist", id)
}

func (s *RedeemCodeRepoSuite) requireCodeGone(id int64) {
	_, err := s.repo.GetByID(s.ctx, id)
	s.Require().ErrorIs(err, service.ErrRedeemCodeNotFound, "code %d must be deleted", id)
}

// Deleting a redeemed code would erase it from the user's balance history and lower total_recharged.
func (s *RedeemCodeRepoSuite) TestDeleteUnlessUsed_ProtectsUserHistory() {
	user := s.createUser(uniqueTestValue(s.T(), "delete-guard") + "@example.com")
	used := s.newGuardCode(service.RedeemTypeBalance, service.StatusUsed, 10, &user.ID)
	unused := s.newGuardCode(service.RedeemTypeBalance, service.StatusUnused, 10, nil)

	err := s.repo.DeleteUnlessUsed(s.ctx, used.ID)
	s.Require().ErrorIs(err, service.ErrRedeemCodeUsed)
	s.Require().Equal("REDEEM_CODE_USED", infraerrors.Reason(err))
	s.Require().Equal(409, infraerrors.Code(err))
	s.Require().Equal("used redeem codes cannot be deleted", infraerrors.Message(err))
	s.requireCodeExists(used.ID)

	_, page, err := s.repo.ListByUserPaginated(s.ctx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 20}, "")
	s.Require().NoError(err)
	s.Require().Equal(int64(1), page.Total, "balance history keeps the redeemed code")
	totalRecharged, err := s.repo.SumPositiveBalanceByUser(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().InDelta(10, totalRecharged, 1e-9, "total_recharged is unchanged")

	s.Require().NoError(s.repo.DeleteUnlessUsed(s.ctx, unused.ID))
	s.requireCodeGone(unused.ID)

	err = s.repo.DeleteUnlessUsed(s.ctx, unused.ID)
	s.Require().ErrorIs(err, service.ErrRedeemCodeNotFound)
	s.Require().Equal(404, infraerrors.Code(err))
}

// used_by stays set when a redeemed code's status changes (codes expired before G05), and the admin_* rows are the
// balance / concurrency adjustment records: neither may be deleted. Codes nobody redeemed stay deletable.
func (s *RedeemCodeRepoSuite) TestDeleteUnlessUsed_FormerlyUsedAndAdminRecordsProtected() {
	user := s.createUser(uniqueTestValue(s.T(), "delete-guard-admin") + "@example.com")
	protected := map[string]*service.RedeemCode{
		"formerly used, now expired": s.newGuardCode(service.RedeemTypeBalance, service.StatusExpired, 10, &user.ID),
		"used, used_by missing":      s.newGuardCode(service.RedeemTypeBalance, service.StatusUsed, 10, nil),
		"admin_balance record":       s.newGuardCode(service.AdjustmentTypeAdminBalance, service.StatusUsed, 5, &user.ID),
		"admin_concurrency (unused)": s.newGuardCode(service.AdjustmentTypeAdminConcurrency, service.StatusUnused, 1, nil),
	}
	for name, code := range protected {
		err := s.repo.DeleteUnlessUsed(s.ctx, code.ID)
		s.Require().ErrorIs(err, service.ErrRedeemCodeUsed, name)
		s.requireCodeExists(code.ID)
	}

	deletable := map[string]*service.RedeemCode{
		"expired, never used":  s.newGuardCode(service.RedeemTypeBalance, service.StatusExpired, 10, nil),
		"disabled, never used": s.newGuardCode(service.RedeemTypeConcurrency, service.StatusDisabled, 2, nil),
	}
	for name, code := range deletable {
		s.Require().NoError(s.repo.DeleteUnlessUsed(s.ctx, code.ID), name)
		s.requireCodeGone(code.ID)
	}
}

func (s *RedeemCodeRepoSuite) TestBatchDeleteUnlessUsed_SkipsProtected() {
	user := s.createUser(uniqueTestValue(s.T(), "batch-delete-guard") + "@example.com")
	used := s.newGuardCode(service.RedeemTypeBalance, service.StatusUsed, 10, &user.ID)
	unused := s.newGuardCode(service.RedeemTypeBalance, service.StatusUnused, 10, nil)
	adminRecord := s.newGuardCode(service.AdjustmentTypeAdminBalance, service.StatusUsed, 3, &user.ID)
	expired := s.newGuardCode(service.RedeemTypeBalance, service.StatusExpired, 10, nil)
	disabled := s.newGuardCode(service.RedeemTypeSubscription, service.StatusDisabled, 0, nil)
	missing := s.missingCodeID()

	ids := []int64{adminRecord.ID, used.ID, unused.ID, expired.ID, disabled.ID, missing, unused.ID, 0, -1}
	deleted, skipped, err := s.repo.BatchDeleteUnlessUsed(s.ctx, ids)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), deleted, "missing, duplicate and non-positive ids are not counted")
	wantSkipped := []int64{used.ID, adminRecord.ID}
	sort.Slice(wantSkipped, func(i, j int) bool { return wantSkipped[i] < wantSkipped[j] })
	s.Require().Equal(wantSkipped, skipped, "protected ids, ascending")

	s.requireCodeExists(used.ID)
	s.requireCodeExists(adminRecord.ID)
	s.requireCodeGone(unused.ID)
	s.requireCodeGone(expired.ID)
	s.requireCodeGone(disabled.ID)
}

func (s *RedeemCodeRepoSuite) TestBatchDeleteUnlessUsed_EmptyInput() {
	for _, ids := range [][]int64{nil, {}, {0, -3}} {
		deleted, skipped, err := s.repo.BatchDeleteUnlessUsed(s.ctx, ids)
		s.Require().NoError(err)
		s.Require().Zero(deleted)
		s.Require().NotNil(skipped)
		s.Require().Empty(skipped)
	}
}

// More ids than one chunk: codes on both sides of the chunk boundary are handled.
func (s *RedeemCodeRepoSuite) TestBatchDeleteUnlessUsed_SpansChunks() {
	first := s.newGuardCode(service.RedeemTypeBalance, service.StatusUnused, 1, nil)
	last := s.newGuardCode(service.RedeemTypeBalance, service.StatusUnused, 1, nil)
	protected := s.newGuardCode(service.RedeemTypeBalance, service.StatusUsed, 1, nil)

	ids := []int64{first.ID}
	for i := int64(0); i < 2*redeemCodeDeleteChunkSize; i++ {
		ids = append(ids, math.MaxInt64-i)
	}
	ids = append(ids, protected.ID, last.ID)

	deleted, skipped, err := s.repo.BatchDeleteUnlessUsed(s.ctx, ids)
	s.Require().NoError(err)
	s.Require().Equal(int64(2), deleted)
	s.Require().Equal([]int64{protected.ID}, skipped)
	s.requireCodeGone(first.ID)
	s.requireCodeGone(last.ID)
	s.requireCodeExists(protected.ID)
}

// Without a caller transaction the batch opens and commits its own.
func TestRedeemCodeRepository_BatchDeleteUnlessUsed_OwnTransaction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewRedeemCodeRepository(client)

	create := func(status string) int64 {
		code := &service.RedeemCode{Code: "DG-" + uuid.NewString()[:13], Type: service.RedeemTypeBalance, Value: 1, Status: status}
		require.NoError(t, repo.Create(ctx, code))
		return code.ID
	}
	unusedID := create(service.StatusUnused)
	usedID := create(service.StatusUsed)
	t.Cleanup(func() {
		_, _ = client.RedeemCode.Delete().Where(redeemcode.IDIn(unusedID, usedID)).Exec(context.Background())
	})

	deleted, skipped, err := repo.BatchDeleteUnlessUsed(ctx, []int64{unusedID, usedID})
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	require.Equal(t, []int64{usedID}, skipped)

	_, err = repo.GetByID(ctx, unusedID)
	require.ErrorIs(t, err, service.ErrRedeemCodeNotFound)
	_, err = repo.GetByID(ctx, usedID)
	require.NoError(t, err)

	require.ErrorIs(t, repo.DeleteUnlessUsed(ctx, usedID), service.ErrRedeemCodeUsed)
}
