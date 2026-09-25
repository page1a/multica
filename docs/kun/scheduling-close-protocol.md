# 调度对齐：统一 close protocol（DENE-230 / Stage 1）

本页是 DENE-229 长程任务的 close protocol 契约。Stage 2（DENE-231）按本页落地，不再自行决定字段名、写入时机、收尾状态或唤醒动作。本页只定义机制与最小闭环，**不重写调度器**，不改角色权限、模型绑定、外部通知策略。

核对基线：`origin/kun` @ `32fd66b6e`（2026-09-15）。行号指向该提交。本页描述的 Stage 4 补偿扫描（DENE-233）已在 DENE-520 摘除，恢复到官方 upstream 行为。之后又补回两条窄兜底：run 收工但票还在 `in_progress` 时补排一次 run（DENE-382），以及阻塞 / 待验收的等待巡检（DENE-850），见 §7。

## 0. 一页结论

- 平台今天**只会**在子票从非终态进入 `done` 或 `cancelled`、且该完成**关闭了 stage 屏障**时，才给父票写系统评论并唤醒父票 assignee。评论本身、`in_review`、`blocked`、跨票文字依赖，都不会唤醒父票。
- Agent 运行时 brief 把「交付本票自己的 ask」写成 `in_review`。`in_review` **不是** stage 终态。按 brief 正确收尾的 staged 子票会把父票卡在 `in_progress`。这是 DENE-196 / DENE-209 / DENE-189 停滞的机制原因，也是本协议要解开的点。
- 统一收尾五要素：`结论 + 状态 + 证据 + 下一责任人 + 唤醒动作`。缺任一要素视为未收口。
- child-done 唤醒失败没有补偿扫描（错误被 warn 后吞掉），这是官方行为，DENE-520 已确认保持。兜底只有两条，都有频率上限：run 收工而票停在 `in_progress` 时补排一次同一执行人的 run；阻塞或待验收的票安静 30 分钟后由等待巡检叫醒一次（§7）。其余停滞仍靠 child-done、`close.waiting_on` 的即时唤醒或人读评论/看板发现。

## 1. 现状盘点（真实案例 × 代码）

每条都落到案例评论 id 或文件:行。五个案例覆盖：本票家族、stage 屏障成功路径、`in_review` 卡屏障、跨票隐式等待、额度失败无补偿。

### 1.1 DENE-229 家族（本票）— staged 串行，Stage 1 未关屏障

| 项 | 事实 |
| --- | --- |
| 父票 | DENE-229 `01a0a4ad-43aa-7041-aa45-aa75b87dcf0b`，assignee 游戏调度-布尔玛 `9310ad38-468f-4d94-a68c-6f9eb4bf5d0a`，`in_progress` |
| 子票 | Stage 1 DENE-230 `in_progress`；Stage 2–5 DENE-231/232/233/234 均 `backlog` |
| 唤醒事件 | 无。DENE-230 未进入 `done`/`cancelled`，stage 屏障未关 |
| 状态流转 | 原架构席 Fable 在 2026-09-15T10:48:05Z 以额度耗尽失败（评论 `01a0a4ae-5c7a-7661-879f-6a285872f98b`）。失败任务只可能把本票从 `in_progress` 打回 `todo`（见 3.3），**不会**通知父票 |
| 父子 stage | `--stage N` 分组；server 只在最低未完成 stage 的每个子票都终态时唤醒父 assignee（`issue_child_done.go:134-157`） |
| 跨票依赖 | 无。后续 stage 靠父票被唤醒后 `backlog → todo` 晋升（`issues.md` Stages 段） |
| 代码 | `server/internal/handler/issue_child_done.go:70-167`（单票路径）；`isTerminalChildStatus` 只认 `done`/`cancelled`（`:417-419`） |

父票拆阶段评论 `01a0a4af-e83b-7d11-9898-2c2d787dcb11` 用 `mention://agent/9310ad38-…` 叫醒了 Dispatcher。那是**显式 mention**，不是 stage 屏障。Stage 1 完成前 Dispatcher 不会被屏障再叫醒。

### 1.2 DENE-213 → DENE-209 — stage 屏障成功路径（对照）

| 项 | 事实 |
| --- | --- |
| 子票 | DENE-213 Stage 2，`done` |
| 父票 | DENE-209，当时被系统评论唤醒 |
| 唤醒事件 | 系统评论 `01a0a48c-ebb8-734f-8096-d172f6091152`（2026-09-15T10:11:33Z）：`[@游戏实现-贝吉塔](mention://agent/0b406af1-…) Stage 2 of this issue is complete — its last sub-issue [DENE-213](mention://issue/…)` |
| 状态流转 | 子票 `→ done` 关闭 Stage 2；父票 assignee 被 `dispatchParentAssigneeTrigger` 入队 |
| 父子 stage | DENE-209 只有 Stage 2 一个 staged 子票；`stageProgressSummary` 报 `Stage 2: 1/1 done`，`nextStage == 0`，指令是「decide whether complete … `in_review` 或 create next stage」（`issue_child_done.go:580-587`） |
| 后续 | 贝吉塔回复 `01a0a48e-e36d-7dcf-a697-369e7d558061`：把 DENE-209 置 `in_review`，**明确等待 DENE-196 复验**，不再开 Stage 3 |

这是平台现有的唯一可靠父票唤醒：子票 `done` + 屏障关闭 + 父 assignee 为 agent/squad。

### 1.3 DENE-209 卡在 `in_review` — 跨票隐式等待

| 项 | 事实 |
| --- | --- |
| 本票 | DENE-209 `01a0a40e-8051-7cba-a74f-21eaf5700bf3`，`in_review`，unstaged 子票挂在 DENE-196 下 |
| 唤醒事件 | 对父票 DENE-196：**无**。`in_review` 不是终态，`notifyParentOfChildDone` 在 `prevTerminal \|\| !nowTerminal` 处直接 return（`:97-99`） |
| 状态流转 | 代码+发布已齐 → `in_review`（等人验收），不是 `done` |
| 父子 stage | 对 DENE-196：DENE-209 是 **unstaged** 唯一子票。unstaged 集合是一个隐式 stage，要全部终态才唤醒（`:487-505`）。`in_review` 把这个隐式 stage 一直开着 |
| 跨票依赖 | 「等 DENE-196 教室复验 Rules 返回 3/3」（评论 `01a0a48e-e36d-7dcf-a697-369e7d558061`）。这是**文字等待**，没有 `parent_issue_id` 反边，也没有对 DENE-196 assignee 的 `mention://agent/…`。DENE-196 当时没有被这句评论叫醒 |

