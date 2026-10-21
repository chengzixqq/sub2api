package repository

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	entsql "entgo.io/ent/dialect/sql"
)

type redeemCodeRepository struct {
	client *dbent.Client
}

func NewRedeemCodeRepository(client *dbent.Client) service.RedeemCodeRepository {
	return &redeemCodeRepository{client: client}
}

func (r *redeemCodeRepository) Create(ctx context.Context, code *service.RedeemCode) error {
	client := clientFromContext(ctx, r.client)
	created, err := client.RedeemCode.Create().
		SetCode(code.Code).
		SetType(code.Type).
		SetValue(code.Value).
		SetStatus(code.Status).
		SetNotes(code.Notes).
		SetValidityDays(code.ValidityDays).
		SetNillableExpiresAt(code.ExpiresAt).
		SetNillableUsedBy(code.UsedBy).
		SetNillableUsedAt(code.UsedAt).
		SetNillableGroupID(code.GroupID).
		SetNillableBatchID(code.BatchID).
		SetNillableCreatedBy(code.CreatedBy).
		Save(ctx)
	if err == nil {
		code.ID = created.ID
		code.CreatedAt = created.CreatedAt
	}
	return err
}

func (r *redeemCodeRepository) CreateBatch(ctx context.Context, codes []service.RedeemCode) error {
	if len(codes) == 0 {
		return nil
	}

	client := clientFromContext(ctx, r.client)
	for _, code := range codes {
		if code.ValueExact != "" {
			return r.createBatchExact(ctx, client, codes)
		}
	}
	const createBatchSize = 1000
	builders := make([]*dbent.RedeemCodeCreate, 0, min(len(codes), createBatchSize))
	for i := range codes {
		c := &codes[i]
		b := client.RedeemCode.Create().
			SetCode(c.Code).
			SetType(c.Type).
			SetValue(c.Value).
			SetStatus(c.Status).
			SetNotes(c.Notes).
			SetValidityDays(c.ValidityDays).
			SetNillableExpiresAt(c.ExpiresAt).
			SetNillableUsedBy(c.UsedBy).
			SetNillableUsedAt(c.UsedAt).
			SetNillableGroupID(c.GroupID)
		builders = append(builders, b)
		if len(builders) == createBatchSize {
			if err := client.RedeemCode.CreateBulk(builders...).Exec(ctx); err != nil {
				return err
			}
			builders = builders[:0]
		}
	}

	if len(builders) == 0 {
		return nil
	}
	return client.RedeemCode.CreateBulk(builders...).Exec(ctx)
}

// createBatchExact inserts codes with exact DECIMAL values and writes the new id and created_at back into codes, so
// the admin adjustment ledger can link each entry to its compatibility row (E10).
func (r *redeemCodeRepository) createBatchExact(ctx context.Context, client *dbent.Client, codes []service.RedeemCode) error {
	const insertSQL = `
INSERT INTO redeem_codes (
    code, type, value, status, used_by, used_at, notes, created_at, expires_at, group_id, validity_days
) VALUES ($1, $2, $3::numeric, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11)
RETURNING id, created_at`
	for i := range codes {
		code := &codes[i]
		value := code.ValueExact
		if value == "" {
			value = decimal.NewFromFloat(code.Value).Round(8).StringFixed(8)
		}
		createdAt := code.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		id, storedAt, err := insertRedeemCodeReturningID(ctx, client, insertSQL,
			code.Code, code.Type, value, code.Status, code.UsedBy, code.UsedAt,
			code.Notes, createdAt, code.ExpiresAt, code.GroupID, code.ValidityDays,
		)
		if err != nil {
			return err
		}
		code.ID = id
		code.CreatedAt = storedAt
	}
	return nil
}

func insertRedeemCodeReturningID(ctx context.Context, client *dbent.Client, query string, args ...any) (id int64, createdAt time.Time, err error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, time.Time{}, err
		}
		return 0, time.Time{}, errors.New("insert redeem code returned no row")
	}
	if err := rows.Scan(&id, &createdAt); err != nil {
		return 0, time.Time{}, err
	}
	return id, createdAt, rows.Err()
}

func (r *redeemCodeRepository) GetByID(ctx context.Context, id int64) (*service.RedeemCode, error) {
	m, err := r.client.RedeemCode.Query().
		Where(redeemcode.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrRedeemCodeNotFound
		}
		return nil, err
	}
	return redeemCodeEntityToService(m), nil
}

