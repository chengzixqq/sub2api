# Frontend translation build checks

Build frontend assets from the same checkout and frozen dependency lock that will
be released:

```sh
cd frontend
pnpm install --frozen-lockfile
pnpm run build
```

Use the pnpm version declared in `frontend/package.json`. Do not copy an existing
`backend/internal/web/dist` from another checkout into a release before rebuilding
and verifying it. The Docker frontend stage already installs the frozen lock and
builds these assets before the backend embeds them.

The app loads raw translation strings with the CSP-compatible vue-i18n runtime.
The runtime and its resolved Intlify dependency chain must match the exact
`@intlify/message-compiler` version declared in the project. Vite checks that
alignment when loading `vite.config.ts`, and stops with a frozen-install instruction
if dependencies are stale or missing. A top-level compiler with an outdated
nested runtime compiler is also rejected. The package scripts explicitly select
`vite.config.ts`, because older checkouts may retain an emitted `vite.config.js`
that Vite would otherwise prefer. For direct CLI calls, also use
`vite build --config vite.config.ts`. TypeScript now emits configuration files
under `.cache/tsconfig-node` so they cannot shadow the source configuration.

`pnpm run check:i18n` covers key completeness, message compilation, dependency
alignment, and real Chinese/English runtime interpolation and locale switching.
It runs as part of the build. Literal pipes in translation strings must use
`{'|'}`; unescaped pipes select plural alternatives and can silently drop text.

For a release, retain the source commit, lockfile hash, package manager and
vue-i18n versions, build/test exit statuses, and generated asset hashes. Verify
the assets from the new build before embedding or deploying them. Source and
asset-copy hashes alone do not establish dependency consistency.
