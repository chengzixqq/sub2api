package service

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type ProbeCoalescingRuntime struct {
	Mode                 string `json:"mode"`
	WindowSeconds        int    `json:"window_seconds"`
	LeaderTimeoutSeconds int    `json:"leader_timeout_seconds"`
	AttemptBudget        int    `json:"attempt_budget"`
}

func normalizeProbeCoalescingMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off", "shadow", "active":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "shadow"
	}
}

func parseProbePositive(raw string, fallback, max int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return fallback
	}
	if max > 0 && v > max {
		return max
	}
	return v
}

func defaultProbeCoalescingRuntime() ProbeCoalescingRuntime {
	return ProbeCoalescingRuntime{Mode: "shadow", WindowSeconds: 60, LeaderTimeoutSeconds: 8, AttemptBudget: 8}
}

func (s *SettingService) GetProbeCoalescingRuntime(ctx context.Context) ProbeCoalescingRuntime {
	d := defaultProbeCoalescingRuntime()
	if s == nil || s.settingRepo == nil {
		return d
	}
	vals, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyProbeCoalescingMode,
		SettingKeyProbeCoalescingWindowSeconds,
		SettingKeyProbeCoalescingLeaderTimeoutSeconds,
		SettingKeyProbeCoalescingAttemptBudget,
	})
	if err != nil {
		return d
	}
	d.Mode = normalizeProbeCoalescingMode(vals[SettingKeyProbeCoalescingMode])
	d.WindowSeconds = parseProbePositive(vals[SettingKeyProbeCoalescingWindowSeconds], d.WindowSeconds, 3600)
	d.LeaderTimeoutSeconds = parseProbePositive(vals[SettingKeyProbeCoalescingLeaderTimeoutSeconds], d.LeaderTimeoutSeconds, 60)
	d.AttemptBudget = parseProbePositive(vals[SettingKeyProbeCoalescingAttemptBudget], d.AttemptBudget, 64)
	return d
}

func (r ProbeCoalescingRuntime) Window() time.Duration {
	return time.Duration(r.WindowSeconds) * time.Second
}
func (r ProbeCoalescingRuntime) LeaderTimeout() time.Duration {
	return time.Duration(r.LeaderTimeoutSeconds) * time.Second
}
