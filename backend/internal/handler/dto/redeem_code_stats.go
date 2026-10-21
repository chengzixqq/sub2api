package dto

import (
	"maps"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// redeemCodeStatsLegacyTrialType is a type the stats response always carried; no code has it, so it stays 0 for old
// clients.
const redeemCodeStatsLegacyTrialType = "trial"

// AdminRedeemCodeStats is GET /admin/redeem-codes/stats. active_codes equals by_status.unused, used_codes and
// expired_codes equal their by_status entries, and the four by_status entries add up to total_codes.
type AdminRedeemCodeStats struct {
	TotalCodes            int64                      `json:"total_codes"`
	ActiveCodes           int64                      `json:"active_codes"`
	UsedCodes             int64                      `json:"used_codes"`
	ExpiredCodes          int64                      `json:"expired_codes"`
	TotalValueDistributed float64                    `json:"total_value_distributed"`
	ByType                map[string]int64           `json:"by_type"`
	ByStatus              AdminRedeemCodeStatusCount `json:"by_status"`
}

// AdminRedeemCodeStatusCount counts codes by the status the admin list shows (an unused code past expires_at is
// expired).
type AdminRedeemCodeStatusCount struct {
	Unused   int64 `json:"unused"`
	Used     int64 `json:"used"`
	Expired  int64 `json:"expired"`
	Disabled int64 `json:"disabled"`
}

func RedeemCodeStatsFromServiceAdmin(s *service.RedeemCodeStats) AdminRedeemCodeStats {
	if s == nil {
		s = &service.RedeemCodeStats{}
	}
	byType := make(map[string]int64, len(s.ByType)+1)
	byType[redeemCodeStatsLegacyTrialType] = 0
	maps.Copy(byType, s.ByType)
	return AdminRedeemCodeStats{
		TotalCodes:            s.Total,
		ActiveCodes:           s.Unused,
		UsedCodes:             s.Used,
		ExpiredCodes:          s.Expired,
		TotalValueDistributed: s.ValueDistributed,
		ByType:                byType,
		ByStatus: AdminRedeemCodeStatusCount{
			Unused:   s.Unused,
			Used:     s.Used,
			Expired:  s.Expired,
			Disabled: s.Disabled,
		},
	}
}