DENE-213 的任务说明要求「去 DENE-196 发一条评论，mention 验收席」。那条显式 mention 才是跨票唤醒的唯一合法载体；漏写就静默停。

### 1.4 DENE-196 / DENE-189 — `in_review` 卡住父票 Stage 3

| 项 | 事实 |
| --- | --- |
| 父票 | DENE-189 `01a0a34e-b523-7444-96c8-1238d86f57a1`，`in_progress`。Stage 1 DENE-190/191 `done` 2/2；Stage 2 DENE-192 `done` 1/1；Stage 3 DENE-196 `in_review` 0/1 |
| 唤醒事件 | Stage 1、Stage 2 关闭时父票曾被唤醒（否则 Stage 2/3 不会被晋升）。Stage 3 **仍开着**，因为 DENE-196 不是 `done`/`cancelled` |
| 状态流转 | 验收未齐 → `in_review`。人类在 DENE-196 问「为什么没有 agent 继续接手」（评论 `01a0a493-2f3f-7005-a23c-0a7ccde7d7df`，2026-09-15T10:18:24Z）。该评论是 `mention://member` 级的人类发言，**没有** `mention://agent`，所以 `computeCommentAgentTriggers` 走 member 路径而不是显式 agent 入队（`comment.go:2671-2678`）。紧接着系统回复 session limit（`01a0a493-a482-73ad-b774-b924ed4ee2be`） |
| 补偿 | Dispatcher 12 分钟后手工 `mention://agent/d565bed0-…` 叫醒验收席（评论 `01a0a49e-91b3-7ded-bcb6-a7a2624db5e1`）。这是人/Dispatcher 补偿，不是 server 扫描 |
| 代码 | 屏障条件 `stageBarrierClosed`（`issue_child_done.go:498-522`）；父票 `in_progress` 不跳过（跳过的是 parent `done`/`cancelled`/`backlog`/member，`:116-132`） |

根因一句话：Stage 3 子票按 brief 进入 `in_review`，屏障不关，父票 DENE-189 一直 `in_progress` 无人自动醒。

### 1.5 DENE-230 Fable 额度失败 — 失败不唤醒任何人

| 项 | 事实 |
| --- | --- |
| 事件 | 架构席 run 在 10:48:05Z 失败，系统评论 `01a0a4ae-5c7a-7661-879f-6a285872f98b`：`You've reached your Fable limit` |
| 唤醒事件 | 无。失败路径只处理**本票** |
| 状态流转 | `HandleFailedTasks`：若本票仍是 `in_progress` 且没有剩余 active task / retry，把本票打回 `todo`（`task.go:5890-5912`）。`in_review` 和 `blocked` 故意不打回（`:5893-5897`） |
| 父子 stage | 不触发。失败不是终态进入 |
| 跨票依赖 | 无。本票被调度改派给 Builder 才继续，靠的是人改 assignee，不是自动补偿 |
| 代码 | `server/internal/service/task.go:5840-5912`；sweeper 旁路同样排除 `in_review`/`blocked`（`runtime_sweeper.go:708-720`） |

对照：stage 屏障 enqueue 失败也是 warn 后 return（`issue_child_done.go:383-388`、`:759-764`、`:816-822`），**没有重放**。文档原话：「A notification that fails is not replayed.」（builtin skill `issues.md:355-360`）

### 1.6 机制摘要（跨案例）

| 动作 | 会不会唤醒父票 assignee | 依据 |
| --- | --- | --- |
| 子票评论，无 `mention://agent\|squad` | 否 | `CreateComment` 不碰父票；agent 评论默认不走 assignee fallback（`comment.go:2681-2698`，仅同票 squad leader 例外） |
| 子票评论 `mention://issue/<parent>` | 否 | issue mention 不入队（`mentions.md` 表；`comment.go:2671-2673` 只认 agent/squad） |
| 子票评论 `mention://member/<id>` | 否 | member mention 解析后直接 `return nil, nil`（`comment.go:2677-2678`） |
| 子票 `→ in_review` 或 `→ blocked` | 否 | 非终态（`isTerminalChildStatus`，`:417-419`） |
| 子票 `→ done`/`cancelled` 但同 stage 还有未终态兄弟 | 否 | `stageBarrierClosed` 返回 false，静默（`:155-157`、`:488-489`） |
| 子票 `→ done`/`cancelled` 且屏障关闭，父票 backlog/done/cancelled/member | 否 | `:116-132` |
| 子票 `→ done`/`cancelled` 且屏障关闭，父票 agent/squad | **是**，一次 | `postChildDoneComment` + `dispatchParentAssigneeTrigger`（`:329-407`、`:714-725`） |
| GitHub PR `Closes DENE-N` 合并 | 是（若因此进入 `done` 且屏障关闭） | `github.go:1921-1938` `advanceIssueToDone` 调同一条 `notifyParentOfChildDone` |
| 父票系统评论里的 `mention://agent` | 不走通用 comment trigger | 系统评论绕过 listener；唤醒靠显式 `EnqueueTaskForMention` / `EnqueueTaskForSquadLeader`（`:54-65`、`:401-407`） |
| Autopilot | 与本协议无关 | 只响应 schedule/webhook/manual（autopilot 文档），不扫 issue 停滞 |

`attribution.KindStageWakeup`（`attribution.go:137`）只是词汇，**没有任何 enqueue 路径写入这个 kind**。实际 stage 唤醒走 mention/squad-leader enqueue。

## 2. 统一 close protocol

一次收尾必须同时具备五要素。只发评论不算完成。

### 2.1 五要素

| 要素 | 字段（Stage 2 写死） | 必须可观察 |
| --- | --- | --- |
| 结论 | `close.conclusion` | `delivered` / `blocked` / `awaiting_review` / `awaiting_human` |
| 状态 | `close.status` **且** `issue.status` 与之相等 | 见 2.3 决策表 |
| 证据 | `close.evidence_comment_id` | 本票一条评论（UUID）；可带 `--attachment` |
| 下一责任人 | `close.next_owner_type` + `close.next_owner_id` | `agent` / `squad` / `member` / `none` + UUID 或空串 |
| 唤醒动作 | `close.wake_action` | `stage_done` / `mention` / `none` |

可选：`close.waiting_on`（另一个 issue 的 identifier，如 `DENE-196`），仅当本票在等另一张票时写。这把 1.3 的隐式等待变成显式字段。

### 2.2 写入载体与时机

载体：issue metadata KV（已有 API，`issue_metadata.go:21-32`）。键规则 `^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`，值只能是 string / number / bool。所以全部用扁平 `close.*` 字符串，禁止 JSON 对象。

