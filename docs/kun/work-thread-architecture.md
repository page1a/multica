# Work Thread 连续上下文架构深潜

## 结论

当前系统已经具备“单入口连续执行”的关键能力，但还没有统一的 Work Thread：

- Issue 入口现在由 `agent_task_queue.work_thread_id` 作为持久线程键（历史数据按 Issue + Agent 回填）；session 恢复仍从 `GetLastTaskSession` 读取最近可恢复的终态任务。
- Chat 入口现在把 `chat_session.id` 写入每个 `agent_task_queue.work_thread_id`，发送、claim、完成、取消仍在 chat session 锁下串行化；恢复优先读取 `chat_session.session_id`，缺失时回退到 `GetLastChatTaskSession`。
- 评论线程目前只影响任务去重和 `comment_thread_id`，没有成为跨聊天、Issue、消息线的统一输入队列。
- `agent_task_queue` 既是执行历史，又承担排队、租约、重试、取消、session 指针和实时状态，导致“任务行”和“连续线程”被混用。

因此，第一阶段不应重写现有队列或 provider adapter；应先增加一个线程级编排模块，把现有 task row、chat session 和评论输入收敛到同一个外部接口，再逐步把持久化和 UI 切换到该接口。

## 真实调用链

```text
聊天输入 / CLI
  └─ SendDirectChatMessage / EnqueueChatTask
       └─ agent_task_queue(chat_session_id)
            └─ daemon claim → StartTask → provider session
                 └─ chat_message + realtime task/chat events

Issue 指派 / @mention / 评论回复
  └─ IssueTrigger / TaskService.EnqueueTaskForIssue|Mention
       └─ agent_task_queue(issue_id, agent_id, comment_thread_id)
            └─ daemon claim → StartTask → provider session
                 └─ issue comment / activity log + realtime task events
```

两个入口最终都会进入 daemon claim 和 provider adapter，但在入队、输入封存、状态查询和 UI 投影处仍然分叉。`chat_input_task_id` 只解决 Chat 单轮输入边界；Issue 侧仍使用 `trigger_comment_id`、`coalesced_comment_ids` 和 issue snapshot 组合表达输入。

## 已成立的不变式

### 线程串行

- Chat 发送在 `LockChatSessionForEnqueue` 下检查 `HasPendingChatTurnForSession`，并将消息与 task ownership 放在同一事务。
- Chat claim、完成、取消和 runtime rebind 使用同一 session 锁顺序，避免两个 turn 同时推进 resume pointer。
- Issue 侧由 pending unique index、owner row fence 和 claim 查询保证同一 `(issue, agent)` 只有一个活动槽位；评论可合并到现有 pending task。

### 取消后继续

取消不会自动清掉 provider session：

1. daemon 在任务已经建立 provider session 后通过 `UpdateAgentTaskSession` 写入 `agent_task_queue.session_id/work_dir`。
2. `CancelTaskWithResult` 只结束当前 task，并在 Chat 场景推进 `chat_session.session_id`。
3. 后续 claim 的 `GetLastChatTaskSession` / `GetLastTaskSession` 明确接受 `cancelled` 终态。
4. 只有 session 被标记为 retired、provider 明确拒绝恢复、上下文溢出或其他 `resumeUnsafeFailure` 才切换到新 session。

### 有界恢复

恢复查询已经按 session 做 latest-terminal 判断，并排除已退休 session、污染历史和 resume overflow。Issue 侧还保存 `issue_snapshot` 与 `session_rollout_missing`，可以解释一次上下文断裂；Chat 侧有 `channel_context_revision`，可以限制频道上下文代际。

## “停止后继续”案例验证

现有测试覆盖了目标行为的核心后端链路：

- `server/internal/handler/daemon_claim_cancelled_session_test.go`
  - 取消的 Chat 首轮仍能把真实 provider session 作为下一轮的 `PriorSessionID`。
  - 取消后推进 `chat_session.session_id`，并覆盖“取消前尚未产生 session”时不误覆盖旧指针。
  - 覆盖取消与 follow-up claim 的并发窗口，证明 follow-up 不会在 pointer advance 前抢跑。
