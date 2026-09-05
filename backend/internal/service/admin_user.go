package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/authidentity"
	"github.com/Wei-Shaw/sub2api/ent/authidentitychannel"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

// User management implementations
func (s *adminServiceImpl) ListUsers(ctx context.Context, page, pageSize int, filters UserListFilters, sortBy, sortOrder string) ([]User, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	users, result, err := s.userRepo.ListWithFilters(ctx, params, filters)
	if err != nil {
		return nil, 0, err
	}
	if len(users) > 0 {
		userIDs := make([]int64, 0, len(users))
		for i := range users {
			userIDs = append(userIDs, users[i].ID)
		}
		lastUsedByUserID, latestErr := s.userRepo.GetLatestUsedAtByUserIDs(ctx, userIDs)
		if latestErr != nil {
			logger.LegacyPrintf("service.admin", "failed to load user last_used_at in batch: err=%v", latestErr)
		} else {
			for i := range users {
				users[i].LastUsedAt = lastUsedByUserID[users[i].ID]
			}
		}
	}
	// 批量加载用户专属分组倍率
	if s.userGroupRateRepo != nil && len(users) > 0 {
		if batchRepo, ok := s.userGroupRateRepo.(userGroupRateBatchReader); ok {
			userIDs := make([]int64, 0, len(users))
			for i := range users {
				userIDs = append(userIDs, users[i].ID)
			}
			ratesByUser, err := batchRepo.GetByUserIDs(ctx, userIDs)
			if err != nil {
				logger.LegacyPrintf("service.admin", "failed to load user group rates in batch: err=%v", err)
				s.loadUserGroupRatesOneByOne(ctx, users)
			} else {
				for i := range users {
					if rates, ok := ratesByUser[users[i].ID]; ok {
						users[i].GroupRates = rates
					}
				}
			}
		} else {
			s.loadUserGroupRatesOneByOne(ctx, users)
		}
	}
	return users, result.Total, nil
}

func (s *adminServiceImpl) loadUserGroupRatesOneByOne(ctx context.Context, users []User) {
	if s.userGroupRateRepo == nil {
		return
	}
	for i := range users {
		rates, err := s.userGroupRateRepo.GetByUserID(ctx, users[i].ID)
		if err != nil {
			logger.LegacyPrintf("service.admin", "failed to load user group rates: user_id=%d err=%v", users[i].ID, err)
			continue
		}
		users[i].GroupRates = rates
	}
}

func (s *adminServiceImpl) GetUser(ctx context.Context, id int64) (*User, error) {
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	lastUsedAt, latestErr := s.userRepo.GetLatestUsedAtByUserID(ctx, id)
	if latestErr != nil {
		logger.LegacyPrintf("service.admin", "failed to load user last_used_at: user_id=%d err=%v", id, latestErr)
	} else {
		user.LastUsedAt = lastUsedAt
	}
	// 加载用户专属分组倍率
	if s.userGroupRateRepo != nil {
		rates, err := s.userGroupRateRepo.GetByUserID(ctx, id)
		if err != nil {
			logger.LegacyPrintf("service.admin", "failed to load user group rates: user_id=%d err=%v", id, err)
		} else {
			user.GroupRates = rates
		}
	}
	return user, nil
}

func (s *adminServiceImpl) GetUserIncludeDeleted(ctx context.Context, id int64) (*User, error) {
	return s.userRepo.GetByIDIncludeDeleted(ctx, id)
}

// normalizeUserRole 校验并归一化角色输入。
// 空字符串返回 fallback(未提供时的默认角色);非法值返回错误。
func normalizeUserRole(role, fallback string) (string, error) {
	if role == "" {
		return fallback, nil
	}
	if role != RoleAdmin && role != RoleUser && role != RoleVendor {
		return "", fmt.Errorf("invalid role: %q (must be %s, %s or %s)", role, RoleAdmin, RoleUser, RoleVendor)
	}
	return role, nil
}

func (s *adminServiceImpl) CreateUser(ctx context.Context, input *CreateUserInput) (*User, error) {
	balance := 0.0
	if input.Balance != nil {
		balance = *input.Balance
	} else if s.settingService != nil {
		balance = s.settingService.GetDefaultBalance(ctx)
	}

	// 角色可由管理员在创建时指定(admin/user);未提供时默认 user。
	role, err := normalizeUserRole(input.Role, RoleUser)
	if err != nil {
		return nil, err
	}

	user := &User{
		Email:         input.Email,
		Username:      input.Username,
		Notes:         input.Notes,
		Role:          role,
		Balance:       balance,
		Concurrency:   input.Concurrency,
		RPMLimit:      input.RPMLimit,
		Status:        StatusActive,
		AllowedGroups: input.AllowedGroups,

		RestrictPublicGroups: input.RestrictPublicGroups,
	}
	if err := user.SetPassword(input.Password); err != nil {
		return nil, err
	}
	if err := s.userRepo.Create(ctx, user); err != nil {
		return nil, err
	}
	// 创建管理员属权限敏感操作，落审计日志（含操作者），便于事后追溯。
	if user.Role == RoleAdmin {
		logger.LegacyPrintf("service.admin", "audit: admin user created actor_admin_id=%d target_user_id=%d",
			input.ActorAdminID, user.ID)
	}
	s.assignDefaultSubscriptions(ctx, user.ID)
	return user, nil
}

// ensureNotLastAdmin 降级管理员前确认系统中仍存在其他管理员，防止零 admin 锁死。
// 注：读取与写入之间存在竞态窗口，极端并发下仍可能双双降级；作为后台低频操作
// 的兜底保护足够，彻底防护需依赖数据库层约束。
func (s *adminServiceImpl) ensureNotLastAdmin(ctx context.Context) error {
	noSubs := false
	_, result, err := s.userRepo.ListWithFilters(ctx,
		pagination.PaginationParams{Page: 1, PageSize: 1},
		UserListFilters{Role: RoleAdmin, IncludeSubscriptions: &noSubs},
	)
	if err != nil {
		return fmt.Errorf("count admin users: %w", err)
	}
	if result == nil || result.Total <= 1 {
		return errors.New("cannot demote the last admin user")
	}
	return nil
}

func (s *adminServiceImpl) assignDefaultSubscriptions(ctx context.Context, userID int64) {
	if s.settingService == nil || s.defaultSubAssigner == nil || userID <= 0 {
		return
	}
	items := s.settingService.GetDefaultSubscriptions(ctx)
	for _, item := range items {
		if _, _, err := s.defaultSubAssigner.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
			UserID:       userID,
			GroupID:      item.GroupID,
			ValidityDays: item.ValidityDays,
			Notes:        "auto assigned by default user subscriptions setting",
		}); err != nil {
			logger.LegacyPrintf("service.admin", "failed to assign default subscription: user_id=%d group_id=%d err=%v", userID, item.GroupID, err)
		}
	}
}