CLI（现有，不新增子命令）：

```bash
multica issue metadata set <issue-id> --key close.conclusion --value delivered
multica issue metadata set <issue-id> --key close.status --value done
multica issue metadata set <issue-id> --key close.evidence_comment_id --value <comment-uuid>
multica issue metadata set <issue-id> --key close.next_owner_type --value agent
multica issue metadata set <issue-id> --key close.next_owner_id --value <agent-uuid>
multica issue metadata set <issue-id> --key close.wake_action --value stage_done
multica issue metadata set <issue-id> --key close.waiting_on --value DENE-196   # 可选
multica issue metadata set <issue-id> --key close.at --value 2026-09-15T12:00:00Z
```

写入时机（顺序写死，Stage 2 测试按此断言）：

1. 先写 `issue.status`（`multica issue status …`）。失败则停止，不写 metadata，不发唤醒 mention。
2. 再发证据评论（`--content-file`）。若本回合有 triggering comment，用同一 `--parent`。评论必须含五要素的人读版（见 2.5）。拿到评论 id。
3. 若 `wake_action = mention`，证据评论**正文内**必须含合法 `[@Name](mention://agent|squad/<uuid>)`。`mention://member/…` 和 `mention://issue/…` 不算唤醒。
4. 最后逐键写 `close.*`。`close.status` 必须等于步骤 1 写入后的 `issue.status`。`close.evidence_comment_id` 必须等于步骤 2 的评论 id。
5. 同一回合退出前，`issue.status`、评论、metadata 三者一致。缺一视为未收口。

禁止：

- 只评论不改状态。
- 改状态不写证据评论。
- 对父票发「FYI」而不 mention 父票 assignee（那不会入队）。
- 把 `in_review` 当成 stage 完成。
- 在 `wake_action = stage_done` 时只写 `in_review`。

### 2.3 决策表（结论 → 状态 → 唤醒）

判定条件按从上到下第一条命中执行。`needs_acceptance` = 顶层父票 AC 要求 Reviewer / 人类验收 / 真机确认，且该验收还没发生。子票不拥有验收结论：只报告执行结果并按 `delivered` 收口，由父票验收人连同整棵子票树统一检查。`is_staged_child` = `parent_issue_id` 非空且（自己有 `stage` 或父票存在任何 staged 兄弟）。

| # | 结论 | 判定条件 | `issue.status` | `next_owner` | `wake_action` | 会唤醒谁 |
| --- | --- | --- | --- | --- | --- | --- |
| A | `delivered` | 本票 ask 已交付，**不** `needs_acceptance`，且 `is_staged_child` | `done` | 父票 assignee（agent/squad）或 `none`（父票 member/无 assignee） | `stage_done` | 仅当本完成关闭屏障时，由 **server** 唤醒父票 assignee。Agent **不要**再 mention 父票 assignee（防双发） |
| B | `delivered` | 本票 ask 已交付，**不** `needs_acceptance`，不是 staged child | `done` | `none`，除非 AC 点名要叫醒某人 | `none` 或 `mention`（仅当 AC 点名） | 无父票屏障。需要叫醒时必须 `mention` |
| C | `awaiting_review` | 顶层父票 `needs_acceptance` 且验收人是 agent（Reviewer 席） | `in_review` | 该 Reviewer agent | `mention` | 证据评论里 `mention://agent/<reviewer>`。**不** `done`；子票屏障已由终态事实关闭 |
| D | `awaiting_human` | 顶层父票 `needs_acceptance` 且验收人是人类 | `in_review` | 该 member | `none` | `mention://member/…` **不会入队**。人类靠 inbox/看板。可另 `mention` 一个 dispatcher agent 做看门，此时 `wake_action=mention` 且 next_owner 是那个 agent |
| E | `blocked` | 缺权限 / 外人决策 / 外部依赖 | `blocked` | 能解阻塞的人：人类决策用 member；能继续跑的 agent 用 agent | `mention`（next_owner 是 agent/squad 时）或 `none`（纯人类） | 不关屏障。父票继续等 |
| F | 本回合没有交付本票 ask（答问、旁证） | — | **不改状态** | — | — | 不写 `close.*` |

`cancelled` 不在本协议的 agent 收尾表里。取消是用户决策（`issues.md` cancelled 段）。Agent 不得把做不完写成 `cancelled`。

### 2.4 四种收尾场景（对照上表）

**`done`（场景 A/B）**

- 用：本票可观察交付已齐，不再等本票上的验收。典型：Operator 发布票（DENE-213）、无 Reviewer 门的调研票。
- 状态：`done`。
- 证据：结论 + 命令/测试/PR。
- 下一责任人：staged 时是父票 assignee，但唤醒交给 server。
- 唤醒：`stage_done`。PR 带 `Closes DENE-N` 且合并，等价于本路径（`github.go:1921-1938`）。

**`in_review`（场景 C，Agent Reviewer）**

- 用：PR / 设计 / 实现需要 Reviewer 终审。本票 DENE-230 走这条：先 `in_review` + mention Reviewer；Reviewer 通过并合并后由 `Closes` 把票打成 `done`，那时才关 Stage 1 屏障。
- 状态：`in_review`。
- 证据：PR URL、验证命令、未测项。
- 下一责任人：Reviewer agent UUID。
- 唤醒：证据评论含 `mention://agent/<reviewer>`。**禁止**同时 `done`。
- 通过之后不留在本场景。验收席发一条带单独 `verdict: pass` 行的评论（`multica issue comment add <id> --verdict pass`），平台合并关联 PR 并置 `done`；合不进去就改成结构化阻塞（DENE-850）。停在 `in_review` 只属于场景 D，而且必须写明人和决定。路由的「需要人拍板」不是场景 D。

**`in_review`（场景 D，人工验收）**

- 用：真机、余额、第三方账号、kk zi 本人感受。DENE-193 电话播报是原型。这不是默认。验收通过但没写明人和决定的，走放行，不走这里。
- 状态：`in_review`。
- 证据：已做项 +「待人工测试」清单，不得把未测写成已过。
- 下一责任人：人类 member。`wake_action=none`（member mention 不入队）。若需要 agent 盯着，另设 dispatcher 为 next_owner 并 `mention`。
- 唤醒：不关屏障。父票继续等，这是故意的——人工验收未过不能晋升下一 stage。

**`blocked`（场景 E）**

- 用：缺授权、产品决策、外部依赖。
- 状态：`blocked`。
- 证据：缺什么、谁能给、不猜替代。
- 下一责任人：能解阻塞的 agent 或人类。
- 唤醒：agent/squad 用 `mention`；人类 `none`。不关屏障。