func (r *redeemCodeRepository) GetByCode(ctx context.Context, code string) (*service.RedeemCode, error) {
	m, err := r.client.RedeemCode.Query().
		Where(redeemcode.CodeEQ(code)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrRedeemCodeNotFound
		}
		return nil, err
	}
	return redeemCodeEntityToService(m), nil
}

func (r *redeemCodeRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.client.RedeemCode.Delete().Where(redeemcode.IDEQ(id)).Exec(ctx)
	return err
}

// redeemCodeDeleteChunkSize bounds the ids per statement (each id is one bind parameter).
const redeemCodeDeleteChunkSize = 1000

// redeemCodeDeletable matches the codes an admin may delete: those that never reached a user's balance history.
// used_by is checked besides status because a redeemed code keeps used_by when its status is changed afterwards.
func redeemCodeDeletable(extra ...predicate.RedeemCode) []predicate.RedeemCode {
	return append([]predicate.RedeemCode{
		redeemcode.StatusNEQ(service.StatusUsed),
		redeemcode.UsedByIsNil(),
		redeemcode.TypeNotIn(service.AdjustmentTypeAdminBalance, service.AdjustmentTypeAdminConcurrency),
	}, extra...)
}

// DeleteUnlessUsed checks the condition inside the DELETE (no read-then-delete race); only when nothing was deleted
// does it look the code up to tell a protected code (409) from a missing one (404).
func (r *redeemCodeRepository) DeleteUnlessUsed(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	affected, err := client.RedeemCode.Delete().Where(redeemCodeDeletable(redeemcode.IDEQ(id))...).Exec(ctx)
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	exists, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return service.ErrRedeemCodeDeleteUsed
	}
	return service.ErrRedeemCodeNotFound
}

func (r *redeemCodeRepository) BatchDeleteUnlessUsed(ctx context.Context, ids []int64) (int64, []int64, error) {
	uniqueIDs := uniquePositiveInt64s(ids)
	if len(uniqueIDs) == 0 {
		return 0, []int64{}, nil
	}
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return batchDeleteRedeemCodesUnlessUsed(ctx, tx.Client(), uniqueIDs)
	}

	tx, err := r.client.Tx(ctx)
	if errors.Is(err, dbent.ErrTxStarted) {
		return batchDeleteRedeemCodesUnlessUsed(ctx, r.client, uniqueIDs)
	}
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	deleted, skipped, err := batchDeleteRedeemCodesUnlessUsed(dbent.NewTxContext(ctx, tx), tx.Client(), uniqueIDs)
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return deleted, skipped, nil
}

// batchDeleteRedeemCodesUnlessUsed deletes chunk by chunk inside the caller's transaction. Whatever of a chunk is still
// there after its DELETE was protected: ids are never reused, so a missing id cannot reappear.
func batchDeleteRedeemCodesUnlessUsed(ctx context.Context, client *dbent.Client, ids []int64) (int64, []int64, error) {
	var deleted int64
	skipped := []int64{}
	for start := 0; start < len(ids); start += redeemCodeDeleteChunkSize {
		chunk := ids[start:min(start+redeemCodeDeleteChunkSize, len(ids))]
		affected, err := client.RedeemCode.Delete().Where(redeemCodeDeletable(redeemcode.IDIn(chunk...))...).Exec(ctx)
		if err != nil {
			return 0, nil, err
		}
		deleted += int64(affected)
		remaining, err := client.RedeemCode.Query().Where(redeemcode.IDIn(chunk...)).IDs(ctx)
		if err != nil {
			return 0, nil, err
		}
		skipped = append(skipped, remaining...)
	}
	slices.Sort(skipped)
	return deleted, skipped, nil
}

func (r *redeemCodeRepository) List(ctx context.Context, params pagination.PaginationParams) ([]service.RedeemCode, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, service.RedeemCodeListFilter{})
}

