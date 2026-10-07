## 设置覆盖清单

对照设置页 `packages/views/settings/components/settings-page.tsx` 的每一项，写它打到哪条服务端接口、这条接口是上游还是魔改、CLI 现在怎么碰到它。Git 连接的安装流程归 DENE-968，这里只记设置页上那一行，不另做。

来源用这四个词：

- **界面有、接口是上游的**：设置页在用，路由在 `origin/main` 上就有。
- **魔改、界面在用**：这条路由是 kun 加上的，设置页已经在调。
- **接口在、界面没用**：服务端有这条，设置页没有对应的一项。
- **仅本地**：界面有，没有服务端接口。

`部分` 表示已有专用命令，但没有覆盖这一页的全部操作。高频对象继续走专用命令。长尾走 `multica settings get/set <key>`，CLI 只做 key 到已有接口的对应和 JSON 进出，权限仍在服务端。

### 个人

| 设置页 | 服务端接口 | 来源 | CLI |
| --- | --- | --- | --- |
| 个人资料 | `PATCH /api/me` | 界面有、接口是上游的 | 有：`user profile` |
| 偏好（语言、时区） | `PATCH /api/me` | 界面有、接口是上游的 | 部分：`user profile` 改的是个人资料，没有单独的偏好 key |
| 通知 | `GET` / `PATCH /api/notification-preferences` | 界面有、接口是上游的 | 没有 |
| 快捷键 | 无。存在本机 shortcut store | 仅本地 | 没有 |
| 令牌 | `GET` / `POST /api/tokens`，`DELETE /api/tokens/{id}` | 界面有、接口是上游的 | 没有 |

### 工作区

| 设置页 | 服务端接口 | 来源 | CLI |
| --- | --- | --- | --- |
| 工作区 | `GET` / `PATCH /api/workspaces/{id}` | 界面有、接口是上游的 | 有：`workspace get` / `workspace update` |
| 成员 | `/api/workspaces/{id}/members` | 界面有、接口是上游的 | 有：`workspace member` |
| 项目共享 | `PUT /api/projects/{id}/visibility`（`SetProjectVisibility`），预览 `GET .../visibility/preview`（`PreviewProjectVisibility`） | 魔改、界面在用 | 没有。最小通用入口未收这一行，防漂移把它记为例外 |
| 账单 | `/api/cloud-billing/*` | 界面有、接口是上游的 | 没有 |
| 智能体权限 | `GET` / `PUT /api/workspaces/{id}/agent-spawn`；运行上限仍在 `PATCH /api/workspaces/{id}` | 魔改、界面在用 | 部分：新建权限走 `settings get/set agent.spawn`；运行上限两项还没有 CLI（搬页前就没有） |
| 配置迁移 | `GET /api/workspaces/{id}/config/export`，`POST .../config/import` | 魔改、界面在用 | 没有。`multica transfer` 是另一条工作区迁移，不走这两条 |

### 任务

| 设置页 | 服务端接口 | 来源 | CLI |
| --- | --- | --- | --- |
| 任务状态 | `/api/issue-statuses` | 界面有、接口是上游的 | 没有。`issue status` 改的是某一张任务的状态，不是这张状态目录 |
| 标签 | `/api/labels` | 界面有、接口是上游的 | 有：`label` |
| 属性 | `/api/properties` | 界面有、接口是上游的 | 有：`property` |
| 快捷操作 | `/api/quick-actions` | 界面有、接口是上游的 | 没有 |
| 路由席位 | 工作区 settings 里的 routing-projects（项目没设领域时的兜底） | 魔改、界面在用 | 有：`workspace routing-projects` |
| 领域（派票页） | `/api/domains` | 魔改、界面在用 | 有：`domain list` / `add` / `rename` / `delete` |

### 连接

| 设置页 | 服务端接口 | 来源 | CLI |
| --- | --- | --- | --- |
| 仓库列表 | 工作区记录上的 `repos`，`PATCH /api/workspaces/{id}` | 界面有、接口是上游的 | 有：`repo list` / `repo add` / `repo remove` |
| 仓库可见范围 | 写：`PUT /api/repos/visibility`。读：没有单独的 GET，当前范围在工作区的仓库记录上 | 魔改、界面在用 | `settings get/set repo.visibility` |
| 仓库共享 | `GET` / `POST /api/repos/shares`，撤销 `DELETE /api/repos/shares?url=&member_id=` | 魔改、界面在用 | `settings get/set repo.shares` |
| GitHub | `/api/workspaces/{id}/github/installations` | 界面有、接口是上游的 | 没有。安装流程归 DENE-968 |
| Slack | `/api/workspaces/{id}/slack/installations` | 界面有、接口是上游的 | 没有 |
| 飞书 | `/api/workspaces/{id}/lark/installations` | 界面有、接口是上游的 | 没有 |
| 钉钉 | `/api/workspaces/{id}/dingtalk/installations` | 界面有、接口是上游的 | 没有 |
| Telegram | `/api/workspaces/{id}/telegram/installations` | 界面有、接口是上游的 | 没有 |
| 企业微信 | `/api/workspaces/{id}/wecom/installations` | 界面有、接口是上游的 | 没有 |
| Composio | `/api/integrations/composio/connections` | 界面有、接口是上游的 | 没有 |
| MCP | `/api/mcp-servers`，`/api/agents/{id}/mcp-servers` | 界面有、接口是上游的 | 有：`workspace mcp`、`agent mcp` |
| 插件 | `/api/workspaces/{id}/plugins` | 界面有、接口是上游的 | 没有 |