### 2.5 证据评论人读模板

Agent 评论必须让人类不看 metadata 也能读懂。固定小标题，顺序不许换：

```markdown
## 结论
<delivered | blocked | awaiting_review | awaiting_human> — 一句话。

## 状态
已写入 `<status>`。

## 证据
- …

## 下一责任人
[@Name](mention://agent/<uuid>)   <!-- 或 member；member 不算唤醒 -->

## 唤醒动作
<stage_done | mention | none>。等待：<identifier 或 无>。
```

`wake_action=mention` 时，「下一责任人」行必须是可解析的 agent/squad mention，否则视为唤醒失败。

### 2.6 为什么「只发评论不改状态」叫不醒父票

三条独立的代码路径，缺哪条都不醒：

1. **评论不写父票。** `CreateComment` 只在当前 issue 插入。子票上的普通评论不会调用 `notifyParentOfChildDone`（该函数只挂在 `UpdateIssue` 状态变更后，`issue.go:3753-3759`，以及 batch / GitHub merge）。
2. **即便评论里写了父票 identifier 或 `mention://issue/<parent>`，也不入队。** 解析器只把 `agent`/`squad` 放进 trigger set（`comment.go:2664-2673`；`util/mention.go:11-16`）。
3. **即便子票状态变了，只要不是进入 `done`/`cancelled`，屏障函数直接 return。** `in_review` 在运行时 brief 里叫「交付」，在 stage 机器里叫「还没完成」。这是 DENE-196 把 DENE-189 Stage 3 卡住、DENE-209 把 DENE-196 隐式 stage 卡住的同一条规则。

系统 child-done 评论本身也**不能**指望通用 mention listener：它 `author_type=system`，listener 对 system 短接，唤醒是 `dispatchParentAssigneeTrigger` 显式入队（`issue_child_done.go:401-407`）。所以「在父票留一句人话」同样不够——必须让子票进入终态，或者在**父票**上发带 `mention://agent|squad` 的非 system 评论。

## 3. 状态机

七个内置 key 同时也是七个行为等价类（`issuestatus.go:3-16,40-48`）。自定义状态继承其 category 的平台行为，不继承「听起来像」的语义。

### 3.1 合法迁移 × 触发者

表中「任意」表示平台不禁止该写入；产品协议仍按 2.3 选目标。触发者：

- **CLI/UI**：`UpdateIssue` / `UpdateIssueStatus` / batch（agent 或人类）
- **server**：失败回滚、GitHub `Closes` 合并
- **enqueue**：该迁移会不会给当前 assignee 入队（`WillEnqueueRun`，`issue_trigger.go:97-140`）

| 从 \ 到 | backlog | todo | in_progress | in_review | blocked | done | cancelled |
| --- | --- | --- | --- | --- | --- | --- | --- |
| （create） | CLI 停车，不入队 | CLI，入队 | CLI，入队 | CLI，入队 | CLI，入队 | CLI，不入队 | CLI，不入队 |
| backlog | — | CLI，**入队**（status source） | CLI，入队 | CLI，入队 | CLI，入队 | CLI，不入队 | CLI，不入队 |
| todo | CLI | — | CLI（agent 惯例） | CLI | CLI | CLI | CLI |
| in_progress | CLI | **server** 在任务失败且无 active/retry 时（`task.go:5898-5912`）；CLI 也可以 | — | CLI（交付门） | CLI | CLI / GitHub merge | CLI |
| in_review | CLI | CLI | CLI | — | CLI | CLI / GitHub merge | CLI |
| blocked | CLI | CLI | CLI | CLI | — | CLI | CLI |
| done | CLI（重开） | CLI | CLI | CLI | CLI | — | CLI |
| cancelled | CLI（重开） | CLI | CLI | CLI | CLI | CLI | — |

`WillEnqueueRun` 只在两种 source 入队（`issue_trigger.go:123-140`）：

1. `assign`：create 或改 assignee，且当前有效状态不是 `backlog`。
2. `status`：从 backlog 类**离开**到非 backlog、非 done、非 cancelled。

因此：`in_progress → in_review`、`in_review → done`、`todo → in_progress` **都不会**因为状态迁移而入队。Agent 把票从 `todo` 自己改成 `in_progress` 只是看板信号（runtime brief），不是一次新 run。

`cancelled` / 任何状态变更都**不**取消已经在飞的 task（`issue.go:3729-3740`）。停 run 必须 `issue cancel-task`。

### 3.2 终态（对 stage 屏障）

`isTerminalChildStatus`（`issue_child_done.go:417-419`）只认 canonical `done` 和 `cancelled`。自定义 status 先经 `childStatusResolver` 映射到 category（`:430-449`）。

**Stage 2 不得把 `in_review` 或 `blocked` 加进终态。** 那会让未验收的 Stage 3 直接晋升 Stage 4。本协议用 `done` 表达「本票不再挡屏障」，用 `in_review`/`blocked` 表达「本票还在挡」。

### 3.3 server 自动写入（仅此几条）

| 写入 | 条件 | 代码 |
| --- | --- | --- |
| `in_progress → todo` | 任务失败，无剩余 active task，无 retry；且当前有效状态是 `in_progress` | `task.go:5890-5912` |
| `* → done` | 已链接 PR 带 close intent，且 PR 合并 | `github.go:1921-1938` |
| 不写状态，只评论+入队 | 子票进入终态且屏障关闭 | `issue_child_done.go:329-407` |

没有「超时把 `in_review` 打回 `todo`」。没有「停滞扫描」——server 不扫描停滞，也不自动改 `in_review`/`blocked`。停滞只由 §4 的既有事件驱动，或由人读评论发现。

## 4. 触发矩阵

事件 → 唤醒谁 → 载体 → 失败表现。「失败时补偿」一列写的是实际兜底：Stage 4 看门狗已在 DENE-520 摘除，下表各行的入队失败只留 warn 日志或 `trigger_outcomes` 记录。§7 的两条窄兜底只看票的状态（收工仍 `in_progress`、安静的 `blocked` / `in_review`），不重放这里失败的某一次入队。

