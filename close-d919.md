已完成 DENE-899：聊天分享的 workspace 可见性已贯通迁移、SQL 列表/游标/待处理任务、Go 访问判定与实时广播；workspace 档不要求项目绑定，guest 不可见。共享分享对话框已提供“Entire workspace”档，web/desktop 共用标题栏入口与编辑访问入口沿用。

本轮补齐实时广播过滤：workspace 聊天的 session_created/session_updated 会发送给 workspace 内非 guest 成员，避免仍只到操作者。

验证：
- `go test ./internal/handler/ -run 'ChatProjectSharing|ChatVisibility|ChatHandoff' -count=1` 通过
- `pnpm --filter @multica/views typecheck` 通过
- `pnpm --filter @multica/views exec vitest run chat` 通过（28 files / 314 tests）
- `make migration-lint` 通过

提交：`d882f6cd39 fix(chat): broadcast workspace chats to members`
