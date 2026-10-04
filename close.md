实现与验证已完成，交付 PR：https://github.com/jeff-kunkun/multica/pull/513

代码提交：ac5887bbb0

已交付：
- 手机边缘右滑返回与应用内 history 保护。
- 浮动聊天、Dialog、Sheet 的返回手势关闭。
- 窄屏 44px 触控区，同时保留紧凑按钮尺寸。
- viewport-fit=cover、安全区、toast/聊天/Dialog/Sheet 承载与 HTML 预览。

验证：
- pnpm --filter @multica/web typecheck
- pnpm --filter @multica/desktop typecheck
- git diff --check
- Ego 已打开 HTML 预览并确认可访问；本机 Chromium 安装因磁盘空间错误失败，375/768/1280 真实像素截图仍待验收席补做或确认。

当前状态：代码已 ready，等待验收席确认截图证据后合并。