| ID | 事件 | 唤醒谁 | 载体 | 失败表现 | 补偿 |
| --- | --- | --- | --- | --- | --- |
| T1 | 子票进入 `done`/`cancelled` 且屏障关闭，父 assignee = agent | 父票 agent | server 系统评论 + `EnqueueTaskForMention`（`issue_child_done.go:727-765`） | warn 日志，状态已提交 | 无自动补偿：失败只留 warn |
| T2 | 同 T1，父 assignee = squad | 父票 **leader only**（不扇出队员） | `EnqueueTaskForSquadLeader`（`:767-823`） | 同上 | 无自动补偿：失败只留 warn |
| T3 | 同 T1，父 assignee = member / 无 / backlog / 已终态 | 无人 | 整段 skip（`:116-132`） | 静默 | 无自动补偿；Dispatcher 人读后决定 |
| T4 | 子票进入终态但屏障未关 | 无人 | 静默（`:155-157`） | 无日志 | 预期行为；未关屏障不是失败 |
| T5 | 评论含 `[@x](mention://agent\|squad/<uuid>)` | 该 agent 或 squad leader | `computeCommentAgentTriggers` → enqueue（`comment.go:2637-2673`） | `trigger_outcomes`：`blocked` / `coalesced` / `deferred` | 读 `trigger_outcomes`；`blocked` 由 Dispatcher 人读后手工 mention，禁止对 `coalesced`/`deferred` 重发 |
| T6 | 评论含 member / issue / `@all` only | 无人（`@all` 还抑制 assignee 隐式路由） | 解析后 return nil | 静默 | 协议禁止把这三种当唤醒 |
| T7 | 成员在非 note 评论、无显式 agent mention | issue assignee（隐式） | member 路径（`comment.go:2681` 之后） | 普通 comment 入队失败 | 不是 close protocol 主路径 |
| T8 | agent 在 **squad 票**上发结果评论 | 该票 squad leader | `routeAssignedSquadLeaderFallback`（`:2692-2696`） | 同票协调闭环 | 不跨票 |
| T9 | `backlog →` 非 backlog/非终态，已有 agent/squad assignee | 该 assignee / leader | `WillEnqueueRun` status source（`issue_trigger.go:131-137`） | 未入队则 issue 停在新状态 | 无自动补偿；需人发现 |
| T10 | create/assign 到非 backlog 的 agent/squad | 该 assignee / leader | `WillEnqueueRun` assign source | 运行时不可用会 `noteRuntimeUnusable` | 无自动补偿；需人发现 |
| T11 | GitHub PR 合并且 close intent | 无直接 agent；issue → `done`，然后走 T1/T2 | `advanceIssueToDone` | warn，issue 可能仍非 done | 合并后核对 `issue.status`；不是 `done` 则 CLI 补写 `done`（仍走 T1） |
| T12 | 任务失败 | 不唤醒他人；可能本票 `→ todo` | `HandleFailedTasks` | retry / delegated recovery | 无自动补偿；额度类失败（案例 1.5）不得自动换模型（不变约束） |
| T13 | Autopilot schedule/webhook/manual | autopilot 的 agent/squad leader | autopilot run | `skipped` + `failure_reason` | autopilot runs 列表，不进 issue 扫描 |
| T14 | `close.wake_action=mention` 的证据评论 | `close.next_owner_id` | 同 T5 | 同 T5 | 无自动补偿：metadata 已写但无对应 queued/running 时需人发现 |
| T15 | `close.waiting_on` 指向的票进入 `done`/`cancelled` | 等待方 assignee（agent / squad leader） | server 系统评论 + `EnqueueTaskForMention` / `EnqueueTaskForSquadLeader`（`issue_waiting_on.go`，与 T1/T2 同一入队面） | warn 日志，状态已提交 | 无自动补偿。同一 `(issue, agent)` 已有 queued/running 则跳过 enqueue |

幂等（所有入队共用）：

- `HasPendingTaskForIssueAndAgent` 按 `(issue, agent, reviewed head)` 去重（`issue_child_done.go:749-757`、`issue_trigger.go:203-218`）。
- T15 跨票等待唤醒按 `HasActiveTaskForIssueAndAgent` 去重：同一 `(issue, agent)` 已有 queued / dispatched / running / waiting_local_directory ⇒ 仍写系统评论，跳过 enqueue（`issue_waiting_on.go` `dispatchWaitingOnAssigneeTrigger`）。
- 同线程 pending 的 mention 结果是 `coalesced`，禁止重发。
- 没有针对单次入队失败的补偿扫描（DENE-520）。§7 的窄兜底之外，任何补发的 mention 都是人/Dispatcher 的手工动作，且必须先读 pending/active；已有则只写评论不 enqueue。

## 5. 不变约束

Stage 2–5 改代码时必须保持。违反任一条约等于重写调度器。

1. **不改 enqueue 谓词。** `WillEnqueueRun`、`computeCommentAgentTriggers`、`dispatchParentAssigneeTrigger` 的准入条件保持原语义。本协议只规定 agent **何时调用现有 API**，不新增第三条「close 事件」入队通道。
2. **不改 stage 终态集合。** 仍是 `done`/`cancelled`。不把 `in_review`/`blocked` 当屏障关闭。
3. **不改角色权限与模型绑定。** 不改 agent/squad 的 runtime、model、visibility、invoke gate。额度失败（案例 1.5）换席是调度人决策，不是本协议自动行为。
4. **不改外部通知策略。** child-done 继续不读 `notification_preference`（`issue_child_done.go:683-687`）；system 评论继续跳过 subscriber listener。
5. **不重复派发。** 任何补发（人工或 Dispatcher 手工 mention）必须经过 `HasPendingTaskForIssueAndAgent` / `HasActiveTaskForIssue`。`coalesced`/`deferred` 的 mention 禁止再贴一条相同 mention。
6. **squad child-done 只叫醒 leader。** 不扇出队员（`:676-680`）。
7. **member 父票保持静默。** 不给人类父票发系统 child-done（`:127-132`）。
8. **backlog 父票保持静默。** 避免 #4320 / MUL-3497 的自动激活（`:119-126`）。
9. **Agent 不得对 daemon 任务分支 `reset --hard` / `rebase`。** 已有工作流约束，close protocol 不放开。
10. **`mention://member` 与 `mention://issue` 永不入队。** 协议里的「唤醒」只允许 `mention://agent`、`mention://squad`、或 server stage 屏障。
11. **不启用 Autopilot 扫票。** 停滞兜底只有 §7 的两条 server 内置路径，不引入新 autopilot 规则（避免和权限/通知纠缠）。
12. **不把 `KindStageWakeup` 接到新代码路径。** 它未被任何 enqueue 使用；补偿扫描已摘除，也不再有归因写入。

## 6. Stage 2 接口（DENE-231 直接照做）

Stage 2 只做三件事：把 2.3 决策表写进 Builder/Reviewer/Operator/Dispatcher 的收尾约束；用 metadata + 评论模板落地；加测试或验收脚本证明「只评论不改状态」不再被当成完成。

