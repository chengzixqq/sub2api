package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// TestUsageLogRepositoryGetGroupStatsHonorsModelFilterSource locks the model
// filter of dashboard/groups (and the groups part of snapshot-v2) to the
// requested model_source; an empty source keeps the requested-model match.
func TestUsageLogRepositoryGetGroupStatsHonorsModelFilterSource(t *testing.T) {
	requested := "COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model)"
	upstream := "COALESCE(NULLIF(TRIM(ul.upstream_model), ''), ul.model)"
	mapping := "(" + requested + " || ' -> ' || " + upstream + ")"

	cases := []struct {
		name    string
		source  string
		model   string
		want    string
		notWant string
	}{
		{name: "upstream", source: usagestats.ModelSourceUpstream, model: "claude-opus-4-20250514", want: upstream, notWant: requested},
		{name: "mapping", source: usagestats.ModelSourceMapping, model: "claude-opus-4 -> claude-opus-4-20250514", want: mapping},
		{name: "requested", source: usagestats.ModelSourceRequested, model: "claude-opus-4", want: requested, notWant: upstream},
		{name: "empty source stays requested", source: "", model: "claude-opus-4", want: requested, notWant: upstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			condition := fmt.Sprintf(" AND %s = $3", tc.want)
			matcher := sqlmock.QueryMatcherFunc(func(_, actual string) error {
				if !strings.Contains(actual, condition) {
					return fmt.Errorf("query lacks %q:\n%s", condition, actual)
				}
				if tc.notWant != "" && strings.Contains(actual, tc.notWant) {
					return fmt.Errorf("query must not match the model with %q:\n%s", tc.notWant, actual)
				}
				return nil
			})
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := &usageLogRepository{sql: db}

			start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			end := start.Add(24 * time.Hour)
			mock.ExpectQuery("group stats").
				WithArgs(start, end, tc.model).
				WillReturnRows(sqlmock.NewRows([]string{
					"group_id", "group_name", "requests", "total_tokens",
					"cost", "actual_cost", "account_cost",
				}).AddRow(int64(3), "g", int64(2), int64(40), 0.2, 0.1, 0.05))

			filters := usagestats.UsageLogFilters{Model: tc.model, ModelFilterSource: tc.source}
			results, err := repo.GetGroupStatsWithUsageFilters(context.Background(), start, end, filters)
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Equal(t, int64(2), results[0].Requests)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
