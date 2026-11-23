//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type clearSnapshotSettings struct {
	service.SettingRepository
	reads          atomic.Int32
	accounts       *clearSnapshotAccounts
	observedClears atomic.Int32
}

func (r *clearSnapshotSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyClaudeCustomization {
		r.reads.Add(1)
		if r.accounts != nil {
			r.observedClears.Store(r.accounts.clears.Load())
		}
	}
	return "", nil
}
func (*clearSnapshotSettings) SetMultiple(context.Context, map[string]string) error { return nil }
func (*clearSnapshotSettings) Set(context.Context, string, string) error            { return nil }
func (*clearSnapshotSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (*clearSnapshotSettings) ListClaudeAlignmentOverrides(context.Context, []string) ([]service.ClaudeAlignmentOverrides, error) {
	return nil, nil
}

type clearSnapshotAccounts struct {
	service.AdminAccountRepository
	clears   atomic.Int32
	failAt   string
	inactive bool
}

func (r *clearSnapshotAccounts) ClearError(context.Context, int64) error {
	if r.failAt == "clear" {
		return errors.New("clear failed")
	}
	r.clears.Add(1)
	return nil
}
func (r *clearSnapshotAccounts) ClearErrorPreservingInactive(ctx context.Context, id int64) (bool, error) {
	if r.inactive {
		return false, nil
	}
	return true, r.ClearError(ctx, id)
}
func (r *clearSnapshotAccounts) ClearRateLimit(context.Context, int64) error {
	if r.failAt == "runtime" {
		return errors.New("runtime failed")
	}
	return nil
}
func (*clearSnapshotAccounts) ClearAntigravityQuotaScopes(context.Context, int64) error { return nil }
func (*clearSnapshotAccounts) ClearModelRateLimits(context.Context, int64) error        { return nil }
func (*clearSnapshotAccounts) ClearTempUnschedulable(context.Context, int64) error      { return nil }
func (*clearSnapshotAccounts) GetByIDScoped(_ context.Context, id int64) (*service.Account, error) {
	return &service.Account{ID: id, Status: service.StatusActive, Type: service.AccountTypeAPIKey}, nil
}

func TestAccountErrorSnapshotRefreshOnce(t *testing.T) {
	for _, mode := range []string{"batch50", "single", "commit50", "rollback50", "single_commit", "single_rollback", "skipped", "clear_failed", "runtime_failed"} {
		t.Run(mode, func(t *testing.T) {
			root := &clearSnapshotSettings{}
			settings := service.ProvideSettingService(nil, root, nil, nil, &config.Config{}, service.NewAlignmentHitRecorder(nil))
			root.reads.Store(0)
			accounts := &clearSnapshotAccounts{}
			root.accounts = accounts
			if mode == "skipped" {
				accounts.inactive = true
			}
			if mode == "clear_failed" {
				accounts.failAt = "clear"
			}
			if mode == "runtime_failed" {
				accounts.failAt = "runtime"
			}
			admin := service.NewAdminService(nil, nil, nil, accounts, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := setupClearErrorRouter(admin)
			ctx := service.WithScope(t.Context(), service.AdminScope())
			var tx *dbent.Tx
			var mock sqlmock.Sqlmock
			if mode == "commit50" || mode == "rollback50" || mode == "single_commit" || mode == "single_rollback" {
				db, m, err := sqlmock.New()
				require.NoError(t, err)
				mock = m
				client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
				t.Cleanup(func() { _ = client.Close() })
				mock.ExpectBegin()
				tx, err = client.Tx(ctx)
				require.NoError(t, err)
				ctx = dbent.NewTxContext(ctx, tx)
			}
			ids := make([]int64, 50)
			for i := range ids {
				ids[i] = int64(i + 1)
			}
			payload, err := json.Marshal(map[string]any{"account_ids": ids, "preserve_inactive": mode == "skipped"})
			require.NoError(t, err)
			req := jsonRequest(http.MethodPost, "/accounts/batch-clear-error", string(payload))
			if mode == "single" || mode == "single_commit" || mode == "single_rollback" {
				req = httptest.NewRequest(http.MethodPost, "/accounts/1/clear-error", nil)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req.WithContext(ctx))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			expectedClears := int32(50)
			if mode == "single" || mode == "single_commit" || mode == "single_rollback" {
				expectedClears = 1
			}
			if mode == "skipped" || mode == "clear_failed" {
				expectedClears = 0
			}
			require.Equal(t, expectedClears, accounts.clears.Load())
			if tx != nil {
				require.Zero(t, root.reads.Load(), "must not publish uncommitted state")
				if mode == "commit50" || mode == "single_commit" {
					mock.ExpectCommit()
					require.NoError(t, tx.Commit())
				} else {
					mock.ExpectRollback()
					require.NoError(t, tx.Rollback())
				}
				require.NoError(t, mock.ExpectationsWereMet())
			}
			want := int32(1)
			if mode == "rollback50" || mode == "single_rollback" || mode == "skipped" || mode == "clear_failed" {
				want = 0
			}
			require.Equal(t, want, root.reads.Load(), "one snapshot refresh per operation")
			if want == 1 {
				require.Equal(t, expectedClears, root.observedClears.Load(), "publish only after every worker has finished")
			}
		})
	}
}