### 6.1 字段（写死）

全部挂在 issue metadata，string 值。键不存在 = 本票尚未按协议收口。

| 键 | 允许值 | 谁写 | 何时 |
| --- | --- | --- | --- |
| `close.conclusion` | `delivered` `blocked` `awaiting_review` `awaiting_human` | 收尾 agent | 状态写入成功之后 |
| `close.status` | 与当时 `issue.status` 相同的 key | 同上 | 同上 |
| `close.evidence_comment_id` | 评论 UUID | 同上 | 证据评论创建成功之后 |
| `close.next_owner_type` | `agent` `squad` `member` `none` | 同上 | 同上 |
| `close.next_owner_id` | UUID；type=`none` 时 `""` | 同上 | 同上 |
| `close.wake_action` | `stage_done` `mention` `none` | 同上 | 同上 |
| `close.waiting_on` | identifier（`DENE-196`）或 `""` | 同上 | 有跨票等待时必填，否则 `""` |
| `close.at` | RFC3339 UTC | 同上 | 最后一键 |
| `close.block_kind` | `decision` `permission` `external` `dependency` `capacity`；仅 blocked 收口必填 | 收尾 agent | 与 blocked 收口一并写入 |
| `close.block_action` | 非空，最多 80 个字符；仅 blocked 收口必填 | 收尾 agent | 与 blocked 收口一并写入 |

阻塞扩展校验：`conclusion=blocked` 的新记录必须同时提供上述两个字段；旧记录缺少两字段时保持可读兼容。`block_kind=dependency` 必须有非空 `close.waiting_on`，且 `decision` / `permission` 必须指定具体的 `member`、`agent` 或 `squad` 责任人。非 blocked 收口的两个字段必须为空或不存在，避免解除阻塞后残留旧原因。人类审核逾期阈值按产品决策为 24 小时；`capacity` 阻塞不计入“需要你”摘要。

切到 `blocked` 本身还要在 `multica issue status` 上带上挡路说明（DENE-850）：`--blocked-by`、`--wake-at`、`--wait-condition` 加 `--wait-timeout`、或 `--needs-human`，至少一种。Agent 不带这些字段会被拒绝；上次用过、已经到点或已经叫醒过的记录不算。票离开 `blocked` 时整套 `block.*` 等待会被清掉。`close.waiting_on` 仍然会在被等票进入终态时叫醒等待方；`block.blocked_by` 是同一条边上的多票写法。验收通过只认单独一行的 `verdict: pass`（或 `multica issue comment add --verdict pass`），由平台合并并关票，合不进去就写成结构化阻塞，不留在 `in_review`。句子里的「通过」只提示怎么写这一行，不会合并。同一段等待最多叫醒一次；验收人是人、或票在等 `needs_human` 时只留言，不排运行。巡检只看本功能开始盯上之后才进入阻塞或待验收的票。这层不替代取消重试（DENE-813）、额度换席（DENE-836）或停用席位叫醒（DENE-848）。跑满工作区时限（`task_time_limit`）按重试预算在原会话和工作目录里续跑，续跑先收口已有进度再把剩余工作拆小；预算用尽改为 `blocked` 并留言，不再停在 `todo`。执行席是 agent 的父票切到 `in_review` 时如果验收席为空，补一个异族验收席并开始验收，选不出来就不进 `in_review`。直接写成 `done` 时，关联 PR 还开着：能干净合并且检查是绿的就先合并再关，否则改成带等待条件和到点叫醒的结构化阻塞。

校验（Stage 2 测试写死）：

- `close.status` ∈ 七个 canonical key，且 `== issue.status`。
- `wake_action=stage_done` ⇒ `close.status` ∈ {`done`,`cancelled`} 且 `close.conclusion=delivered`。
- `wake_action=mention` ⇒ `next_owner_type` ∈ {`agent`,`squad`} 且 `next_owner_id` 非空，且证据评论 body 匹配 `mention://(agent|squad)/<next_owner_id>`。
- `conclusion=awaiting_review` ⇒ `close.status=in_review` 且 `wake_action=mention`。
- `conclusion=awaiting_human` ⇒ `close.status=in_review` 且 `next_owner_type=member`（若同时 mention 了 dispatcher，允许 `next_owner_type=agent` 且 `wake_action=mention`，但 `waiting_on` 或证据里必须写出人类验收人）。
- `conclusion=blocked` ⇒ `close.status=blocked`。
- `conclusion=blocked` ⇒ 新记录的 `close.block_kind` / `close.block_action` 合法且动作不超过 80 个字符；`dependency` 必须配 `waiting_on`。
- `waiting_on` 非空 ⇒ `close.status` ∈ {`in_review`,`blocked`,`in_progress`}，禁止 `done`。

### 6.2 角色收尾动作

| 角色 | 默认结论 | 默认状态 | 唤醒 |
| --- | --- | --- | --- |
| Builder（父票有 PR/需审） | `awaiting_review` | `in_review` | mention Reviewer。PR 标题带 identifier；**不要**在仍等 Reviewer 时 `done`。`Closes` 留给合并 |
| Builder（子票交付） | `delivered` | `done` | `stage_done`；不设置或触发独立 Reviewer，由父票统一验收 |
| Builder（无验收门） | `delivered` | `done` | `stage_done`，禁止再 mention 父 assignee |
| Reviewer 通过，这次改动自己的检查是绿的，且没有显式人工保留 | `delivered` | 发 `--verdict pass` 评论，平台合并 PR 并置 `done`；合不进去平台改成 `blocked` 并叫醒执行人 | `stage_done` |
| Reviewer 通过，但票上写明在等某个人做某个只有这个人能做的决定 | `awaiting_human` | `in_review` | `none`。评论写出那个人和要定的事。路由评论里的「需要人拍板」不是这一行 |
| Reviewer 通过，但这次改动自己的检查是红的 | 不收口 | `in_progress`，mention Builder | `mention` |
| Reviewer 通过且已经合并 | `delivered` | 若 webhook 未把票打成 `done`，CLI 补 `done` | `stage_done` |
| Reviewer `needs-work` | 不收口 | `in_progress` 或保持，并 mention Builder | `mention` |
| Operator 发布/运维票交付 | `delivered` | `done` | `stage_done` 或按 AC mention 下一席 |
| Dispatcher 晋升下一 stage | 不写子票 `close.*` | 子票 `backlog → todo`（T9） | server 入队；Dispatcher 本票保持 `in_progress` 直到整条链完成 |
| 任一角色人工验收未完成 | `awaiting_human` | `in_review` | 见 2.3 D |

