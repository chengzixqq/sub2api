package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// activeSubscriptionGroupIDs mirrors ListActiveByUserID (status active and not
// yet expired) over subscriptions that ListWithFilters already batch-loaded;
// that preload filters on status only, so expiry is checked here.
func activeSubscriptionGroupIDs(subs []UserSubscription) map[int64]struct{} {
	out := make(map[int64]struct{}, len(subs))
	for i := range subs {
		if subs[i].IsActive() {
			out[subs[i].GroupID] = struct{}{}
		}
	}
	return out
}

// normalizeAnnouncementReadState trims and lower-cases read_state; anything
// other than "", read or unread is ErrAnnouncementInvalidReadStatusFilter.
func normalizeAnnouncementReadState(raw string) (string, error) {
	state := strings.ToLower(strings.TrimSpace(raw))
	switch state {
	case "", AnnouncementReadStateRead, AnnouncementReadStateUnread:
		return state, nil
	default:
		return "", ErrAnnouncementInvalidReadStatusFilter
	}
}

// ListUserReadStatusFiltered serves GET read-status. Without read_state,
// eligible or sort_by=read_at it keeps the original ListWithFilters path so the
// Web response is unchanged; with any of them it runs the SQL list in which
// eligibility, filters, totals and read_at ordering are computed together.
func (s *AnnouncementService) ListUserReadStatusFiltered(
	ctx context.Context,
	announcementID int64,
	params pagination.PaginationParams,
	filter AnnouncementReadStatusFilter,
) ([]AnnouncementUserReadStatus, *pagination.PaginationResult, error) {
	state, err := normalizeAnnouncementReadState(filter.ReadState)
	if err != nil {
		return nil, nil, err
	}
	filter.ReadState = state
	filter.Search = strings.TrimSpace(filter.Search)

	sortByReadAt := strings.EqualFold(strings.TrimSpace(params.SortBy), "read_at")
	if state == "" && filter.Eligible == nil && !sortByReadAt {
		return s.ListUserReadStatus(ctx, announcementID, params, filter.Search)
	}

	ann, err := s.announcementRepo.GetByID(ctx, announcementID)
	if err != nil {
		return nil, nil, err
	}
	out, page, err := s.readRepo.ListUserReadStatus(ctx, announcementID, ann.Targeting, params, filter)
	if err != nil {
		return nil, nil, fmt.Errorf("list user read status: %w", err)
	}
	return out, page, nil
}

// GetReadSummary computes read counts for one announcement (drafts included).
func (s *AnnouncementService) GetReadSummary(ctx context.Context, announcementID int64) (*AnnouncementReadSummary, error) {
	ann, err := s.announcementRepo.GetByID(ctx, announcementID)
	if err != nil {
		return nil, err
	}
	counts, err := s.readRepo.SummarizeReads(ctx, ann.ID, ann.Targeting)
	if err != nil {
		return nil, fmt.Errorf("summarize announcement reads: %w", err)
	}
	return &AnnouncementReadSummary{
		AnnouncementID:         ann.ID,
		AnnouncementReadCounts: counts,
		ComputedAt:             time.Now().UTC(),
	}, nil
}

// ReadStatsFor summarizes each announcement (one query apiece), keyed by ID.
func (s *AnnouncementService) ReadStatsFor(ctx context.Context, anns []Announcement) (map[int64]AnnouncementReadCounts, error) {
	out := make(map[int64]AnnouncementReadCounts, len(anns))
	for i := range anns {
		counts, err := s.readRepo.SummarizeReads(ctx, anns[i].ID, anns[i].Targeting)
		if err != nil {
			return nil, fmt.Errorf("summarize announcement %d reads: %w", anns[i].ID, err)
		}
		out[anns[i].ID] = counts
	}
	return out, nil
}
