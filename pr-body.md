Closes DENE-1291

完成手机端全局外壳与移动共性适配：

- Web 视口启用 `viewport-fit=cover`；浮动聊天、toast、Dialog、底部 Sheet 统一避开 safe-area，长内容可滚动。
- 手机边缘右滑返回沿用应用内 history；浮动聊天、Dialog、Sheet 打开时各自压入历史项，系统返回手势优先关闭当前覆盖层。
- 窄屏默认交互控件保留 44px 最小触控区；`xs`、`sm`、紧凑图标按钮保留原有紧凑尺寸，避免表格和工具栏被撑高。
- HTML 预览：`docs/design/dene-1291-mobile-shell-preview.html`。

验证：

- `pnpm --filter @multica/web typecheck` 通过。
- `pnpm --filter @multica/desktop typecheck` 通过。
- UI 包类型检查通过，并执行 `git diff --check`。
- Ego 浏览器已打开并探测 HTML 预览；本机磁盘空间不足，Chromium 无法安装，因此未生成新的真实像素截图。已有票据附件中的 375px PNG 仍保留为历史示意，不作为本轮真实截图证据。

手机端：本 PR 只改 Web/Desktop 共享外壳；`apps/mobile` 没有对应组件入口。

## 知识审计
- 术语/ADR：不改
- AGENTS.md：不改
- DESIGN.md / INTERACTION.md：不改
- Skill：不改
- Multica Project Context：不改
