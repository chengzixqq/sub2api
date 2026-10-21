package service

import (
	"context"
	"maps"
)

// RedeemCodeStats counts the redeem codes a list filter selects. The four status counts use the list's meaning of
// status (an unused code past its expires_at counts as expired; disabled ignores expiry) and add up to Total, as long
// as no row carries a status outside those four. ValueDistributed is the net face value of used balance codes
// (admin_balance adjustment records excluded), rounded to 8 decimals.
type RedeemCodeStats struct {
	Total            int64
	Unused           int64
	Used             int64
	Expired          int64
	Disabled         int64
	ByType           map[string]int64
	ValueDistributed float64
}

// RedeemCodeStatsRepository is implemented by repositories that can count codes with the list's filter. It ignores
// filter.Status: the counts are the status breakdown itself.
type RedeemCodeStatsRepository interface {
	StatsWithFilters(ctx context.Context, filter RedeemCodeListFilter) (*RedeemCodeStats, error)
}

// GetRedeemCodeStats counts the codes the admin list would return for filter, by status and type. filter.Status is
// dropped. ByType always carries every type the list filters accept (0 when absent), plus any other type found.
func (s *adminServiceImpl) GetRedeemCodeStats(ctx context.Context, filter RedeemCodeListFilter) (*RedeemCodeStats, error) {
	statsRepo, ok := s.redeemCodeRepo.(RedeemCodeStatsRepository)
	if !ok {
		return nil, ErrServiceUnavailable
	}
	filter.Status = ""
	stats, err := statsRepo.StatsWithFilters(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := *stats
	out.ByType = make(map[string]int64, len(redeemCodeListTypes)+len(stats.ByType))
	for codeType := range redeemCodeListTypes {
		out.ByType[codeType] = 0
	}
	maps.Copy(out.ByType, stats.ByType)
	return &out, nil
}
