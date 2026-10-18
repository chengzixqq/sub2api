package service

import (
	"context"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	quickTopupMaxLimit = 20
	quickTopupMaxDays  = 365
)

// QuickTopupQuery selects the quick top-up avatar strip. The repository also
// receives the global low-balance settings resolved by the service.
type QuickTopupQuery struct {
	RecentLimit     int
	LowLimit        int
	RecentDays      int
	GlobalEnabled   bool
	GlobalThreshold float64
}

// QuickTopupRecentUser is a user whose balance an admin adjusted recently.
type QuickTopupRecentUser struct {
	UserID         int64     `json:"user_id"`
	Email          string    `json:"email"`
	Username       string    `json:"username"`
	Status         string    `json:"status"`
	Balance        float64   `json:"balance"`
	LastAdjustedAt time.Time `json:"last_adjusted_at"`
	LastDelta      string    `json:"last_delta"`
	LastOperation  string    `json:"last_operation"`
}

// QuickTopupLowBalanceUser is an active user below their effective threshold.
type QuickTopupLowBalanceUser struct {
	UserID             int64   `json:"user_id"`
	Email              string  `json:"email"`
	Username           string  `json:"username"`
	Status             string  `json:"status"`
	Balance            float64 `json:"balance"`
	EffectiveThreshold float64 `json:"effective_threshold"`
	ThresholdSource    string  `json:"threshold_source"` // user | global
	ThresholdType      string  `json:"threshold_type"`   // fixed | percentage
}

type QuickTopupResult struct {
	Recent            []QuickTopupRecentUser     `json:"recent"`
	LowBalance        []QuickTopupLowBalanceUser `json:"low_balance"`
	LowBalanceEnabled bool                       `json:"low_balance_enabled"`
}

// QuickTopupRepository reads both lists of the avatar strip.
type QuickTopupRepository interface {
	QuickTopup(ctx context.Context, q QuickTopupQuery) (*QuickTopupResult, error)
}

type balanceNotifySettingsReader interface {
	GetMultiple(ctx context.Context, keys []string) (map[string]string, error)
}

// SetBalanceNotifySettings supplies the global low-balance settings used by QuickTopup.
func (s *AdminUserAdjustmentService) SetBalanceNotifySettings(settings balanceNotifySettingsReader) {
	if s != nil {
		s.settings = settings
	}
}

func ValidateQuickTopupQuery(q QuickTopupQuery) error {
	if q.RecentLimit < 0 || q.RecentLimit > quickTopupMaxLimit || q.LowLimit < 0 || q.LowLimit > quickTopupMaxLimit {
		return infraerrors.BadRequest("INVALID_LIMIT", "recent_limit and low_limit must be between 0 and 20")
	}
	if q.RecentDays < 1 || q.RecentDays > quickTopupMaxDays {
		return infraerrors.BadRequest("INVALID_RECENT_DAYS", "recent_days must be between 1 and 365")
	}
	return nil
}

// QuickTopup returns recently adjusted users (deduplicated, with the current
// balance) and active users whose balance is below their effective low-balance
// threshold: the user's own threshold first, otherwise the global one;
// percentage thresholds are taken of total_recharged.
func (s *AdminUserAdjustmentService) QuickTopup(ctx context.Context, q QuickTopupQuery) (*QuickTopupResult, error) {
	if err := ValidateQuickTopupQuery(q); err != nil {
		return nil, err
	}
	repo, ok := s.repo.(QuickTopupRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("QUICK_TOPUP_UNAVAILABLE", "quick top-up is not supported by the adjustment repository")
	}
	q.GlobalEnabled, q.GlobalThreshold = s.globalLowBalance(ctx)
	result, err := repo.QuickTopup(ctx, q)
	if err != nil {
		return nil, err
	}
	result.LowBalanceEnabled = q.GlobalEnabled
	return result, nil
}

func (s *AdminUserAdjustmentService) globalLowBalance(ctx context.Context) (enabled bool, threshold float64) {
	if s.settings == nil {
		return false, 0
	}
	values, err := s.settings.GetMultiple(ctx, []string{SettingKeyBalanceLowNotifyEnabled, SettingKeyBalanceLowNotifyThreshold})
	if err != nil {
		return false, 0
	}
	enabled = values[SettingKeyBalanceLowNotifyEnabled] == "true"
	if v, err := strconv.ParseFloat(values[SettingKeyBalanceLowNotifyThreshold], 64); err == nil && v > 0 {
		threshold = v
	}
	return enabled, threshold
}

// ProvideAdminUserAdjustmentService wires the adjustment service with the
// setting repository used for the global low-balance threshold.
func ProvideAdminUserAdjustmentService(repo AdminUserAdjustmentRepository, settingRepo SettingRepository) *AdminUserAdjustmentService {
	svc := NewAdminUserAdjustmentService(repo)
	if settingRepo != nil {
		svc.SetBalanceNotifySettings(settingRepo)
	}
	return svc
}
