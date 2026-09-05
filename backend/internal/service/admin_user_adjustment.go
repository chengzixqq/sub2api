package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	AdjustmentKindBalance     = "balance"
	AdjustmentKindConcurrency = "concurrency"

	AdjustmentOperationAdd      = "add"
	AdjustmentOperationSubtract = "subtract"
	AdjustmentOperationSet      = "set"
	AdjustmentOperationLegacy   = "legacy"
)

type adminAdjustmentMetadataKey struct{}

// AdminAdjustmentMetadata is captured by the HTTP handler and carried through
// the existing AdminService interface without changing every test double.
type AdminAdjustmentMetadata struct {
	ActionID      uuid.UUID
	Idempotent    bool
	OperatorID    *int64
	OperatorEmail string
	Notes         string
	ClientIP      string
	AuthMethod    string
	RequestID     string
}

func WithAdminAdjustmentMetadata(ctx context.Context, metadata AdminAdjustmentMetadata) context.Context {
	if metadata.ActionID == uuid.Nil {
		metadata.ActionID = uuid.New()
	}
	return context.WithValue(ctx, adminAdjustmentMetadataKey{}, metadata)
}

func AdminAdjustmentMetadataFromContext(ctx context.Context) AdminAdjustmentMetadata {
	if metadata, ok := ctx.Value(adminAdjustmentMetadataKey{}).(AdminAdjustmentMetadata); ok {
		if metadata.ActionID == uuid.Nil {
			metadata.ActionID = uuid.New()
		}
		return metadata
	}
	return AdminAdjustmentMetadata{ActionID: uuid.New()}
}

type AdminUserAdjustmentWrite struct {
	ActionID           uuid.UUID
	Kind               string
	Operation          string
	RequestedValue     *string
	Delta              string
	BeforeValue        *string
	AfterValue         *string
	UserID             int64
	UserEmail          *string
	UserName           *string
	OperatorUserID     *int64
	OperatorEmail      *string
	OperatorName       *string
	Notes              *string
	ClientIP           *string
	AuthMethod         *string
	RequestID          *string
	Source             string
	LegacyRedeemCodeID *int64
	CreatedAt          time.Time
}

type AdminUserAdjustment struct {
	ID                 int64     `json:"id"`
	ActionID           string    `json:"action_id"`
	Kind               string    `json:"kind"`
	Operation          string    `json:"operation"`
	RequestedValue     *string   `json:"requested_value"`
	Delta              string    `json:"delta"`
	BeforeValue        *string   `json:"before_value"`
	AfterValue         *string   `json:"after_value"`
	UserID             *int64    `json:"user_id"`
	UserEmail          *string   `json:"user_email"`
	UserName           *string   `json:"user_name"`
	OperatorUserID     *int64    `json:"operator_user_id"`
	OperatorEmail      *string   `json:"operator_email"`
	OperatorName       *string   `json:"operator_name"`
	Notes              *string   `json:"notes"`
	ClientIP           *string   `json:"client_ip"`
	AuthMethod         *string   `json:"auth_method"`
	RequestID          *string   `json:"request_id"`
	Source             string    `json:"source"`
	LegacyRedeemCodeID *int64    `json:"legacy_redeem_code_id"`
	CreatedAt          time.Time `json:"created_at"`
}

type AdminUserAdjustmentFilter struct {
	Kind      string
	Operation string
	Direction string
	Keyword   string
	Operator  string
	StartTime *time.Time
	EndTime   *time.Time
}

type AdminUserAdjustmentSummary struct {
	RecordCount         int64  `json:"record_count"`
	BalanceIncrease     string `json:"balance_increase"`
	BalanceDecrease     string `json:"balance_decrease"`
	BalanceNet          string `json:"balance_net"`
	ConcurrencyIncrease string `json:"concurrency_increase"`
	ConcurrencyDecrease string `json:"concurrency_decrease"`
	ConcurrencyNet      string `json:"concurrency_net"`
}

type AdminUserAdjustmentRepository interface {
	CreateBatch(ctx context.Context, rows []AdminUserAdjustmentWrite) error
	LockAction(ctx context.Context, actionID uuid.UUID) error
	CountByActionID(ctx context.Context, actionID uuid.UUID) (int, error)
	List(ctx context.Context, filter AdminUserAdjustmentFilter, page, pageSize int) ([]AdminUserAdjustment, AdminUserAdjustmentSummary, error)
	Stream(ctx context.Context, filter AdminUserAdjustmentFilter, consume func(AdminUserAdjustment) error) error
}

type AdminUserAdjustmentService struct {
	repo AdminUserAdjustmentRepository
}

func NewAdminUserAdjustmentService(repo AdminUserAdjustmentRepository) *AdminUserAdjustmentService {
	return &AdminUserAdjustmentService{repo: repo}
}

func (s *AdminUserAdjustmentService) List(ctx context.Context, filter AdminUserAdjustmentFilter, page, pageSize int) ([]AdminUserAdjustment, AdminUserAdjustmentSummary, error) {
	return s.repo.List(ctx, filter, page, pageSize)
}

func (s *AdminUserAdjustmentService) Stream(ctx context.Context, filter AdminUserAdjustmentFilter, consume func(AdminUserAdjustment) error) error {
	return s.repo.Stream(ctx, filter, consume)
}

func adjustmentStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func adjustmentOperationForDelta(requestedOperation string, deltaSign int) string {
	if requestedOperation == AdjustmentOperationSet {
		return AdjustmentOperationSet
	}
	if deltaSign < 0 {
		return AdjustmentOperationSubtract
	}
	return AdjustmentOperationAdd
}