// redeemCodeFilterPredicates turns every list filter except status (see redeemCodeStatusPredicate) into conditions, so
// the list, export and stats select the same codes.
func redeemCodeFilterPredicates(filter service.RedeemCodeListFilter) []predicate.RedeemCode {
	var preds []predicate.RedeemCode
	if filter.Type != "" {
		preds = append(preds, redeemcode.TypeEQ(filter.Type))
	}
	if len(filter.Types) > 0 {
		preds = append(preds, redeemcode.TypeIn(filter.Types...))
	}
	if len(filter.ExcludeTypes) > 0 {
		preds = append(preds, redeemcode.TypeNotIn(filter.ExcludeTypes...))
	}
	if filter.BatchID != nil {
		preds = append(preds, redeemcode.BatchIDEQ(*filter.BatchID))
	}
	if filter.CreatedBy != nil {
		preds = append(preds, redeemcode.CreatedByEQ(*filter.CreatedBy))
	}
	if filter.CreatedFrom != nil {
		preds = append(preds, redeemcode.CreatedAtGTE(*filter.CreatedFrom))
	}
	if filter.CreatedTo != nil {
		preds = append(preds, redeemcode.CreatedAtLT(*filter.CreatedTo))
	}
	if filter.Notes != "" {
		preds = append(preds, redeemcode.NotesContainsFold(filter.Notes))
	}
	if filter.Search != "" {
		preds = append(preds, redeemcode.Or(
			redeemcode.CodeContainsFold(filter.Search),
			redeemcode.HasUserWith(user.EmailContainsFold(filter.Search)),
		))
	}
	return preds
}

// redeemCodeLapsed matches unused codes whose expires_at has passed: the list shows them as expired, not unused.
func redeemCodeLapsed(now time.Time) predicate.RedeemCode {
	return redeemcode.And(
		redeemcode.StatusEQ(service.StatusUnused),
		redeemcode.ExpiresAtNotNil(),
		redeemcode.ExpiresAtLTE(now),
	)
}

// redeemCodeStatusPredicate matches a status as the list shows it: expired includes lapsed unused codes, unused
// excludes them, any other status matches the column.
func redeemCodeStatusPredicate(status string, now time.Time) predicate.RedeemCode {
	switch status {
	case service.StatusExpired:
		return redeemcode.Or(redeemcode.StatusEQ(service.StatusExpired), redeemCodeLapsed(now))
	case service.StatusUnused:
		return redeemcode.And(
			redeemcode.StatusEQ(service.StatusUnused),
			redeemcode.Or(redeemcode.ExpiresAtIsNil(), redeemcode.ExpiresAtGT(now)),
		)
	default:
		return redeemcode.StatusEQ(status)
	}
}

func (r *redeemCodeRepository) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filter service.RedeemCodeListFilter) ([]service.RedeemCode, *pagination.PaginationResult, error) {
	q := r.client.RedeemCode.Query().Where(redeemCodeFilterPredicates(filter)...)
	if filter.Status != "" {
		q = q.Where(redeemCodeStatusPredicate(filter.Status, time.Now()))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	codesQuery := q.
		WithUser().
		WithGroup().
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range redeemCodeListOrder(params) {
		codesQuery = codesQuery.Order(order)
	}

	codes, err := codesQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outCodes := redeemCodeEntitiesToService(codes)

	return outCodes, paginationResultFromTotal(int64(total), params), nil
}

func redeemCodeListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	var field string
	switch sortBy {
	case "type":
		field = redeemcode.FieldType
	case "value":
		field = redeemcode.FieldValue
	case "status":
		field = redeemcode.FieldStatus
	case "used_at":
		field = redeemcode.FieldUsedAt
	case "created_at":
		field = redeemcode.FieldCreatedAt
	case "expires_at":
		field = redeemcode.FieldExpiresAt
	case "code":
		field = redeemcode.FieldCode
	default:
		field = redeemcode.FieldID
	}

	if sortOrder == pagination.SortOrderAsc {
		return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(redeemcode.FieldID)}
	}
	return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(redeemcode.FieldID)}
}

func (r *redeemCodeRepository) Update(ctx context.Context, code *service.RedeemCode) error {
	up := r.client.RedeemCode.UpdateOneID(code.ID).
		SetCode(code.Code).
		SetType(code.Type).
		SetValue(code.Value).
		SetStatus(code.Status).
		SetNotes(code.Notes).
		SetValidityDays(code.ValidityDays)

	if code.UsedBy != nil {
		up.SetUsedBy(*code.UsedBy)
	} else {
		up.ClearUsedBy()
	}
	if code.UsedAt != nil {
		up.SetUsedAt(*code.UsedAt)
	} else {
		up.ClearUsedAt()
	}
	if code.GroupID != nil {
		up.SetGroupID(*code.GroupID)
	} else {
		up.ClearGroupID()
	}
	if code.ExpiresAt != nil {
		up.SetExpiresAt(*code.ExpiresAt)
	} else {
		up.ClearExpiresAt()
	}

	updated, err := up.Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrRedeemCodeNotFound
		}
		return err
	}
	code.CreatedAt = updated.CreatedAt
	return nil
}

