package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
	rows, err := r.client.QueryContext(ctx, fmt.Sprintf(accountDispatchPolicySelect, "p.group_id = $1"), groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAccountDispatchPolicies(rows)
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
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	// Serialize with user/group soft deletion even when the selected list is empty.
	var liveUserID, liveGroupID int64
	if err := scanSingleRow(ctx, client, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, []any{policy.UserID}, &liveUserID); err != nil {
		return nil, translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	if err := scanSingleRow(ctx, client, `SELECT id FROM groups WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, []any{policy.GroupID}, &liveGroupID); err != nil {
		return nil, translatePersistenceError(err, service.ErrGroupNotFound, nil)
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
		return nil, errors.New("account dispatch policy disappeared while updating")
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
	for _, grant := range policy.Accounts {
		rows, err := client.QueryContext(ctx, `
			SELECT a.platform, a.workspace_id, grp.platform
			FROM accounts a CROSS JOIN groups grp
			WHERE a.id = $1 AND grp.id = $2
			  AND a.deleted_at IS NULL AND grp.deleted_at IS NULL
			FOR NO KEY UPDATE OF a, grp
		`, grant.AccountID, policy.GroupID)
		if err != nil {
			return nil, err
		}
		active := rows.Next()
		var accountPlatform, groupPlatform string
		var workspaceID int64
		if active {
			if err := rows.Scan(&accountPlatform, &workspaceID, &groupPlatform); err != nil {
				_ = rows.Close()
				return nil, err
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if !active {
			return nil, fmt.Errorf("account %d was deleted while updating dispatch policy", grant.AccountID)
		}
		rows, err = client.QueryContext(ctx, `SELECT 1 FROM account_groups WHERE account_id = $1 AND group_id = $2 FOR UPDATE`, grant.AccountID, policy.GroupID)
		if err != nil {
			return nil, err
		}
		member := rows.Next()
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if grant.Origin == service.AccountDispatchOriginMember && !member || grant.Origin == service.AccountDispatchOriginExtra && member {
			return nil, fmt.Errorf("account %d group membership changed while updating dispatch policy", grant.AccountID)
		}
		if grant.Origin == service.AccountDispatchOriginExtra && (groupPlatform == service.PlatformComposite || accountPlatform != groupPlatform) {
			return nil, fmt.Errorf("account %d platform changed while updating dispatch policy", grant.AccountID)
		}
		if workspaceID <= 0 {
			return nil, fmt.Errorf("account %d has invalid workspace", grant.AccountID)
		}
		if grant.Origin == service.AccountDispatchOriginExtra && workspaceID != 1 {
			rows, err := client.QueryContext(ctx, `
				SELECT 1 FROM workspace_group_grants wg
				JOIN workspaces w ON w.id = wg.workspace_id AND w.deleted_at IS NULL AND w.status = 'active'
				WHERE wg.workspace_id = $1 AND wg.group_id = $2 AND wg.enabled = TRUE
				FOR SHARE OF wg, w
			`, workspaceID, policy.GroupID)
			if err != nil {
				return nil, err
			}
			granted := rows.Next()
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
			if !granted {
				return nil, fmt.Errorf("account %d workspace no longer has this group", grant.AccountID)
			}
		}
		if _, err := client.ExecContext(ctx, `
			INSERT INTO user_group_account_grants (user_id, group_id, account_id, origin)
			VALUES ($1, $2, $3, $4)
		`, policy.UserID, policy.GroupID, grant.AccountID, grant.Origin); err != nil {
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
	return deleteAccountDispatchGrants(ctx, client, "account_id = $1 AND origin = 'member' AND group_id = ANY($2::bigint[])", accountID, pq.Array(groupIDs))
}

// Structural revocation is a policy edit too: advance the surviving parent so a
// stale administrator cannot recreate a revoked grant with an old If-Match.
// predicate is supplied only by the repository's fixed internal call sites.
func deleteAccountDispatchGrants(ctx context.Context, client *dbent.Client, predicate string, args ...any) error {
	_, err := client.ExecContext(ctx, `
		WITH revoked AS (
			DELETE FROM user_group_account_grants WHERE `+predicate+`
			RETURNING user_id, group_id
		)
		UPDATE user_group_account_policies p
		SET version = nextval('user_group_account_policy_version_seq'), updated_at = NOW()
		WHERE EXISTS (SELECT 1 FROM revoked r WHERE r.user_id = p.user_id AND r.group_id = p.group_id)
	`, args...)
	return err
}
