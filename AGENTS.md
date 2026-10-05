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
- Allocate a migration with `make migration-new NAME=...`: it takes the next number after the latest on `origin/kun` (never after your branch's local files) and creates the empty up/down pair; run `make migration-lint` before opening the PR. See `docs/kun/migration-numbering.md`.
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

响应式 Web/Desktop 页面按 375 / 768 / 1280 三档做真实浏览器核验；先复用共享移动外壳的返回栈、安全区、44px 触控区和窄屏承载，再处理页面族自己的问题。静态 CSS 推断不能替代截图证据，当前盘点入口见 [DENE-1277 报告](docs/evidence/DENE-1277/report.html)。

## State Rules

- TanStack Query owns API/server data. Zustand owns client state such as filters, drafts, modals, and tab layout; persist only durable preferences/drafts/layout, not server data or ephemeral UI state.
- Web/desktop shared stores live in `packages/core/`. Desktop platform stores remain in desktop; mobile stores remain in mobile. Do not define stores in `packages/views/`.
- On web/desktop, workspace identity is route-driven; platform mirrors exist only for request headers, storage namespaces, and reconnects. React Context is for platform plumbing, not a second server-state store.
- Among stores, only auth/workspace stores may call `api.*` directly; other server interactions belong in queries/mutations.
- Workspace-scoped query keys include `wsId`; account-level keys remain account-scoped. Hooks needing workspace context accept `wsId` unless guaranteed to run under its provider. The same scoping holds for persisted preferences: one that belongs to the person, not the workspace (the alignment-method memory, for one), is keyed by user id on `defaultStorage` and never parked in a workspace draft — a workspace-scoped copy makes two workspaces disagree about the same user's last choice.
- Zustand selectors return stable references; use shallow comparison for allocated objects/arrays.
- WebSocket events patch or invalidate Query caches, not server payloads in Zustand. Clearing client-owned pointers is allowed with one responder and a self-initiated guard when this client can cause the event.
- Optimistic field patches require a predictable result, rare failure, trivial rollback, and staying on the current screen. Snapshot before patching, roll back on failure, and invalidate uncertain projections on settle.
- Create/delete/leave and confirmation flows await the server before navigation or cleanup; do not optimistically delete entities. Exceptions: the existing workspace-leave race noted under Desktop Rules, and mobile inbox mark-read as documented in its instructions.
- Message sends use visible pending state and retry on failure.

## 工作单（本 fork）

工作单住 Multica Issues，用 `multica` CLI。代码 PR 走 GitHub（`jeff-kunkun/multica`）：标题带 identifier，关单写 `Closes DENE-N`。不要再开 GitHub issue。

`main` 只做官方镜像，`kun` 是魔改主线兼**测试线**：功能分支从 `kun` 切出、PR 打回 `kun`。`release` 是**发布线**，只接受从 `kun` 快进合入，不直接开发。Desktop 双通道发版（`vX.Y.Z-test.N` 走测试通道、`vX.Y.Z` 走正式通道）与上游同步流程见 `KUN-FORK.md`。

魔改功能只要需要 Agent 做超过一步，就收成一条由服务端校验的命令（例：收口 `multica issue close`、交棒 `multica issue handoff`、建计划 `multica plan apply`、推进阶段 `multica issue stage advance`，见 DENE-858）。简报只写动词和决策表，不让 Agent 背多步仪式。

魔改功能按「三面齐」交付：服务端能力、Web/Desktop 界面、`multica` CLI 三处都能用，且都走同一个服务端接口和校验，不在某一面另写一份规则。界面要让人看得到、改得了，权限不足或被禁用时要说明原因；CLI 要让 Agent 不开浏览器就能查和改，`--help` 能查到用法，`--output json` 能拿到结果。Agent 需要主动调用的命令，要同步写进 `multica-platform` skill，改法见 `/multica`。某一面确实不该有（例如纯内部调度），在 PR 描述里写明缺哪一面、为什么缺；以后要补的，开票跟进，不默默省略。

往 Agent 简报（`server/internal/daemon/execenv` 渲染的运行时规则）里加内容前，先过三问：服务端能校验吗（能就做进命令）、只在某个动作时才用到吗（放 `--help` 或 skill）、每回合都变吗（放回合消息）；三问都过才进简报，且不得突破体积测试的上限，细则见 [ADR-0007](docs/adr/0007-context-injection-principles.md)。

前端改动必须同时交代手机端：Web/Desktop 的改动要在手机网页（390px 宽）下能用、能关、内容不被截断，HTML 预览稿和自测截图都附手机版；`apps/mobile` 原生 App 有同一屏或同一入口时一起改，不改就在 PR 描述里写明缺哪一面、为什么缺。

通用带选项提问是跨三面的共享契约：服务端 `/api/asks` 负责校验和回答状态，Web/Desktop 的任务动态、聊天、收件箱与 CLI `multica ask` 复用同一对象和接口；新增提问入口要同步更新 `server/internal/service/builtin_skills/multica-platform/references/asks.md`。

## 项目记忆入口

- 领域词汇表：[CONTEXT.md](CONTEXT.md)
- 文档索引：[docs/README.md](docs/README.md)
- 架构决定：[docs/adr/](docs/adr/)
- 证据索引：[docs/evidence/INDEX.md](docs/evidence/INDEX.md)
- 当前移动端适配盘点：[docs/evidence/DENE-1277/report.html](docs/evidence/DENE-1277/report.html)

自动派票的执行席边界：
- 路由开启时，服务端负责决定智能体或小队的执行席；`todo` / `backlog` 上智能体写入的执行人会被路由从零判断，不能把猜测当成人的指派。
- 票过了 `todo` 后，智能体不能把执行席换成另一个智能体或小队；需要换人就关成 `blocked` 让路由给建议，工作太难用 `issue escalate`，有人的原话才用 `--per-quote`。验收交棒、额度接力等服务端内部动作不受这条限制。
- 每条自动选派评论都要用统一的「为什么是他」来源标签（当前为 `原话`、`人工`、`档位`、`兜底`）。聊天智能体默认只派票；负责人接票后按开工评论、关键进展、`issue close` 收尾的路径推进，不能自行把票改给别人。

## Goal 模式边界

- Goal 是一张任务加一条完成线，不新增任务类型或任务状态；完成线由若干可核验检查项组成，人确认后锁定，执行人不能自行降低标准或修改。
- 目标状态、完成线、轮次、证据和三项累计预算都住在服务端任务上；续跑、验收和刹车由服务端决定，换智能体时按任务记录接续。
- 目标预算按 token、运行次数和总时长累计；达到 80% 提示，达到上限或连续多轮无进展时暂停并用统一带选项提问请求下一步。单个席位额度耗尽时，先按同档位和方向接力，不把席位额度误当成目标预算。