func (s *adminServiceImpl) UpdateUser(ctx context.Context, id int64, input *UpdateUserInput) (*User, error) {
	// 校验用户专属分组倍率：必须 > 0（nil 合法，表示清除专属倍率）
	if input.GroupRates != nil {
		for groupID, rate := range input.GroupRates {
			if rate != nil && *rate <= 0 {
				return nil, fmt.Errorf("rate_multiplier must be > 0 (group_id=%d)", groupID)
			}
		}
	}

	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Protect admin users: cannot disable admin accounts
	if user.Role == "admin" && input.Status == "disabled" {
		return nil, errors.New("cannot disable admin user")
	}

	oldConcurrency := user.Concurrency
	oldStatus := user.Status
	oldRole := user.Role
	oldRPMLimit := user.RPMLimit
	oldAllowedGroups := append([]int64(nil), user.AllowedGroups...)
	oldRestrictPublicGroups := user.RestrictPublicGroups

	// fields 与下面的 input.X 判空条件一一对应：管理员没提交的列不写回，
	// 避免这份快照回滚并发的扣费、状态变更或批量限额调整。
	var fields UserUpdateFields

	if input.Email != "" {
		user.Email = input.Email
		fields.Email = true
	}
	if input.Password != "" {
		if err := user.SetPassword(input.Password); err != nil {
			return nil, err
		}
		fields.PasswordHash = true
	}

	if input.Username != nil {
		user.Username = *input.Username
		fields.Username = true
	}
	if input.Notes != nil {
		user.Notes = *input.Notes
		fields.Notes = true
	}

	if input.Status != "" {
		user.Status = input.Status
		fields.Status = true
	}

	// 角色变更(admin/user);空字符串表示不修改。
	if input.Role != "" {
		role, err := normalizeUserRole(input.Role, user.Role)
		if err != nil {
			return nil, err
		}
		// 防锁死保护：不允许降级系统中最后一个管理员（自我降级已在 handler 层拦截，
		// 此处兜底覆盖跨管理员互降导致零 admin 的场景）。
		//
		// 判定「离开 admin」而非枚举目标角色：vendor 引入时这里漏判过一次，
		// 只匹配 RoleUser 让 admin→vendor 绕开了保护。今后再加角色也不必回来改。
		if user.Role == RoleAdmin && role != RoleAdmin {
			if err := s.ensureNotLastAdmin(ctx); err != nil {
				return nil, err
			}
		}
		user.Role = role
		fields.Role = true
	}

	if input.Concurrency != nil {
		user.Concurrency = *input.Concurrency
		fields.Concurrency = true
	}

	if input.RPMLimit != nil {
		user.RPMLimit = *input.RPMLimit
		fields.RPMLimit = true
	}

	if input.AllowedGroups != nil {
		user.AllowedGroups = *input.AllowedGroups
		fields.AllowedGroups = true
	}
	if input.RestrictPublicGroups != nil {
		user.RestrictPublicGroups = *input.RestrictPublicGroups
		fields.RestrictPublicGroups = true
	}

	concurrencyLedgerPersisted := false
	groupRatesPersisted := false
	persistConcurrencyLedger := fields.Concurrency && s.canPersistAdminAdjustments()
	useUpdateTransaction := s.entClient != nil && (persistConcurrencyLedger || (input.GroupRates != nil && s.userGroupRateRepo != nil))
	if useUpdateTransaction {
		tx, err := s.entClient.Tx(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		opCtx := dbent.NewTxContext(ctx, tx)
		metadata := AdminAdjustmentMetadataFromContext(ctx)
		requested := max(user.Concurrency, 0)
		if persistConcurrencyLedger {
			committed, err := s.adjustmentActionAlreadyCommitted(opCtx, metadata)
			if err != nil {
				return nil, err
			}
			if committed {
				return s.userRepo.GetByID(opCtx, id)
			}
			fields.Concurrency = false
		}
		// Keep the same lock order as an email-only update: email advisory
		// lock first, then the user row. This avoids a row/advisory inversion
		// when one request changes email and concurrency together.
		if err := s.userRepo.Update(opCtx, user, fields); err != nil {
			return nil, err
		}
		if persistConcurrencyLedger {
			before, after, snapshotEmail, snapshotName, err := atomicSetUserConcurrency(opCtx, tx.Client(), user.ID, requested)
			if err != nil {
				return nil, err
			}
			oldConcurrency = before
			user.Concurrency = after
			if after != before {
				if metadata.Notes == "" {
					metadata.Notes = input.AdjustmentNotes
				}
				requestedValue := decimal.NewFromInt(int64(requested)).StringFixed(8)
				beforeValue := decimal.NewFromInt(int64(before)).StringFixed(8)
				afterValue := decimal.NewFromInt(int64(after)).StringFixed(8)
				deltaValue := decimal.NewFromInt(int64(after - before)).StringFixed(8)
				if err := s.createAdjustmentRecords(opCtx, tx.Client(), []AdminUserAdjustmentWrite{{
					ActionID: metadata.ActionID, Kind: AdjustmentKindConcurrency,
					Operation: AdjustmentOperationSet, RequestedValue: &requestedValue,
					Delta: deltaValue, BeforeValue: &beforeValue, AfterValue: &afterValue,
					UserID: user.ID, UserEmail: adjustmentStringPtr(snapshotEmail), UserName: adjustmentStringPtr(snapshotName),
					Source: "admin_user_update",
				}}, metadata); err != nil {
					return nil, err
				}
			}
		}
		if input.GroupRates != nil && s.userGroupRateRepo != nil {
			if err := s.userGroupRateRepo.SyncUserGroupRates(opCtx, user.ID, input.GroupRates); err != nil {
				return nil, fmt.Errorf("sync user group rates: %w", err)
			}
			groupRatesPersisted = true
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		concurrencyLedgerPersisted = persistConcurrencyLedger
	} else if err := s.userRepo.Update(ctx, user, fields); err != nil {
		return nil, err
	}

	// 角色变更属权限敏感操作，落审计日志（含操作者），便于事后追溯。
	if user.Role != oldRole {
		logger.LegacyPrintf("service.admin", "audit: user role changed actor_admin_id=%d target_user_id=%d old_role=%s new_role=%s",
			input.ActorAdminID, user.ID, oldRole, user.Role)
	}

	// 同步用户专属分组倍率
	if !groupRatesPersisted && input.GroupRates != nil && s.userGroupRateRepo != nil {
		if err := s.userGroupRateRepo.SyncUserGroupRates(ctx, user.ID, input.GroupRates); err != nil {
			return nil, fmt.Errorf("sync user group rates: %w", err)
		}
	}

	if s.authCacheInvalidator != nil {
		// RPMLimit 直接参与 billing_cache_service.checkRPM 的三级级联，
		// allowed_groups 参与 API Key 专属分组授权判断；不失效缓存会让修改在一个 L2 TTL 内失去效果。
		if user.Concurrency != oldConcurrency || user.Status != oldStatus || user.Role != oldRole || user.RPMLimit != oldRPMLimit || user.RestrictPublicGroups != oldRestrictPublicGroups || !sameInt64Set(user.AllowedGroups, oldAllowedGroups) {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, user.ID)
		}
	}

	concurrencyDiff := user.Concurrency - oldConcurrency
	if concurrencyDiff != 0 && !concurrencyLedgerPersisted && s.redeemCodeRepo != nil {
		code, err := GenerateRedeemCode()
		if err != nil {
			logger.LegacyPrintf("service.admin", "failed to generate adjustment redeem code: %v", err)
			return user, nil
		}
		adjustmentRecord := &RedeemCode{
			Code:   code,
			Type:   AdjustmentTypeAdminConcurrency,
			Value:  float64(concurrencyDiff),
			Status: StatusUsed,
			UsedBy: &user.ID,
		}
		now := time.Now()
		adjustmentRecord.UsedAt = &now
		if err := s.redeemCodeRepo.Create(ctx, adjustmentRecord); err != nil {
			logger.LegacyPrintf("service.admin", "failed to create concurrency adjustment redeem code: %v", err)
		}
	}

	return user, nil
}

func atomicSetUserConcurrency(ctx context.Context, client *dbent.Client, userID int64, value int) (before, after int, email, name string, err error) {
	rows, err := client.QueryContext(ctx, `
WITH target AS (
    SELECT id, concurrency AS before_value
    FROM users
    WHERE id = $1 AND deleted_at IS NULL
    FOR UPDATE
), updated AS (
    UPDATE users AS u
    SET concurrency = $2, updated_at = NOW()
    FROM target
    WHERE u.id = target.id
    RETURNING target.before_value, u.concurrency AS after_value,
              u.email, COALESCE(NULLIF(u.username, ''), '') AS user_name
)
SELECT before_value, after_value, email, user_name FROM updated`, userID, max(value, 0))
	if err != nil {
		return 0, 0, "", "", err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, 0, "", "", err
		}
		return 0, 0, "", "", ErrUserNotFound
	}
	if err := rows.Scan(&before, &after, &email, &name); err != nil {
		return 0, 0, "", "", err
	}
	return before, after, email, name, rows.Err()
}

