package service

import (
	"context"
	"sort"
	"time"
)

type observationPreviewKey struct{}

// WithChannelMonitorPreview is only applied after owner authorization at the API boundary.
func WithChannelMonitorPreview(ctx context.Context) context.Context {
	return context.WithValue(ctx, observationPreviewKey{}, true)
}

func observationSourceForGroup(cfg ChannelMonitorObservationConfig, groupID int64, preview bool) string {
	if preview || (cfg.Mode == "live" && (len(cfg.LiveGroupIDs) == 0 || observationHasID(cfg.LiveGroupIDs, groupID))) {
		return "compact"
	}
	return "legacy"
}

type ObservationCurrentStatus struct {
	State     string     `json:"state"`
	Source    string     `json:"source"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	Reason    string     `json:"reason"`
}

type ObservationProbeStatus struct {
	Status              string     `json:"status"`
	LastCheckedAt       *time.Time `json:"last_checked_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

type ObservationQuotaStatus struct {
	Status    string     `json:"status"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func observationCurrentStatus(facts []ChannelMonitorObservationFact, cfg ChannelMonitorObservationConfig, platform string, group int64, model string, now time.Time, collectorState string) ObservationCurrentStatus {
	out := ObservationCurrentStatus{State: "unknown", Source: "none", Reason: "no_samples"}
	if collectorState != "" && collectorState != "healthy" {
		out.Reason = "collection_unavailable"
		return out
	}
	var recent ChannelMonitorObservationFact
	for _, fact := range facts {
		if fact.Source != "" && fact.Source != "traffic" {
			continue
		}
		if fact.SuccessRequests+fact.ChannelErrors+fact.ClientErrors+fact.CancelledRequests+fact.UnknownRequests == 0 {
			continue
		}
		at := fact.BucketStart.UTC()
		if out.UpdatedAt == nil || at.After(*out.UpdatedAt) {
			copy := at
			out.UpdatedAt = &copy
		}
		if !at.Before(now.Add(-5*time.Minute)) && at.Before(now) {
			recent.Add(fact)
		}
	}
	if out.UpdatedAt != nil && out.UpdatedAt.Before(now.Add(-5*time.Minute)) {
		out.State, out.Reason = "stale", "traffic_expired"
		return out
	}
	m, health := observationMetrics(recent, cfg, platform, group, model, 5*time.Minute, true)
	switch m.SampleState {
	case "sufficient":
		out.State, out.Source, out.Reason = health.Reliability, "traffic", "recent_traffic"
		if health.Latency == "warning" || health.Latency == "critical" {
			if out.State == "healthy" {
				out.State, out.Reason = "warning", "high_latency"
			}
		}
	case "insufficient":
		out.Reason = "insufficient_samples"
	}
	return out
}

func (s *ChannelMonitorOverviewService) SetModelCatalogue(reader func(context.Context, *Group) ([]string, error)) {
	s.modelCatalogue = reader
}

func observationLegacyMetrics(m ChannelMonitorV2Metric, cfg ChannelMonitorObservationConfig, platform string, group int64, model string, admin bool) (ObservationMetrics, ObservationHealth) {
	f := ChannelMonitorObservationFact{SuccessRequests: m.SuccessRequests, ChannelErrors: m.ErrorRequests,
		InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, CacheCreationTokens: m.CacheCreationTokens, CacheReadTokens: m.CacheReadTokens}
	out, health := observationMetrics(f, cfg, platform, group, model, time.Minute, admin)
	out.TTFT, out.Duration = m.TTFT, m.Duration
	if admin {
		out.RPM, out.TPM = m.RPM, m.TPM
	} else {
		out.TTFT.SampleCount, out.Duration.SampleCount = 0, 0
	}
	if m.TTFT.P50Ms != nil && m.TTFT.SampleCount >= cfg.MinimumSample {
		warning, critical := cfg.LatencyThresholds(platform, group, model)
		switch {
		case *m.TTFT.P50Ms >= critical:
			health.Latency = "critical"
		case *m.TTFT.P50Ms >= warning:
			health.Latency = "warning"
		default:
			health.Latency = "healthy"
		}
	}
	return out, health
}

func legacyObservationBuckets(points []ChannelMonitorV2TrendPoint, cfg ChannelMonitorObservationConfig, platform string, group int64, model string, admin bool) []ObservationBucket {
	out := make([]ObservationBucket, 0, len(points))
	for _, point := range points {
		m, h := observationLegacyMetrics(point.Metrics, cfg, platform, group, model, admin)
		out = append(out, ObservationBucket{BucketStart: point.BucketStart, Metrics: m, Health: h})
	}
	return out
}

func legacyObservationFact(m ChannelMonitorV2Metric, at time.Time, group int64, model string) ChannelMonitorObservationFact {
	return ChannelMonitorObservationFact{Source: "traffic", BucketStart: at, GroupID: group, Model: model,
		SuccessRequests: m.SuccessRequests, ChannelErrors: m.ErrorRequests, InputTokens: m.InputTokens,
		OutputTokens: m.OutputTokens, CacheReadTokens: m.CacheReadTokens, CacheCreationTokens: m.CacheCreationTokens}
}

func observationModelVisible(g Group, catalogue map[int64]map[string]bool, model string) bool {
	return g.ModelAllowlist.Allows(model) && (catalogue[g.ID] == nil || catalogue[g.ID][model])
}

func (s *ChannelMonitorOverviewService) legacyOverview(ctx context.Context, f ChannelMonitorV2Filter, legacy ChannelMonitorV2Config, policy ChannelMonitorObservationConfig, groups map[int64]Group, catalogue map[int64]map[string]bool, admin bool) ([]ObservationChannel, ObservationCoverage, error) {
	matrix, err := s.legacy.GetMatrix(ctx, f, legacy, ChannelMonitorV2GroupByPlatformGroupModel, true)
	if err != nil {
		return nil, ObservationCoverage{}, err
	}
	coverage := ObservationCoverage{ChannelMonitorV2Coverage: matrix.Coverage, State: "partial", CollectorState: "healthy", DetailRetentionHours: 24, UnsupportedProtocols: []string{}, GapReasons: []string{"legacy_log_semantics"}}
	if matrix.Coverage.DataThrough.IsZero() || time.Since(matrix.Coverage.DataThrough) > 2*time.Minute {
		coverage.CollectorState = "stale"
	}
	byGroup := map[int64][]ChannelMonitorObservationFact{}
	models := map[int64][]ObservationModel{}
	for _, row := range matrix.Items {
		if row.GroupID == nil || !observationHasID(f.AllowedGroupIDs, *row.GroupID) {
			continue
		}
		g, ok := groups[*row.GroupID]
		if !ok || !observationModelVisible(g, catalogue, row.Model) {
			continue
		}
		m, h := observationLegacyMetrics(row.Metrics, policy, g.Platform, g.ID, row.Model, admin)
		models[g.ID] = append(models[g.ID], ObservationModel{Model: row.Model, Metrics: m, Health: h, Buckets: legacyObservationBuckets(row.Buckets, policy, g.Platform, g.ID, row.Model, admin)})
		for _, bucket := range row.Buckets {
			byGroup[g.ID] = append(byGroup[g.ID], legacyObservationFact(bucket.Metrics, bucket.BucketStart, g.ID, row.Model))
		}
	}
	rows := make([]ObservationChannel, 0, len(f.AllowedGroupIDs))
	for _, id := range f.AllowedGroupIDs {
		g := groups[id]
		m, h, buckets := observationSeries(byGroup[id], policy, g.Platform, id, "", f, admin)
		row := ObservationChannel{Source: "legacy", Platform: g.Platform, GroupID: id, GroupName: g.Name, Metrics: m, Traffic: m, Health: h, Buckets: buckets, Models: models[id]}
		// Legacy percentiles cannot be merged. Carry the worst visible-model
		// latency state without inventing an aggregate P50 or histogram.
		bucketLatency := map[time.Time]string{}
		for _, model := range row.Models {
			row.Health.Latency = observationWorstLatency(row.Health.Latency, model.Health.Latency)
			for _, bucket := range model.Buckets {
				at := bucket.BucketStart.UTC().Truncate(f.Bucket)
				bucketLatency[at] = observationWorstLatency(bucketLatency[at], bucket.Health.Latency)
			}
		}
		for i := range row.Buckets {
			row.Buckets[i].Health.Latency = observationWorstLatency(row.Buckets[i].Health.Latency, bucketLatency[row.Buckets[i].BucketStart])
		}
		if row.Models == nil {
			row.Models = []ObservationModel{}
		}
		for model := range catalogue[id] {
			if len(f.Models) > 0 && !observationHasString(f.Models, model) {
				continue
			}
			found := false
			for _, item := range row.Models {
				if item.Model == model {
					found = true
					break
				}
			}
			if !found {
				mm, mh, mb := observationSeries(nil, policy, g.Platform, id, model, f, admin)
				row.Models = append(row.Models, ObservationModel{Model: model, Metrics: mm, Health: mh, Buckets: mb})
			}
		}
		sort.Slice(row.Models, func(i, j int) bool { return row.Models[i].Model < row.Models[j].Model })
		if admin {
			rate := g.RateMultiplier * g.PeakMultiplierAt(time.Now())
			row.RateMultiplier = &rate
		}
		rows = append(rows, row)
	}
	return rows, coverage, nil
}

func (s *ChannelMonitorOverviewService) SetProbeService(probes *ChannelMonitorProbeService) {
	s.probes = probes
}

func observationWorstLatency(a, b string) string {
	rank := map[string]int{"healthy": 1, "warning": 2, "critical": 3}
	if rank[b] > rank[a] {
		return b
	}
	if a == "" {
		return "unknown"
	}
	return a
}

func applyObservationLatency(status *ObservationCurrentStatus, latency string) {
	if status.Source == "traffic" && status.State == "healthy" && (latency == "warning" || latency == "critical") {
		status.State, status.Reason = "warning", "high_latency"
	}
}

func (s *ChannelMonitorOverviewService) fillCurrentStatuses(ctx context.Context, out *ObservationOverview, f ChannelMonitorV2Filter, cfg ChannelMonitorObservationConfig, groups map[int64]Group, catalogue map[int64]map[string]bool, admin bool, viewer int64, refresh bool) error {
	now := time.Now().UTC().Truncate(time.Minute)
	f.Start, f.End, f.Bucket = now.Add(-5*time.Minute), now, time.Minute
	byGroup := map[int64][]ChannelMonitorObservationFact{}
	legacyLatency := map[int64]map[string]string{}
	collectorStates := map[string]string{"compact": out.Coverage.CollectorState, "legacy": "healthy"}
	if len(f.AllowedGroupIDs) > 0 {
		snapshot, err := s.query(ctx, f, cfg.Version, "current", viewer, refresh)
		if err != nil {
			return err
		}
		collectorStates["compact"] = snapshot.Coverage.CollectorState
		for _, fact := range snapshot.Facts {
			if observationModelVisible(groups[fact.GroupID], catalogue, fact.Model) {
				byGroup[fact.GroupID] = append(byGroup[fact.GroupID], fact)
			}
		}
	}
	if !cfg.Enabled {
		collectorStates["compact"] = "disabled"
	}
	allIDs, legacyIDs := []int64{}, []int64{}
	for _, row := range out.Items {
		allIDs = append(allIDs, row.GroupID)
		if row.Source == "legacy" {
			legacyIDs = append(legacyIDs, row.GroupID)
		}
	}
	if len(legacyIDs) > 0 {
		for _, id := range legacyIDs {
			delete(byGroup, id)
		}
		legacy, err := s.legacy.GetConfig(ctx)
		if err != nil {
			return err
		}
		f.AllowedGroupIDs = legacyIDs
		matrix, err := s.legacy.GetMatrix(ctx, f, *legacy, ChannelMonitorV2GroupByPlatformGroupModel, true)
		if err != nil {
			return err
		}
		if matrix.Coverage.DataThrough.IsZero() || time.Since(matrix.Coverage.DataThrough) > 2*time.Minute {
			collectorStates["legacy"] = "stale"
		}
		for _, row := range matrix.Items {
			if row.GroupID != nil && observationHasID(legacyIDs, *row.GroupID) && observationModelVisible(groups[*row.GroupID], catalogue, row.Model) {
				byGroup[*row.GroupID] = append(byGroup[*row.GroupID], legacyObservationFact(row.Metrics, now.Add(-time.Minute), *row.GroupID, row.Model))
				if legacyLatency[*row.GroupID] == nil {
					legacyLatency[*row.GroupID] = map[string]string{}
				}
				_, health := observationLegacyMetrics(row.Metrics, cfg, groups[*row.GroupID].Platform, *row.GroupID, row.Model, true)
				legacyLatency[*row.GroupID][row.Model] = observationWorstLatency(legacyLatency[*row.GroupID][row.Model], health.Latency)
			}
		}
	}
	var probes ChannelMonitorProbeEvidence
	var quotas map[int64]ChannelMonitorQuotaSummary
	if s.probes != nil && len(allIDs) > 0 {
		var err error
		probes, err = s.probes.Evidence(ctx, allIDs, time.Now().UTC())
		if err != nil {
			out.Coverage.GapReasons = append(out.Coverage.GapReasons, "probe_unavailable")
		}
		quotas, err = s.probes.QuotaSummaries(ctx, allIDs, time.Now().UTC())
		if err != nil {
			out.Coverage.GapReasons = append(out.Coverage.GapReasons, "quota_unavailable")
		}
	}
	for i := range out.Items {
		row := &out.Items[i]
		facts := byGroup[row.GroupID]
		state := collectorStates[row.Source]
		row.CurrentStatus = observationCurrentStatus(facts, cfg, row.Platform, row.GroupID, "", now, state)
		if row.Source == "legacy" {
			for _, latency := range legacyLatency[row.GroupID] {
				applyObservationLatency(&row.CurrentStatus, latency)
			}
		}
		for j := range row.Models {
			model := &row.Models[j]
			mfacts := []ChannelMonitorObservationFact{}
			for _, fact := range facts {
				if fact.Model == model.Model {
					mfacts = append(mfacts, fact)
				}
			}
			model.CurrentStatus = observationCurrentStatus(mfacts, cfg, row.Platform, row.GroupID, model.Model, now, state)
			if row.Source == "legacy" {
				applyObservationLatency(&model.CurrentStatus, legacyLatency[row.GroupID][model.Model])
			}
			model.Probe = observationProbeEvidence(probes, row.GroupID, model.Model, time.Now().UTC())
			applyObservationProbe(&model.CurrentStatus, model.Probe)
		}
		row.Probe = observationGroupProbe(row.Models)
		applyObservationProbe(&row.CurrentStatus, row.Probe)
		row.Quota = &ObservationQuotaStatus{Status: "unknown"}
		if q, ok := quotas[row.GroupID]; ok {
			row.Quota = &ObservationQuotaStatus{Status: q.State, UpdatedAt: q.UpdatedAt}
		}
	}
	return nil
}

func observationProbeEvidence(evidence ChannelMonitorProbeEvidence, group int64, model string, now time.Time) *ObservationProbeStatus {
	runsByTarget := map[int64][]ChannelMonitorProbeRun{}
	for _, run := range evidence.Runs {
		if run.GroupID == group && (model == "" || run.Model == model) && run.CompletedAt != nil && !run.CompletedAt.Before(now.Add(-20*time.Minute)) {
			runsByTarget[run.TargetID] = append(runsByTarget[run.TargetID], run)
		}
	}
	rank := map[string]int{"unknown": 0, "healthy": 1, "warning": 2, "critical": 3}
	var out *ObservationProbeStatus
	for _, target := range evidence.Targets {
		if !target.Enabled || target.GroupID != group || (model != "" && target.Model != model) {
			continue
		}
		selected := runsByTarget[target.ID]
		if len(selected) == 0 {
			continue
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].CompletedAt.After(*selected[j].CompletedAt) })
		latest := selected[0]
		candidate := &ObservationProbeStatus{Status: "unknown", LastCheckedAt: latest.CompletedAt}
		if latest.Success != nil && latest.AccountID != nil {
			if *latest.Success {
				candidate.Status = "healthy"
			} else {
				candidate.Status = "warning"
				for _, run := range selected {
					if run.Success == nil || *run.Success {
						break
					}
					candidate.ConsecutiveFailures++
				}
				if candidate.ConsecutiveFailures >= 2 {
					candidate.Status = "critical"
				}
			}
		}
		if out == nil || rank[candidate.Status] > rank[out.Status] || (rank[candidate.Status] == rank[out.Status] && candidate.LastCheckedAt.After(*out.LastCheckedAt)) {
			out = candidate
		}
	}
	return out
}

func applyObservationProbe(status *ObservationCurrentStatus, probe *ObservationProbeStatus) {
	if status.Source == "traffic" || status.Reason == "collection_unavailable" || probe == nil || probe.Status == "unknown" {
		return
	}
	status.State, status.Source, status.UpdatedAt, status.Reason = probe.Status, "probe", probe.LastCheckedAt, "recent_probe"
}

func observationGroupProbe(models []ObservationModel) *ObservationProbeStatus {
	if len(models) == 0 {
		return nil
	}
	complete := true
	var out *ObservationProbeStatus
	rank := map[string]int{"healthy": 1, "warning": 2, "critical": 3}
	for _, model := range models {
		probe := model.Probe
		if probe == nil || rank[probe.Status] == 0 {
			complete = false
			continue
		}
		if out == nil || rank[probe.Status] > rank[out.Status] {
			copy := *probe
			out = &copy
		}
	}
	if !complete && (out == nil || out.Status == "healthy") {
		return nil
	}
	return out
}
