# Automatic stall actions

停滞处理和任务本身共用同一张票、评论与收件箱记录，不另建清理页。

服务端每 30 分钟跑一次停滞巡检（选择这个频率是为了让 1.5 天静默阈值有可预测的半小时级误差，同时不为每次子票事件增加一次全库扫描）。当父票的全部子票进入终态、父票连续 1.5 天没有活动、没有进行中的运行或关联 PR 时，巡检会把仍开放的父票标记为 `done`，并在票上写系统说明。子票完成当下仍会照常通知并叫醒父票负责人；父票若有 `close.conclusion=in_progress` 或 `deferred` 的有意暂停记录，服务端不处理。自动收口或自动取消后 7 天内可以撤销，撤销会恢复服务端记录的原状态。

没有关联子票的安静开放票会在同一轮巡检交给工作区已配置的 routing AI 判断；低置信度、模型不可用或判断为可保留的票都不处理。AI 认为重复/无效后先进入公示，服务端会强制把“已核对关联 PR、附件与其他交付产物”写进理由；有关联 PR 的票直接跳过。没有 routing AI 时，可信调用方可以先写入 `stall.candidate=true` 再由巡检接管。公示 24 小时内保留即可阻止自动取消；到期无人保留才会取消。公示时写入的评论不会重置 24 小时窗口。每一步都会有票上系统评论和收件箱每日汇总提醒，收件箱条目也提供保留/撤销按钮。

```bash
multica issue stall list --output json
multica issue stall review <issue-id> --reason "已核对相关票和交付物，判断为重复或无效" --output json
multica issue stall keep <issue-id> --output json
multica issue stall undo <issue-id> --output json
```

`list` 返回公示中、已保留、已取消、已自动收口和已撤销的处理记录。`keep` 只接受仍在 24 小时公示窗口内的票；`undo` 只接受仍在 7 天窗口内且由系统自动处理的票。Web/Desktop 使用相同的 `/api/issues/stall-actions`、`/api/issues/{id}/stall/keep` 与 `/api/issues/{id}/stall/undo` 接口。