func sameInt64Set(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[int64]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		if counts[v] == 0 {
			return false
		}
		counts[v]--
	}
	return true
}

func (s *adminServiceImpl) DeleteUser(ctx context.Context, id int64) error {
	// Protect admin users: cannot delete admin accounts
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if user.Role == "admin" {
		return errors.New("cannot delete admin user")
	}

	apiKeys, err := s.listUserAPIKeysForDeletion(ctx, id)
	if err != nil {
		return err
	}

	if s.entClient != nil {
		tx, err := s.entClient.Tx(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		opCtx := dbent.NewTxContext(ctx, tx)
		if err := s.deleteUserWithAPIKeys(opCtx, id, apiKeys); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	} else {
		if err := s.deleteUserWithAPIKeys(ctx, id, apiKeys); err != nil {
			return err
		}
	}

	if s.authCacheInvalidator != nil {
		for _, key := range apiKeys {
			if keyValue := strings.TrimSpace(key.Key); keyValue != "" {
				s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, keyValue)
			}
		}
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, id)
	}
	return nil
}

func (s *adminServiceImpl) listUserAPIKeysForDeletion(ctx context.Context, userID int64) ([]APIKey, error) {
	if s.apiKeyRepo == nil {
		return nil, nil
	}

	const pageSize = 1000
	keys := make([]APIKey, 0)
	for page := 1; ; page++ {
		batch, result, err := s.apiKeyRepo.ListByUserID(ctx, userID, pagination.PaginationParams{
			Page:      page,
			PageSize:  pageSize,
			SortBy:    "id",
			SortOrder: pagination.SortOrderAsc,
		}, APIKeyListFilters{})
		if err != nil {
			return nil, fmt.Errorf("list user api keys: %w", err)
		}
		keys = append(keys, batch...)
		if len(batch) == 0 || len(batch) < pageSize || result == nil || int64(len(keys)) >= result.Total {
			break
		}
	}
	return keys, nil
}

func (s *adminServiceImpl) deleteUserWithAPIKeys(ctx context.Context, userID int64, apiKeys []APIKey) error {
	if s.apiKeyRepo != nil {
		for _, key := range apiKeys {
			if key.ID <= 0 {
				continue
			}
			if err := s.apiKeyRepo.DeleteWithAudit(ctx, key.ID); err != nil {
				logger.LegacyPrintf("service.admin", "delete user api key failed: user_id=%d api_key_id=%d err=%v", userID, key.ID, err)
				return fmt.Errorf("delete user api key %d: %w", key.ID, err)
			}
		}
	}

	if err := s.userRepo.Delete(ctx, userID); err != nil {
		logger.LegacyPrintf("service.admin", "delete user failed: user_id=%d err=%v", userID, err)
		return err
	}
	return nil
}

func (s *adminServiceImpl) BatchUpdateConcurrency(ctx context.Context, userIDs []int64, value int, mode string) (int, error) {
	cleaned := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, uid := range userIDs {
		if uid <= 0 {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		cleaned = append(cleaned, uid)
	}
	if len(cleaned) == 0 {
		return 0, nil
	}

	if mode != AdjustmentOperationSet && mode != AdjustmentOperationAdd {
		return 0, errors.New("invalid mode: must be 'set' or 'add'")
	}

	var affected int
	var err error
	if s.canPersistAdminAdjustments() {
		affected, err = s.batchUpdateConcurrencyWithLedger(ctx, cleaned, value, mode, nil, "admin_batch_concurrency")
	} else if mode == AdjustmentOperationSet {
		affected, err = s.userRepo.BatchSetConcurrency(ctx, cleaned, value)
	} else {
		affected, err = s.userRepo.BatchAddConcurrency(ctx, cleaned, value)
	}
	if err != nil {
		return 0, err
	}

	if s.authCacheInvalidator != nil {
		for _, uid := range cleaned {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, uid)
		}
	}
	return affected, nil
}

func (s *adminServiceImpl) BatchUpdateLimits(ctx context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error) {
	if concurrency == nil && rpmLimit == nil {
		return 0, fmt.Errorf("at least one of concurrency or rpm_limit is required")
	}

	cleaned := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		cleaned = append(cleaned, userID)
	}
	if len(cleaned) == 0 {
		return 0, nil
	}

	var affected int
	var err error
	if concurrency != nil && s.canPersistAdminAdjustments() {
		affected, err = s.batchUpdateConcurrencyWithLedger(ctx, cleaned, *concurrency, AdjustmentOperationSet, rpmLimit, "admin_batch_limits")
	} else {
		affected, err = s.userRepo.BatchUpdateLimits(ctx, cleaned, concurrency, rpmLimit)
	}
	if err != nil {
		return 0, err
	}
	if s.authCacheInvalidator != nil {
		for _, userID := range cleaned {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
	}
	return affected, nil
}

var maxAdminBalance = decimal.RequireFromString("999999999999.99999999")

func parseAdminBalance(balance string, operation string) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(strings.TrimSpace(balance))
	if err != nil {
		return decimal.Zero, infraerrors.BadRequest("INVALID_BALANCE", "balance must be a decimal number")
	}
	if value.IsNegative() || value.GreaterThan(maxAdminBalance) || !value.Equal(value.Round(8)) {
		return decimal.Zero, infraerrors.BadRequest("INVALID_BALANCE", "balance must be between 0 and 999999999999.99999999 with at most 8 decimal places")
	}
	if operation != AdjustmentOperationSet && !value.IsPositive() {
		return decimal.Zero, infraerrors.BadRequest("INVALID_BALANCE", "balance must be greater than zero for add or subtract")
	}
	return value.Round(8), nil
}

