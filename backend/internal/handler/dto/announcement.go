package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type Announcement struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	NotifyMode string `json:"notify_mode"`

	Targeting service.AnnouncementTargeting `json:"targeting"`

	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	CreatedBy *int64 `json:"created_by,omitempty"`
	UpdatedBy *int64 `json:"updated_by,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// ReadStats is only set when the request asked for include_read_stats.
	ReadStats *AnnouncementReadStats `json:"read_stats,omitempty"`
}

type AnnouncementReadStats struct {
	EligibleCount     int64 `json:"eligible_count"`
	ReadCount         int64 `json:"read_count"`
	EligibleReadCount int64 `json:"eligible_read_count"`
}

type AnnouncementReadSummary struct {
	AnnouncementID    int64  `json:"announcement_id"`
	TotalUsers        int64  `json:"total_users"`
	EligibleCount     int64  `json:"eligible_count"`
	ReadCount         int64  `json:"read_count"`
	EligibleReadCount int64  `json:"eligible_read_count"`
	ComputedAt        string `json:"computed_at"`
}

func AnnouncementReadStatsFromService(c service.AnnouncementReadCounts) *AnnouncementReadStats {
	return &AnnouncementReadStats{
		EligibleCount:     c.EligibleCount,
		ReadCount:         c.ReadCount,
		EligibleReadCount: c.EligibleReadCount,
	}
}

func AnnouncementReadSummaryFromService(s *service.AnnouncementReadSummary) *AnnouncementReadSummary {
	if s == nil {
		return nil
	}
	return &AnnouncementReadSummary{
		AnnouncementID:    s.AnnouncementID,
		TotalUsers:        s.TotalUsers,
		EligibleCount:     s.EligibleCount,
		ReadCount:         s.ReadCount,
		EligibleReadCount: s.EligibleReadCount,
		ComputedAt:        s.ComputedAt.UTC().Format(time.RFC3339),
	}
}

type UserAnnouncement struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	NotifyMode string `json:"notify_mode"`

	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	ReadAt *time.Time `json:"read_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func AnnouncementFromService(a *service.Announcement) *Announcement {
	if a == nil {
		return nil
	}
	return &Announcement{
		ID:         a.ID,
		Title:      a.Title,
		Content:    a.Content,
		Status:     a.Status,
		NotifyMode: a.NotifyMode,
		Targeting:  a.Targeting,
		StartsAt:   a.StartsAt,
		EndsAt:     a.EndsAt,
		CreatedBy:  a.CreatedBy,
		UpdatedBy:  a.UpdatedBy,
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
	}
}

func UserAnnouncementFromService(a *service.UserAnnouncement) *UserAnnouncement {
	if a == nil {
		return nil
	}
	return &UserAnnouncement{
		ID:         a.Announcement.ID,
		Title:      a.Announcement.Title,
		Content:    a.Announcement.Content,
		NotifyMode: a.Announcement.NotifyMode,
		StartsAt:   a.Announcement.StartsAt,
		EndsAt:     a.Announcement.EndsAt,
		ReadAt:     a.ReadAt,
		CreatedAt:  a.Announcement.CreatedAt,
		UpdatedAt:  a.Announcement.UpdatedAt,
	}
}
