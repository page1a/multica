## DENE-1293 移动端验收证据

验收基线：Web/Desktop 390px 宽；共享详情页使用 `BreadcrumbHeader`，操作区与宽表使用触摸横向滚动，内容区域允许纵向滚动。原生 `apps/mobile` 没有这些协作与资源详情屏，本轮不改原生 App。

| 页面 | 路径 | 结论 | 代码/验证依据 |
| --- | --- | --- | --- |
| 聊天 | `/[workspace]/chat`、`/chat/[sessionId]` | 已核对 | `packages/views/chat/chat-page.tsx` 保留来源回退与消息区滚动。 |
| 智能体（含新建） | `/agents`、`/agents/new`、`/agents/[id]` | 已核对 | 新建流程使用 `useBackOrReplace`；详情沿用共享返回。 |
| 小队 | `/squads`、`/squads/[id]` | 已修 | `squad-detail-page.tsx` 使用 `BreadcrumbHeader`，操作区可横向触摸滚动。 |
| 技能 | `/skills`、`/skills/[id]` | 已修 | `skill-detail-page.tsx` 使用 `BreadcrumbHeader`，长说明在内容区换行。 |
| 自动化 | `/autopilots`、`/autopilots/[id]` | 已修 | `autopilot-detail-page.tsx` 使用 `BreadcrumbHeader`，操作区不挤压标题。 |
| 运行时 | `/runtimes`、`/runtimes/[id]` | 已修 | 骨架手机单列；删除级联 Agent 表可横向滚动；详情使用共享返回。 |
| 成员 | `/members/[id]` | 已核对 | `member-detail-page.tsx` 沿用导航回退，列表保持纵向滚动。 |
| 用量 | `/usage` | 已修 | 每日明细表保留 600px 内容轨道并支持横向触摸滚动。 |
| 账单 | `/billing` | 已核对 | 账单页沿用列表返回，金额列不强制压缩。 |
| 设置 | `/settings` | 已核对 | 设置分组可纵向滚动，控件保持触摸尺寸。 |
| 关联工作区 | `/linked` | 已核对 | 关联详情沿用共享返回，长名称允许换行。 |
| 附件预览 | `/attachments/[id]/preview` | 已核对 | 预览关闭/返回入口可触摸，内容区独立滚动。 |

共享返回行为：有浏览历史时调用 `back`，无历史时 `replace` 到最后一个祖先；左上角按钮使用本地化的返回 aria-label。操作区使用 `overflow-x-auto`，宽表使用独立横向滚动容器。

代码验证：

- `pnpm --filter @multica/views exec vitest run layout/breadcrumb-header.test.tsx runtimes/components/runtime-detail-visibility.test.tsx runtimes/components/usage-section.test.tsx projects/components/project-detail.test.tsx`（4 files / 28 tests passed）
- `pnpm --filter @multica/views typecheck`
- `git diff --check`

HTML 预览稿：`.scratch/mobile-preview.html`（390px 视口，含返回、操作区横滑、12 项页面清单）。