验收通过的默认是放行，不是停在 `in_review` 等人去点合并。DENE-792 停在这里：验收已经通过，这次改动自己的检查是绿的，主干上本来就红、且和基线一致的检查被写成了「暂不合并、暂不关票」。那不是停的理由。`kun` 没有分支保护，当时的 PR 是可以合并的。

放行条件，同一轮做完：验收结论是通过；这次改动自己负责的检查是绿的（基线上同样失败的检查不算这次的失败）；票上没有写明还在等哪个人做哪个决定。然后合并 PR，再把票写成 `done`（合并 webhook 已经写成 `done` 就不要再写一遍）。

仍然停在 `in_review` 的唯一通过路径是显式人工保留：`close.conclusion=awaiting_human`，评论里写出那个人和要定的事。路由评论里的「需要人拍板」不是这张保留——那句话的意思是席位照样检查、合并、关票，只有人能定的那一件再 @ 人。

这次改动自己的检查是红的：不收口，退回 `in_progress` 并 mention Builder。GitHub 真的拒绝合并时，把拒绝原因写进评论，用 `blocked` + `block_kind=permission`，不要在 PR 仍可合并时假装要等人。

Dispatcher **禁止**在 Stage N 子票仍是 `in_review`/`blocked`/`in_progress` 时把 Stage N+1 从 `backlog` 提到 `todo`。晋升条件写死：`issue children` 里该 stage 的 `done` 计数 = `total`（cancelled 计入 done 侧，与 `status_category` 终态一致）。

### 6.3 Stage 2 代码落点（最小，不改调度器）

1. **builtin skill** `server/internal/service/builtin_skills/multica-platform/references/issues.md`：加「Close protocol」一节，指向本页决策表。这是 agent 运行时会读到的约束。
2. **本 fork 文档**保持本页为权威；skill 节是摘录，不得另写一套状态含义。
3. **测试**（择一，Stage 2 必须有自动断言）：
   - Go：给定「子票 `in_review` + 同 stage 无其他兄弟」，`stageBarrierClosed` 仍为 false（已有终态测试，补一条 `in_review` 不关屏障的回归，防止以后有人「修好」它）。
   - 或脚本：对 fixture metadata 跑 6.1 校验。
4. **不要改** `issue_child_done.go` 的屏障语义、mention 解析、invoke gate。

### 6.4 明确不在 Stage 2 做的

- 前端展示下一唤醒者 / 阻塞原因（Stage 5）。
- 30 分钟停滞扫描（Stage 4 曾实现，DENE-520 已整层摘除）。
- 把跨票 `waiting_on` 升级成真实父子边（Stage 3）。
- Autopilot 规则。
- 新的 `KindStageWakeup` 入队路径。

## 7. 补偿：看门狗已摘除，只留两条窄兜底

官方 upstream 没有唤醒失败的补偿扫描，本 fork 也不再有针对单次唤醒失败的扫描：DENE-233 引入的 Stage 4 看门狗（四扫描 A–D、`stagnation_watchdog*.go`、5 分钟 sweeper 旁路）已在 DENE-520 按 kk zi 的判定整层摘除。

- child-done 的五类失败（加载父票、列兄弟、写系统评论、enqueue agent、enqueue squad leader）只写 **best-effort warn 日志**，不重试、不落表、不补扫。状态已经提交，失败不回滚。
- 评论 mention 的失败同样只体现在 `trigger_outcomes`（`blocked` / `coalesced` / `deferred`），没有后台补发。
- 停滞因此只能由平台既有事件解开，或由人读评论/看板发现：§4 的 T1/T2（stage 屏障关闭）、T15（`close.waiting_on` 被等票进入终态）、T5/T9/T10（mention / 状态 / 指派入队）。
- 保留 `stage_wakeup_failure` 表与迁移 480–482：自建实例已应用，删迁移会破坏迁移历史；表不再有新写入，也不再被读取。

之后补回的两条兜底只看票的状态，不重放某一次失败的入队：

- **收工仍在 `in_progress`（DENE-382）。** run 正常结束、票还在 `in_progress`、后面没有排队的 run，也没有未终态子票时，平台写一条带 `completion-stall:run-completed-without-terminal-status` 的系统评论，并给同一执行人补排一次 run，让它按收口协议收尾。同一张票 30 分钟内最多一次。不改状态。代码：`server/internal/service/task_completion_stall.go`。
- **阻塞 / 待验收的等待巡检（DENE-850）。** 每分钟扫一次。`blocked` 或 `in_review` 的票安静满 30 分钟、没有 run 在跑时，按 `block.*` 等待记录叫醒执行人或验收席；同一段等待最多叫醒一次，等的是人时只留言不排 run。代码：`server/internal/blockwait/`、`server/internal/handler/issue_block_wait.go`。


## 8. 哪些结论会唤醒谁（给 Dispatcher 的速查）

| 结论 | 状态 | 立刻被唤醒的人 | 父票何时醒 |
| --- | --- | --- | --- |
| `delivered` + staged | `done` | 无（agent 不 mention 父票） | 屏障关闭时 server 唤醒父 assignee |
| `delivered` + 非 staged | `done` | 仅当 AC 要求 mention | 永不自动 |
| `awaiting_review` | `in_review` | Reviewer agent | 等本票最终 `done` |
| `awaiting_human` | `in_review` | 无人（或可选 dispatcher） | 等人类收口后的 `done` |
| `blocked` | `blocked` | 能解阻塞的 agent（若有） | 等解阻并最终 `done` |
| 只评论 | 不变 | 仅当评论里有 agent/squad mention | 永不 |

DENE-230 本票走 `awaiting_review`：Reviewer 醒，布尔玛（父票）要等 PR 合并把本票打成 `done` 之后才被 Stage 1 屏障叫醒，然后才能把 DENE-231 从 `backlog` 提到 `todo`。

## 9. Stage 3 已落地；Stage 4 已摘除（DENE-520），Stage 5 已落地

### 9.1 改造规则（DENE-232）

同一家族、共享上游、无资源冲突 → **真实父子 stage**，不要写 `close.waiting_on`。跨家族、无法挂到同一 parent 下 → 写 `close.waiting_on`，被等票进入 `done`/`cancelled` 时由 server 叫醒等待方。

