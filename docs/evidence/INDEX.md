# 证据索引

本索引只记录指向沉淀票附件的短指针；原始日志、截图和样本留在对应票据附件。

- DENE-1050：统一带选项提问的三面交付；[PR #448](https://github.com/jeff-kunkun/multica/pull/448)，界面预览 `preview/ask-options-card.html`。
- DENE-1051：任务完成线、锁定状态与三项预算；[PR #449](https://github.com/jeff-kunkun/multica/pull/449)，界面预览 [`docs/design/goal-section-preview.html`](../design/goal-section-preview.html)。
- DENE-1052：目标任务自动续跑、预算刹车与席位接力；[PR #452](https://github.com/jeff-kunkun/multica/pull/452)。
- DENE-1053：统一补全完成线页及四个目标入口；[PR #453](https://github.com/jeff-kunkun/multica/pull/453)。
- DENE-1201：进行中改派保护、执行席来源可见性与三面路由契约；[PR #479](https://github.com/jeff-kunkun/multica/pull/479)。
- DENE-1277：全站 Web/Desktop 响应式盘点与页面族拆分；[盘点报告](DENE-1277/report.html)，第二阶段子票 DENE-1292 至 DENE-1296 仍在进行。
- DENE-1291：共享移动外壳的返回栈、安全区、触控尺寸与窄屏承载；[PR #513](https://github.com/jeff-kunkun/multica/pull/513)，[HTML 预览](../design/dene-1291-mobile-shell-preview.html)。
- DENE-1342：巡检识别没人驱动的票、两次原席位重跑后上报父票，`multica issue dispose` 四种处置；[预览与 375/768/1280 截图](DENE-1342/preview.html)。
- DENE-1328：任务状态卡（`multica issue context`、`--decision`、拍板增改删）；[预览与 1280/390 截图](DENE-1328/preview.html)。
- DENE-1346：聊天插话 / 排队 / 打断重来与常驻停止按钮，任务页叫醒方式统一；[预览与 1280/390 截图](DENE-1346/preview.html)。
- DENE-1362：原生 App 聊天插话 / 排队 / 打断重来，回复中常驻停止按钮；模拟器截图 [回复中输入](DENE-1362/typing.webp)、[弹层](DENE-1362/sheet.webp)、[键盘弹起](DENE-1362/keyboard-stop.webp)、[插话后](DENE-1362/sent.webp)、[不支持插话](DENE-1362/no-steer.webp)。
- DENE-1349：Cursor / Copilot / CodeArts / DevEco / Antigravity / OpenClaw 用重启续接插话，同一会话 ID 继续原任务；[cursor-agent 实测与预览](DENE-1349/README.md)。
- DENE-1348：OpenCode 1.x、Pi 接通插话，Qwen Code / CodeBuddy / DSH 降级排队并写明原因；[实测记录](DENE-1348/report.md)。
- DENE-1347：ACP 类 CLI 停一步续接插话（11 个 CLI 共用一段代码），Grok 原始协议实测通过，Kimi/Hermes 按 Kun 决定不实测；[实测记录](DENE-1347/README.md)，[菜单预览（含 390px）](DENE-1347/steer-menu-preview.html)。
- DENE-1451：工作区领域列表、项目多领域、任务单领域、特化 = 基础角色 + 领域，路由与原话按任务领域落到特化；[预览与 1280/390 截图](DENE-1451/preview.html)。
- DENE-1479：Web/Desktop 选智能体统一按领域排序（负责人、验收席、@ 提及、聊天、自动化、项目负责人），tarot 对口特化在前、Multica 魔改 基础角色在前；[1280/390 截图汇总](DENE-1479/index.html)。
- DENE-1643：聊天挂连通工作区的只读参考项目（+ → 项目上下文 → 连通的项目（只读），失效标记，建连通页文案）；真实聊天页 375/390/768/1280 操作截图与服务端结果在 [live/](DENE-1643/live/steps.txt)（勾选、移除、取消共享与撤销后失效、逐个移除失效项），早期静态稿 [预览](DENE-1643/preview.html)。
- DENE-1661：收口的知识声明要对上交付文件、聊天沉淀合进主线（`multica chat sediment`），项目记忆卡片新增「最近沉淀」，子任务收口条显示实际写入的文件；[预览](DENE-1661/preview.html)，[1280](DENE-1661/preview-1280.png) / [390](DENE-1661/preview-390.png) 截图。
- DENE-1670：DENE-1659 的实页与完整链路验收——本地候选环境里跑真实 CLI → 服务端 → 数据库 → 页面的聊天沉淀（本地合入主线、远端未推送拒记、推送后记录），项目记忆卡片与收口条 375/390/768/1280 实页截图；[报告](DENE-1670/report.html)，复现脚本见报告内 scripts/。
- DENE-1665：聊天开出的单自动记下来源聊天；聊天里开单卡（谁在做、为什么、实时状态，改给我 / 改给智能体 / 撤回），任务单显示「来自聊天」；`GET /api/chat/sessions/{id}/tickets` 与 `multica chat tickets`；[1280/390 截图](DENE-1665/preview.html)。
- DENE-1672：聊天开出的任务完成 / 卡住 / 待验收时往来源聊天发回执卡（结论、PR、沉淀），`multica chat tickets` 带结果列，状态卡「来源」行与侧栏「原话」，聊天每回合带上它开出的任务现状；[375/390/768/1280 截图与 CLI 输出](DENE-1672/preview.html)。
- DENE-1677：路由定档改为选择题加规则表，判断模型只能上调一档；设置页只读规则表与自动选派评论的答题记录，[预览（含 390px）](DENE-1677/preview.html)、[桌面](DENE-1677/desktop.png)、[手机](DENE-1677/mobile.png)。
- DENE-1667：聊天底部「本聊天的单」进度条与浮窗/手机抽屉（刚变置顶），聊天里听汇报（Mermaid 图 + 要你做的 + 操作按钮，听完读掉收件箱），「听到哪了」按人 + 项目存服务端，项目页听汇报入口，`multica project report`；[预览与 375/768/1280 截图](DENE-1667/preview.html)，原生 App 的汇报图与入口在 DENE-1682。
- DENE-1647：验收席失败后的接力去向（执行记录「失败后已转给 X」/「等人决定」）和席位鉴权暂停说明；桌面与 390px 手机静态稿 [预览](DENE-1647/preview.html)、[桌面截图](DENE-1647/desktop.png)、[手机截图](DENE-1647/mobile.png)。

- DENE-1679：子任务回执汇总给父票——完成评论、状态卡「子任务回执」、来源聊天的父票回执卡与 `multica issue context --output json` 的 `children` 都列出每张子票的结论、PR、沉淀；[375/390/768/1280 截图与 CLI 输出](DENE-1679/preview.html)。
- DENE-1680：老板层沉淀与记忆卫生——项目记忆卡片「最近沉淀」显示「汇总自 DENE-N」和每条改动的动作（新建/更新/待合并/标记已被取代），`multica project memory status` 同样列出来源；[375/390/768/1280 截图与 CLI 输出](DENE-1680/preview.html)。
- DENE-1681：项目记忆卡片加沉淀与回流监控——近 14 天写入与删除行数（来源票 / 聊天）、收口没沉淀（声明无可沉淀 / 没做审计）、沉淀轮次与空转、聊天派单回流（已回报 / 无结论 / 进行中），每行可点回来源；`multica project memory monitor` 同一接口；[375/390/768/1280 截图与 CLI 输出](DENE-1681/preview.html)。
