package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

type ObservationMetrics struct {
	SuccessRequests        int64                   `json:"success_requests"`
	ChannelErrors          int64                   `json:"channel_errors"`
	ClientErrors           int64                   `json:"client_errors"`
	CancelledRequests      int64                   `json:"cancelled_requests"`
	UnknownRequests        int64                   `json:"unknown_requests"`
	RequestCount           int64                   `json:"request_count"`
	SuccessRate            *float64                `json:"success_rate"`
	ReliabilityRate        *float64                `json:"reliability_rate"`
	SampleState            string                  `json:"sample_state"`
	CacheRate              *float64                `json:"cache_rate"`
	TTFT                   ChannelMonitorV2Latency `json:"ttft"`
	Duration               ChannelMonitorV2Latency `json:"duration"`
	RPM                    float64                 `json:"rpm"`
	TPM                    float64                 `json:"tpm"`
	RetryRecoveredRequests int64                   `json:"retry_recovered_requests"`
	AttemptCount           int64                   `json:"attempt_count"`
	PhaseAvgMs             map[string]float64      `json:"phase_avg_ms,omitempty"`
	ErrorCategories        map[string]int64        `json:"error_categories,omitempty"`
}

type ObservationHealth struct {
	Reliability string `json:"reliability"`
	Latency     string `json:"latency"`
}

type ObservationBucket struct {
	BucketStart time.Time          `json:"bucket_start"`
	Metrics     ObservationMetrics `json:"metrics"`
	Health      ObservationHealth  `json:"health"`
}

type ObservationModel struct {
	Model   string              `json:"model"`
	Metrics ObservationMetrics  `json:"metrics"`
	Health  ObservationHealth   `json:"health"`
	Buckets []ObservationBucket `json:"buckets"`
}

type ObservationChannel struct {
	Platform       string              `json:"platform"`
	GroupID        int64               `json:"group_id"`
	GroupName      string              `json:"group_name"`
	RateMultiplier *float64            `json:"rate_multiplier,omitempty"`
	Metrics        ObservationMetrics  `json:"metrics"`
	Health         ObservationHealth   `json:"health"`
	Buckets        []ObservationBucket `json:"buckets"`
	Models         []ObservationModel  `json:"models"`
}

type ObservationCoverage struct {
	ChannelMonitorV2Coverage
	State                string     `json:"state"`
	SourceStartedAt      *time.Time `json:"source_started_at,omitempty"`
	DetailRetentionHours int        `json:"detail_retention_hours"`
	UnsupportedProtocols []string   `json:"unsupported_protocols"`
	InFlight             *int64     `json:"in_flight,omitempty"`
}

type ObservationOverview struct {
	ContractVersion int                        `json:"contract_version"`
	Source          string                     `json:"source"`
	Mode            string                     `json:"mode"`
	Coverage        ObservationCoverage        `json:"coverage"`
	Dimensions      ChannelMonitorV2Dimensions `json:"dimensions"`
	Items           []ObservationChannel       `json:"items"`
}