func (r *redeemCodeRepository) BatchUpdate(ctx context.Context, ids []int64, fields service.RedeemCodeBatchUpdateFields) (int64, error) {
	uniqueIDs := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return 0, nil
	}

	if tx := dbent.TxFromContext(ctx); tx != nil {
		return r.batchUpdate(ctx, tx.Client(), uniqueIDs, fields)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	defer func() { _ = tx.Rollback() }()

	updated, err := r.batchUpdate(txCtx, tx.Client(), uniqueIDs, fields)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

func (r *redeemCodeRepository) batchUpdate(ctx context.Context, client *dbent.Client, ids []int64, fields service.RedeemCodeBatchUpdateFields) (int64, error) {
	existing, err := client.RedeemCode.Query().
		Where(redeemcode.IDIn(ids...)).
		All(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) != len(ids) {
		return 0, service.ErrRedeemCodeNotFound
	}
	if fields.TouchesUsedSensitiveFields() {
		for _, code := range existing {
			// used_by stays set after a redeemed code's status changes (e.g. expired), so it counts as used too:
			// otherwise the batch could turn it back into unused and let another user redeem it again.
			if code.Status == service.StatusUsed || code.UsedBy != nil {
				return 0, service.ErrRedeemCodeUsed
			}
		}
	}

	up := client.RedeemCode.Update().Where(redeemcode.IDIn(ids...))
	if fields.Status != nil {
		up.SetStatus(*fields.Status)
	}
	if fields.Notes != nil {
		up.SetNotes(*fields.Notes)
	}
	if fields.ExpiresAt.Set {
		if fields.ExpiresAt.Value != nil {
			up.SetExpiresAt(*fields.ExpiresAt.Value)
		} else {
			up.ClearExpiresAt()
		}
	}
	if fields.GroupID.Set {
		if fields.GroupID.Value != nil {
			up.SetGroupID(*fields.GroupID.Value)
		} else {
			up.ClearGroupID()
		}
	}

	affected, err := up.Save(ctx)
	if err != nil {
		return 0, err
	}
	if affected != len(ids) {
		return 0, service.ErrRedeemCodeNotFound
	}
	return int64(affected), nil
}

// LockBatch serialises generate calls for one batch id within the caller's transaction (released on commit).
func (r *redeemCodeRepository) LockBatch(ctx context.Context, batchID uuid.UUID) error {
	_, err := clientFromContext(ctx, r.client).ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, "redeem_batch:"+batchID.String())
	return err
}

