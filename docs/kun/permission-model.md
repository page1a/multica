# 权限底座：四档档位 × 三档共享范围

DENE-695 权限系统的判定规则。这篇是给人读的矩阵；可执行的版本是 `server/internal/permission`，它的测试把每一格都写死了，两边不一致时以测试为准并回来改这篇。

## 两层，互不越界

| 层 | 回答的问题 | 来源 |
| --- | --- | --- |
| 共享范围（visibility） | 这个资源对你来说**存不存在** | 资源自己身上的 `visibility` 字段 |
| 档位（role） | 对一个存在的资源，你**能做什么** | `member.role` |

- 共享永远不给写权限：把东西共享给访客，他还是只读。
- 档位永远不给视野：Member 再怎么是正式成员，没共享给他的东西他也看不到。
- 唯一的例外方向是 Owner / Admin 的「管理兜底」，见下文——它来自 `listAccessibleProjectIDs` 的既有行为，不是新规则。

**范围内的资源**：Issue（评论、附件跟随所属 issue）、项目、仓库。
**不在范围内**：Agent 与 Squad（归 `agent.permission_mode` + `agent_invocation_target`，Parent B）；工作区设置 / 成员 / 计费（只看档位，不参与共享）。

## 判定顺序

每一次「用户 + 资源 + 操作」按这个顺序走，走到第一个否定就停：

1. **是不是这个工作区的成员。** 不是 → 找不到。
2. **看不看得见**（`permission.CanSee`）。看不见 → 找不到。**不是「无权限」**——后者等于告诉对方这个东西存在。列表、搜索、聚合、通知同理，直接不出现。
3. **档位允不允许这个操作**（`permission.Allowed`）。不允许 → 无权限（403）。此时对方已经看得见资源，说「无权限」不泄露任何东西。

不针对具体资源的操作（新建、邀请、改设置、计费）跳过第 2 步，只看档位（`permission.AllowedInWorkspace`）。

## 第一层：看不看得见

「关系」三选一：**创建者**、**在项目里**（资源所属项目在此人的 `listAccessibleProjectIDs` 结果里）、**无关**。

| 档位 | 范围 | 无关 | 在项目里 | 创建者 |
| --- | --- | --- | --- | --- |
| Owner / Admin | `private` | 不可见 | 不可见 | 可见 |
| Owner / Admin | `project` | —（见注） | 可见 | 可见 |
| Owner / Admin | `workspace` | 可见 | 可见 | 可见 |
| Member | `private` | 不可见 | 不可见 | 可见 |
| Member | `project` | 不可见 | 可见 | 可见 |
| Member | `workspace` | 可见 | 可见 | 可见 |
| Guest | `private` | 不可见 | 不可见 | 可见 |
| Guest | `project` | 不可见 | 可见 | 可见 |
| Guest | `workspace` | **不可见** | **不可见** | 可见 |

注：Owner / Admin 对工作区内**每一个**项目都算「在项目里」（管理兜底），所以他们和 `project` 范围的资源不存在「无关」这种关系。

读这张表的几条要点：

- **零信任**：新建资源默认 `private`。什么都没共享时，Member 与 Guest 那几行只剩「创建者」一列是可见——也就是只看得到自己建的，别人的一概不存在。
- **`private` 对 Owner / Admin 同样不可见。** 管理兜底只覆盖 `project` 范围（它是「所有项目」的兜底，不是「所有资源」的兜底）。这和 `issue_view` 现在的行为一致。
- **`workspace` 不含访客**，哪怕访客恰好也在那个项目里。要给访客看，范围必须是 `project`。
- **创建者永远看得见自己建的东西。** 访客不能新建，这一列对访客只在「原来是 Member、后来被降成访客」时才会出现：他还看得见自己以前建的，但已经只读。
- `project` 范围只能设在属于某个项目的资源上（`permission.CanSetVisibility`）；不属于任何项目的资源只能 `private` 或 `workspace`。对「项目」这种资源，「所属项目」就是它自己。

## 第二层：能做什么

前提是第一层已经判了可见。

| 操作 | Owner | Admin | Member | Guest |
| --- | --- | --- | --- | --- |
| 查看 | 可 | 可 | 可 | 可 |
| 评论 / 回复 | 可 | 可 | 可 | 否 |
| 编辑、改状态、传附件、指派 | 可 | 可 | 可 | 否 |
| 改这个资源的共享范围 | 可 | 可 | 仅自己建的 | 否 |
| 把人加进 / 移出项目 | 可 | 可 | 仅自己带的项目 | 否 |