func (s *adminServiceImpl) UpdateUserBalance(ctx context.Context, userID int64, balance string, operation string, notes string) (*User, error) {
	requestedBalance, err := parseAdminBalance(balance, operation)
	if err != nil {
		return nil, err
	}
	if !s.canPersistAdminAdjustments() {
		return s.updateUserBalanceCompatibility(ctx, userID, requestedBalance, operation, notes)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	opCtx := dbent.NewTxContext(ctx, tx)
	metadata := AdminAdjustmentMetadataFromContext(ctx)
	committed, err := s.adjustmentActionAlreadyCommitted(opCtx, metadata)
	if err != nil {
		return nil, err
	}
	if committed {
		return s.userRepo.GetByID(opCtx, userID)
	}

	// 余额调整必须走原子接口：先读后整行写回会把并发的计费扣款覆盖掉。
	var change BalanceChange
	exactRepo, exact := s.userRepo.(ExactBalanceAdjustmentRepository)
	switch operation {
	case AdjustmentOperationSet:
		if exact {
			change, err = exactRepo.SetBalanceExact(opCtx, userID, requestedBalance.StringFixed(8))
		} else {
			value, _ := requestedBalance.Float64()
			change, err = s.userRepo.SetBalance(opCtx, userID, value)
		}
	case AdjustmentOperationAdd:
		if exact {
			change, err = exactRepo.AdjustBalanceExact(opCtx, userID, requestedBalance.StringFixed(8))
		} else {
			value, _ := requestedBalance.Float64()
			change, err = s.userRepo.AdjustBalance(opCtx, userID, value)
		}
	case AdjustmentOperationSubtract:
		if exact {
			change, err = exactRepo.AdjustBalanceExact(opCtx, userID, requestedBalance.Neg().StringFixed(8))
		} else {
			value, _ := requestedBalance.Float64()
			change, err = s.userRepo.AdjustBalance(opCtx, userID, -value)
		}
	default:
		return nil, fmt.Errorf("unsupported balance operation: %q", operation)
	}
	if errors.Is(err, ErrBalanceNegative) {
		return nil, fmt.Errorf("balance cannot be negative, current balance: %.2f, requested operation would result in: %.2f", change.Old, change.New)
	}
	if err != nil {
		return nil, err
	}

	beforeDecimal, afterDecimal, balanceDiff, err := exactBalanceChange(change)
	if err != nil {
		return nil, err
	}
	if !balanceDiff.IsZero() {
		email, name, snapshotErr := adminAdjustmentUserSnapshot(opCtx, tx.Client(), userID)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		metadata.Notes = notes
		requested := requestedBalance.StringFixed(8)
		beforeValue := beforeDecimal.StringFixed(8)
		afterValue := afterDecimal.StringFixed(8)
		deltaValue := balanceDiff.StringFixed(8)
		if err := s.createAdjustmentRecords(opCtx, tx.Client(), []AdminUserAdjustmentWrite{{
			ActionID:       metadata.ActionID,
			Kind:           AdjustmentKindBalance,
			Operation:      adjustmentOperationForDelta(operation, balanceDiff.Sign()),
			RequestedValue: &requested,
			Delta:          deltaValue,
			BeforeValue:    &beforeValue,
			AfterValue:     &afterValue,
			UserID:         userID,
			UserEmail:      adjustmentStringPtr(email),
			UserName:       adjustmentStringPtr(name),
			Source:         "admin_balance",
		}}, metadata); err != nil {
			return nil, err
		}
	}
	user, err := s.userRepo.GetByID(opCtx, userID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	balanceDiffFloat, _ := balanceDiff.Float64()
	requestedFloat, _ := requestedBalance.Float64()
	s.afterBalanceAdjustmentCommit(ctx, userID, operation, requestedFloat, balanceDiffFloat)
	return user, nil
}

func exactBalanceChange(change BalanceChange) (before, after, delta decimal.Decimal, err error) {
	if change.OldExact == "" || change.NewExact == "" {
		before = decimal.NewFromFloat(change.Old).Round(8)
		after = decimal.NewFromFloat(change.New).Round(8)
		return before, after, after.Sub(before), nil
	}
	before, err = decimal.NewFromString(change.OldExact)
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("parse exact balance before value: %w", err)
	}
	after, err = decimal.NewFromString(change.NewExact)
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("parse exact balance after value: %w", err)
	}
	return before, after, after.Sub(before), nil
}

func (s *adminServiceImpl) updateUserBalanceCompatibility(ctx context.Context, userID int64, balance decimal.Decimal, operation string, notes string) (*User, error) {
	var (
		change BalanceChange
		err    error
	)
	value, _ := balance.Float64()
	switch operation {
	case AdjustmentOperationSet:
		change, err = s.userRepo.SetBalance(ctx, userID, value)
	case AdjustmentOperationAdd:
		change, err = s.userRepo.AdjustBalance(ctx, userID, value)
	case AdjustmentOperationSubtract:
		change, err = s.userRepo.AdjustBalance(ctx, userID, -value)
	default:
		return nil, fmt.Errorf("unsupported balance operation: %q", operation)
	}
	if errors.Is(err, ErrBalanceNegative) {
		return nil, fmt.Errorf("balance cannot be negative, current balance: %.2f, requested operation would result in: %.2f", change.Old, change.New)
	}
	if err != nil {
		return nil, err
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	balanceDiff := change.New - change.Old
	if balanceDiff != 0 && s.redeemCodeRepo != nil {
		code, generateErr := GenerateRedeemCode()
		if generateErr != nil {
			return nil, generateErr
		}
		now := time.Now()
		if createErr := s.redeemCodeRepo.Create(ctx, &RedeemCode{
			Code: code, Type: AdjustmentTypeAdminBalance, Value: balanceDiff,
			Status: StatusUsed, UsedBy: &user.ID, Notes: notes, UsedAt: &now,
		}); createErr != nil {
			logger.LegacyPrintf("service.admin", "failed to create balance adjustment redeem code: %v", createErr)
		}
	}
	s.afterBalanceAdjustmentCommit(ctx, userID, operation, value, balanceDiff)
	return user, nil
}

func (s *adminServiceImpl) afterBalanceAdjustmentCommit(ctx context.Context, userID int64, operation string, requested, delta float64) {
	if delta == 0 {
		return
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	s.tryAccrueAffiliateRebateForAdminRecharge(ctx, userID, operation, requested)
	if s.billingCacheService != nil {
		go func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.billingCacheService.InvalidateUserBalance(cacheCtx, userID); err != nil {
				logger.LegacyPrintf("service.admin", "invalidate user balance cache failed: user_id=%d err=%v", userID, err)
			}
		}()
	}
}

type adminConcurrencyChange struct {
	UserID int64
	Email  string
	Name   string
	Before int
	After  int
}

func (s *adminServiceImpl) canPersistAdminAdjustments() bool {
	return s.entClient != nil && s.redeemCodeRepo != nil && s.adjustmentRepo != nil
}

func (s *adminServiceImpl) adjustmentActionAlreadyCommitted(ctx context.Context, metadata AdminAdjustmentMetadata) (bool, error) {
	if !metadata.Idempotent {
		return false, nil
	}
	if err := s.adjustmentRepo.LockAction(ctx, metadata.ActionID); err != nil {
		return false, fmt.Errorf("lock admin adjustment action: %w", err)
	}
	count, err := s.adjustmentRepo.CountByActionID(ctx, metadata.ActionID)
	if err != nil {
		return false, fmt.Errorf("check admin adjustment action: %w", err)
	}
	return count > 0, nil
}

func adminAdjustmentUserSnapshot(ctx context.Context, client *dbent.Client, userID int64) (email, name string, err error) {
	rows, err := client.QueryContext(ctx, `SELECT email, COALESCE(NULLIF(username, ''), '') FROM users WHERE id = $1 AND deleted_at IS NULL`, userID)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", "", err
		}
		return "", "", ErrUserNotFound
	}
	if err := rows.Scan(&email, &name); err != nil {
		return "", "", err
	}
	return email, name, rows.Err()
}