// ListByBatchID returns every code of one generated batch, ordered by id.
func (r *redeemCodeRepository) ListByBatchID(ctx context.Context, batchID uuid.UUID) ([]service.RedeemCode, error) {
	models, err := clientFromContext(ctx, r.client).RedeemCode.Query().
		Where(redeemcode.BatchIDEQ(batchID)).
		Order(dbent.Asc(redeemcode.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return redeemCodeEntitiesToService(models), nil
}

func (r *redeemCodeRepository) Use(ctx context.Context, id, userID int64) error {
	now := time.Now()
	client := clientFromContext(ctx, r.client)
	// used_by IS NULL is defence in depth: a code that was ever redeemed can never be redeemed again, whatever its status.
	affected, err := client.RedeemCode.Update().
		Where(redeemcode.IDEQ(id), redeemcode.StatusEQ(service.StatusUnused), redeemcode.UsedByIsNil()).
		SetStatus(service.StatusUsed).
		SetUsedBy(userID).
		SetUsedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrRedeemCodeUsed
	}
	return nil
}

func (r *redeemCodeRepository) ListByUser(ctx context.Context, userID int64, limit int) ([]service.RedeemCode, error) {
	if limit <= 0 {
		limit = 10
	}

	codes, err := r.client.RedeemCode.Query().
		Where(redeemcode.UsedByEQ(userID)).
		WithGroup().
		Order(dbent.Desc(redeemcode.FieldUsedAt), dbent.Desc(redeemcode.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}

	return redeemCodeEntitiesToService(codes), nil
}

// ListByUserPaginated returns paginated balance/concurrency history for a user.
// Supports optional type filter (e.g. "balance", "admin_balance", "concurrency", "admin_concurrency", "subscription").
func (r *redeemCodeRepository) ListByUserPaginated(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]service.RedeemCode, *pagination.PaginationResult, error) {
	q := r.client.RedeemCode.Query().
		Where(redeemcode.UsedByEQ(userID))

	// Optional type filter
	if codeType != "" {
		q = q.Where(redeemcode.TypeEQ(codeType))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	codes, err := q.
		WithGroup().
		Offset(params.Offset()).
		Limit(params.Limit()).
		Order(dbent.Desc(redeemcode.FieldUsedAt), dbent.Desc(redeemcode.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	return redeemCodeEntitiesToService(codes), paginationResultFromTotal(int64(total), params), nil
}

// SumPositiveBalanceByUser returns total recharged amount (sum of value > 0 where type is balance/admin_balance).
func (r *redeemCodeRepository) SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error) {
	var result []struct {
		Sum float64 `json:"sum"`
	}
	err := r.client.RedeemCode.Query().
		Where(
			redeemcode.UsedByEQ(userID),
			redeemcode.ValueGT(0),
			redeemcode.TypeIn("balance", "admin_balance"),
		).
		Aggregate(dbent.As(dbent.Sum(redeemcode.FieldValue), "sum")).
		Scan(ctx, &result)
	if err != nil {
		return 0, err
	}
	if len(result) == 0 {
		return 0, nil
	}
	return result[0].Sum, nil
}

// SumRechargeBreakdownByUser splits the user's top-ups by source for balance-history (E10): positive balance redeem
// codes, positive admin_balance rows, affiliate rebates transferred to the balance and promo-code bonuses. redeem +
// admin equals SumPositiveBalanceByUser.
func (r *redeemCodeRepository) SumRechargeBreakdownByUser(ctx context.Context, userID int64) (breakdown service.UserRechargeBreakdown, err error) {
	rows, err := r.client.QueryContext(ctx, `
SELECT
    COALESCE((SELECT SUM(value) FROM redeem_codes WHERE used_by = $1 AND type = 'balance' AND value > 0), 0)::float8,
    COALESCE((SELECT SUM(value) FROM redeem_codes WHERE used_by = $1 AND type = 'admin_balance' AND value > 0), 0)::float8,
    COALESCE((SELECT SUM(amount) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'transfer' AND amount > 0), 0)::float8,
    COALESCE((SELECT SUM(bonus_amount) FROM promo_code_usages WHERE user_id = $1 AND bonus_amount > 0), 0)::float8`, userID)
	if err != nil {
		return breakdown, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return breakdown, err
		}
		return breakdown, errors.New("recharge breakdown returned no row")
	}
	if err := rows.Scan(&breakdown.Redeem, &breakdown.Admin, &breakdown.Affiliate, &breakdown.Promo); err != nil {
		return breakdown, err
	}
	return breakdown, rows.Err()
}

func redeemCodeEntityToService(m *dbent.RedeemCode) *service.RedeemCode {
	if m == nil {
		return nil
	}
	out := &service.RedeemCode{
		ID:           m.ID,
		Code:         m.Code,
		Type:         m.Type,
		Value:        m.Value,
		Status:       m.Status,
		UsedBy:       m.UsedBy,
		UsedAt:       m.UsedAt,
		Notes:        derefString(m.Notes),
		CreatedAt:    m.CreatedAt,
		ExpiresAt:    m.ExpiresAt,
		GroupID:      m.GroupID,
		ValidityDays: m.ValidityDays,
		BatchID:      m.BatchID,
		CreatedBy:    m.CreatedBy,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	if m.Edges.Group != nil {
		out.Group = groupEntityToService(m.Edges.Group)
	}
	return out
}

func redeemCodeEntitiesToService(models []*dbent.RedeemCode) []service.RedeemCode {
	out := make([]service.RedeemCode, 0, len(models))
	for i := range models {
		if s := redeemCodeEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}
