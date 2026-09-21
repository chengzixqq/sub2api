# Unified Channel Monitor Implementation

## Contract and Safety Boundary

The accepted design replaces the stalled full-event Observation write path with
compact, deduplicated traffic aggregates. Production is out of scope until the
operator approves deployment. Existing migrations, billing records, and legacy
monitor history are not rewritten or deleted.

- One overview API and authorization policy for all audiences. Shadow reads use
  legacy V2; only an explicit owner preview reads unpublished compact facts.
- Traffic, probes, and quota are separate evidence sources. Probes never affect
  customer billing or real traffic counters. Quota cannot change channel health.
- Completed events have a 24-hour ingestion horizon and 25-hour deduplication.
- Minute aggregates: 48 hours. Hour aggregates: 30 days. Sanitized samples:
  24 hours, at most 8,000 failures and 2,000 successes per UTC day globally.
- Idle probes: 15 minutes, 96/target/day and 1,000/global/day, one upstream
  generation, no retry, 20-second timeout, at most 64 output tokens.
- Overview refresh: 60 seconds. Default window: 24 hours in five-minute buckets.

## Implementation

- [x] Compact storage, transactional deduplication and UTC partition maintenance.
- [x] HTTP terminals, Responses WebSocket turns, synchronous and single-request
  asynchronous image terminals; batch images and videos remain coverage gaps.
- [x] Persistent probe targets, budgets, idempotency, leases and independent quota.
- [x] Unified overview, owner preview, per-group rollout and authorized catalogue.
- [x] Owner-only account attempts and redacted diagnostic samples.
- [x] Final unified-list frontend and browser checks.
- [x] Final backend unit, targeted PostgreSQL integration/race and build checks.
- [x] Sustained two-times collector load with exact aggregate comparison.
- [ ] Unchanged-baseline versus candidate gateway P95 overhead <= 5%.
- [ ] 24-hour isolated soak without stalled collection.

These implementation checkboxes do not certify readiness for production. Test
results, environment gaps and release blockers are recorded below separately.

## API Contract

All paths below are relative to `/api/v1`. Existing endpoints and fields remain.

| Endpoint | Contract |
| --- | --- |
| `GET /channel-monitor-v2/overview` | Authorized groups/models, public projection |
| `GET /admin/channel-monitor-v2/overview` | Owner projection; explicit `preview=true` for unpublished compact facts |
| `GET/PUT /admin/channel-monitor-v2/observation-config` | Versioned unified policy; stale version returns 409 |
| `GET /admin/channel-monitor-v2/groups/:id/accounts` | Owner-only account attempts and bounded samples |
| `GET/POST /admin/channel-monitor-v2/probe-targets` | Owner-only configured group/model/protocol targets |
| `PUT /admin/channel-monitor-v2/probe-targets/:id` | Version-checked target update, including disable |
| `POST /admin/channel-monitor-v2/probe-targets/:id/probe` | Owner-only, idempotent, shares automatic-probe budget and lease |
| `GET /admin/channel-monitor-v2/probe-budget` | Owner-only UTC budget consumption |

The overview retains `metrics`, `health` and `buckets`, and adds `source`,
`current_status`, separate `traffic`/`probe`/`quota`, ingestion timestamps,
`collector_state`, and coverage/gap information. `pending_events` and `in_flight`
are owner-only. Request counts, throughput, sample counts, error samples, account
IDs and probe cost are not part of the ordinary-user projection.

Model dimensions come from the authorized catalogue before applying the selected
model filter. Group allowlists and OpenAI pinned manifests remain authoritative.
A failed pinned lookup fails closed. An untested model cannot become healthy
because a different model's probe succeeded. Historical combined attempt
failures are exposed as `unclassified_attempts`; they are never promoted to
channel errors.

## Source and Rollout Semantics

`enabled` controls collection, not publication. `mode=shadow` serves official
legacy V2 to both owner and user unless the owner explicitly requests preview.
`mode=live` selects compact facts for `live_group_ids`; an empty list means all
authorized groups. Other groups retain legacy V2. Counters from the two sources
are never added for the same group. Legacy evidence is labeled as partial log
semantics, not a reconstructed compact history.

`probe_enabled` defaults to false. `quota_enabled` defaults to true and remains
independent of collection and legacy V1/V2 settings. Quota fetch failures do not
alter channel connectivity or disable an account. Supported provider metadata is
read directly; OAuth providers requiring a paid generation use passive cached
headers instead and report stale/unsupported evidence explicitly.

Probe execution currently fails closed for OAuth, configured account proxies,
shadow accounts, custom header overrides and unsupported protocol/model limits.
It does not silently retry a different account. Such pre-dispatch failures have
no success/failure health vote. Billable usage returned even with an upstream
failure is audited separately; no customer-billing path is invoked.

