# Channel Monitor Compact Capacity Evidence

Status: local isolation checks completed; the isolated 24-hour soak was stopped
before completion and is not acceptance evidence. No production deployment or
completed 24-hour soak is claimed by this report. Workload results below are
local measurements, not production capacity certification.

## Scope and Root Cause

The retired Observation writer refused new events when its physical PostgreSQL
relations reached the fixed 8 GiB ceiling. Ordinary DELETE does not return allocated table files
to the filesystem, so the old capacity latch could remain set after cleanup.

The compact constructor uses new tables and an independent configuration row.
Migration 239 copies legacy thresholds and enabled state once, forces shadow,
clears live groups, disables probes, and enables read-only quota display. It does
not rewrite any legacy policy, event, usage, billing, or monitor history.

## Retention and Write Bounds

| Data | Logical retention | Physical management |
| --- | --- | --- |
| UUID and SHA256 dedup | 25 hours | Up to 20,000 expired rows per maintenance pass; autovacuum tuned |
| Minute terminal and account facts | 48 hours | UTC daily partitions; less than one extra day before file reclamation |
| Hour terminal and account facts | 30 days | UTC daily partitions; less than one extra day before file reclamation |
| Diagnostic samples | 24 hours | Queries enforce cutoff immediately; bounded boundary-day row cleanup every 60 seconds; expired daily partitions dropped |
| Writer sessions and gaps | 30 days | Bounded row cleanup |

Samples are limited to 8,000 failure candidates and 2,000 approximately 1%-sampled
successes per UTC day. The sample transaction is independent of the exact-counter
transaction. Losing a diagnostic sample cannot roll back or double-count a
committed terminal aggregate. Sample rows contain no user/key IDs or payloads.

Each accepted unique request still writes one dedup row, two terminal aggregates,
and bounded account-attempt aggregates. This is not a claim of constant storage
independent of throughput. Canonical model and account cardinality affect the
aggregate working set and must remain bounded.

## Reproducible Workload

Environment: PostgreSQL 18.1, Windows x86_64, local loopback.
An isolated database is mandatory. The integration harness rejects a non-loopback
DSN or a database name outside `sub2api_*_test`.

The benchmark drives the real collector, queue, repository and PostgreSQL:

- 110 new events/second.
- Five concurrent producers, including 28 duplicate submissions/second.
- Separate traffic/probe sources; 8 groups, 16 models, 32 effective terminal
  dimensions; chat and Responses protocols.
- Mixed success, channel error, client error, cancellation and unknown terminals.
- Account attempts, retry attempts, tokens, histograms and diagnostic samples.
- Exact equality of all counters/histograms in both minute and hour aggregates;
  account attempt totals independently checked; zero lost/rejected events required.
- Maximum event-completion-to-durable-commit latency must be below 120 seconds.

From `backend/` in PowerShell:

```powershell
$env:SUB2API_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55439/sub2api_monitor_capacity_final_test?sslmode=disable'
go test -tags integration ./internal/repository -run '^$' -bench '^BenchmarkCompactObservationSustained$' -benchtime=120x -timeout=5m -count=1 -v
```

One benchmark iteration is one second, not one request. Go also runs a one-second
calibration. Evidence rows remain in the isolated database for inspection.

## Measured Results

Final 120-second run, including the conservative watermark/backlog correction:

| Measurement | Result |
| --- | --- |
| Unique terminal events | 13,200 |
| Accepted submissions including duplicates | 16,560 |
| Rejected submissions / lost events | 0 / 0 |
| Exact account attempts | 16,560 |
| Diagnostic samples | 5,368 |
| Maximum sampled queue depth | 138 of 8,192 |
| P95 commit latency | 1.094 seconds |
| Maximum commit latency | 1.755 seconds |
| Allocated compact-table growth | 5,275,648 bytes |
| End allocation | 6,299,648 bytes |

The earlier run also passed (P95 1.066 seconds, maximum 2.200 seconds). The final
measurement includes any diagnostic-sample work after aggregate commit, making it
a conservative upper bound on commit latency. Both runs used fresh isolated
databases. The final run left no unclosed collector sessions.

The dedup table occupied 1,990,656 bytes for 13,310 rows including calibration,
approximately 150 bytes/row with indexes in this small run. Simple extrapolation
gives about 1.49 GB at 110 events/second over 25 hours.
These are sizing estimates, not measured steady-state results; page fill,
autovacuum, WAL, row churn and filesystem headroom require a longer run.

Samples used 2,957,312 bytes; terminal aggregates 720,896 bytes; account aggregates
475,136 bytes; control tables 155,648 bytes. The workload uses 32 effective terminal
dimensions. Do not extrapolate its short-run total
allocation linearly: diagnostic daily caps and retained minute/hour cardinality
have different growth curves.

The old Observation tables remain untouched. Deploy-time
disk budgeting must include them in addition to the new collector's working set;
this change does not reclaim legacy files without a separate approved cleanup.

## Watermark and Failure Gates

The current collector advances its durable progress watermark only after a
successful flush with an empty queue, or during healthy idle time. A fresh process
heartbeat cannot advance it over queued work or a failed write. Queue depth is
persisted separately; backlog blocks automatic idle probes. Cross-process coverage
uses the minimum progress of fresh active collectors. Losses remain explicit gaps.
Only the admin projection receives the numerical pending-events count.

Regression coverage includes concurrent duplicate replay, transaction rollback,
sample caps and redaction, UTC partition cleanup, healthy idle progress, frozen
progress on failures/backlog, queue-overflow gaps, protocol separation, Responses
WebSocket activity suppressing idle probes, and restart downtime.

Verified commands after the watermark change:

```powershell
$env:SUB2API_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55439/sub2api_monitor_final_test?sslmode=disable'
go test -race -tags integration ./internal/repository -run 'TestCompactObservation|TestObservationRawIntervals' -count=1
go test -race ./internal/service ./internal/handler -run 'TestImageTask|TestAsyncImage|TestChannelMonitorCollector' -count=1
```

Migration 239 SHA256:
`D3C9DD962E8281F79FA364186D69586544F8491FD2CEEA860F71A68B4A78817E`.

## Remaining Gates and Limits

- No 24-hour soak, real production-volume database restore, or production capacity
  certification has been performed. Production remains unchanged.
- The collector is an in-memory queue, not a transactional outbox. A process crash
  can lose accepted-but-uncommitted work; downtime/coverage must remain explicit.
- This new epoch does not reconstruct missing old Observation data from billing.
- Aggregate partitions can physically retain less than one extra day past their
  logical retention cutoff. Sample rows are trimmed at the exact cutoff on the
  maintenance cadence; cleanup failures must remain visible operationally.
- Single-request asynchronous Images snapshot immutable group scope and submit
  only after terminal Redis persistence. Their background execution participates
  in in-flight and shutdown-loss accounting. Old tasks lacking the snapshot are
  not retroactively attributed. Batch Images and video remain explicit coverage gaps.
- Safe batch coverage needs immutable group/platform scope, per-item terminal
  identity and timing, and transactionally recoverable terminal publication.
  Batch settlement success must not count every item as successful.

An explicitly requested isolated 24-hour run can use the same benchmark with
`-benchtime=86400x -timeout=25h`. The attempted local run was stopped by the
operator before completion. Local status and logs are excluded from this source
snapshot; no running or completed soak is claimed.
