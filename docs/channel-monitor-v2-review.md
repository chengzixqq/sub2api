# Channel Monitor V2 review

Review commit: `ce37f8d87` on `codex/channel-monitor-v2-cards`.
The `main` branch was not modified.

## Fixes verified in this review

- Generation capture no longer reads or rewrites inbound request bodies. Large
  requests remain byte-for-byte intact.
- Capture is limited to generation POST endpoints; model listing, token
  counting, WebSocket handshakes and async polling are not counted as completed
  generations.
- SSE parsing is incremental and accepts a final data event without a blank
  separator. Empty and partial streams are recorded as channel failures;
  gateway-generated 4xx responses are client errors unless an upstream attempt
  proves otherwise.
- Transport attempts are attached to terminal events, including custom
  upstream base paths. Composite groups use the resolved provider platform.
- Gateway timing phases and first upstream byte are persisted as bounded
  numeric facts. Shutdown with in-flight requests records a coverage gap.
- Observation queries read the mutable trailing source bucket from detail rows
  and avoid reusing a stale refresh boundary. Failed refreshes clear the old
  snapshot, and the UI clears dimensions while a new query is pending.
- User/admin layout preference is scoped by account, role and workspace. The
  cards page uses observation dimensions for its filter menus.

## Evidence

- `go test ./...` — pass.
- `go test -race ./internal/handler ./internal/service ./internal/repository -run 'ChannelMonitor|Observation' -count=1` — pass.
- Frontend full Vitest — 290 files, 2103 tests passed.
- `pnpm run typecheck` and `pnpm run lint:check` — pass.
- `make build` — pass; Vite emitted only existing chunk-size/dynamic-import warnings.
- Playwright synthetic preview — authenticated fixture rendered the platform
  section, group card, metrics, model expansion and refresh state. The preview
  used mocked API responses only; setup fallback requests produced expected
  fixture console errors, and no upstream request was sent.

## Remaining limits before production rollout

- PostgreSQL migration, retention cleanup, replay/idempotency and capacity
  behavior still need an isolated PostgreSQL integration run; this workstation
  has no PostgreSQL instance.
- WebSocket and asynchronous task protocols remain explicitly marked as
  unsupported coverage and need protocol-specific terminal adapters before they
  can contribute to reliability.
- No production traffic, upstream probes or production deployment was done in
  this review. The ignored `output/`, deployment evidence and reconciliation
  files remain outside the commit.
- The historical release archive under ignored `output/channel-monitor-v2-release`
  predates this review and must not be used as the release snapshot. Generate a
  new archive and rollback check from the final commit if packaging is needed.