func (s *adminServiceImpl) createAdjustmentRecords(ctx context.Context, client *dbent.Client, rows []AdminUserAdjustmentWrite, metadata AdminAdjustmentMetadata) error {
	if len(rows) == 0 {
		return nil
	}
	var operatorName *string
	if metadata.OperatorID != nil {
		operatorEmail, name, err := adminAdjustmentUserSnapshot(ctx, client, *metadata.OperatorID)
		if err != nil {
			return fmt.Errorf("snapshot adjustment operator: %w", err)
		}
		metadata.OperatorEmail = operatorEmail
		operatorName = adjustmentStringPtr(name)
	}
	now := time.Now()
	redeemCodes := make([]RedeemCode, 0, len(rows))
	for i := range rows {
		code, err := GenerateRedeemCode()
		if err != nil {
			return err
		}
		recordType := AdjustmentTypeAdminBalance
		if rows[i].Kind == AdjustmentKindConcurrency {
			recordType = AdjustmentTypeAdminConcurrency
		}
		userID := rows[i].UserID
		delta, err := decimal.NewFromString(rows[i].Delta)
		if err != nil {
			return fmt.Errorf("parse adjustment delta: %w", err)
		}
		deltaFloat, _ := delta.Float64()
		redeemCodes = append(redeemCodes, RedeemCode{
			Code: code, Type: recordType, Value: deltaFloat, ValueExact: delta.StringFixed(8), Status: StatusUsed,
			UsedBy: &userID, UsedAt: &now, Notes: metadata.Notes,
		})
		rows[i].OperatorUserID = metadata.OperatorID
		rows[i].OperatorEmail = adjustmentStringPtr(metadata.OperatorEmail)
		rows[i].OperatorName = operatorName
		rows[i].Notes = adjustmentStringPtr(metadata.Notes)
		rows[i].ClientIP = adjustmentStringPtr(metadata.ClientIP)
		rows[i].AuthMethod = adjustmentStringPtr(metadata.AuthMethod)
		rows[i].RequestID = adjustmentStringPtr(metadata.RequestID)
		rows[i].CreatedAt = now
	}
	if err := s.redeemCodeRepo.CreateBatch(ctx, redeemCodes); err != nil {
		return fmt.Errorf("create compatibility adjustment records: %w", err)
	}
	if err := s.adjustmentRepo.CreateBatch(ctx, rows); err != nil {
		return fmt.Errorf("create admin user adjustments: %w", err)
	}
	return nil
}

func (s *adminServiceImpl) batchUpdateConcurrencyWithLedger(ctx context.Context, userIDs []int64, value int, mode string, rpmLimit *int, source string) (int, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	opCtx := dbent.NewTxContext(ctx, tx)
	metadata := AdminAdjustmentMetadataFromContext(ctx)
	committed, err := s.adjustmentActionAlreadyCommitted(opCtx, metadata)
	if err != nil {
		return 0, err
	}
	if committed {
		return countActiveAdjustmentTargets(opCtx, tx.Client(), userIDs)
	}
	changes, err := atomicBatchUpdateConcurrency(opCtx, tx.Client(), userIDs, value, mode, rpmLimit)
	if err != nil {
		return 0, err
	}
	rows := make([]AdminUserAdjustmentWrite, 0, len(changes))
	requested := decimal.NewFromInt(int64(value)).StringFixed(8)
	if mode == AdjustmentOperationSet && value < 0 {
		requested = decimal.Zero.StringFixed(8)
	}
	for _, change := range changes {
		delta := change.After - change.Before
		if delta == 0 {
			continue
		}
		beforeValue := decimal.NewFromInt(int64(change.Before)).StringFixed(8)
		afterValue := decimal.NewFromInt(int64(change.After)).StringFixed(8)
		deltaValue := decimal.NewFromInt(int64(delta)).StringFixed(8)
		rows = append(rows, AdminUserAdjustmentWrite{
			ActionID: metadata.ActionID, Kind: AdjustmentKindConcurrency,
			Operation: adjustmentOperationForDelta(mode, delta), RequestedValue: &requested,
			Delta: deltaValue, BeforeValue: &beforeValue, AfterValue: &afterValue,
			UserID: change.UserID, UserEmail: adjustmentStringPtr(change.Email), UserName: adjustmentStringPtr(change.Name), Source: source,
		})
	}
	if err := s.createAdjustmentRecords(opCtx, tx.Client(), rows, metadata); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(changes), nil
}

func countActiveAdjustmentTargets(ctx context.Context, client *dbent.Client, userIDs []int64) (count int, err error) {
	rows, err := client.QueryContext(ctx,
		`SELECT COUNT(*) FROM users WHERE id = ANY($1) AND deleted_at IS NULL`, pq.Array(userIDs))
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
		return 0, errors.New("active adjustment target count returned no row")
	}
	if err := rows.Scan(&count); err != nil {
		return 0, err
	}
	return count, rows.Err()
}

