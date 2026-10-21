package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type adminUserAdjustmentRepository struct {
	client *dbent.Client
}

func NewAdminUserAdjustmentRepository(client *dbent.Client) service.AdminUserAdjustmentRepository {
	return &adminUserAdjustmentRepository{client: client}
}

func (r *adminUserAdjustmentRepository) CreateBatch(ctx context.Context, rows []service.AdminUserAdjustmentWrite) error {
	if len(rows) == 0 {
		return nil
	}
	client := clientFromContext(ctx, r.client)
	const insertSQL = `
INSERT INTO admin_user_adjustments (
    action_id, kind, operation, requested_value, delta, before_value, after_value,
    user_id, user_email, user_name, operator_user_id, operator_email, operator_name,
    notes, client_ip, auth_method, request_id, source, legacy_redeem_code_id, created_at, redeem_code_id
) VALUES (
    $1, $2, $3, NULLIF($4, '')::numeric, $5::numeric, NULLIF($6, '')::numeric, NULLIF($7, '')::numeric,
    $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21
)`
	for i := range rows {
		row := rows[i]
		createdAt := row.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		if _, err := client.ExecContext(ctx, insertSQL,
			row.ActionID, row.Kind, row.Operation, stringOrEmpty(row.RequestedValue), row.Delta,
			stringOrEmpty(row.BeforeValue), stringOrEmpty(row.AfterValue), row.UserID,
			row.UserEmail, row.UserName, row.OperatorUserID, row.OperatorEmail, row.OperatorName,
			row.Notes, row.ClientIP, row.AuthMethod, row.RequestID, row.Source,
			row.LegacyRedeemCodeID, createdAt, row.RedeemCodeID,
		); err != nil {
			return err
		}
	}
	return nil
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (r *adminUserAdjustmentRepository) CountByActionID(ctx context.Context, actionID uuid.UUID) (count int, err error) {
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx,
		`SELECT COUNT(*) FROM admin_user_adjustments WHERE action_id = $1`, actionID)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("admin user adjustment action count returned no row")
	}
	if err := rows.Scan(&count); err != nil {
		return 0, err
	}
	return count, rows.Err()
}

func (r *adminUserAdjustmentRepository) LockAction(ctx context.Context, actionID uuid.UUID) error {
	_, err := clientFromContext(ctx, r.client).ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, actionID)
	return err
}

func adjustmentWhere(filter service.AdminUserAdjustmentFilter) (string, []any) {
	clauses := []string{"TRUE"}
	args := make([]any, 0, 7)
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.Kind != "" {
		add("kind = $%d", filter.Kind)
	}
	if filter.Operation != "" {
		add("operation = $%d", filter.Operation)
	}
	switch filter.Direction {
	case "increase":
		clauses = append(clauses, "delta > 0")
	case "decrease":
		clauses = append(clauses, "delta < 0")
	}
	if filter.Keyword != "" {
		pattern := "%" + escapeLikePattern(filter.Keyword) + "%"
		args = append(args, pattern)
		n := len(args)
		clauses = append(clauses, fmt.Sprintf("(user_id::text ILIKE $%d ESCAPE '\\' OR COALESCE(user_email, '') ILIKE $%d ESCAPE '\\' OR COALESCE(user_name, '') ILIKE $%d ESCAPE '\\')", n, n, n))
	}
	if filter.Operator != "" {
		pattern := "%" + escapeLikePattern(filter.Operator) + "%"
		args = append(args, pattern)
		n := len(args)
		clauses = append(clauses, fmt.Sprintf("(operator_user_id::text ILIKE $%d ESCAPE '\\' OR COALESCE(operator_email, '') ILIKE $%d ESCAPE '\\' OR COALESCE(operator_name, '') ILIKE $%d ESCAPE '\\')", n, n, n))
	}
	if filter.RequestID != "" {
		add("request_id = $%d", filter.RequestID)
	}
	if filter.ActionID != nil {
		add("action_id = $%d", *filter.ActionID)
	}
	if filter.UserID != nil {
		add("user_id = $%d", *filter.UserID)
	}
	if filter.StartTime != nil {
		add("created_at >= $%d", *filter.StartTime)
	}
	if filter.EndTime != nil {
		add("created_at < $%d", *filter.EndTime)
	}
	return strings.Join(clauses, " AND "), args
}