- `server/cmd/server/retired_session_test.go`
  - 取消/完成后保留健康 session。
  - 同一 session 的更新失败会阻止从更老的 completed row 复活污染 session。
  - provider auth、Antigravity token、resume overflow 等不可恢复情况会被排除。
- `server/pkg/taskfailure/resume_test.go`
  - daemon 分类、SQL resume guard 和 provider failure reason 保持一致。

这个案例说明底层能力已经存在；缺口是线程级状态没有成为聊天、Issue 和消息线的共同事实源，因此 UI 和入口仍可能把同一用户任务显示/调度成多条独立 run。

## 已落地的线程连续性基线

任务表已持久化 `work_thread_id`、`context_generation`、`context_message_limit`、`context_token_budget` 和 `continuity_break_reason`。Issue 入队会复用该 Issue + Agent 最近线程，Chat 入队复用 chat session 线程键，重试复制父任务线程；claim 同时按线程键和兼容的 Issue + Agent / Chat session 条件串行化。队列允许当前 Turn 运行时继续接收输入，后续输入等待同一线程的下一 Turn。

## 当前缺口

1. **还没有跨入口 Work Thread 主键**：Issue 与 Chat 任务现在各自持久化 `work_thread_id`，但尚未表达同一任务从聊天转入 Issue，或 Issue 评论回到聊天的连续关系。
2. **输入与执行耦合**：评论、@、CLI 文本最终都被压进 task row 的 trigger/comment 字段，没有独立的 queue/interrupt/preempt 语义，也不能可靠记录“输入已收到但尚未被某一 Turn 消费”。
3. **线程级配置缺失**：agent、model、runtime、permission、workdir、上下文预算散落在 agent/task/chat_session；无法用一个稳定指纹判断“沿用线程”还是“新建线程”。
4. **状态投影分裂**：Chat 使用 `pendingTask` 与 Chat realtime，Issue 使用 task timeline/activity；二者没有统一的 current turn、queued inputs、continuity break reason。
5. **恢复原因不完整**：已有 failure reason 和 rollout missing，但没有统一记录“为什么继续沿用 session / 为什么建立新上下文”的线程事件。
6. **并发锁的语义不同**：Chat 依赖 session row lock；Issue 依赖 task owner row fence + unique index。跨入口时无法共享同一串行锁。

## 建议的数据模型

先引入一个小而深的 `WorkThreadCoordinator` 接口，隐藏数据库迁移和现有 task service 的差异：

```text
openOrReuseThread(subject, executionFingerprint) -> WorkThread
appendInput(threadID, input, mode) -> WorkThreadInput
scheduleNextTurn(threadID) -> WorkThreadTurn
interruptTurn(threadID, reason) -> WorkThreadTurn
resumeTurn(threadID) -> WorkThreadTurn
getThreadSnapshot(threadID, budget) -> ThreadSnapshot
```

建议的持久化实体：

```text
work_thread
  id, workspace_id
  subject_kind(issue | chat_session | linked)
  subject_id / issue_id / chat_session_id
  agent_id, runtime_id, model, permission_fingerprint
  provider_session_id, work_dir, durable_work_dir
  current_turn_id, continuity_generation
  status, continuity_state, break_reason
  context_message_limit, context_token_limit
  created_at, updated_at

work_thread_turn
  id, thread_id, ordinal, task_id
  status, mode(normal | retry | resume | fresh)
  started_at, completed_at, cancel_reason, failure_reason
  session_before, session_after, context_generation

work_thread_input
  id, thread_id, source_kind(chat | issue_comment | mention | cli)
  source_id, payload, received_at
  scheduling_mode(queue | interrupt | preempt)
  status(pending | claimed | applied | superseded)
  claimed_turn_id, dedupe_key
```

这三个实体不替代 `agent_task_queue`：task row 继续作为执行、计费和 daemon 协议记录；Work Thread 只拥有连续性、输入顺序和线程级状态。这样可以把复杂实现藏在一个 deep module 后面，调用方不再直接拼接 `session_id`、`retry_of_task_id`、`chat_input_task_id` 和评论字段。

## 线程归属规则

默认复用现有线程的条件：