工作区级操作（不看共享）：

| 操作 | Owner | Admin | Member | Guest |
| --- | --- | --- | --- | --- |
| 新建 issue / 项目 / 仓库 | 可 | 可 | 可 | 否 |
| 被指派、被 @ 触发运行 | 可 | 可 | 可 | 否 |
| 邀请成员、改别人档位 | 可 | 可 | 否 | 否 |
| 工作区设置 / 集成 / 密钥 | 可 | 可 | 否 | 否 |
| 计费、转让或删除工作区 | 可 | 否 | 否 | 否 |

Guest 一整列除了「查看」全是否，没有任何关系能翻过来：即使他是某资源的创建者、某项目的 lead，也不能改范围、不能管项目成员。全局只读拦截层（DENE-697）只需要问一句 `Role.CanWrite()`。

**失败即关闭**：不认识的档位、不认识的范围值、不认识的操作，一律按「看不见 / 不允许」处理。

## 数据模型

- `member.role`：CHECK 扩成 `owner / admin / member / guest`（migration 502）。存量行不动，无人降级。
- 资源上的 `visibility TEXT NOT NULL DEFAULT 'private'`，CHECK 只允许 `private / project / workspace`，外加一条配对约束「`project` 范围必须有所属项目」——写法照抄 `479_issue_view_project_visibility`。加列与存量回填成 `workspace` 属于 DENE-698。
- **不新建任何名单表。**「项目成员」= `project_member` 的行 + 项目 lead，由 `listAccessibleProjectIDs` 合并；共享界面里不逐个勾人，也不给某个人单独选读写。
- 回滚只收紧不放宽。502 的 down 不能把访客改写成 Member（那是给只读的人发写权限），所以访客在回滚时直接失去成员资格，连同他们的 `project_member` 行一起删掉；重新上线后再邀请。

### 「访客」已经可以在接口里选了（DENE-697 起）

502 只让数据库**放得下** `guest`，当时故意没放开接口：那时绝大多数写接口只检查「是不是成员」，不看档位，放出一个访客等于发给他 Member 的全部写权限，却顶着「只读」的名字。那条约束是「接口放行 `guest` 必须和拦截层同一次上线」，DENE-697 已经兑现：

- 全局只读拦截层 `middleware.GuestReadOnly` 挂在整个已认证 `/api` 路由组的最前面，按 HTTP 方法判定，而不是靠每个 handler 自己记得检查。
- `normalizeMemberRole` 放行 `guest`；邀请表的 role CHECK 扩成 `admin / member / guest`（migration 508）。`workspace_share_link` 不走 `normalizeMemberRole`，仍是 `admin / member`，不在本次范围内。

拦截层只对写方法（非 GET/HEAD/OPTIONS）生效，并留了一份**只涉及本人账号状态**的白名单：`/api/me`、`/api/cli-token`、`/api/feedback`、`/api/client-usage`、`/api/inbox`、`/api/notification-preferences`，加上「新建自己的工作区」和「退出工作区」。DENE-718 补上了同样只改本人账号、却会被当前工作区请求头误伤的几条：接受或拒绝发给自己的邀请（`/api/invitations`）、用分享链接加入（`/api/share-links/join`）、自己的个人访问令牌（`/api/tokens`），以及 Lark / Slack / DingTalk / WeCom / Telegram 的绑定兑换（`/binding/redeem`）。前缀匹配按路径段边界比较，`/api/me` 不会误命中 `/api/members`。访客改不了任何工作区内容，但能改自己的名字、标记通知已读、退出工作区、处理自己的邀请和令牌。

拦截层还顺手把它读到的 `member` 行按「用户 + 工作区」配对塞进 context，`RequireWorkspaceMember` 直接复用，所以写请求的成员查询次数不变，仍是一次。

## 缓存失效

先纠正一个前提：**两级缓存里都没有档位，也没有共享范围。**

| 缓存 | 键 → 值 | TTL | 里面有什么 |
| --- | --- | --- | --- |
| `MembershipCache` | 用户 + 工作区 → `"1"` | 5 分钟 | 只有「是成员」这一个事实 |
| `PATCache` | token 哈希 → 用户 id | 10 分钟 | 只有「这个 token 是谁的」 |

普通请求走 `RequireWorkspaceMember` 中间件，每次都从数据库读 `member` 行，所以**改档位对普通接口本来就是即时的**。真正会延迟的只有绕过中间件、直接信 `MembershipCache` 的三处：daemon 工作区校验两处（`handler/daemon.go`）、附件下载一处（`handler/file.go`）。