The current-state window is five minutes, independent of the selected chart
range. Insufficient traffic may use a probe younger than twenty minutes. A
collector error/backlog blocks probe-based green status and automatic idle
probing. Client errors, cancellation and incomplete streams remain distinct.

## Migration and Capacity

`239_channel_monitor_compact.sql` and `240_channel_monitor_probe.sql` are additive.
They do not drop or rewrite old Observation or business tables. Existing deployed
migration checksums must still be verified before any future deployment.

The compact epoch has its own policy row. Migration copies existing thresholds
once, forces shadow, clears live groups and disables probes. Data retention and
measured workload sizing are detailed in [channel-monitor-capacity.md](channel-monitor-capacity.md).

The memory queue is best-effort. Durable UUID deduplication prevents committed
events from being counted twice, including retry after an ambiguous commit.
Process failure can lose queued events. Heartbeat, durable progress, pending
events, last ingestion and coverage gaps are separate facts.

## Verification Evidence

Evidence directory: `outputs/monitor-unified/`. Isolated PostgreSQL is local
loopback on port 55439, never the production database. New databases were created
when an unapplied migration changed; applied checksum records were not altered.

- Compact migration/transaction/race tests use `sub2api_monitor_final_test`.
- Probe concurrency, UTC budgets, leases and conflict tests use
  `sub2api_probe_final_test`; see `probe-final-integration.log`.
- Collector load evidence is described in the capacity report. This is not a
  gateway P95 benchmark and is not a 24-hour soak.
- `make test-integration` without a Docker/Redis harness can exit successfully
  while the repository harness skips its database suites. That command alone
  must never be recorded as full integration acceptance.
- `make test-unit`, `go test ./...`, and `make test-integration` passed after
  the review fixes. Their logs are `backend-test-unit-post-review.log`,
  `backend-go-test-all-post-review.log`, and
  `backend-test-integration-post-review.log`. The integration-tag command does
  not provision or connect to a Redis instance, so it is not Redis integration
  evidence.
- Fresh isolated PostgreSQL probe integration passed all seven cases: concurrent
  idempotency, global and per-target budgets, UTC rollover, lease/version
  conflicts, quota claims, interrupted runs, and duplicate target conflict.
- Targeted collector/handler/service race tests and final `make build` passed;
  the latter compiled the backend with `main.Version=0.2.5` and rebuilt the
  frontend. Build artifact SHA256 is recorded in
  `outputs/monitor-unified/source-manifest.json`.
- Browser screenshots live under `frontend/output/playwright/`. Fixture-backed
  rendering checks do not constitute a live-provider or production smoke test.
- The full frontend Vitest suite passed 316 files and 2,342 tests. After the
  final API type update, its targeted API contract test (seven tests),
  `pnpm run typecheck`, `pnpm run lint:check`, and `pnpm run build` all passed;
  logs use the `*-final.log` names in the evidence directory. Fixture-backed
  desktop and 390px mobile list captures are `monitor-unified-list-desktop.png` and
  `monitor-unified-list-mobile.png`; `monitor-unified-list-desktop-model-detail.png`
  confirms model-scoped drawer metrics. The mobile page had no horizontal
  overflow (`390px` document width at a `390px` viewport).
- Independent local review was used. External cross-model review was skipped at
  the operator's request.

## Release Blockers

Production deployment remains unapproved and blocked. In addition to final test
results, the following evidence is still required:

1. Real isolated Redis integration and the full database/process test matrix.
2. Baseline/candidate gateway P95 comparison under equivalent traffic, <= 5% cost.
3. A full 24-hour isolated run, including UTC rollover and maintenance, without
   stalled collection and with event latency <= 120 seconds in healthy conditions.
   The attempted local run was stopped by operator before completion and its
   orphaned benchmark process was then terminated; it is not acceptance
   evidence. Its state is recorded in
   `outputs/monitor-unified/soak-status.json`.

Do not replace these gates with unit tests, a two-minute load test or a successful
build. Existing historical losses cannot be inferred from the new epoch.

## Future Rollout and Rollback

Only after the operator separately approves production work:

1. Verify all existing migration checksums and capture current configuration and
   backups. Apply the additive migrations with probes disabled and mode shadow.
2. Observe shadow for 24 hours with owner-only preview. Check durable progress,
   losses, queue depth, database size, gateway latency and billing invariance.
3. Enable only reviewed lightweight targets; check budgets/cost and quota state.
4. Publish explicitly selected groups with version-checked policy updates.
5. On an anomaly, disable new probes and set mode back to shadow. Disable compact
   collection separately if needed. Continue serving official legacy V2.

Do not switch back to the stalled full-event Observation writer, reverse business
database migrations, delete history, or reclaim old tables as part of this
rollback. Old-table reclamation requires another explicit operator decision.
