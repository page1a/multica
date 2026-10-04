## DENE-1276 路由设置对账

核对基准：Web 设置页的工作区 `settings.routing` 和智能体记录上的路由字段。服务端路由只从这两份工作区/智能体数据读取；`server/internal/routing/ladder.json` 只提供未配置方向和档位的默认阶梯，不覆盖席位标签。

| 配置项 | Web/Desktop | 服务端读取点 | CLI | 结论 |
| --- | --- | --- | --- | --- |
| 路由启用、分析/判断角色、模型、来源、运行时、思考级别 | Settings → Routing；保存 `settings.routing` | `routing.Settings`、`Mode()`、`PrimaryTarget()` | `workspace routing get/set` | 一致 |
| 置信度阈值、验收席滞留时长 | Settings → Routing | `Settings.ConfidenceThreshold`、`StaleReviewHours` | 暂无专用 CLI；服务端仍读取同一字段 | 数据源一致；CLI 长尾缺口，后续可补 |
| 用量优先 | Settings → Routing → `usage_priority` | `Settings.SeatOrder()` / `Ladder.WithSeatOrder()` | `workspace routing get/set --usage-priority on|off` | 已补齐 CLI，并覆盖默认值（缺省为 on） |
| 允许上调一档 | Settings → Routing → `allow_upshift` | `Settings.SeatOrder()` / `Ladder` 上调逻辑 | `workspace routing get/set --allow-upshift on|off` | 已补齐 CLI |
| 接着做、负载分流 | Settings → Routing → `prefer_continuation`、`prefer_idle` | 路由选择器读取同一开关 | `workspace routing get/set --continuation`、`--load` | 一致 |
| 席位档位、用量 | Routing seats table；智能体记录 `routing_tier`、`routing_usage` | `ListRoutingAgents` 与阶梯选择 | `agent update --routing-tier/--routing-usage` | 一致；服务端不再用旧 ladder 档位覆盖标签 |
| 智能体方向 | 智能体详情/分身关系与项目方向映射 | 路由按项目映射选择同档方向席位 | `workspace routing-projects list/set/unset` | 一致 |
| 跟随基础角色 | 智能体详情的 Runtime inheritance | `runtime_inherited` 为 true 时服务端拒绝直接改从属运行配置 | `agent update --runtime-inherited=false`；详情页已有开关 | 席位表灰显是约束；解除跟随需在智能体详情页或 CLI 先关闭跟随，再改档位/用量 |
| 验收席规则 | 任务动态/验收交接 | 路由的 reviewer 选择与 `issue handoff` | `issue handoff` | 一致 |
| 额度接力 | 任务接力/运行时配额状态 | quota relay 读取运行时和席位额度 | `issue handoff`/服务端内部接力 | 一致；没有可编辑的独立工作区开关 |
| 自动化 | Autopilot 设置与触发器 | `/api/autopilots/*` 与服务端触发器 | `autopilot` 命令族 | 一致；触发器签名密钥为写入后不可读的接口，不进入通用 routing JSON |

### 结论与边界

- 本轮发现并修复的前后端/CLI 不一致是 `workspace routing get` 不显示、`set` 不能修改 `usage_priority` 和 `allow_upshift`。
- 旧席位档位表只用于默认阶梯；已存在的席位标签和实际模型优先。
- 分身席位灰显不是丢失写权限：它表示 `runtime_inherited=true`。解除跟随会保留当前运行配置，之后才允许独立编辑；这是服务端的两步校验，界面已在智能体详情提供入口。
- 置信度、滞留时长等长尾字段已有统一服务端真源，但没有专用 CLI 参数；它们不再由环境变量或 CLI 推断。后续若需要 Agent 直接编辑，应新增同一 `settings.routing` 的 CLI 参数，而不是另存一份配置。
