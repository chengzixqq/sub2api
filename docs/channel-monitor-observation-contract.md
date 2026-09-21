# Channel Monitor Observation Contract

Implementation baseline: `92e646c06`. This addition is independent of billing,
scheduling and the legacy monitor APIs. No production rollout is part of this work.

## HTTP

Add `GET /channel-monitor-v2/overview` and the corresponding `/admin` route.
Use the existing repeated `range`, `platform`, `group_id`, `model` filters.
Optional `end_time` freezes all partitions to one RFC3339 instant. Reject invalid
times. `refresh=true` bypasses all snapshot caches. Admin `audience=user` requests
the user projection, never permission elevation. Legacy endpoints remain unchanged.

Response payload:

```ts
interface ObservationOverview {
  contract_version: 2
  source: 'terminal_v1'
  mode: 'shadow' | 'live'
  coverage: MonitorCoverage & {
    state: 'complete' | 'partial' | 'stale' | 'unavailable'
    source_started_at?: string
    detail_retention_hours: 72
    unsupported_protocols: string[]
  }
  dimensions: MonitorDimensions
  items: ObservationChannel[]
}
interface ObservationMetrics {
  success_requests: number
  channel_errors: number
  client_errors: number
  cancelled_requests: number
  unknown_requests: number
  request_count: number
  success_rate: number | null
  reliability_rate: number | null
  sample_state: 'no_samples' | 'insufficient' | 'sufficient'
  cache_rate: number | null
  ttft: LatencyMetric
  duration: LatencyMetric
  rpm: number
  tpm: number
  retry_recovered_requests: number
  attempt_count: number
  phase_avg_ms?: Record<string, number>
  error_categories?: Record<string, number>
}
interface ObservationHealth {
  reliability: 'unknown' | 'healthy' | 'warning' | 'critical'
  latency: 'unknown' | 'healthy' | 'warning' | 'critical'
}
interface ObservationBucket {
  bucket_start: string
  metrics: ObservationMetrics
  health: ObservationHealth
}
interface ObservationModel {
  model: string
  metrics: ObservationMetrics
  health: ObservationHealth
  buckets: ObservationBucket[]
}
interface ObservationChannel {
  platform: string
  group_id: number
  group_name: string
  rate_multiplier?: number
  metrics: ObservationMetrics
  health: ObservationHealth
  buckets: ObservationBucket[]
  models: ObservationModel[]
}
```

Absolute counters, latency sample counts, throughput, phase data and error details
are stripped by the server for the user projection. Sample state is calculated
before redaction. User pricing is resolved outside shared aggregate caches.

## Configuration

Add a separate observation policy endpoint under `/admin/channel-monitor-v2/observation-config`.
Policy: `enabled=true`, `mode=shadow`, `detail_retention_hours=72`,
`refresh_interval_seconds=60`, `minimum_sample=50`,
`healthy_reliability=0.99`, `warning_reliability=0.95`,
`warning_ttft_ms=3000`, `critical_ttft_ms=10000`, `overrides=[]`.
Overrides match optional platform/group_id/model; more specific matches win,
then model, group, platform. Ambiguous duplicate selectors are rejected.
Shadow collects and exposes source-labelled results, never overwrites legacy data.

## Collection and Storage

Use server UUIDs, terminal state per request, and bounded attempts per request.
Collect only numeric fields and bounded identifiers; no payload, credentials,
URLs, free-form errors or headers. Separate completed request counts from attempts.
HTTP status alone does not establish streaming success. Explicit protocol adapters
provide completion/output markers. Unsupported protocols remain unknown.

The repository contract returns grouped numeric observation facts and histograms
for overview assembly. A dedicated short-lived event store is incrementally and
idempotently aggregated into independent long-lived tables. Retain details 72h,
existing aggregate tiers up to 90d. Never delete/rebuild sealed data from expired
details. Queue loss, crashes, failed flushes and missing adapters lower coverage.

## Delivery

Preserve original source archive and SHA256; deliver a modified archive, patch,
verification report and executable rollback tested against a separate copy.
Frontend preview must use the actual components with explicit synthetic fixtures
when production data is unavailable. No upstream requests or production changes.