const adjustmentSelect = `
SELECT id, action_id::text, kind, operation,
       requested_value::text, delta::text, before_value::text, after_value::text,
       user_id, user_email, user_name, operator_user_id, operator_email, operator_name, notes,
       client_ip, auth_method, request_id, source, legacy_redeem_code_id, created_at, redeem_code_id
FROM admin_user_adjustments`

func scanAdminUserAdjustment(scanner interface{ Scan(...any) error }) (service.AdminUserAdjustment, error) {
	var row service.AdminUserAdjustment
	var requested, before, after sql.NullString
	var userEmail, userName, operatorEmail, operatorName, notes, clientIP, authMethod, requestID sql.NullString
	var userID, operatorID, legacyID, redeemCodeID sql.NullInt64
	err := scanner.Scan(
		&row.ID, &row.ActionID, &row.Kind, &row.Operation,
		&requested, &row.Delta, &before, &after,
		&userID, &userEmail, &userName, &operatorID, &operatorEmail, &operatorName, &notes,
		&clientIP, &authMethod, &requestID, &row.Source, &legacyID, &row.CreatedAt, &redeemCodeID,
	)
	if err != nil {
		return row, err
	}
	row.RequestedValue = nullableString(requested)
	row.BeforeValue = nullableString(before)
	row.AfterValue = nullableString(after)
	row.UserEmail = nullableString(userEmail)
	row.UserName = nullableString(userName)
	row.OperatorEmail = nullableString(operatorEmail)
	row.OperatorName = nullableString(operatorName)
	row.Notes = nullableString(notes)
	row.ClientIP = nullableString(clientIP)
	row.AuthMethod = nullableString(authMethod)
	row.RequestID = nullableString(requestID)
	if userID.Valid {
		row.UserID = &userID.Int64
	}
	if operatorID.Valid {
		row.OperatorUserID = &operatorID.Int64
	}
	if legacyID.Valid {
		row.LegacyRedeemCodeID = &legacyID.Int64
	}
	if redeemCodeID.Valid {
		row.RedeemCodeID = &redeemCodeID.Int64
	}
	return row, nil
}

// linksByRedeemCodeIDsSQL finds the ledger entry behind each compatibility redeem code of one user. Entries written
// since E10 carry redeem_code_id; entries migrated by 223 carry legacy_redeem_code_id; entries written in between
// have neither and are matched on what the writer stored identically in both tables: user, kind, created_at = used_at
// and delta = value. An exact link wins over the fallback match.
const linksByRedeemCodeIDsSQL = `
SELECT rc.id, a.id, a.before_value::text, a.after_value::text, a.operator_email
FROM redeem_codes rc
JOIN LATERAL (
    SELECT adj.id, adj.before_value, adj.after_value, adj.operator_email,
           CASE WHEN adj.redeem_code_id = rc.id OR adj.legacy_redeem_code_id = rc.id THEN 0 ELSE 1 END AS link_rank
    FROM admin_user_adjustments adj
    WHERE adj.user_id = rc.used_by
      AND adj.kind = CASE rc.type WHEN 'admin_balance' THEN 'balance' ELSE 'concurrency' END
      AND (
          adj.redeem_code_id = rc.id
          OR adj.legacy_redeem_code_id = rc.id
          OR (
              adj.redeem_code_id IS NULL AND adj.legacy_redeem_code_id IS NULL
              AND adj.created_at = rc.used_at AND adj.delta = rc.value
          )
      )
    ORDER BY link_rank, adj.id
    LIMIT 1
) a ON TRUE
WHERE rc.id = ANY($2)
  AND rc.used_by = $1
  AND rc.type IN ('admin_balance', 'admin_concurrency')`

