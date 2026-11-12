package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const accountDispatchPolicySelect = `
	SELECT p.user_id, p.group_id, p.version, p.mode, u.username, u.email,
	       a.id, g.origin, COALESCE(a.name, ''), COALESCE(a.dispatch_scope, ''), ag.account_id
	FROM user_group_account_policies p
	JOIN users u ON u.id = p.user_id AND u.deleted_at IS NULL
	JOIN groups grp ON grp.id = p.group_id AND grp.deleted_at IS NULL
	LEFT JOIN user_group_account_grants g ON g.user_id = p.user_id AND g.group_id = p.group_id
	LEFT JOIN accounts a ON a.id = g.account_id AND a.deleted_at IS NULL
	    AND (g.origin = 'member' OR (
	        grp.platform <> 'composite' AND a.platform = grp.platform
	        AND (a.workspace_id = 1 OR EXISTS (
	            SELECT 1 FROM workspace_group_grants wg
	            JOIN workspaces w ON w.id = wg.workspace_id AND w.deleted_at IS NULL AND w.status = 'active'
	            WHERE wg.workspace_id = a.workspace_id AND wg.group_id = p.group_id AND wg.enabled = TRUE
	        ))
	    ))
	LEFT JOIN account_groups ag ON ag.account_id = g.account_id AND ag.group_id = p.group_id
	WHERE %s
	ORDER BY p.user_id, g.account_id
`

const adminAccountDispatchPolicySelect = `
	SELECT p.user_id, p.group_id, p.version, p.mode, u.username, u.email,
	       g.account_id, g.origin, COALESCE(a.name, ''), COALESCE(a.dispatch_scope, ''),
	       CASE
	           WHEN a.id IS NULL OR a.deleted_at IS NOT NULL THEN 'account_deleted'
	           WHEN g.origin = 'member' AND ag.account_id IS NULL THEN 'member_removed'
	           WHEN g.origin = 'extra' AND ag.account_id IS NOT NULL THEN 'extra_account_is_member'
	           WHEN g.origin = 'extra' AND grp.platform = 'composite' THEN 'composite_group_not_supported'
	           WHEN g.origin = 'extra' AND a.platform <> grp.platform THEN 'platform_mismatch'
	           WHEN a.workspace_id <= 0 THEN 'invalid_workspace'
	           WHEN g.origin = 'extra' AND a.workspace_id <> 1 AND NOT EXISTS (
	               SELECT 1 FROM workspace_group_grants wg
	               JOIN workspaces w ON w.id = wg.workspace_id
	                    AND w.deleted_at IS NULL AND w.status = 'active'
	               WHERE wg.workspace_id = a.workspace_id
	                 AND wg.group_id = p.group_id AND wg.enabled = TRUE
	           ) THEN 'workspace_group_not_granted'
	           ELSE NULL
	       END AS ineligible_reason
	FROM user_group_account_policies p
	JOIN users u ON u.id = p.user_id AND u.deleted_at IS NULL
	JOIN groups grp ON grp.id = p.group_id AND grp.deleted_at IS NULL
	LEFT JOIN user_group_account_grants g ON g.user_id = p.user_id AND g.group_id = p.group_id
	LEFT JOIN accounts a ON a.id = g.account_id
	LEFT JOIN account_groups ag ON ag.account_id = g.account_id AND ag.group_id = p.group_id
	WHERE p.group_id = $1
	ORDER BY p.user_id, g.account_id
`