由此定下四条规则：

1. **档位与共享范围永远不进这两级缓存。** 可见性判定每次读库（`visibility` 列 + `listAccessibleProjectIDs`）。要提速就在单次请求内复用结果，不跨请求缓存——跨请求缓存一旦出现，「即时生效」就变成了又一处要记得失效的地方。
2. **档位变更、移除成员 → 失效该用户的 `MembershipCache`。** 现状已经做到（`UpdateMember` / `DeleteMember` / 退出 / 删工作区都调了 `Invalidate`）。
3. **`PATCache` 不需要因为档位或共享变更而失效。** 它只回答「token 属于谁」，这个答案不随档位变。它唯一需要失效的时机是吊销 token，现状已做。把它列进「每次共享变更都要清」只会制造无意义的缓存击穿。
4. **信 `MembershipCache` 的三处，缓存命中之后仍要补判定。** 这是本次排查发现的真正漏洞：
   - 附件下载：原本命中缓存就直接放行，完全不看附件所属 issue 的可见性。DENE-698 已在 `loadAttachmentForRequest` / `loadAttachmentForDownload` 两条路径补上 `requireAttachmentIssueVisible`：附件挂在 issue 上时按母 issue 的范围判定，拒绝时回 404「attachment not found」，与不存在完全同形。不挂 issue 的附件（聊天、头像）由各自的接口管辖，不在这一层拦。
   - daemon 两处：访客不应能注册或操作 runtime。DENE-697 选了「干脆不给访客写缓存」这条：`requireDaemonWorkspaceAccess` / `verifyDaemonWorkspaceAccess` 读到成员行后先过 `daemonAccessAllowedForTier`，访客按「找不到」拒绝，也不写 `MembershipCache`——缓存里存的是「是成员」这一个事实，一条代表访客的缓存项会让后面所有命中都放行。

共享变更（改范围、项目加人减人）因为第 1 条，**没有任何缓存需要清**：下一次请求读库就是新答案。前端侧由 WebSocket 事件让相关 Query 失效即可，和现有 `member:updated` 同一个模式。

## 留给后续票的边界

- **lead 看不见自己带的 `private` 项目。** 如果 A 建了一个 `private` 项目并把 B 设成 lead，按矩阵 B 看不见它（`private` 只认创建者）。矩阵答案是唯一的，但体验上会怪；DENE-698 做「设 lead」时应提示把项目范围改成 `project`。
- **创建者离开工作区后，他的 `private` 资源对所有人不可见**（包括 Owner）。需要产品决定：移除成员时转交给操作人，还是保留为孤儿。不决定也不会出错，只是那些资源谁也找不回来。
- 模块级可见性（DENE-699）是叠在这两层之上的第三道「与」门，不改变这张矩阵的任何一格。见下方「模块级可见性」。

## 三档共享范围落地（DENE-698）

### 数据模型

| 迁移 | 做了什么 |
| --- | --- |
| 509 | `project.visibility`（CHECK 三档，默认 `private`）+ `project.created_by`；存量项目回填 `workspace` |
| 510 | `issue.visibility` + 配对约束 `visibility <> 'project' OR project_id IS NOT NULL`；存量 issue 回填 `workspace` |
| 511 | 仓库不是表，是 `workspace.repos` 里的 JSONB 条目，范围盖在条目上；存量回填 `workspace` |
| 512 | `visibility_audit`：谁、何时、改了哪个资源、从哪档到哪档、这一档覆盖多少人、是直接改还是被项目扫中 |
| 513–515 | 上述三张表/列各自的并发索引，一个文件一条语句 |

**down 只收紧不放宽**：回滚时先把 `project` / `workspace` 的行统一改成 `private` 再删列，所以回滚之后没有任何人能看到比回滚前更多的东西——代价是回滚后一切都要重新分享，这是刻意选的方向。

### 被指派人永远看得见

档位之上还有两条「名字写在资源上」的关系：创建者与**被指派人**。把活指派给谁，本身就是一次分享——指派了却打不开是产品不该能到达的状态——所以被指派人在任何档位下都看得见那张 issue，和创建者同级。指派不改档位，只多认一个人；project / repo 没有指派人，这条只对 issue 成立。SQL 与 Go 两侧都带这一项，parity 测试把「指派给我 / 指派给别人 / 指派给智能体 / 未指派」也叉进矩阵。