func observationMetrics(f ChannelMonitorObservationFact, cfg ChannelMonitorObservationConfig, platform string, group int64, model string, window time.Duration, admin bool) (ObservationMetrics, ObservationHealth) {
	classified := f.SuccessRequests + f.ChannelErrors + f.ClientErrors + f.CancelledRequests
	eligible := f.SuccessRequests + f.ChannelErrors
	m := ObservationMetrics{SuccessRequests: f.SuccessRequests, ChannelErrors: f.ChannelErrors, ClientErrors: f.ClientErrors, CancelledRequests: f.CancelledRequests, UnknownRequests: f.UnknownRequests,
		RequestCount: classified + f.UnknownRequests, SampleState: "no_samples", RetryRecoveredRequests: f.RetryRecoveredRequests, AttemptCount: f.AttemptCount,
		TTFT: ChannelMonitorObservationLatency(f.TTFTSumMs, f.TTFTCount, f.TTFTHistogram), Duration: ChannelMonitorObservationLatency(f.DurationSumMs, f.DurationCount, f.DurationHistogram)}
	h := ObservationHealth{Reliability: "unknown", Latency: "unknown"}
	if classified > 0 {
		v := float64(f.SuccessRequests) / float64(classified)
		m.SuccessRate = &v
	}
	if eligible > 0 {
		v := float64(f.SuccessRequests) / float64(eligible)
		m.ReliabilityRate = &v
		m.SampleState = "insufficient"
	}
	if eligible >= cfg.MinimumSample {
		m.SampleState = "sufficient"
		switch {
		case *m.ReliabilityRate >= cfg.HealthyReliability:
			h.Reliability = "healthy"
		case *m.ReliabilityRate >= cfg.WarningReliability:
			h.Reliability = "warning"
		default:
			h.Reliability = "critical"
		}
	}
	if m.RequestCount > 0 && eligible == 0 {
		m.SampleState = "insufficient"
	}
	if f.TTFTCount >= cfg.MinimumSample && m.TTFT.P50Ms != nil {
		warning, critical := cfg.LatencyThresholds(platform, group, model)
		switch {
		case *m.TTFT.P50Ms >= critical:
			h.Latency = "critical"
		case *m.TTFT.P50Ms >= warning:
			h.Latency = "warning"
		default:
			h.Latency = "healthy"
		}
	}
	if denom := f.InputTokens + f.CacheReadTokens + f.CacheCreationTokens; denom > 0 {
		v := float64(f.CacheReadTokens) / float64(denom)
		m.CacheRate = &v
	}
	if admin {
		if window > 0 {
			m.RPM = float64(m.RequestCount) / window.Minutes()
			m.TPM = float64(f.InputTokens+f.OutputTokens+f.CacheReadTokens+f.CacheCreationTokens) / window.Minutes()
		}
		m.PhaseAvgMs = map[string]float64{}
		for k, v := range f.PhaseSumMs {
			if f.PhaseCounts[k] > 0 {
				m.PhaseAvgMs[k] = float64(v) / float64(f.PhaseCounts[k])
			}
		}
		m.ErrorCategories = f.ErrorCategories
	} else {
		m.SuccessRequests = 0
		m.ChannelErrors = 0
		m.ClientErrors = 0
		m.CancelledRequests = 0
		m.UnknownRequests = 0
		m.RequestCount = 0
		m.AttemptCount = 0
		m.RetryRecoveredRequests = 0
		m.TTFT.SampleCount = 0
		m.Duration.SampleCount = 0
	}
	return m, h
}

type observationCached struct {
	snapshot *ChannelMonitorObservationSnapshot
	expires  time.Time
}
type observationPending struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	snapshot *ChannelMonitorObservationSnapshot
	err      error
}

// Facts are immutable inside the bounded cache. Pricing and audience projection are
// applied after retrieval; cancellation ends shared work only when all readers leave.
type ChannelMonitorOverviewService struct {
	repo      ChannelMonitorObservationRepository
	legacy    ChannelMonitorV2Repository
	groups    GroupRepository
	collector *ChannelMonitorCollector
	mu        sync.Mutex
	cache     map[string]observationCached
	pending   map[string]*observationPending
}

func NewChannelMonitorOverviewService(repo ChannelMonitorObservationRepository, legacy ChannelMonitorV2Repository, groups GroupRepository, collector *ChannelMonitorCollector) *ChannelMonitorOverviewService {
	return &ChannelMonitorOverviewService{repo: repo, legacy: legacy, groups: groups, collector: collector, cache: map[string]observationCached{}, pending: map[string]*observationPending{}}
}

func (s *ChannelMonitorOverviewService) GetConfig(ctx context.Context) (*ChannelMonitorObservationConfig, error) {
	return s.repo.GetConfig(ctx)
}
func (s *ChannelMonitorOverviewService) UpdateConfig(ctx context.Context, cfg ChannelMonitorObservationConfig) (*ChannelMonitorObservationConfig, error) {
	if err := ValidateChannelMonitorObservationConfig(cfg); err != nil {
		return nil, err
	}
	out, err := s.repo.UpdateConfig(ctx, cfg, cfg.Version)
	if err == nil {
		s.mu.Lock()
		s.cache = map[string]observationCached{}
		s.mu.Unlock()
		if s.collector != nil {
			s.collector.SetConfig(*out)
		}
	}
	return out, err
}

func (s *ChannelMonitorOverviewService) query(ctx context.Context, f ChannelMonitorV2Filter, version int, audience string, viewer int64, refresh bool) (*ChannelMonitorObservationSnapshot, error) {
	encoded, err := json.Marshal(struct {
		Filter   ChannelMonitorV2Filter
		Version  int
		Audience string
		Viewer   int64
	}{f, version, audience, viewer})
	if err != nil {
		return nil, err
	}
	key := string(encoded)
	s.mu.Lock()
	if !refresh {
		if hit, ok := s.cache[key]; ok && time.Now().Before(hit.expires) {
			s.mu.Unlock()
			return hit.snapshot, nil
		}
	}
	p := s.pending[key]
	if p == nil {
		// Preserve server-derived scope values while detaching an individual cancellation.
		qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		p = &observationPending{done: make(chan struct{}), cancel: cancel}
		s.pending[key] = p
		go func() {
			snapshot, queryErr := s.repo.Query(qctx, f)
			s.mu.Lock()
			defer s.mu.Unlock()
			defer cancel()
			p.snapshot, p.err = snapshot, queryErr
			if s.pending[key] == p {
				delete(s.pending, key)
				if queryErr == nil && snapshot != nil && qctx.Err() == nil {
					if len(s.cache) >= 64 {
						s.cache = map[string]observationCached{}
					}
					s.cache[key] = observationCached{snapshot: snapshot, expires: time.Now().Add(30 * time.Second)}
				}
			}
			close(p.done)
		}()
	}
	p.waiters++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		p.waiters--
		if p.waiters == 0 && s.pending[key] == p {
			delete(s.pending, key)
			p.cancel()
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return p.snapshot, p.err
	}
}