func (r *accountRepository) GetAccountDispatchPolicy(ctx context.Context, userID, groupID int64) (*service.AccountDispatchPolicy, error) {
	rows, err := r.client.QueryContext(ctx, fmt.Sprintf(accountDispatchPolicySelect, "p.user_id = $1 AND p.group_id = $2"), userID, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	policies, err := scanAccountDispatchPolicies(rows)
	if err != nil || len(policies) == 0 {
		return nil, err
	}
	return &policies[0], nil
}

func (r *accountRepository) ListAccountDispatchPolicies(ctx context.Context, groupID int64) ([]service.AccountDispatchPolicy, error) {
	rows, err := r.client.QueryContext(ctx, adminAccountDispatchPolicySelect, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAdminAccountDispatchPolicies(rows)
}

func scanAdminAccountDispatchPolicies(rows *sql.Rows) ([]service.AccountDispatchPolicy, error) {
	policies := make([]service.AccountDispatchPolicy, 0)
	for rows.Next() {
		var userID, groupID, version int64
		var mode, username, email string
		var accountID sql.NullInt64
		var origin, accountName, dispatchScope, ineligibleReason sql.NullString
		if err := rows.Scan(&userID, &groupID, &version, &mode, &username, &email,
			&accountID, &origin, &accountName, &dispatchScope, &ineligibleReason); err != nil {
			return nil, err
		}
		if len(policies) == 0 || policies[len(policies)-1].UserID != userID {
			policies = append(policies, service.AccountDispatchPolicy{
				UserID: userID, GroupID: groupID, Version: version, Mode: mode,
				Accounts: make([]service.AccountDispatchGrant, 0), Username: username, Email: email,
			})
		}
		if !accountID.Valid {
			continue
		}
		eligible := !ineligibleReason.Valid
		grant := service.AccountDispatchGrant{
			AccountID: accountID.Int64, Origin: origin.String,
			AccountName: accountName.String, DispatchScope: dispatchScope.String,
			Eligible: &eligible,
		}
		if ineligibleReason.Valid {
			grant.IneligibleReason = ineligibleReason.String
		}
		policies[len(policies)-1].Accounts = append(policies[len(policies)-1].Accounts, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return policies, nil
}

func scanAccountDispatchPolicies(rows *sql.Rows) ([]service.AccountDispatchPolicy, error) {
	policies := make([]service.AccountDispatchPolicy, 0)
	for rows.Next() {
		var userID, groupID, version int64
		var mode, username, email string
		var accountID sql.NullInt64
		var memberAccountID sql.NullInt64
		var origin, accountName, dispatchScope sql.NullString
		if err := rows.Scan(&userID, &groupID, &version, &mode, &username, &email, &accountID, &origin, &accountName, &dispatchScope, &memberAccountID); err != nil {
			return nil, err
		}
		if len(policies) == 0 || policies[len(policies)-1].UserID != userID {
			policies = append(policies, service.AccountDispatchPolicy{
				UserID: userID, GroupID: groupID, Version: version, Mode: mode,
				Accounts: make([]service.AccountDispatchGrant, 0), Username: username, Email: email,
			})
		}
		if accountID.Valid &&
			(origin.String != service.AccountDispatchOriginMember || memberAccountID.Valid) {
			policies[len(policies)-1].Accounts = append(policies[len(policies)-1].Accounts, service.AccountDispatchGrant{
				AccountID: accountID.Int64, Origin: origin.String,
				AccountName: accountName.String, DispatchScope: dispatchScope.String,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return policies, nil
}

func (r *accountRepository) PutAccountDispatchPolicy(ctx context.Context, policy *service.AccountDispatchPolicy, expectedVersion int64) (*service.AccountDispatchPolicy, error) {
	if policy == nil {
		return nil, service.ErrAccountNilInput
	}
	if policy.UserID <= 0 || policy.GroupID <= 0 || expectedVersion < 0 {
		return nil, fmt.Errorf("invalid account dispatch policy identifiers or version")
	}
	if policy.Mode != service.AccountDispatchModeAppend && policy.Mode != service.AccountDispatchModeReplace {
		return nil, fmt.Errorf("invalid account dispatch policy mode %q", policy.Mode)
	}
	if len(policy.Accounts) > 500 {
		return nil, fmt.Errorf("account dispatch policy supports at most 500 accounts")
	}
	grants := append([]service.AccountDispatchGrant(nil), policy.Accounts...)
	sort.Slice(grants, func(i, j int) bool { return grants[i].AccountID < grants[j].AccountID })
	accountIDs := make([]int64, 0, len(grants))
	for i, grant := range grants {
		if grant.AccountID <= 0 || (grant.Origin != service.AccountDispatchOriginMember && grant.Origin != service.AccountDispatchOriginExtra) {
			return nil, fmt.Errorf("invalid account dispatch grant")
		}
		if i > 0 && grants[i-1].AccountID == grant.AccountID {
			return nil, fmt.Errorf("duplicate account dispatch grant for account %d", grant.AccountID)
		}
		accountIDs = append(accountIDs, grant.AccountID)
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	// Keep a single lock order shared with membership mutations: selected accounts,
	// user, group, membership rows, policy row, then grant rows.
	type dispatchAccount struct {
		platform    string
		workspaceID int64
	}
	accounts := make(map[int64]dispatchAccount, len(accountIDs))
	if len(accountIDs) > 0 {
		rows, err := client.QueryContext(ctx, `
			SELECT id, platform, workspace_id FROM accounts
			WHERE id = ANY($1::bigint[]) AND deleted_at IS NULL
			ORDER BY id FOR NO KEY UPDATE
		`, pq.Array(accountIDs))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var account dispatchAccount
			if err := rows.Scan(&id, &account.platform, &account.workspaceID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			accounts[id] = account
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(accounts) != len(accountIDs) {
			return nil, fmt.Errorf("one or more account dispatch accounts were deleted")
		}
	}
	// Serialize with user/group soft deletion even when the selected list is empty.
	var liveUserID int64
	if err := scanSingleRow(ctx, client, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, []any{policy.UserID}, &liveUserID); err != nil {
		return nil, translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	var groupPlatform string
	if err := scanSingleRow(ctx, client, `SELECT platform FROM groups WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, []any{policy.GroupID}, &groupPlatform); err != nil {
		return nil, translatePersistenceError(err, service.ErrGroupNotFound, nil)
	}
	memberIDs := make(map[int64]struct{}, len(accountIDs))
	if len(accountIDs) > 0 {
		rows, err := client.QueryContext(ctx, `
			SELECT account_id FROM account_groups
			WHERE group_id = $1 AND account_id = ANY($2::bigint[])
			ORDER BY account_id FOR UPDATE
		`, policy.GroupID, pq.Array(accountIDs))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var accountID int64
			if err := rows.Scan(&accountID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			memberIDs[accountID] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	workspaceIDs := make([]int64, 0)
	workspaceSeen := make(map[int64]struct{})
	for _, grant := range grants {
		account := accounts[grant.AccountID]
		_, member := memberIDs[grant.AccountID]
		if grant.Origin == service.AccountDispatchOriginMember && !member || grant.Origin == service.AccountDispatchOriginExtra && member {
			return nil, fmt.Errorf("account %d group membership changed while updating dispatch policy", grant.AccountID)
		}
		if grant.Origin == service.AccountDispatchOriginExtra && (groupPlatform == service.PlatformComposite || account.platform != groupPlatform) {
			return nil, fmt.Errorf("account %d platform is incompatible with the target group", grant.AccountID)
		}
		if account.workspaceID <= 0 {
			return nil, fmt.Errorf("account %d has invalid workspace", grant.AccountID)
		}
		if grant.Origin == service.AccountDispatchOriginExtra && account.workspaceID != 1 {
			if _, exists := workspaceSeen[account.workspaceID]; !exists {
				workspaceSeen[account.workspaceID] = struct{}{}
				workspaceIDs = append(workspaceIDs, account.workspaceID)
			}
		}
	}
	if len(workspaceIDs) > 0 {
		sort.Slice(workspaceIDs, func(i, j int) bool { return workspaceIDs[i] < workspaceIDs[j] })
		rows, err := client.QueryContext(ctx, `
			SELECT wg.workspace_id FROM workspace_group_grants wg
			JOIN workspaces w ON w.id = wg.workspace_id
			  AND w.deleted_at IS NULL AND w.status = 'active'
			WHERE wg.workspace_id = ANY($1::bigint[]) AND wg.group_id = $2 AND wg.enabled = TRUE
			ORDER BY wg.workspace_id FOR SHARE OF wg, w
		`, pq.Array(workspaceIDs), policy.GroupID)
		if err != nil {
			return nil, err
		}
		grantedWorkspaces := make(map[int64]struct{}, len(workspaceIDs))
		for rows.Next() {
			var workspaceID int64
			if err := rows.Scan(&workspaceID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			grantedWorkspaces[workspaceID] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, grant := range grants {
			account := accounts[grant.AccountID]
			if grant.Origin == service.AccountDispatchOriginExtra && account.workspaceID != 1 {
				if _, ok := grantedWorkspaces[account.workspaceID]; !ok {
					return nil, fmt.Errorf("account %d workspace no longer has this group", grant.AccountID)
				}
			}
		}
	}
	if _, err := client.ExecContext(ctx, `
		INSERT INTO user_group_account_policies (user_id, group_id, mode, version)
		VALUES ($1, $2, $3, 0) ON CONFLICT (user_id, group_id) DO NOTHING
	`, policy.UserID, policy.GroupID, policy.Mode); err != nil {
		return nil, err
	}
	rows, err := client.QueryContext(ctx, `
		SELECT version FROM user_group_account_policies
		WHERE user_id = $1 AND group_id = $2 FOR UPDATE
	`, policy.UserID, policy.GroupID)
	if err != nil {
		return nil, err
	}
	var version int64
	if !rows.Next() {
		_ = rows.Close()
		return nil, fmt.Errorf("account dispatch policy disappeared while updating")
	}
	if err := rows.Scan(&version); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if version != expectedVersion {
		return nil, service.ErrAccountDispatchPolicyVersionConflict(version)
	}
	if _, err := client.ExecContext(ctx, `
		UPDATE user_group_account_policies
		SET mode = $3, version = nextval('user_group_account_policy_version_seq'), updated_at = NOW()
		WHERE user_id = $1 AND group_id = $2
	`, policy.UserID, policy.GroupID, policy.Mode); err != nil {
		return nil, err
	}
	if _, err := client.ExecContext(ctx, `
		DELETE FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2
	`, policy.UserID, policy.GroupID); err != nil {
		return nil, err
	}
	if len(grants) > 0 {
		grantOrigins := make([]string, len(grants))
		for i, grant := range grants {
			grantOrigins[i] = grant.Origin
		}
		if _, err := client.ExecContext(ctx, `
			INSERT INTO user_group_account_grants (user_id, group_id, account_id, origin)
			SELECT $1, $2, g.account_id, g.origin
			FROM unnest($3::bigint[], $4::text[]) AS g(account_id, origin)
		`, policy.UserID, policy.GroupID, pq.Array(accountIDs), pq.Array(grantOrigins)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetAccountDispatchPolicy(ctx, policy.UserID, policy.GroupID)
}

func (r *accountRepository) DeleteAccountDispatchPolicy(ctx context.Context, userID, groupID, expectedVersion int64) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	var liveID int64
	if err := scanSingleRow(ctx, client, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, []any{userID}, &liveID); err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	if err := scanSingleRow(ctx, client, `SELECT id FROM groups WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, []any{groupID}, &liveID); err != nil {
		return translatePersistenceError(err, service.ErrGroupNotFound, nil)
	}
	rows, err := client.QueryContext(ctx, `
		SELECT version FROM user_group_account_policies
		WHERE user_id = $1 AND group_id = $2 FOR UPDATE
	`, userID, groupID)
	if err != nil {
		return err
	}
	var version int64
	if !rows.Next() {
		_ = rows.Close()
		return service.ErrAccountDispatchPolicyVersionConflict(0)
	}
	if err := rows.Scan(&version); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if version != expectedVersion {
		return service.ErrAccountDispatchPolicyVersionConflict(version)
	}
	if _, err := client.ExecContext(ctx, `
		DELETE FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2
	`, userID, groupID); err != nil {
		return err
	}
	return tx.Commit()
}

var _ service.AccountDispatchPolicyWriter = (*accountRepository)(nil)

func deleteMemberAccountDispatchGrants(ctx context.Context, client *dbent.Client, accountID int64, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	return deleteAccountDispatchGrants(ctx, client, "g.account_id = $1 AND g.origin = 'member' AND g.group_id = ANY($2::bigint[])", accountID, pq.Array(groupIDs))
}

func lockMemberAccountDispatchPolicyUsers(ctx context.Context, client *dbent.Client, groupID int64, accountIDs []int64) error {
	ids := sortedUniqueIDs(accountIDs)
	if len(ids) == 0 {
		return nil
	}
	rows, err := client.QueryContext(ctx, `
		SELECT u.id
		FROM users u
		WHERE EXISTS (
			SELECT 1
			FROM user_group_account_policies p
			JOIN user_group_account_grants g ON g.user_id = p.user_id AND g.group_id = p.group_id
			WHERE p.user_id = u.id AND p.group_id = $1
			  AND g.origin = 'member' AND g.account_id = ANY($2::bigint[])
		)
		ORDER BY u.id
		FOR NO KEY UPDATE OF u
	`, groupID, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Structural revocation is a policy edit too: advance the surviving parent so a
// stale administrator cannot recreate a revoked grant with an old If-Match.
// predicate is supplied only by the repository's fixed internal call sites.
func deleteAccountDispatchGrants(ctx context.Context, client *dbent.Client, predicate string, args ...any) error {
	// Lock policy parents first in deterministic order. Writers take these rows
	// before changing grants, while callers already hold the affected account and
	// membership rows, preventing the opposite lock order from revocation paths.
	rows, err := client.QueryContext(ctx, `
		SELECT p.user_id, p.group_id
		FROM user_group_account_policies p
		WHERE EXISTS (
			SELECT 1 FROM user_group_account_grants g
			WHERE g.user_id = p.user_id AND g.group_id = p.group_id AND `+predicate+`
		)
		ORDER BY p.user_id, p.group_id
		FOR UPDATE OF p
	`, args...)
	if err != nil {
		return err
	}
	locked := 0
	for rows.Next() {
		var userID, groupID int64
		if err := rows.Scan(&userID, &groupID); err != nil {
			_ = rows.Close()
			return err
		}
		locked++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if locked == 0 {
		return nil
	}
	_, err = client.ExecContext(ctx, `
		WITH revoked AS (
			DELETE FROM user_group_account_grants AS g WHERE `+predicate+`
			RETURNING g.user_id, g.group_id
		), affected AS (
			SELECT DISTINCT user_id, group_id FROM revoked
		)
		UPDATE user_group_account_policies p
		SET version = nextval('user_group_account_policy_version_seq'), updated_at = NOW()
		FROM affected r
		WHERE p.user_id = r.user_id AND p.group_id = r.group_id
	`, args...)
	return err
}