// LinksByRedeemCodeIDs returns the ledger entry behind each of the given admin_balance / admin_concurrency redeem
// codes of the user, keyed by redeem code id. Codes without a matching entry are absent.
func (r *adminUserAdjustmentRepository) LinksByRedeemCodeIDs(ctx context.Context, userID int64, redeemCodeIDs []int64) (map[int64]service.AdminAdjustmentLink, error) {
	links := make(map[int64]service.AdminAdjustmentLink, len(redeemCodeIDs))
	if userID <= 0 || len(redeemCodeIDs) == 0 {
		return links, nil
	}
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx, linksByRedeemCodeIDsSQL, userID, pq.Array(redeemCodeIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var codeID int64
		var link service.AdminAdjustmentLink
		var before, after, operatorEmail sql.NullString
		if err := rows.Scan(&codeID, &link.AdjustmentID, &before, &after, &operatorEmail); err != nil {
			return nil, err
		}
		link.BeforeValue = nullableString(before)
		link.AfterValue = nullableString(after)
		link.OperatorEmail = nullableString(operatorEmail)
		links[codeID] = link
	}
	return links, rows.Err()
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func (r *adminUserAdjustmentRepository) List(ctx context.Context, filter service.AdminUserAdjustmentFilter, page, pageSize int) ([]service.AdminUserAdjustment, service.AdminUserAdjustmentSummary, error) {
	if dbent.TxFromContext(ctx) != nil {
		return r.list(ctx, clientFromContext(ctx, r.client), filter, page, pageSize)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, service.AdminUserAdjustmentSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	txClient := tx.Client()
	if _, err := txClient.ExecContext(txCtx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return nil, service.AdminUserAdjustmentSummary{}, err
	}
	items, summary, err := r.list(txCtx, txClient, filter, page, pageSize)
	if err != nil {
		return nil, summary, err
	}
	if err := tx.Commit(); err != nil {
		return nil, summary, err
	}
	return items, summary, nil
}

func (r *adminUserAdjustmentRepository) list(ctx context.Context, client *dbent.Client, filter service.AdminUserAdjustmentFilter, page, pageSize int) ([]service.AdminUserAdjustment, service.AdminUserAdjustmentSummary, error) {
	where, args := adjustmentWhere(filter)
	summaryQuery := fmt.Sprintf(`
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN kind = 'balance' AND delta > 0 THEN delta ELSE 0 END), 0)::text,
       COALESCE(SUM(CASE WHEN kind = 'balance' AND delta < 0 THEN -delta ELSE 0 END), 0)::text,
       COALESCE(SUM(CASE WHEN kind = 'balance' THEN delta ELSE 0 END), 0)::text,
       COALESCE(SUM(CASE WHEN kind = 'concurrency' AND delta > 0 THEN delta ELSE 0 END), 0)::text,
       COALESCE(SUM(CASE WHEN kind = 'concurrency' AND delta < 0 THEN -delta ELSE 0 END), 0)::text,
       COALESCE(SUM(CASE WHEN kind = 'concurrency' THEN delta ELSE 0 END), 0)::text
	FROM admin_user_adjustments WHERE %s`, where)
	var summary service.AdminUserAdjustmentSummary
	summaryRows, err := client.QueryContext(ctx, summaryQuery, args...)
	if err != nil {
		return nil, summary, err
	}
	if !summaryRows.Next() {
		if err := summaryRows.Err(); err != nil {
			_ = summaryRows.Close()
			return nil, summary, err
		}
		_ = summaryRows.Close()
		return nil, summary, fmt.Errorf("admin user adjustment summary returned no row")
	}
	err = summaryRows.Scan(
		&summary.RecordCount,
		&summary.BalanceIncrease,
		&summary.BalanceDecrease,
		&summary.BalanceNet,
		&summary.ConcurrencyIncrease,
		&summary.ConcurrencyDecrease,
		&summary.ConcurrencyNet,
	)
	if err != nil {
		_ = summaryRows.Close()
		return nil, summary, err
	}
	if err := summaryRows.Err(); err != nil {
		_ = summaryRows.Close()
		return nil, summary, err
	}
	if err := summaryRows.Close(); err != nil {
		return nil, summary, err
	}

	pageArgs := append([]any(nil), args...)
	pageArgs = append(pageArgs, pageSize, (page-1)*pageSize)
	query := fmt.Sprintf("%s WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", adjustmentSelect, where, len(pageArgs)-1, len(pageArgs))
	rows, err := client.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return nil, summary, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.AdminUserAdjustment, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanAdminUserAdjustment(rows)
		if scanErr != nil {
			return nil, summary, scanErr
		}
		items = append(items, item)
	}
	return items, summary, rows.Err()
}

func (r *adminUserAdjustmentRepository) Stream(ctx context.Context, filter service.AdminUserAdjustmentFilter, consume func(service.AdminUserAdjustment) error) error {
	where, args := adjustmentWhere(filter)
	query := fmt.Sprintf("%s WHERE %s ORDER BY created_at DESC, id DESC", adjustmentSelect, where)
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		item, scanErr := scanAdminUserAdjustment(rows)
		if scanErr != nil {
			return scanErr
		}
		if err := consume(item); err != nil {
			return err
		}
	}
	return rows.Err()
}
