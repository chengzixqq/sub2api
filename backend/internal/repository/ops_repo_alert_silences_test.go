package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var opsSilenceColumns = []string{"id", "rule_id", "platform", "group_id", "region", "until", "reason", "created_by", "created_at", "created_by_name"}

func newOpsSilenceRepo(t *testing.T) (*opsRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &opsRepository{db: db}, mock
}

func TestListAlertSilences_ActiveForRule(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	now := time.Date(2026, 10, 18, 1, 41, 0, 0, time.UTC)
	until := now.Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("FROM ops_alert_silences s")).
		WithArgs(int64(41), now, 100).
		WillReturnRows(sqlmock.NewRows(opsSilenceColumns).
			AddRow(int64(7), int64(41), "", int64(2), nil, until, "夜间维护", int64(1), now, "admin"))

	ruleID := int64(41)
	out, err := repo.ListAlertSilences(context.Background(), &service.OpsAlertSilenceFilter{RuleID: &ruleID, ActiveOnly: true, Limit: 100, Now: now})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, int64(7), out[0].ID)
	require.Equal(t, "", out[0].Platform)
	require.Equal(t, int64(2), *out[0].GroupID)
	require.Nil(t, out[0].Region)
	require.Equal(t, "admin", out[0].CreatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Shortening a silence also shortens overlapping silences of the same scope, so the event is no longer silenced
// beyond the new time by an older, longer entry.
func TestUpdateAlertSilenceUntil_ShortensSameScope(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	until := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	reason := "改为 1 小时"

	mock.ExpectQuery(regexp.QuoteMeta("WITH target AS")).
		WithArgs(int64(7), until, reason).
		WillReturnRows(sqlmock.NewRows(opsSilenceColumns).
			AddRow(int64(7), int64(41), "openai", nil, nil, until, reason, nil, until.Add(-time.Hour), ""))

	out, err := repo.UpdateAlertSilenceUntil(context.Background(), 7, until, &reason)
	require.NoError(t, err)
	require.Equal(t, until, out.Until)
	require.Equal(t, reason, out.Reason)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateAlertSilenceUntil_Missing(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	mock.ExpectQuery(regexp.QuoteMeta("WITH target AS")).WillReturnRows(sqlmock.NewRows(opsSilenceColumns))

	_, err := repo.UpdateAlertSilenceUntil(context.Background(), 404, time.Now().Add(time.Hour), nil)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestEndAlertSilence(t *testing.T) {
	now := time.Date(2026, 10, 18, 1, 41, 0, 0, time.UTC)

	t.Run("ends the scope's active silences", func(t *testing.T) {
		repo, mock := newOpsSilenceRepo(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH target AS")).WithArgs(int64(7), now).
			WillReturnRows(sqlmock.NewRows([]string{"found"}).AddRow(true))
		require.NoError(t, repo.EndAlertSilence(context.Background(), 7, now))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("already expired silence is still a success", func(t *testing.T) {
		repo, mock := newOpsSilenceRepo(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH target AS")).WithArgs(int64(7), now).
			WillReturnRows(sqlmock.NewRows([]string{"found"}).AddRow(true))
		require.NoError(t, repo.EndAlertSilence(context.Background(), 7, now))
	})

	t.Run("unknown id", func(t *testing.T) {
		repo, mock := newOpsSilenceRepo(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH target AS")).WithArgs(int64(404), now).
			WillReturnRows(sqlmock.NewRows([]string{"found"}).AddRow(false))
		require.ErrorIs(t, repo.EndAlertSilence(context.Background(), 404, now), sql.ErrNoRows)
	})
}