| 形状 | 用什么 | 为什么 |
| --- | --- | --- |
| DENE-189 Stage 1：DENE-190 ∥ DENE-191 | 同一 parent、同一 `stage=1` | 共享上游（TEST 现场），无资源冲突。Stage 1 两个都 `done` 才关屏障，父票醒一次 |
| DENE-196 卡 DENE-189 Stage 3 | 保持父子 stage；禁止把 `in_review` 当完成 | 屏障只认 `done`/`cancelled`。验收未过就该挡下一 stage |
| DENE-209 等 DENE-196（评论 `01a0a48e-e36d-7dcf-a697-369e7d558061`） | 跨家族：DENE-209 写 `close.waiting_on=DENE-196` | DENE-209 已是 DENE-196 的 unstaged 子票，再反等父票没有 `parent_issue_id` 反边。文字等待不可观察 |

改造前 / 改造后：

```text
改造前
  DENE-209 in_review
    评论：「等 DENE-196 教室复验」
    无 parent 反边、无 mention://agent、无 close.waiting_on
    DENE-196 → done 时：无人叫醒 DENE-209
    发现停滞：靠人看评论，12 分钟级（案例 1.4 Dispatcher 手工 mention）

改造后
  同家族并行（190∥191 形状）
    两个子票 stage=1，共享 parent
    两个都 done → 屏障关 → T1/T2 叫醒父 assignee（已有路径）
  跨家族等待
    等待方 close.waiting_on = DENE-196（或 UUID）
    DENE-196 → done/cancelled
      → ListIssuesWaitingOn（GIN @>）
      → 等待方系统评论 + EnqueueTaskForMention
      → 同一 (issue, agent) 已有 queued/running ⇒ 只写评论，不 enqueue
    停滞发现：status 写入当回合（秒级），不再等有人读评论
```

代码落点：

- 查询：`server/pkg/db/queries/issue.sql` `ListIssuesWaitingOn`
- 唤醒：`server/internal/handler/issue_waiting_on.go`
- 挂钩：`UpdateIssue`、`BatchUpdateIssues`、`advanceIssueToDone`（与 child-done 同一批终态入口）
- 幂等：`HasActiveTaskForIssueAndAgent`（queued / dispatched / running / waiting_local_directory）
- 同家族跳过：等待方若是被等票的 parent，只走 stage 屏障，避免双评论

Agent 写法：

- 能挂到同一 parent 的，用 `--parent` + `--stage N`。共享上游且无资源冲突的放进同一 stage。
- 不能挂的，收口时 `close.waiting_on=<identifier>`，状态保持 `in_review` / `blocked` / `in_progress`（§6.1 禁止 `done`）。
- 被等票 `done` 时 **不要** 再 mention 等待方 assignee（server 会叫醒，mention 会双发）。

### 9.2 Stage 4（已摘除，DENE-520）与 Stage 5（已落地，DENE-234）

- **Stage 4**：DENE-233 落地的停滞看门狗（四扫描、系统评论、`stage_wakeup_failure` 留痕、5 分钟 sweeper 旁路）已在 DENE-520 整层摘除，恢复官方行为：没有补偿扫描，child-done 唤醒失败只 warn（见 §7）。`closeprotocol.StatusMatchesIssue` 的 §6.1 断言助手保留不变。Dispatcher 推进回合收缩为：读 `issue children` + 读 `close.*` + 晋升或短结论，禁止重型全景看板。
- **Stage 5**：在 `groupSubIssuesByStage` 旁，每个子票渲染 `SubIssueCloseStrip`（`packages/views/issues/components/sub-issue-close-strip.tsx`）：当前 stage、`close.conclusion`、下一唤醒者（`close.next_owner_type` + `close.next_owner_id`）、等待来源（`close.waiting_on`）、最近 `last_activity_at`。两种异常态显式标出，不渲染成空字段：缺 `close.*`（未按协议收口）、`close.status != issue.status`（漂移）。`blocked` / `in_review` 用 next owner + waiting_on + last_activity 表达卡在谁、卡了多久。数据源是 issue `metadata`；`issue_metadata:changed` 经 `onIssueMetadataChanged` → `patchIssueSnapshot` 写入 children cache，子票条即时刷新。独立实页验收归 Stage 6（DENE-260）。

### 9.3 效率对比（DENE-229 家族，2026-09-15）

数字来自本家族真实记录，Stage 6 复核。改造前对照 §1.3 / §1.4。

| 项 | 改造前 | 改造后（本家族） |
| --- | --- | --- |
| 阶段数 | 隐式等待 + 串行文字依赖，阶段边界不可观察 | 6 个显式 stage（1–5 实现，6 独立验收）。Stage 1–4 已关屏障 |
| Dispatcher 回合 | 7–17 分钟级全景看板；卡点要读评论才发现 | 屏障关闭后一次短回合：读 `issue children` + `close.*` + 晋升或短结论。本家族 4 次晋升（1→2、2→3、3→4、4→5） |
| 停滞发现 | 靠人读评论。DENE-196 卡 DENE-189 Stage 3：人类问「为什么没人接手」后 Dispatcher 12 分钟才手工 mention | 即时路径秒级（T1/T2 stage 屏障、T15 `waiting_on`）。DENE-520 后不再有补偿扫描，无即时事件覆盖的停滞仍靠人读评论发现 |
| 总耗时 | 文字等待可无限挂（DENE-209 等 DENE-196 无人叫醒） | 本家族 Stage 1 创建 10:47:57Z → Stage 4 `close.at` 13:04:42Z，约 2.3h 走完 4 个实现 stage（含 Fable 额度失败改派） |

Stage 1 DENE-230 `metadata: {}`，是真实的「未按协议收口」样本。Stage 2–4 八键齐全且 `close.status=done` 与 issue 一致。历史漂移（DENE-232 / DENE-233 曾 `close.status=in_review` 而 issue 已 `done`）由 Stage 5 的 drift 态覆盖。

### 9.4 上线与回滚（Stage 5）

改动面（仅前端，不改调度器）：

- `packages/core/issues/close-protocol.ts` — 读 `close.*` 八键、missing / drift 判定
- `packages/views/issues/components/sub-issue-close-strip.tsx` — 子票条
- `packages/views/issues/components/issue-detail.tsx` — `SubIssueRow` 挂载
- `packages/views/locales/{en,zh-Hans,ja,ko}/issues.json` — 文案
- 本页 §9.2–§9.4

回退：squash 合入 `kun` 后，revert 该 PR（或 `git revert` 该 merge）。无 migration、无 dual-write、无 server 行为变化。`close.*` metadata 保留。

回滚后行为：子票列表回到「只有 stage 分组、没有下一唤醒者 / 异常态」。调度、屏障、waiting_on 唤醒不受影响。

不回滚的：Stage 1–3 的协议与 waiting_on 路径（Stage 4 的补偿扫描已由 DENE-520 单独摘除）。Stage 5 只是只读展示。
