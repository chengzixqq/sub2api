package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	AnnouncementStatusDraft    = domain.AnnouncementStatusDraft
	AnnouncementStatusActive   = domain.AnnouncementStatusActive
	AnnouncementStatusArchived = domain.AnnouncementStatusArchived
)

const (
	AnnouncementNotifyModeSilent = domain.AnnouncementNotifyModeSilent
	AnnouncementNotifyModePopup  = domain.AnnouncementNotifyModePopup
)

const (
	AnnouncementConditionTypeSubscription = domain.AnnouncementConditionTypeSubscription
	AnnouncementConditionTypeBalance      = domain.AnnouncementConditionTypeBalance
)

const (
	AnnouncementOperatorIn  = domain.AnnouncementOperatorIn
	AnnouncementOperatorGT  = domain.AnnouncementOperatorGT
	AnnouncementOperatorGTE = domain.AnnouncementOperatorGTE
	AnnouncementOperatorLT  = domain.AnnouncementOperatorLT
	AnnouncementOperatorLTE = domain.AnnouncementOperatorLTE
	AnnouncementOperatorEQ  = domain.AnnouncementOperatorEQ
)

var (
	ErrAnnouncementNotFound        = domain.ErrAnnouncementNotFound
	ErrAnnouncementInvalidTarget   = domain.ErrAnnouncementInvalidTarget
	ErrAnnouncementNilInput        = infraerrors.BadRequest("ANNOUNCEMENT_INPUT_REQUIRED", "announcement input is required")
	ErrAnnouncementInvalidTitle    = infraerrors.BadRequest("ANNOUNCEMENT_TITLE_INVALID", "announcement title is invalid")
	ErrAnnouncementContentRequired = infraerrors.BadRequest(
		"ANNOUNCEMENT_CONTENT_REQUIRED",
		"announcement content is required",
	)
	ErrAnnouncementInvalidStatus     = infraerrors.BadRequest("ANNOUNCEMENT_STATUS_INVALID", "announcement status is invalid")
	ErrAnnouncementInvalidNotifyMode = infraerrors.BadRequest(
		"ANNOUNCEMENT_NOTIFY_MODE_INVALID",
		"announcement notify_mode is invalid",
	)
	ErrAnnouncementInvalidSchedule = infraerrors.BadRequest(
		"ANNOUNCEMENT_TIME_RANGE_INVALID",
		"starts_at must be before ends_at",
	)
	ErrAnnouncementInvalidReadStatusFilter = infraerrors.BadRequest(
		"INVALID_READ_STATUS_FILTER",
		"invalid read_state or eligible filter",
	)
)

// Read-status list filter values for AnnouncementReadStatusFilter.ReadState.
const (
	AnnouncementReadStateRead   = "read"
	AnnouncementReadStateUnread = "unread"
)

// AnnouncementReadCounts summarizes who has read an announcement. Only live
// (not soft-deleted) users count; eligibility uses the user's current balance
// and active subscriptions, not a snapshot taken when they read it.
type AnnouncementReadCounts struct {
	TotalUsers        int64
	EligibleCount     int64
	ReadCount         int64
	EligibleReadCount int64
}

type AnnouncementReadSummary struct {
	AnnouncementID int64
	AnnouncementReadCounts
	ComputedAt time.Time
}

// AnnouncementReadStatusFilter narrows the read-status list. ReadState is ""
// (no filter), AnnouncementReadStateRead or AnnouncementReadStateUnread;
// Eligible nil means no eligibility filter.
type AnnouncementReadStatusFilter struct {
	Search    string
	ReadState string
	Eligible  *bool
}

type AnnouncementTargeting = domain.AnnouncementTargeting

type AnnouncementConditionGroup = domain.AnnouncementConditionGroup

type AnnouncementCondition = domain.AnnouncementCondition

type Announcement = domain.Announcement

type AnnouncementListFilters struct {
	Status string
	Search string
}

type AnnouncementRepository interface {
	Create(ctx context.Context, a *Announcement) error
	GetByID(ctx context.Context, id int64) (*Announcement, error)
	Update(ctx context.Context, a *Announcement) error
	Delete(ctx context.Context, id int64) error

	List(ctx context.Context, params pagination.PaginationParams, filters AnnouncementListFilters) ([]Announcement, *pagination.PaginationResult, error)
	ListActive(ctx context.Context, now time.Time) ([]Announcement, error)
}

type AnnouncementReadRepository interface {
	MarkRead(ctx context.Context, announcementID, userID int64, readAt time.Time) error
	GetReadMapByUser(ctx context.Context, userID int64, announcementIDs []int64) (map[int64]time.Time, error)
	GetReadMapByUsers(ctx context.Context, announcementID int64, userIDs []int64) (map[int64]time.Time, error)
	CountByAnnouncementID(ctx context.Context, announcementID int64) (int64, error)

	// SummarizeReads counts live users, those matching targeting, readers, and
	// matching readers of one announcement in a single query.
	SummarizeReads(ctx context.Context, announcementID int64, targeting AnnouncementTargeting) (AnnouncementReadCounts, error)
	// ListUserReadStatus pages live users with their read time and eligibility
	// computed in SQL, so filters, read_at sorting and totals stay exact.
	ListUserReadStatus(
		ctx context.Context,
		announcementID int64,
		targeting AnnouncementTargeting,
		params pagination.PaginationParams,
		filter AnnouncementReadStatusFilter,
	) ([]AnnouncementUserReadStatus, *pagination.PaginationResult, error)
}
