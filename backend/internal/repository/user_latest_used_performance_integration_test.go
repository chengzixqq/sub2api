//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type latestUsedQueryCapture struct {
	sqlExecutor
	query string
	args  []any
}

func (r *latestUsedQueryCapture) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.query, r.args = query, args
	return r.sqlExecutor.QueryContext(ctx, query, args...)
}

type latestUsedPlan struct {
	Relation string           `json:"Relation Name"`
	Index    string           `json:"Index Name"`
	Rows     float64          `json:"Actual Rows"`
	Loops    float64          `json:"Actual Loops"`
	Removed  float64          `json:"Rows Removed by Filter"`
	Children []latestUsedPlan `json:"Plans"`
}

func (p latestUsedPlan) usageWork() (float64, bool) {
	var work float64
	var indexed bool
	if p.Relation == "usage_logs" {
		work = (p.Rows + p.Removed) * p.Loops
		indexed = p.Index != ""
	}
	for _, child := range p.Children {
		childWork, childIndexed := child.usageWork()
		work += childWork
		indexed = indexed || childIndexed
	}
	return work, indexed
}

func (s *UserRepoSuite) TestLatestUsedAt_BoundedIndexRead() {
	user := s.mustCreateUser(&service.User{Email: "latest-used-performance@test.com"})
	missing := s.mustCreateUser(&service.User{Email: "latest-used-no-history@test.com"})
	s.mustInsertUsageLog(user.ID, time.Now().Add(-24*time.Hour))
	_, err := integrationDB.ExecContext(s.ctx, `
		INSERT INTO usage_logs (user_id, api_key_id, account_id, model, input_tokens, output_tokens, total_cost, actual_cost, created_at)
		SELECT ul.user_id, ul.api_key_id, ul.account_id, ul.model, 1, 1, 0.01, 0.01, NOW() - n * INTERVAL '1 second'
		FROM usage_logs ul CROSS JOIN generate_series(1, 10000) n WHERE ul.user_id = $1`, user.ID)
	s.Require().NoError(err)
	// A real service contains multiple users. A single-user ANALYZE fixture has
	// n_distinct=1, making PostgreSQL assume every unknown correlated ID matches
	// every row and incorrectly prefer the smaller global time index for misses.
	// Exercise a skewed multi-user distribution without forcing any planner GUC.
	for i := 0; i < 20; i++ {
		other := s.mustCreateUser(&service.User{Email: fmt.Sprintf("latest-used-cohort-%d@test.com", i)})
		s.mustInsertUsageLog(other.ID, time.Now().Add(-48*time.Hour))
	}
	s.Require().NoError(func() error { _, err := integrationDB.ExecContext(s.ctx, "ANALYZE usage_logs"); return err }())
	var indexDefinition string
	s.Require().NoError(integrationDB.QueryRowContext(s.ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname = 'public' AND tablename = 'usage_logs' AND indexdef LIKE '%(user_id, created_at)%' LIMIT 1`).Scan(&indexDefinition))
	s.T().Logf("existing migration index: %s", indexDefinition)
	capture := &latestUsedQueryCapture{sqlExecutor: integrationDB}
	repo := newUserRepositoryWithSQL(s.client, capture)
	got, err := repo.GetLatestUsedAtByUserIDs(s.ctx, []int64{user.ID, missing.ID, user.ID})
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	s.Require().NotNil(got[user.ID])
	rows, err := integrationDB.QueryContext(s.ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+capture.query, capture.args...)
	s.Require().NoError(err)
	defer rows.Close()
	s.Require().True(rows.Next())
	var raw []byte
	s.Require().NoError(rows.Scan(&raw))
	var plan []struct {
		Plan latestUsedPlan `json:"Plan"`
	}
	s.Require().NoError(json.Unmarshal(raw, &plan))
	s.Require().Len(plan, 1)
	work, indexed := plan[0].Plan.usageWork()
	s.T().Logf("10,001 history rows, duplicate requested ID + no-log user: usage tuples read=%.0f, indexed=%v; plan=%s", work, indexed, raw)
	s.Require().True(indexed, "latest-use lookup must use the existing user/time index")
	s.Require().LessOrEqual(work, float64(2), "latest-use work must be bounded by requested users, not their history size")
}