### 设置页以外，最小通用入口已经接上的

这些不在设置页的侧栏里，但这张票要求智能体能读写。

| 项 | 服务端接口 | 来源 | CLI |
| --- | --- | --- | --- |
| 智能体临时通行证 | `GET` / `POST /api/agents/{id}/access-passes`，撤销 `DELETE .../access-passes/{passId}` | 魔改、界面在用（智能体页，不是设置页） | `settings get/set agent.{id}.access-passes`。撤销体是 `{"revoke_id":"..."}` |
| 运行时开放范围 | 写：`PATCH /api/runtimes/{id}`，体里带 `visibility`。读：单条地址没有 GET，开放范围在运行时列表上 | 界面有、接口是上游的 | `settings get/set runtime.{id}.visibility` |
| 运行时技能开关 | 写：`PUT /api/agents/{id}/runtime-skills/enabled`。读：这条地址只接受写入，当前开关在智能体自己的 `disabled_runtime_skills` 上 | 界面有、接口是上游的 | `settings get/set agent.{id}.runtime-skill` |
| 工作区模块访问策略 | `GET /api/modules`，`PUT /api/modules/{module}/visibility` | 魔改、界面在用 | `settings get modules.visibility`，`settings get/set module.{module}.visibility` |
| 智能体调用权限、环境变量、技能、标签、自动化 | 各自的 `/api/agents/{id}/...` | 界面有、接口是上游的 | 有专用命令：`agent update --permission-mode`、`agent env`、`agent skill` |
| 运行时 profile | `/api/runtime-profiles` | 界面有、接口是上游的 | 有：`runtime profile` |

### 接口在、界面没用

| 项 | 服务端接口 | 来源 | CLI |
| --- | --- | --- | --- |
| 模型目录刷新 | `POST /api/daemon/runtimes/{id}/model-catalog/refresh` | 接口在、界面没用。这是守护进程动作 | 没有 |
| 自动化触发器签名密钥 | `PUT /api/autopilots/{id}/triggers/{id}/signing-secret` | 接口在、界面没用。不在设置页 | 没有。密钥不进通用 JSON |
| 任务可见范围 | `PUT /api/issues/{id}/visibility`（`SetIssueVisibility`） | 魔改。不在设置页，任务页在用 | 没有，仍走任务接口 |

### 不进通用入口的同类接口

下面三个处理函数和可见范围是同一族，防漂移扫描会看到它们。没有进 `settings get/set` 是写明的，不是漏扫。

| 处理函数 | 原因 |
| --- | --- |
| `SetProjectVisibility` | 设置页「项目共享」用项目可见范围，最小通用入口未收 |
| `PreviewProjectVisibility` | 项目可见范围的预览，不是一项可读写的设置 |
| `SetIssueVisibility` | 任务可见范围不在设置页，仍走任务接口 |

## 怎么用

输出只有 JSON。`--output table` 会直接拒绝，不再假装打出表格。

```sh
multica settings get repo.visibility --value-json '{"url":"https://github.com/acme/app.git"}'
multica settings set repo.visibility --value-json '{"url":"https://github.com/acme/app.git","visibility":"workspace"}'
multica settings get repo.shares --value-json '{"url":"https://github.com/acme/app.git"}'
multica settings set repo.shares --value-json '{"url":"https://github.com/acme/app.git","member_id":"MEMBER_ID"}'
multica settings set repo.shares --value-json '{"url":"https://github.com/acme/app.git","member_id":"MEMBER_ID","revoke":true}'
multica settings get runtime.RUNTIME_ID.visibility
multica settings set runtime.RUNTIME_ID.visibility --value-json '{"visibility":"public"}'
multica settings get agent.AGENT_ID.runtime-skill
multica settings set agent.AGENT_ID.runtime-skill --value-file ./runtime-skill.json
multica settings set agent.AGENT_ID.access-passes --value-stdin < pass.json
```

不带 `url` 的 `settings get repo.visibility` 返回工作区里每条仓库的当前可见范围。`repo.visibility` 的写入是 PUT。`runtime.{id}.visibility` 的写入仍是 PATCH。`agent.{id}.runtime-skill` 的写入仍是 PUT。

方法只在 key 表里决定一次。`server/cmd/multica/cmd_settings.go` 的 `settingsKinds` 同时写下路径、方法和它认领的处理函数。

## 防漂移

`TestSettingsSurfaceRoutesAreClaimed` 读 `server/cmd/server/router.go`。下面这些路径上新出现的处理函数，如果没有写进 `settingsKinds`，也没有写进上面的例外表和本文件，测试变红：

- `/api/repos/`
- `access-passes`
- `runtime-skills/`
- `/api/modules`
- 路径恰好是 `/visibility` 或 `/visibility/preview` 的可见范围

删掉已经认领的 `SetRepoVisibility` 也会变红。新加一条 `/api/repos/` 路由却不进通用入口，同样变红。