func atomicBatchUpdateConcurrency(ctx context.Context, client *dbent.Client, userIDs []int64, value int, mode string, rpmLimit *int) ([]adminConcurrencyChange, error) {
	setExpression := "$2"
	if mode == AdjustmentOperationAdd {
		setExpression = "GREATEST(target.before_value + $2, 0)"
	} else if value < 0 {
		value = 0
	}
	args := []any{pq.Array(userIDs), value}
	rpmClause := ""
	if rpmLimit != nil {
		rpm := max(*rpmLimit, 0)
		args = append(args, rpm)
		rpmClause = fmt.Sprintf(", rpm_limit = $%d", len(args))
	}
	query := fmt.Sprintf(`
WITH target AS (
    SELECT id, concurrency AS before_value
    FROM users
    WHERE id = ANY($1) AND deleted_at IS NULL
    FOR UPDATE
), updated AS (
    UPDATE users AS u
    SET concurrency = %s%s, updated_at = NOW()
    FROM target
    WHERE u.id = target.id
    RETURNING u.id, u.email, COALESCE(NULLIF(u.username, ''), '') AS user_name,
              target.before_value, u.concurrency AS after_value
)
SELECT id, email, user_name, before_value, after_value FROM updated ORDER BY id`, setExpression, rpmClause)
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	changes := make([]adminConcurrencyChange, 0, len(userIDs))
	for rows.Next() {
		var change adminConcurrencyChange
		if err := rows.Scan(&change.UserID, &change.Email, &change.Name, &change.Before, &change.After); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func (s *adminServiceImpl) tryAccrueAffiliateRebateForAdminRecharge(ctx context.Context, userID int64, operation string, amount float64) {
	if operation != "add" || amount <= 0 || s.settingService == nil || s.affiliateService == nil {
		return
	}
	if !s.settingService.IsAffiliateAdminRechargeEnabled(ctx) {
		return
	}

	rebate, err := s.affiliateService.AccrueInviteRebate(ctx, userID, amount)
	if err != nil {
		logger.LegacyPrintf("service.admin", "affiliate rebate failed for admin recharge: user_id=%d amount=%.8f err=%v", userID, amount, err)
		return
	}
	if rebate > 0 {
		logger.LegacyPrintf("service.admin", "affiliate rebate accrued for admin recharge: user_id=%d amount=%.8f rebate=%.8f", userID, amount, rebate)
	}
}

func (s *adminServiceImpl) GetUserAPIKeys(ctx context.Context, userID int64, page, pageSize int, sortBy, sortOrder string) ([]APIKey, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	keys, result, err := s.apiKeyRepo.ListByUserID(ctx, userID, params, APIKeyListFilters{})
	if err != nil {
		return nil, 0, err
	}
	return keys, result.Total, nil
}

func (s *adminServiceImpl) GetUserRPMStatus(ctx context.Context, userID int64) (*UserRPMStatus, error) {
	if s.userRPMCache == nil {
		return nil, ErrRPMStatusUnavailable
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	userRPMUsed, err := s.userRPMCache.GetUserRPM(ctx, userID)
	if err != nil {
		logger.LegacyPrintf("service.admin", "failed to get user rpm: user_id=%d err=%v", userID, err)
	}

	keys, _, err := s.GetUserAPIKeys(ctx, userID, 1, 1000, "", "")
	if err != nil {
		return nil, err
	}

	groupIDSet := make(map[int64]struct{})
	for _, key := range keys {
		if key.GroupID != nil && *key.GroupID > 0 {
			groupIDSet[*key.GroupID] = struct{}{}
		}
	}

	groupIDs := make([]int64, 0, len(groupIDSet))
	for groupID := range groupIDSet {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })

	var perGroup []UserGroupRPMStatus
	for _, groupID := range groupIDs {
		used, getErr := s.userRPMCache.GetUserGroupRPM(ctx, userID, groupID)
		if getErr != nil {
			logger.LegacyPrintf("service.admin", "failed to get user group rpm: user_id=%d group_id=%d err=%v", userID, groupID, getErr)
		}

		entry := UserGroupRPMStatus{
			GroupID: groupID,
			Used:    used,
		}

		if s.groupRepo != nil {
			if group, groupErr := s.groupRepo.GetByIDLite(ctx, groupID); groupErr == nil && group != nil {
				entry.GroupName = group.Name
				entry.Limit = group.RPMLimit
				entry.Source = "group"
			} else if groupErr != nil {
				logger.LegacyPrintf("service.admin", "failed to get group rpm status metadata: group_id=%d err=%v", groupID, groupErr)
			}
		}

		if s.userGroupRateRepo != nil {
			override, overrideErr := s.userGroupRateRepo.GetRPMOverrideByUserAndGroup(ctx, userID, groupID)
			if overrideErr != nil {
				logger.LegacyPrintf("service.admin", "failed to get rpm override: user_id=%d group_id=%d err=%v", userID, groupID, overrideErr)
			} else if override != nil {
				entry.Limit = *override
				entry.Source = "override"
			}
		}

		perGroup = append(perGroup, entry)
	}

	return &UserRPMStatus{
		UserRPMUsed:  userRPMUsed,
		UserRPMLimit: user.RPMLimit,
		PerGroup:     perGroup,
	}, nil
}

func (s *adminServiceImpl) GetUserUsageStats(ctx context.Context, userID int64, period string) (any, error) {
	// Return mock data for now
	return map[string]any{
		"period":          period,
		"total_requests":  0,
		"total_cost":      0.0,
		"total_tokens":    0,
		"avg_duration_ms": 0,
	}, nil
}

// GetUserBalanceHistory returns paginated balance/concurrency change records for a user.
func (s *adminServiceImpl) GetUserBalanceHistory(ctx context.Context, userID int64, page, pageSize int, codeType string) ([]RedeemCode, int64, float64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	if codeType == RedeemTypeAffiliateBalance {
		codes, total, err := s.listAffiliateBalanceHistory(ctx, userID, params)
		if err != nil {
			return nil, 0, 0, err
		}
		totalRecharged, err := s.redeemCodeRepo.SumPositiveBalanceByUser(ctx, userID)
		if err != nil {
			return nil, 0, 0, err
		}
		return codes, total, totalRecharged, nil
	}

	if codeType == "" {
		return s.getAllUserBalanceHistory(ctx, userID, params)
	}

	codes, result, err := s.redeemCodeRepo.ListByUserPaginated(ctx, userID, params, codeType)
	if err != nil {
		return nil, 0, 0, err
	}
	total := result.Total
	// Aggregate total recharged amount (only once, regardless of type filter)
	totalRecharged, err := s.redeemCodeRepo.SumPositiveBalanceByUser(ctx, userID)
	if err != nil {
		return nil, 0, 0, err
	}
	return codes, total, totalRecharged, nil
}

func (s *adminServiceImpl) getAllUserBalanceHistory(ctx context.Context, userID int64, params pagination.PaginationParams) ([]RedeemCode, int64, float64, error) {
	needed := params.Offset() + params.Limit()
	if needed < params.Limit() {
		needed = params.Limit()
	}

	redeemCodes, redeemTotal, err := s.listRedeemBalanceHistoryForMerge(ctx, userID, needed)
	if err != nil {
		return nil, 0, 0, err
	}
	affiliateCodes, affiliateTotal, err := s.listAffiliateBalanceHistoryForMerge(ctx, userID, needed)
	if err != nil {
		return nil, 0, 0, err
	}
	codes := mergeBalanceHistoryCodes(redeemCodes, affiliateCodes, params)

	totalRecharged, err := s.redeemCodeRepo.SumPositiveBalanceByUser(ctx, userID)
	if err != nil {
		return nil, 0, 0, err
	}
	return codes, redeemTotal + affiliateTotal, totalRecharged, nil
}

func (s *adminServiceImpl) listRedeemBalanceHistoryForMerge(ctx context.Context, userID int64, needed int) ([]RedeemCode, int64, error) {
	if needed <= 0 {
		return nil, 0, nil
	}

	var (
		out   []RedeemCode
		total int64
	)
	for page := 1; len(out) < needed; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: 1000}
		codes, result, err := s.redeemCodeRepo.ListByUserPaginated(ctx, userID, params, "")
		if err != nil {
			return nil, 0, err
		}
		if result != nil {
			total = result.Total
		}
		out = append(out, codes...)
		if len(codes) < params.Limit() || int64(len(out)) >= total {
			break
		}
	}
	if len(out) > needed {
		out = out[:needed]
	}
	return out, total, nil
}

func (s *adminServiceImpl) listAffiliateBalanceHistoryForMerge(ctx context.Context, userID int64, needed int) ([]RedeemCode, int64, error) {
	if needed <= 0 {
		return nil, 0, nil
	}

	var (
		out   []RedeemCode
		total int64
	)
	for page := 1; len(out) < needed; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: 1000}
		codes, currentTotal, err := s.listAffiliateBalanceHistory(ctx, userID, params)
		if err != nil {
			return nil, 0, err
		}
		total = currentTotal
		out = append(out, codes...)
		if len(codes) < params.Limit() || int64(len(out)) >= total {
			break
		}
	}
	if len(out) > needed {
		out = out[:needed]
	}
	return out, total, nil
}

