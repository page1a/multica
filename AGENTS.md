# Repository Instructions

Multica is a task management platform where people and agents collaborate on issues. These instructions apply to all coding agents working in this repository.

## Scope and Reading Order

- Before changing `apps/mobile/`, also read [apps/mobile/AGENTS.md](apps/mobile/AGENTS.md), even if your tool does not load nested instructions automatically. Platform-specific sections below apply only to the named platform.
- For naming, translations, or Chinese UI/docs copy, read [conventions.mdx](apps/docs/content/docs/developers/conventions.mdx) and [conventions.zh.mdx](apps/docs/content/docs/developers/conventions.zh.mdx).
- Maintain shared rules here and mobile-specific rules in the mobile file. `CLAUDE.md` files only import them. Update instructions in the same change that alters the referenced workflow or boundary; do not add incident timelines, dependency version lists, or duplicate rules.

## Sharing Rules and Package Boundaries

| Location | Responsibility and constraints |
| --- | --- |
| `server/` | Go backend; Chi, sqlc, WebSocket |
| `packages/core/` | Headless logic, API client, Query hooks, shared Zustand stores. No UI libraries, `react-dom`, `localStorage`, or `process.env`; use `StorageAdapter` for persistence. |
| `packages/ui/` | UI primitives and shared styles. No business logic or `@multica/core` imports. |
| `packages/views/` | Shared web/desktop pages and business components. No store definitions, `next/*`, or `react-router-dom`; use `NavigationAdapter`, `useNavigation()`, and `<AppLink>`. |
| `apps/web/` | Next.js routes/layouts and web-only UI. Framework APIs stay here; shared navigation adapters live in `apps/web/platform/`. |
| `apps/desktop/` | Electron and desktop-only UI/state. Application navigation goes through `apps/desktop/src/renderer/src/platform/`. |
| `apps/mobile/` | Independent Expo/React Native client: owns UI, state, hooks, providers, i18n, build, and release. Shares core types and pure utilities, including platform-independent schemas. |
| `apps/docs/` | Fumadocs documentation site |

- Dependency direction is `views -> core + ui`; core and ui remain independent. Shared packages export raw TypeScript compiled by consuming apps.
- Extract logic used by both web and desktop into the appropriate shared package. Keep framework/Electron APIs in the app layer; inject platform-specific UI through props/slots.
- Wire shared features into both web routes and the desktop router or overlay. Reuse existing guards/providers such as `DashboardGuard` in `packages/views/layout/`.
- Each workspace declares its directly imported external dependencies. Use `catalog:` for shared dependencies; mobile pins Expo/React Native dependencies in its own manifest.

## Development and Verification

Use `Makefile`, workspace `package.json` files, and `pnpm-workspace.yaml` for current commands and versions. See [CONTRIBUTING.md](CONTRIBUTING.md) for setup and worktree operations.

- Use the checkout's managed environment: `make up`, `make status`, `make down`. `make down` preserves data; `make destroy` removes the environment and its data.
- Worktrees share PostgreSQL but have isolated databases/ports. Use the environment scripts and `.env.worktree`; do not copy the main checkout's `.env` or manually create a database through an assumed PostgreSQL instance.
- Regenerate sqlc with `make sqlc` after SQL changes.
- Run the narrowest useful checks while iterating, then broaden when risk warrants it. Report what actually ran and any skipped checks.

Run these from the repository root:

| Scope | Checks |
| --- | --- |
| Frontend excluding mobile | `pnpm typecheck`, `pnpm lint`, `pnpm test` |
| Go backend | `make test` |
| End-to-end | `pnpm exec playwright test` |
| Combined web/backend verification | `make check` |
| Mobile | Commands in [apps/mobile/AGENTS.md](apps/mobile/AGENTS.md#verification) |

Root frontend commands and `make check` do not verify mobile. Docs-only changes can use link/reference checks and `git diff --check`; state that code tests were not run.

## State Rules

- TanStack Query owns API/server data. Zustand owns client state such as filters, drafts, modals, and tab layout; persist only durable preferences/drafts/layout, not server data or ephemeral UI state.
- Web/desktop shared stores live in `packages/core/`. Desktop platform stores remain in desktop; mobile stores remain in mobile. Do not define stores in `packages/views/`.
- On web/desktop, workspace identity is route-driven; platform mirrors exist only for request headers, storage namespaces, and reconnects. React Context is for platform plumbing, not a second server-state store.
- Among stores, only auth/workspace stores may call `api.*` directly; other server interactions belong in queries/mutations.
- Workspace-scoped query keys include `wsId`; account-level keys remain account-scoped. Hooks needing workspace context accept `wsId` unless guaranteed to run under its provider.
- Zustand selectors return stable references; use shallow comparison for allocated objects/arrays.
- WebSocket events patch or invalidate Query caches, not server payloads in Zustand. Clearing client-owned pointers is allowed with one responder and a self-initiated guard when this client can cause the event.
- Optimistic field patches require a predictable result, rare failure, trivial rollback, and staying on the current screen. Snapshot before patching, roll back on failure, and invalidate uncertain projections on settle.
- Create/delete/leave and confirmation flows await the server before navigation or cleanup; do not optimistically delete entities. Exceptions: the existing workspace-leave race noted under Desktop Rules, and mobile inbox mark-read as documented in its instructions.
- Message sends use visible pending state and retry on failure.

## 工作单（本 fork）

工作单住 Multica Issues，用 `multica` CLI。代码 PR 走 GitHub（`jeff-kunkun/multica`）：标题带 identifier，关单写 `Closes DENE-N`。不要再开 GitHub issue。

`main` 只做官方镜像，`kun` 是魔改主线：功能分支从 `kun` 切出、PR 打回 `kun`。上游同步流程见 `KUN-FORK.md`。