func (s *ChannelMonitorOverviewService) Overview(ctx context.Context, f ChannelMonitorV2Filter, admin bool, viewer int64, refresh bool) (*ObservationOverview, error) {
	policy, err := s.repo.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	legacy, err := s.legacy.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled || !legacy.Enabled {
		return nil, ErrChannelMonitorDisabled
	}
	groups, err := s.groups.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[int64]Group{}
	configuredPlatforms := map[string]bool{}
	for _, p := range legacy.Platforms {
		if p.Enabled {
			configuredPlatforms[p.Platform] = true
		}
	}
	for _, g := range groups {
		if (f.RestrictGroups && !observationHasID(f.AllowedGroupIDs, g.ID)) || (len(legacy.GroupIDs) > 0 && !observationHasID(legacy.GroupIDs, g.ID)) {
			continue
		}
		if !configuredPlatforms[g.Platform] && g.Platform != "composite" {
			continue
		}
		allowed[g.ID] = g
	}
	dims := ChannelMonitorV2Dimensions{Platforms: []ChannelMonitorV2Dimension{}, Groups: []ChannelMonitorV2GroupDimension{}, Models: []ChannelMonitorV2Dimension{}}
	platforms := map[string]bool{}
	for _, g := range allowed {
		platforms[g.Platform] = true
		dims.Groups = append(dims.Groups, ChannelMonitorV2GroupDimension{ID: g.ID, Name: g.Name, Platform: g.Platform})
	}
	for p := range platforms {
		dims.Platforms = append(dims.Platforms, ChannelMonitorV2Dimension{Value: p, Label: p})
	}
	sort.Slice(dims.Platforms, func(i, j int) bool { return dims.Platforms[i].Value < dims.Platforms[j].Value })
	sort.Slice(dims.Groups, func(i, j int) bool { return dims.Groups[i].ID < dims.Groups[j].ID })
	// Seed the catalog from authorized active groups, even when no requests exist.
	f.AllowedGroupIDs = []int64{}
	f.RestrictGroups = true
	for id, g := range allowed {
		if (len(f.GroupIDs) == 0 || observationHasID(f.GroupIDs, id)) && (len(f.Platforms) == 0 || observationHasString(f.Platforms, g.Platform)) {
			f.AllowedGroupIDs = append(f.AllowedGroupIDs, id)
		}
	}
	sort.Slice(f.AllowedGroupIDs, func(i, j int) bool { return f.AllowedGroupIDs[i] < f.AllowedGroupIDs[j] })
	// Group platform is authoritative for cards; composite groups span provider facts.
	f.Platforms = nil
	audience := "user"
	if admin {
		audience = "admin"
	}
	snapshot, err := s.query(ctx, f, policy.Version*100000+legacy.Version, audience, viewer, refresh)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, errors.New("missing observation snapshot")
	}
	coverage := ObservationCoverage{ChannelMonitorV2Coverage: ChannelMonitorV2Coverage{RequestedStart: f.Start, RequestedEnd: f.End, CoverageStart: f.Start, DataThrough: snapshot.Coverage.DataThrough, ComputedAt: time.Now().UTC(), BucketSeconds: int(f.Bucket.Seconds())}, State: snapshot.Coverage.State, SourceStartedAt: snapshot.Coverage.SourceStartedAt, DetailRetentionHours: 72, UnsupportedProtocols: snapshot.Coverage.UnsupportedProtocols}
	if coverage.UnsupportedProtocols == nil {
		coverage.UnsupportedProtocols = []string{}
	}
	if coverage.SourceStartedAt != nil && coverage.SourceStartedAt.After(coverage.CoverageStart) {
		coverage.CoverageStart = *coverage.SourceStartedAt
		if coverage.State == "complete" {
			coverage.State = "partial"
		}
	}
	coverage.CoverageComplete = coverage.State == "complete"
	if !coverage.DataThrough.IsZero() {
		coverage.AggregationLagSeconds = int64(time.Since(coverage.DataThrough).Seconds())
		if coverage.AggregationLagSeconds < 0 {
			coverage.AggregationLagSeconds = 0
		}
	}
	if admin {
		n := snapshot.Coverage.InFlight
		coverage.InFlight = &n
	}
	out := &ObservationOverview{ContractVersion: 2, Source: "terminal_v1", Mode: policy.Mode, Coverage: coverage, Dimensions: dims, Items: []ObservationChannel{}}
	byGroup := map[int64][]ChannelMonitorObservationFact{}
	modelCatalog := map[string]ChannelMonitorV2Dimension{}
	for _, fact := range snapshot.Facts {
		g, ok := allowed[fact.GroupID]
		if !ok || !observationHasID(f.AllowedGroupIDs, fact.GroupID) {
			continue
		}
		if len(f.Models) > 0 && !observationHasString(f.Models, fact.Model) {
			continue
		}
		byGroup[fact.GroupID] = append(byGroup[fact.GroupID], fact)
		modelCatalog[g.Platform+"\x00"+fact.Model] = ChannelMonitorV2Dimension{Value: fact.Model, Label: fact.Model, Platform: g.Platform}
		if fact.UnknownRequests > 0 && out.Coverage.State == "complete" {
			out.Coverage.State = "partial"
			out.Coverage.CoverageComplete = false
		}
	}
	for _, m := range modelCatalog {
		out.Dimensions.Models = append(out.Dimensions.Models, m)
	}
	sort.Slice(out.Dimensions.Models, func(i, j int) bool { return out.Dimensions.Models[i].Value < out.Dimensions.Models[j].Value })
	for _, id := range f.AllowedGroupIDs {
		g := allowed[id]
		facts := byGroup[id]
		if len(f.Models) > 0 && len(facts) == 0 {
			continue
		}
		m, h, buckets := observationSeries(facts, *policy, g.Platform, id, "", f, admin)
		row := ObservationChannel{Platform: g.Platform, GroupID: id, GroupName: g.Name, Metrics: m, Health: h, Buckets: buckets, Models: []ObservationModel{}}
		if admin {
			rate := g.RateMultiplier * g.PeakMultiplierAt(time.Now())
			row.RateMultiplier = &rate
		}
		models := map[string][]ChannelMonitorObservationFact{}
		for _, fact := range facts {
			models[fact.Model] = append(models[fact.Model], fact)
		}
		for model, mfacts := range models {
			mm, mh, mb := observationSeries(mfacts, *policy, g.Platform, id, model, f, admin)
			row.Models = append(row.Models, ObservationModel{Model: model, Metrics: mm, Health: mh, Buckets: mb})
		}
		sort.Slice(row.Models, func(i, j int) bool { return row.Models[i].Model < row.Models[j].Model })
		out.Items = append(out.Items, row)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.Platform != b.Platform {
			return a.Platform < b.Platform
		}
		if a.GroupName != b.GroupName {
			return a.GroupName < b.GroupName
		}
		return a.GroupID < b.GroupID
	})
	return out, nil
}