func (s *adminServiceImpl) listAffiliateBalanceHistory(ctx context.Context, userID int64, params pagination.PaginationParams) ([]RedeemCode, int64, error) {
	if s == nil || s.entClient == nil || userID <= 0 {
		return nil, 0, nil
	}

	rows, err := s.entClient.QueryContext(ctx, `
SELECT id,
       amount::double precision,
       created_at
FROM user_affiliate_ledger
WHERE user_id = $1
  AND action = 'transfer'
ORDER BY created_at DESC, id DESC
OFFSET $2
LIMIT $3`, userID, params.Offset(), params.Limit())
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	codes := make([]RedeemCode, 0, params.Limit())
	for rows.Next() {
		var id int64
		var amount float64
		var createdAt time.Time
		if err := rows.Scan(&id, &amount, &createdAt); err != nil {
			return nil, 0, err
		}
		usedBy := userID
		usedAt := createdAt
		codes = append(codes, RedeemCode{
			ID:        -id,
			Code:      fmt.Sprintf("AFF-%d", id),
			Type:      RedeemTypeAffiliateBalance,
			Value:     amount,
			Status:    StatusUsed,
			UsedBy:    &usedBy,
			UsedAt:    &usedAt,
			CreatedAt: createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	total, err := countAffiliateBalanceHistory(ctx, s.entClient, userID)
	if err != nil {
		return nil, 0, err
	}
	return codes, total, nil
}

func countAffiliateBalanceHistory(ctx context.Context, client *dbent.Client, userID int64) (int64, error) {
	rows, err := client.QueryContext(ctx, `
SELECT COUNT(*)
FROM user_affiliate_ledger
WHERE user_id = $1
  AND action = 'transfer'`, userID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	var total sql.NullInt64
	if rows.Next() {
		if err := rows.Scan(&total); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func mergeBalanceHistoryCodes(redeemCodes, affiliateCodes []RedeemCode, params pagination.PaginationParams) []RedeemCode {
	combined := append(append([]RedeemCode{}, redeemCodes...), affiliateCodes...)
	sort.SliceStable(combined, func(i, j int) bool {
		return redeemCodeHistoryTime(combined[i]).After(redeemCodeHistoryTime(combined[j]))
	})
	offset := params.Offset()
	if offset >= len(combined) {
		return []RedeemCode{}
	}
	end := offset + params.Limit()
	if end > len(combined) {
		end = len(combined)
	}
	return combined[offset:end]
}

func redeemCodeHistoryTime(code RedeemCode) time.Time {
	if code.UsedAt != nil {
		return *code.UsedAt
	}
	return code.CreatedAt
}

func (s *adminServiceImpl) BindUserAuthIdentity(ctx context.Context, userID int64, input AdminBindAuthIdentityInput) (*AdminBoundAuthIdentity, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user_id must be greater than 0")
	}
	if s == nil || s.entClient == nil || s.userRepo == nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_UNAVAILABLE", "auth identity binding service is unavailable")
	}
	if _, err := s.userRepo.GetByID(ctx, userID); err != nil {
		return nil, err
	}

	providerType := normalizeAdminAuthIdentityProviderType(input.ProviderType)
	providerKey := strings.TrimSpace(input.ProviderKey)
	providerSubject := strings.TrimSpace(input.ProviderSubject)
	if providerType == "" {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "provider_type must be one of email, linuxdo, oidc, wechat, or dingtalk")
	}
	if providerKey == "" || providerSubject == "" {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "provider_type, provider_key, and provider_subject are required")
	}
	canonicalProviderKey := canonicalAdminAuthIdentityProviderKey(providerType, "", providerKey)
	compatibleProviderKeys := compatibleAdminAuthIdentityProviderKeys(providerType, providerKey)

	var issuer *string
	if input.Issuer != nil {
		trimmed := strings.TrimSpace(*input.Issuer)
		if trimmed != "" {
			issuer = &trimmed
		}
	}

	channelInput := normalizeAdminBindChannelInput(input.Channel)
	if input.Channel != nil && channelInput == nil {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "channel, channel_app_id, and channel_subject are required when channel binding is provided")
	}

	verifiedAt := time.Now().UTC()
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_TX_FAILED", "failed to start auth identity bind transaction").WithCause(err)
	}
	defer func() { _ = tx.Rollback() }()

	identityRecords, err := tx.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyIn(compatibleProviderKeys...),
			authidentity.ProviderSubjectEQ(providerSubject),
		).
		All(ctx)
	if err != nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	if hasAdminAuthIdentityOwnershipConflict(identityRecords, userID) {
		return nil, infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
	}
	identity := selectOwnedAdminAuthIdentity(identityRecords, userID)

	if identity == nil {
		create := tx.AuthIdentity.Create().
			SetUserID(userID).
			SetProviderType(providerType).
			SetProviderKey(canonicalProviderKey).
			SetProviderSubject(providerSubject).
			SetVerifiedAt(verifiedAt)
		if issuer != nil {
			create = create.SetIssuer(*issuer)
		}
		if input.Metadata != nil {
			create = create.SetMetadata(cloneAdminAuthIdentityMetadata(input.Metadata))
		}
		identity, err = create.Save(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_SAVE_FAILED", "failed to save auth identity").WithCause(err)
		}
	} else {
		update := tx.AuthIdentity.UpdateOneID(identity.ID).
			SetVerifiedAt(verifiedAt).
			SetProviderKey(canonicalProviderKey)
		if issuer != nil {
			update = update.SetIssuer(*issuer)
		}
		if input.Metadata != nil {
			update = update.SetMetadata(cloneAdminAuthIdentityMetadata(input.Metadata))
		}
		identity, err = update.Save(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_SAVE_FAILED", "failed to save auth identity").WithCause(err)
		}
	}

	var channel *dbent.AuthIdentityChannel
	if channelInput != nil {
		channelRecords, err := tx.AuthIdentityChannel.Query().
			Where(
				authidentitychannel.ProviderTypeEQ(providerType),
				authidentitychannel.ProviderKeyIn(compatibleProviderKeys...),
				authidentitychannel.ChannelEQ(channelInput.Channel),
				authidentitychannel.ChannelAppIDEQ(channelInput.ChannelAppID),
				authidentitychannel.ChannelSubjectEQ(channelInput.ChannelSubject),
			).
			WithIdentity().
			All(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_CHANNEL_LOOKUP_FAILED", "failed to inspect auth identity channel ownership").WithCause(err)
		}
		if hasAdminAuthIdentityChannelOwnershipConflict(channelRecords, userID) {
			return nil, infraerrors.Conflict("AUTH_IDENTITY_CHANNEL_OWNERSHIP_CONFLICT", "auth identity channel already belongs to another user")
		}
		channel = selectOwnedAdminAuthIdentityChannel(channelRecords, userID)
		if channel == nil {
			create := tx.AuthIdentityChannel.Create().
				SetIdentityID(identity.ID).
				SetProviderType(providerType).
				SetProviderKey(canonicalProviderKey).
				SetChannel(channelInput.Channel).
				SetChannelAppID(channelInput.ChannelAppID).
				SetChannelSubject(channelInput.ChannelSubject)
			if channelInput.Metadata != nil {
				create = create.SetMetadata(cloneAdminAuthIdentityMetadata(channelInput.Metadata))
			}
			channel, err = create.Save(ctx)
			if err != nil {
				return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_CHANNEL_SAVE_FAILED", "failed to save auth identity channel").WithCause(err)
			}
		} else {
			update := tx.AuthIdentityChannel.UpdateOneID(channel.ID).
				SetIdentityID(identity.ID).
				SetProviderKey(canonicalProviderKey)
			if channelInput.Metadata != nil {
				update = update.SetMetadata(cloneAdminAuthIdentityMetadata(channelInput.Metadata))
			}
			channel, err = update.Save(ctx)
			if err != nil {
				return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_CHANNEL_SAVE_FAILED", "failed to save auth identity channel").WithCause(err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_COMMIT_FAILED", "failed to commit auth identity bind").WithCause(err)
	}
	return buildAdminBoundAuthIdentity(identity, channel), nil
}

func compatibleAdminAuthIdentityProviderKeys(providerType, providerKey string) []string {
	providerType = strings.TrimSpace(strings.ToLower(providerType))
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return []string{providerKey}
	}
	if providerType != "wechat" {
		return []string{providerKey}
	}

	keys := []string{providerKey}
	if !strings.EqualFold(providerKey, "wechat-main") {
		keys = append(keys, "wechat-main")
	}
	if !strings.EqualFold(providerKey, "wechat") {
		keys = append(keys, "wechat")
	}
	return keys
}

func canonicalAdminAuthIdentityProviderKey(providerType, existingKey, requestedKey string) string {
	providerType = strings.TrimSpace(strings.ToLower(providerType))
	existingKey = strings.TrimSpace(existingKey)
	requestedKey = strings.TrimSpace(requestedKey)
	if providerType != "wechat" {
		if requestedKey != "" {
			return requestedKey
		}
		return existingKey
	}
	if strings.EqualFold(existingKey, "wechat") || strings.EqualFold(existingKey, "wechat-main") || strings.EqualFold(requestedKey, "wechat-main") {
		return "wechat-main"
	}
	if requestedKey != "" {
		return requestedKey
	}
	return existingKey
}

