package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// DashboardService finds the sorted ranking by duck typing, so a signature
// drift would not break the build; it would only make non-default sorts fail
// at runtime. Pin the match here.
func TestUsageLogRepositoryProvidesSortedUserRanking(t *testing.T) {
	repo := NewUsageLogRepository(nil, nil)
	_, ok := repo.(interface {
		GetUserSpendingRankingSorted(context.Context, time.Time, time.Time, int, string, usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error)
	})
	require.True(t, ok)
}

func TestUsageLogRepositoryGetUserSpendingRankingSortWhitelist(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	defaultOrder := "ORDER BY actual_cost DESC, tokens DESC, user_id ASC"
	for _, tc := range []struct {
		sortBy string
		order  string
	}{
		{"requests", "ORDER BY requests DESC, actual_cost DESC, user_id ASC"},
		{"tokens", "ORDER BY tokens DESC, actual_cost DESC, user_id ASC"},
		{"actual_cost", defaultOrder},
		{"", defaultOrder},
		{"bogus; DROP TABLE users", defaultOrder},
	} {
		t.Run(tc.sortBy, func(t *testing.T) {
			var captured string
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
				captured = actual
				return nil
			})))
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectQuery("users-ranking").WithArgs(start, end, 12).WillReturnRows(sqlmock.NewRows([]string{"user_id"}))

			repo := &usageLogRepository{sql: db}
			got, err := repo.GetUserSpendingRankingSorted(context.Background(), start, end, 0, tc.sortBy, UsageLogFilters{})
			require.NoError(t, err)
			require.Empty(t, got.Ranking)
			require.NoError(t, mock.ExpectationsWereMet())

			// The ranked CTE and the outer SELECT must agree, or LIMIT would
			// keep different users from the ones finally shown.
			require.Equal(t, 2, strings.Count(captured, tc.order), captured)
			require.Contains(t, captured, "COUNT(*) OVER ()")
			if tc.sortBy != "" {
				require.NotContains(t, captured, "bogus")
			}
		})
	}
}