- subject 相同，或已有显式 link；
- agent、runtime、model、permission fingerprint 相同；
- 当前线程没有被明确 reset；
- provider session 未被 retired；
- 输入属于同一 context generation。

必须创建新线程的条件：

- agent、模型、runtime、权限或执行目录变化；
- 用户明确“重新开始”；
- provider session 不可恢复；
- 上下文压缩后超过线程预算，且受限回读仍无法重建；
- Issue/Chat subject 被明确拆分为独立任务。

## 调度与状态迁移

```text
pending input
  ├─ queue   → queued input → next turn
  ├─ interrupt → cancel current turn (session retained) → next turn
  └─ preempt → mark current turn interrupted → claim input immediately

turn
  queued → dispatched → running
                 ├─ completed
                 ├─ failed (retry same thread when resume-safe)
                 └─ cancelled (resume same thread when session exists)
```

同一 `work_thread` 只允许一个非终态 turn。数据库应使用 `work_thread.current_turn_id` 加部分唯一约束，而不是让每个入口各自推导“是否 busy”。

## 实施顺序

1. **建立只读协调器与快照**：从现有 task/chat 数据投影 `WorkThreadSnapshot`，不改入队行为；补齐跨入口诊断日志。
2. **写入线程与输入表**：Chat、Issue comment、@ 和 CLI 入力先写 `work_thread_input`，再由协调器创建/复用 task row。
3. **统一串行锁**：以 `work_thread` 行锁作为外部串行接缝，保留现有 session/owner lock 作为内部实现，逐步删除入口侧的重复 busy 判断。
4. **接管取消/重试/恢复**：把现有 `GetLast*TaskSession` 结果和 `retired_session_id` 写入 thread turn，统一记录 continuity break reason。
5. **统一实时投影**：新增 `work_thread:*` 事件，Chat 和 Issue UI 都读取同一个 snapshot；旧 task 事件在过渡期继续广播。
6. **受限回读**：线程配置消息数/token 上限；上下文重建只读取 issue snapshot、相关评论线程、最近输入和必要的 task transcript，并记录回读原因。
7. **删除分叉**：稳定运行后，再收缩 Chat/Issue 各自的 pending/status 查询，避免保留三套事实源。

## 第一批验收用例

- 同一 Issue 同一 agent 连续两条评论：只产生一个活动 thread turn，第二条进入 input queue。
- Chat 运行中发送 follow-up：显示 queued，当前 turn 完成后按序消费。
- Chat 运行中点击停止，再发送 follow-up：新 turn 的 `PriorSessionID` 等于取消 turn 的 provider session。
- Issue 运行中插队：当前 turn 只被标记 interrupted，provider session 仍可恢复。
- agent/model/runtime 变化：创建新 thread，旧 thread 状态和 transcript 保留。
- 同一 session 的 poisoned retry：不从更老 task row 复活；记录 `resume_unsafe` 原因。
- 压缩回读：只读取线程预算内的输入/评论快照，不做全量历史扫描。
- Chat、Issue、消息线同时订阅 thread snapshot：current turn、queued input 和 continuity state 一致。

## 本阶段交付边界

本阶段完成了线程主键与上下文预算的服务端落盘，并把 Issue、Chat 入队/重试/claim 接到各自稳定线程键；现有取消后恢复链路继续复用 provider session。聊天与消息线仍需把同一 `work_thread_id` 投影到统一 snapshot，并补齐 queue/interrupt/preempt 的专用输入实体。

## 统一快照读取切片

现在 Issue 和 Chat 都有只读的线程快照接口：

- `GET /api/issues/{id}/work-thread`
- `GET /api/chat/sessions/{sessionId}/work-thread`

两条入口返回同一套投影：线程连续性、当前活动 Turn 和 session 指针、最多 50 条排队输入，以及线程的上下文代次、消息上限、token 预算和 continuity break 原因。Issue 入口复用 issue 可见性边界；Chat 入口复用会话所有权、公开会话和 Agent 访问边界。响应不会返回完整任务 context 或 provider session 内容。

当前持久模型还没有可安全复用的摘要正文列，因此 `context.summary_available` 明确为 `false`；预算和受限回读边界已经可验证，摘要正文留给后续上下文压缩切片接入。