func adminAuthIdentityProviderKeyRank(providerType, providerKey string) int {
	providerType = strings.TrimSpace(strings.ToLower(providerType))
	providerKey = strings.TrimSpace(providerKey)
	if providerType != "wechat" {
		return 0
	}
	switch {
	case strings.EqualFold(providerKey, "wechat-main"):
		return 0
	case strings.EqualFold(providerKey, "wechat"):
		return 2
	default:
		return 1
	}
}

func selectOwnedAdminAuthIdentity(records []*dbent.AuthIdentity, userID int64) *dbent.AuthIdentity {
	var selected *dbent.AuthIdentity
	for _, record := range records {
		if record.UserID != userID {
			continue
		}
		if selected == nil || adminAuthIdentityProviderKeyRank(record.ProviderType, record.ProviderKey) < adminAuthIdentityProviderKeyRank(selected.ProviderType, selected.ProviderKey) {
			selected = record
		}
	}
	return selected
}

func hasAdminAuthIdentityOwnershipConflict(records []*dbent.AuthIdentity, userID int64) bool {
	for _, record := range records {
		if record.UserID != userID {
			return true
		}
	}
	return false
}

func selectOwnedAdminAuthIdentityChannel(records []*dbent.AuthIdentityChannel, userID int64) *dbent.AuthIdentityChannel {
	var selected *dbent.AuthIdentityChannel
	for _, record := range records {
		if record.Edges.Identity == nil || record.Edges.Identity.UserID != userID {
			continue
		}
		if selected == nil || adminAuthIdentityProviderKeyRank(record.ProviderType, record.ProviderKey) < adminAuthIdentityProviderKeyRank(selected.ProviderType, selected.ProviderKey) {
			selected = record
		}
	}
	return selected
}

func hasAdminAuthIdentityChannelOwnershipConflict(records []*dbent.AuthIdentityChannel, userID int64) bool {
	for _, record := range records {
		if record.Edges.Identity != nil && record.Edges.Identity.UserID != userID {
			return true
		}
	}
	return false
}

func normalizeAdminBindChannelInput(input *AdminBindAuthIdentityChannelInput) *AdminBindAuthIdentityChannelInput {
	if input == nil {
		return nil
	}
	channel := &AdminBindAuthIdentityChannelInput{
		Channel:        strings.TrimSpace(input.Channel),
		ChannelAppID:   strings.TrimSpace(input.ChannelAppID),
		ChannelSubject: strings.TrimSpace(input.ChannelSubject),
		Metadata:       cloneAdminAuthIdentityMetadata(input.Metadata),
	}
	if channel.Channel == "" || channel.ChannelAppID == "" || channel.ChannelSubject == "" {
		return nil
	}
	return channel
}

func normalizeAdminAuthIdentityProviderType(input string) string {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "email":
		return "email"
	case "linuxdo":
		return "linuxdo"
	case "oidc":
		return "oidc"
	case "wechat":
		return "wechat"
	case "dingtalk":
		return "dingtalk"
	default:
		return ""
	}
}

func buildAdminBoundAuthIdentity(identity *dbent.AuthIdentity, channel *dbent.AuthIdentityChannel) *AdminBoundAuthIdentity {
	if identity == nil {
		return nil
	}
	result := &AdminBoundAuthIdentity{
		UserID:          identity.UserID,
		ProviderType:    strings.TrimSpace(identity.ProviderType),
		ProviderKey:     strings.TrimSpace(identity.ProviderKey),
		ProviderSubject: strings.TrimSpace(identity.ProviderSubject),
		VerifiedAt:      identity.VerifiedAt,
		Issuer:          identity.Issuer,
		Metadata:        cloneAdminAuthIdentityMetadata(identity.Metadata),
		CreatedAt:       identity.CreatedAt,
		UpdatedAt:       identity.UpdatedAt,
	}
	if channel != nil {
		result.Channel = &AdminBoundAuthIdentityChannel{
			Channel:        strings.TrimSpace(channel.Channel),
			ChannelAppID:   strings.TrimSpace(channel.ChannelAppID),
			ChannelSubject: strings.TrimSpace(channel.ChannelSubject),
			Metadata:       cloneAdminAuthIdentityMetadata(channel.Metadata),
			CreatedAt:      channel.CreatedAt,
			UpdatedAt:      channel.UpdatedAt,
		}
	}
	return result
}

func cloneAdminAuthIdentityMetadata(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	if len(input) == 0 {
		return map[string]any{}
	}
	data, err := json.Marshal(input)
	if err != nil {
		out := make(map[string]any, len(input))
		for key, value := range input {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		out = make(map[string]any, len(input))
		for key, value := range input {
			out[key] = value
		}
	}
	return out
}

// Redeem code management implementations
func (s *adminServiceImpl) ListRedeemCodes(ctx context.Context, page, pageSize int, codeType, status, search string, sortBy, sortOrder string) ([]RedeemCode, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	codes, result, err := s.redeemCodeRepo.ListWithFilters(ctx, params, codeType, status, search)
	if err != nil {
		return nil, 0, err
	}
	return codes, result.Total, nil
}

func (s *adminServiceImpl) GetRedeemCode(ctx context.Context, id int64) (*RedeemCode, error) {
	return s.redeemCodeRepo.GetByID(ctx, id)
}

func (s *adminServiceImpl) GenerateRedeemCodes(ctx context.Context, input *GenerateRedeemCodesInput) ([]RedeemCode, error) {
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		return nil, ErrRedeemCodeExpired
	}

	// 如果是订阅类型，验证必须有 GroupID
	if input.Type == RedeemTypeSubscription {
		if input.GroupID == nil {
			return nil, errors.New("group_id is required for subscription type")
		}
		// 验证分组存在且为订阅类型
		group, err := s.groupRepo.GetByID(ctx, *input.GroupID)
		if err != nil {
			return nil, fmt.Errorf("group not found: %w", err)
		}
		if !group.IsSubscriptionType() {
			return nil, errors.New("group must be subscription type")
		}
	}

	codes := make([]RedeemCode, 0, input.Count)
	for i := 0; i < input.Count; i++ {
		codeValue, err := GenerateRedeemCode()
		if err != nil {
			return nil, err
		}
		code := RedeemCode{
			Code:      codeValue,
			Type:      input.Type,
			Value:     input.Value,
			Status:    StatusUnused,
			ExpiresAt: input.ExpiresAt,
		}
		// 订阅类型专用字段
		if input.Type == RedeemTypeSubscription {
			code.GroupID = input.GroupID
			code.ValidityDays = input.ValidityDays
			if code.ValidityDays <= 0 {
				code.ValidityDays = 30 // 默认30天
			}
		}
		if err := s.redeemCodeRepo.Create(ctx, &code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

func (s *adminServiceImpl) DeleteRedeemCode(ctx context.Context, id int64) error {
	return s.redeemCodeRepo.Delete(ctx, id)
}

func (s *adminServiceImpl) BatchDeleteRedeemCodes(ctx context.Context, ids []int64) (int64, error) {
	var deleted int64
	for _, id := range ids {
		if err := s.redeemCodeRepo.Delete(ctx, id); err == nil {
			deleted++
		}
	}
	return deleted, nil
}

func (s *adminServiceImpl) ExpireRedeemCode(ctx context.Context, id int64) (*RedeemCode, error) {
	code, err := s.redeemCodeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	code.Status = StatusExpired
	if err := s.redeemCodeRepo.Update(ctx, code); err != nil {
		return nil, err
	}
	return code, nil
}