func observationSeries(facts []ChannelMonitorObservationFact, cfg ChannelMonitorObservationConfig, platform string, group int64, model string, f ChannelMonitorV2Filter, admin bool) (ObservationMetrics, ObservationHealth, []ObservationBucket) {
	total := ChannelMonitorObservationFact{}
	byBucket := map[time.Time]*ChannelMonitorObservationFact{}
	for _, fact := range facts {
		total.Add(fact)
		start := fact.BucketStart.UTC().Truncate(f.Bucket)
		if byBucket[start] == nil {
			byBucket[start] = &ChannelMonitorObservationFact{}
		}
		byBucket[start].Add(fact)
	}
	buckets := []ObservationBucket{}
	for start := f.Start.UTC().Truncate(f.Bucket); start.Before(f.End); start = start.Add(f.Bucket) {
		fact := byBucket[start]
		if fact == nil {
			fact = &ChannelMonitorObservationFact{}
		}
		window := minTime(start.Add(f.Bucket), f.End).Sub(maxTime(start, f.Start))
		m, h := observationMetrics(*fact, cfg, platform, group, model, window, admin)
		buckets = append(buckets, ObservationBucket{BucketStart: start, Metrics: m, Health: h})
	}
	m, h := observationMetrics(total, cfg, platform, group, model, f.End.Sub(f.Start), admin)
	return m, h, buckets
}
func observationHasID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func observationHasString(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