### 读侧：一个判定，两种写法

`visibilityViewer`（`handler/visibility.go`）是这一层的唯一入口。它的项目集合直接复用 `listAccessibleProjectIDs`，不新增查询。

- **分页的列表**（issue 搜索、`ListIssues`、issue 表格、分组、项目搜索）把范围拼进 SQL：`issueVisibilitySQL` / `projectVisibilitySQL`。必须在 SQL 里，否则翻页和 total 描述的是调用者看不到的行。
- **不分页的读**（`open_only`、子 issue 列表、项目列表、收件箱）在 Go 里过滤：`canSeeIssueFields` / `canSeeProject` / `canSeeRepo`。
- 两种写法是同一个矩阵的两种语言，`TestIssueVisibilitySQLAgreesWithCanSeeIssue` 把它们钉在一起：任何一边先改都会红。
- 拒绝一律是「不存在」。`hiddenIssueNotFound` 与真正的 404 逐字节相同。

绕过这一层的只有两种调用者，都用命名常量记着原因：`bypassAgent`（智能体的读由 `agent.permission_mode` 与 `agent_invocation_target` 管辖，跑人类的范围会让它看不见刚派给自己的 issue）与 `bypassInternal`（daemon 管道、webhook 扇出，根本没有人类调用者）。

### 写侧：项目是批量开关

- `PUT /api/issues/{id}/visibility`、`PUT /api/projects/{id}/visibility`、`PUT /api/repos/visibility`。
- `GET /api/projects/{id}/visibility/preview` 先回答「会扫到多少」：`affected_count` 与 `previously_private_count`，供确认弹窗用；正式写入返回同样两个数字加 `audience_size`。
- 改项目范围 = 覆盖它当前持有的全部资源。之后进入项目的资源取项目当时的范围，随后各自独立，不再被项目带着走。
- 新建资源默认 `private`，只有一个例外：**智能体创建的 issue 若不属于任何项目，落地就是 `workspace`**。`private` 的含义是「只有创建者看得见」，而智能体不是一个能被展示列表的人——这样的 issue 留在 `private` 会对所有人（包括让它干活的那个人）不可见。在项目里的仍然取项目当时的范围。规则写在 `service.CreateIssue`。
- `project` 档没有项目就不成立：接口先回 400（话说人话），数据库的配对约束兜底。issue 被移出全部项目时，`UpdateIssue` 的 SQL 把它降回 `private`——收紧是自动的，放宽永远不是。
- 每一次变更都写 `visibility_audit`：直接改写一行 `source='direct'`，被项目扫中的资源逐个写 `source='project_bulk'`（一条语句批量写入，避免扫一千个 issue 就来一千个往返）。

## 模块级可见性（DENE-699）

第三道「与」门：资源看得见，还要进得了这个产品区域。清单只有 Issues、Projects、Repos、Runtimes。Agents 与 Squads 不在清单里（归 Parent B）；工作区设置 / 成员 / 计费只跟档位走。

可执行版本是 `permission.CanSeeModule`。Owner / Admin 永远进得了每个模块，否则他们刚设的限制自己也改不回来。其余档位：

| 模块范围 | Member | Guest |
| --- | --- | --- |
| `workspace`（缺省、缺行） | 可进 | 可进（里面的条目仍按资源范围过滤；`workspace` 资源对访客仍然不可见） |
| `project` | 只在指定项目里的人 | 同上 |
| `private` | 不可进 | 不可进 |

缺行按 `workspace` 处理：模块不像 issue 那样被「新建」，升级时如果默认 `private` 会把整个产品锁死。

写接口：`GET /api/modules` 返回四个模块各自的范围和对当前调用者的 `allowed`；`PUT /api/modules/{module}/visibility` 只有 Owner / Admin 能改，每次变更写 `visibility_audit`（`resource_type='module'`）。路由组上的 `RequireModule` 拦直接打接口：看不见就 404，和资源层同一套「找不到」语义。Agent 调用走 `bypassAgent`，不被这道门拦住。

### 已知欠账

`workspaceToResponse` 同时用来构造 `workspace:updated` 广播，一份负载发给所有人，装不下「对这个调用者而言可见的仓库」。所以仓库过滤只加在 GET 路径（`ListWorkspaces` / `GetWorkspace` 调 `visibleWorkspaceRepos`），广播里的 `repos` 仍是全量。要彻底解决得让广播按订阅者分发，或者把仓库列表从工作区负载里拆出去——不在本票范围内。
