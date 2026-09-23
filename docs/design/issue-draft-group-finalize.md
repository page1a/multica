# 对齐终局拆成一组单：载荷形状、身份幂等与事务契约（DENE-368 / 设计）

本页是 DENE-368 的设计稿，属于 DENE-366 的阶段 1。它**不改**生产代码，只给出载荷形状、身份模型、事务边界、派单语义、迁移清单和待拍板项。阶段 2 按本页实现。

核对基线：`origin/kun` @ `eba2fbef94`（2026-09-17）。下文行号指向该提交。

## 0. 一页结论

1. **不改身份约束，改身份的含义。** 迁移 486 的 `CREATE UNIQUE INDEX ... ON issue (origin_id) WHERE origin_type = 'issue_draft'` **一行都不动**。今天它的含义是「一场对齐至多一张 issue」，改完之后它的含义是「一个对齐**节点**至多一张 issue」。旧行天然满足新含义，因为旧的一张单就是只有根节点的退化情形。
2. **根节点的 origin_id 就是 `chat_session_id`**，和今天完全一样；子节点的 origin_id 由服务端从 `(chat_session_id, node key)` 用 UUIDv5 **推导**，不接受客户端直接给 UUID。推导是幂等的来源，`chat_session_id` 参与推导是防篡改的来源。
3. **一组单一个事务。** 新增 `IssueService.CreateGroup`，把今天 `Create`（`server/internal/service/issue.go:213`）的事务内部分抽成 `createInTx`，事务外部分抽成 `afterCommit`，两个入口共用同一套规则，不复制校验。
4. **finalize 仍然是三段短事务**，和今天的结构一模一样：锁→决策→提交 / 建组（不持锁）/ 锁→回写→提交。`LockIssueDraftInWorkspace` 依然不跨越建组过程持有。
5. **阶段 2 需要 0 个迁移。** `issue.stage`（123）、`issue.parent_issue_id`、`origin_type` 的 CHECK（484/485）、唯一索引（486）、`idx_issue_origin`（042）、`idx_issue_parent`（001）全部已就位。需要迁移的只有阶段 3 的「重开一轮」，清单见 §7.2。
6. **派单默认按 stage 只点着第 1 阶段**：stage 1 的子单 `status=todo`，stage ≥ 2 的子单 `status=backlog`，父单在有子单时是协调者——`status=in_progress`、保留负责人、创建时不派实现 run。**谁负责把 stage 2 从 backlog 提上来已定：父单负责人被阶段屏障叫醒后自己提**，见 §6.3。
7. **重开的对象是同一场对齐**（同一个 `chat_session_id`、同一个 draft 行），不是新开一场。身份模型天然支持：轮次不参与节点 id 的推导，第二轮的新节点只是拿到新的 key。

## 1. 现状核对

### 1.1 一对一是写在数据库里的

```sql
-- server/migrations/486_issue_draft_origin_unique.up.sql
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_origin_issue_draft_unique
    ON issue (origin_id)
    WHERE origin_type = 'issue_draft';
```

`server/migrations/483_issue_draft.up.sql` 的表注释把这条索引的地位写得很明确：

> The index, not the lock, is what makes a duplicate impossible: two confirms that both pass the decision step cannot both create, and the loser adopts the winner's issue.

所以前端循环调 finalize 必然失败——第二次调用在 `admitIssueDraftForFinalize`（`server/internal/handler/issue_draft.go:692`）就会因为 draft 已经 `completed` 而直接返回第一张 issue，根本到不了创建。这不是 bug，是协议。

### 1.2 finalize 的三段结构

`FinalizeIssueDraft`（`issue_draft.go:617`）：

| 步骤 | 函数 | 事务 | 干什么 |
| --- | --- | --- | --- |
| 1 | `admitIssueDraftForFinalize:692` | 自己一个 | `FOR UPDATE` 锁住 draft 行，在**锁内读到的值**上决策：`completed` 返回既有 issue；非 `ready` 或 revision 对不上则拒绝 |
| 2 | `createIssueForDraft:902` | `IssueService.Create` 自己开一个 | 建 issue，或认领另一个 confirm 已经建好的（23505 → `GetIssueByOrigin`） |
| 3 | `completeIssueDraft:752` | 自己一个 | 再锁一次，把 `issue_id` 写回 draft |

锁**故意不跨越**第 2 步（`issue_draft.go:633-638`）：`IssueService.Create` 自己开事务，持锁会让每次 confirm 同时占两个连接。这条约束在拆组之后依然成立，而且更重要——建组的事务更长。

### 1.3 载荷是单张 issue 的字段集

`issueDraftPayload`（`issue_draft.go:85-93`）与 `IssueDraftPayload`（`packages/core/types/issue-draft.ts:24-33`）一一对应：`title` / `description` / `status` / `priority` / `assignee_type` / `assignee_id` / `project_id` / `parent_issue_id`。没有 stage，没有子单。

### 1.4 一处容易踩的既有实现

`GetIssueByOrigin`（`server/pkg/db/queries/issue.sql:595`）是 `LIMIT 1`。如果让一组单**共用同一个 `origin_id`**，这条语句就会返回任意一行——`createIssueForDraft:902` 的认领逻辑、`completeIssueDraft` 写回的 `issue_id` 都会变成不确定值。这是「同 origin_id + 复合唯一键」方案的隐性代价，也是 §3.2 拒绝它的理由之一。

## 2. 载荷形状

### 2.1 类型

根节点保持扁平，子节点进一个可选数组。**旧行不需要任何兼容分支**：没有 `children` 就是只有一个根节点的组，也就是今天的行为。

```ts
// packages/core/types/issue-draft.ts

/**
 * 一组单里的一张子单。
 *
 * `key` 是这张子单在**这个 draft 内**的稳定标识，preview 面板第一次把这一行放
 * 进 draft 时铸造一次，之后每次保存都原样带上，永远不重新生成。它不是 UUID，
 * 服务端不直接使用它做主键——服务端把它和 chat_session_id 一起推导出真正的
 * origin_id（见 §3.1）。客户端重新生成 key 会被 §3.3 的根节点冲突挡住。
 */
export interface IssueDraftChild {
  key: string;
  title: string;
  description: string;
  status: string;
  priority: string;
  assignee_type?: string | null;
  assignee_id?: string | null;
  /** 阶段序号，1 起。null / 省略 = 不分阶段（同一个隐式阶段）。 */
  stage?: number | null;
}

export interface IssueDraftPayload {
  // —— 以下 8 个字段一字未改，它们现在描述的是这组单的根（父单） ——
  title: string;
  description: string;
  status: string;
  priority: string;
  assignee_type?: string | null;
  assignee_id?: string | null;
  project_id?: string | null;
  parent_issue_id?: string | null;
  /**
   * 这场对齐拆出来的子单。省略或空数组 = 一组只有一张单，也就是本次改动之前
   * 每一行 draft 的形状。所以「旧的单张形状是否还需要被接受」的答案是：需要，
   * 而且是**构造上**接受的，不是靠一个 if 接受的。
   */
  children?: IssueDraftChild[];
}
```

### 2.2 `<issue_draft>` 块

`issueDraftContract`（`server/internal/handler/issue_draft_policy.go:33-51`）的 JSON shape 扩成：

```
<issue_draft>{"title":"","description":"","status":"","priority":"","children":[{"key":"c1","title":"","description":"","stage":1,"assignee_hint":""}]}</issue_draft>
```

三条硬规则要写进 prompt：

- `key` 一旦发出就不许改。模型每一轮都要把上一轮给过的 key 原样带回来。这是「保留已有字段」规则的延伸，现有 prompt 已经有这条精神（`Preserve good existing draft fields supplied in the user's message`）。
- 模型**不许**写 `assignee_id`。它给 `assignee_hint`（自然语言，比如「后端实现」「前端页面」），由真实名册解析成 agent id 之后再存进 draft（DENE-691 起这一步在服务端路由里做，见 `server/internal/routing/suggest.go`）。理由：模型没有工作区的 agent 名册，让它猜 UUID 是在给 `validateAssigneePair` 送垃圾。DENE-694 起，这个 id 即使最终没通过校验，代价也只是那张单未分配——`draftAssigneeFromNode` 把失败降级成 `assignment_warnings`，不再让整次 confirm 失败。
- 拆单数量上限写进 prompt（推荐 8，硬上限 20，见 §2.4）。

`packages/core/issue-drafts/protocol.ts` 的 `parseIssueDraftBlock` 增加 `children` 解析，规则和现有字段一致：**解析失败 = 这一轮没有 children 更新**，不是清空。`mergeIssueDraftPayload` 对 `children` 用整体替换而不是逐项合并——子单集合是一个整体判断（模型可能删掉一张单），逐项合并会让删除永远不生效；被替换掉的 key 如果在新数组里还在，key 本身就保住了身份。

### 2.3 policy version 必须一起 bump

`issueDraftContract` 是 `issueDraftQuestionPolicy` 和 `issueDraftConversationPolicy` 共用的前半段（`issue_draft_policy.go:87-89`）。文件自己的规矩是「改一个 entry 的 prompt 就要 bump 它的 Version」。契约块改了 = 两个 entry 都改了，所以 `question` 和 `conversation` 的 `Version` **同时** `"1"` → `"2"`。

### 2.4 服务端的载荷边界

| 约束 | 值 | 拒绝时 |
| --- | --- | --- |
| `children` 长度 | ≤ 20 | 400 `draft has too many sub-issues` |
| `key` 非空、去空格后 ≤ 64 字节 | — | 400 `sub-issue key is required` |
| `key` 在一份 payload 内唯一 | — | 400 `duplicate sub-issue key` |
| `stage` | `null` 或 1..20 | 400 `invalid sub-issue stage` |
| 嵌套深度 | 1（子单不能再有子单） | 契约里根本没有这个字段 |
| 整体字节数 | 已有的 `maxIssueDraftBytes` = 256KB | 已有 |

`key` 唯一性必须在解析期查，不能留到数据库：两个相同 key 推导出同一个 origin_id，在**同一个事务内**插第二行会撞自己的唯一索引，拿到的是一个 500 而不是一句能读的话。

不允许孙子单的理由不是省事：stage 屏障（`server/internal/handler/issue_child_done.go:512` `stageBarrierClosed`）是**兄弟范围**的判定，`ListChildIssues(parent.ID)` 只看一层。两层嵌套会让「阶段推进」这件事在第二层悄悄失效。

## 3. 身份与幂等

### 3.1 节点 id 的推导

```go
// server/internal/handler/issue_draft_group.go（新文件）

// issueDraftNodeNamespace 是节点 id 推导的固定命名空间。永远不要改它：它是
// 每一个已经铸造出来的节点 id 的一半输入，改掉等于让所有已建的组失去身份，
// 下一次 confirm 会把整组再建一遍。
var issueDraftNodeNamespace = uuid.MustParse("076522a7-f3b6-414f-afb0-41823470299e")

// issueDraftNodeID 把一个对齐节点解析成它的 origin_id。
//
// 根节点（key == ""）就是 chat_session_id 本身。这一条是整个模型的承重墙：
//   - 它让迁移 486 的唯一索引一行不改就继续成立，旧的一张单是只有根的组；
//   - 它让 GetIssueByOrigin(chat_session_id) 依然精确指向这组单的父单，
//     issue_draft.issue_id 的含义不变；
//   - 它让身份不受客户端摆布——客户端可以乱造子节点的 key，但根节点的 id
//     是服务端从 session 推出来的，所以任何「换一套 key 再确认一次」都会在
//     插根节点时撞唯一索引，整个事务回滚（见 §3.3 时序 B）。
//
// 子节点用 UUIDv5，把 chat_session_id 混进推导，所以一个客户端无法构造出
// 指向别的 draft 的节点 id。分隔符是 NUL，避免 ("a","bc") 和 ("ab","c")
// 撞成同一个摘要。
func issueDraftNodeID(sessionID pgtype.UUID, key string) pgtype.UUID {
	if key == "" {
		return sessionID
	}
	derived := uuid.NewSHA1(issueDraftNodeNamespace,
		[]byte(uuidToString(sessionID)+"\x00"+key))
	return parseUUID(derived.String())
}
```

### 3.2 为什么不是复合唯一键

issue 里点名要评估的两个候选，以及它们各自的代价：

**候选 A：`(origin_id, origin_seq)` 复合唯一。** 需要在 `issue` 这张最热的表上加一个只服务于这一个 origin_type 的 `origin_seq INTEGER` 列，加一条新的唯一索引，再删掉 486。而且 `GetIssueByOrigin`（`issue.sql:595`）的 `LIMIT 1` 立刻变成不确定行为，`createIssueForDraft:902` 的认领路径和 `classifyOrigin`（`service/issue.go:717`）都要跟着改。更关键的是它**没有回答阶段 3**：`origin_seq` 要怎么在第二轮里分配才能幂等？读 `max(origin_seq)` 再加一的话，一次超时重试就会分配到不同的 seq，凭空多出一组单。

**候选 B：origin 只挂父单，子单只挂 parent。** 迁移 486 同样不动，实现最省。但它有两个硬伤：一是子单在自己的 origin 列上再也**不能**被打上 `issue_draft` 标记（会撞同一个 origin_id 的唯一索引），DENE-366 阶段 3 要的「任务执行中从子单跳回对齐」就只能每次多走一跳去读父单；二是第二轮追加的子单**没有任何幂等键**——一次超时重试就会把同样的增量再追加一遍，而这恰恰是阶段 3 的核心动作。

**选定方案（候选 C，本页）：每个节点自己的 origin_id，根节点 = chat_session_id。** 它把「至多一张」的粒度从「一场对齐」下沉到「一个节点」，代价是一个推导函数，收益是候选 A 和 B 的问题同时消失：索引不动、`LIMIT 1` 更正确（origin_id 现在真的是一对一）、每个节点自带幂等键、轮次不参与推导所以阶段 3 的追加天然幂等。

### 3.3 并发两次确认的时序

前提：finalize 的三段短事务结构不变；建组事务内**先插根节点，再插子节点**。先插根是为了让失败尽早发生——子节点先插会做一堆注定回滚的工作。

**时序 A：两个 tab 拿着同一个 revision 同时确认**

```
tab A                               tab B
─────────────────────────────────   ─────────────────────────────────
T1  BEGIN; SELECT ... FOR UPDATE
    status=ready, revision=7  ✓
    COMMIT                          (阻塞在 FOR UPDATE)
T2                                  BEGIN; SELECT ... FOR UPDATE
                                    status=ready, revision=7  ✓
                                    ← 在锁内重读，读到的还是 ready
                                    COMMIT
T3  BEGIN (group tx)
    INSERT root   origin_id=S   ✓
    INSERT child  origin_id=v5(S,c1) ✓
T4                                  BEGIN (group tx)
                                    INSERT root origin_id=S
                                    ← 阻塞：唯一索引上有 A 未提交的元组
T5  COMMIT                          ← 解除阻塞
T6                                  23505
                                    ROLLBACK（子单一张都没落）
                                    GetIssueByOrigin(S) → A 的父单
                                    ListChildIssues(父单) → A 的子单
T7  BEGIN; FOR UPDATE
    MarkIssueDraftCompleted(issue=父单)
    COMMIT → 200                    BEGIN; FOR UPDATE
                                    status 已是 completed
                                    → 返回同一个父单 → 200
```

两边都拿到 200，都指向同一组单。**没有第二组**，理由落在数据库上：B 的根节点插入撞了 486 的唯一索引，而 B 的所有子节点和它在同一个事务里，一起回滚。

T4 到 T5 之间 B 是**阻塞**在唯一索引上的，不是失败。这段阻塞时长 = A 的建组事务时长。可以接受，因为建组事务里只有 INSERT 和已有的工作区行锁，没有任何外部调用；但它比今天长（今天是一张单的事务），所以 §4.3 给了硬性约束：权限校验、状态解析、assignee 校验全部在事务外做完，建组事务内不许有网络 IO 或不相干的读。

**时序 B：两次确认之间 key 整个换了一套**

先澄清一件容易搞错的事：finalize 的请求体里**只有 `expected_revision`**，载荷是服务端从 `issue_draft.draft` 这一行读出来的（`issueParamsFromDraft:805` 读的是 `draft.Draft`）。所以客户端没法在确认那一刻塞一套新 key 进来——要换 key 必须先保存，而保存会 bump revision。

于是真正会发生的是这个：

```
T1  A: admit(expected_revision=7) ✓        draft: ready, revision=7, keys=[c1,c2]
T2                                          B: 保存新载荷 → revision=8, keys=[x1,x2,x3]
T3                                          B: admit(expected_revision=8) ✓  ← 也过了！
                                               status 还是 ready，revision 也对得上
T4  A: BEGIN; INSERT root(S) ✓
       INSERT child v5(S,c1), v5(S,c2) ✓
T5                                          B: BEGIN; INSERT root(S) → 阻塞
T6  A: COMMIT                               B: 23505 → ROLLBACK → 认领 A 的组
```

两次 `admit` **都会通过**——它们检查的是各自看到的 revision，而两次之间确实发生了一次合法的保存。挡住第二组的不是 revision，是**根节点 id 恒等于 S**：B 的 `x1/x2/x3` 三个子节点 id 和 A 的完全不同，如果身份只挂在子节点上，B 会干干净净地建出第二组。

代价要说清楚：**最终落库的是 A 的那一组（revision 7 的内容），B 在 T2 保存的编辑不会进到单里**，draft 完成后指向 A 的组。这是既有假设的延伸——`issue_draft.go:640-645` 已经写明「一场对齐只有一个编辑者、一块屏幕」，所以 T2 和 T1 本来就该是同一个人。本设计不去关这个窗口，只保证它**不会变成两组单**。

**时序 C：建组成功但进程在第 3 步之前死掉**

draft 停在 `ready`，issue 组已经存在。下一次 confirm：`admit` 通过（还是 ready，revision 没变），建组事务里根节点插入撞唯一索引 → 认领已存在的组 → 第 3 步写回 `issue_id`。和今天单张单的恢复路径逐字相同。

### 3.4 认领路径要读回整组

`createIssueForDraft:902` 今天认领的是一行。拆组之后认领的是一组：

```go
// 认领：这组单已经由另一次 confirm（或本次的前一次尝试）建好了。
// 根节点是唯一权威——它的 origin_id 是 chat_session_id，唯一索引保证只有一行。
root, err := h.Queries.GetIssueByOrigin(ctx, ...{OriginID: draft.ChatSessionID})
children, err := h.Queries.ListChildIssues(ctx, root.ID)
```

`ListChildIssues`（`server/pkg/db/queries/issue.sql:571`）已经存在，`issue_child_done.go:143` 在用，按 `number ASC` 排序 —— 因为建组时 `AllocateIssueNumber` 是按载荷顺序调的，读回来的顺序就是对齐里定下的顺序，不用额外排。

它的 `WHERE parent_issue_id = $1` **没有工作区谓词**。在这里是安全的：`root` 来自 `GetIssueByOrigin`（`issue.sql:595`），那条是工作区限定的，所以 `root.ID` 是已经过边界的值。实现时不要换成任何从请求体直接拿到的 parent id。

这里**不能**改成按 origin_type 扫，因为父单下可能有人手工加的子单，它们不属于这组对齐；但对「这组单现在长什么样」这个问题，父单下的全部子单才是答案。回传整组即可，不做过滤。

## 4. 事务与原子性

### 4.1 事务边界

```
finalize 请求
│
├─ tx1  admitIssueDraftForFinalize      （不变）
│       LockIssueDraftInWorkspace FOR UPDATE
│       在锁内读到的值上决策
│       COMMIT / 提前返回
│
├─ tx2  IssueService.CreateGroup        （新）  ← 不持有 draft 行锁
│       1. 解析 + 校验整组（在事务外做完，见 §4.3）
│       2. BEGIN
│       3. 根节点：走今天 Create 的全部事务内步骤
│       4. 子节点 × N：同上，ParentIssueID = 根节点 id
│       5. COMMIT                        ← 整组在这一刻同时可见
│       6. 事务外：逐个 linkAttachments / publish / analytics / enqueue
│
└─ tx3  completeIssueDraft               （不变）
        LockIssueDraftInWorkspace FOR UPDATE
        MarkIssueDraftCompleted(issue_id = 根节点 id)
        COMMIT
```

和今天唯一的结构差异是 tx2 从「一张单的事务」变成「一组单的事务」。**锁依然不跨 tx2 持有**——`483_issue_draft.up.sql` 的表注释写的理由（持锁会让每次 confirm 占两个连接）在组的情形下只会更严重。

### 4.2 `IssueService` 的拆分

今天 `Create`（`service/issue.go:213`）把三件事焊在一起：事务内的建行、`tx.Commit`、事务后的 `linkAttachments` / `publishIssueCreated` / `captureCreatedAnalytics` / `maybeEnqueueOnAssign`。拆成：

```go
// createInTx 是一张 issue 的全部事务内步骤：状态目录共享锁与重解析、parent /
// project 的工作区边界、label 校验与挂载、重复守卫、编号分配、position、
// CreateIssue(WithOrigin)、source context。它必须是建 issue 的唯一权威——
// CreateGroup 走的是同一个函数，不是它的一份拷贝，否则「组里的单」和「单独
// 建的单」会在某次只改了一边的提交里悄悄分叉。
func (s *IssueService) createInTx(ctx context.Context, tx pgx.Tx, qtx *db.Queries,
	p IssueCreateParams, policy IssueCountPolicy) (db.Issue, []db.IssueLabel, error)

// afterCommit 是一张 issue 提交之后的全部动作：附件挂载、issue:created 广播、
// analytics、指派时的 agent / squad 入队。
func (s *IssueService) afterCommit(ctx context.Context, issue db.Issue,
	labels []db.IssueLabel, p IssueCreateParams, opts IssueCreateOpts) (pgtype.UUID, []db.Attachment)

// Create = begin + createInTx + commit + afterCommit（对外行为一字不变）
// CreateGroup = begin + createInTx×(1+N) + commit + afterCommit×(1+N)
```

`AssignedAgentRunFireAt`（channel /issue 的延迟入队）不进 `CreateGroup` 的签名：它是渠道单张创建的专用旋钮，对齐组用不到，让它出现在组的参数里只会多出一条没人走的分支。

### 4.3 事务内不做的事

建组事务只有 INSERT 和已有的行锁。**权限校验、状态解析、assignee 校验全部在 tx2 开始之前做完**，理由和今天 `issueParamsFromDraft:805` 在事务外跑一样：`validateAssigneePair`（`server/internal/handler/issue.go:3781`）会去读 member / agent / squad 行并调 `canInvokeAgent`，把它拖进建组事务会把事务时长绑在一串不相干的读上，而 §3.3 时序 A 的 T4 阻塞时长正好等于这个事务时长。

**一个已知且被接受的窗口**：事务外校验通过、事务内插入之前，某个 agent 被归档。今天单张单也有同样的窗口，处理方式一致——不补，因为补它意味着把权限校验搬进事务。

### 4.4 事务内的三个已有机制在组里的行为

| 机制 | 单张时 | 一组时 | 结论 |
| --- | --- | --- | --- |
| `AllocateIssueNumber`（`service/issue_limit.go:87`） | 调 1 次 | 调 1+N 次 | 正确。`IncrementIssueCounter` 在同一事务内递增，`CountIssuesUpTo` 看得见本事务未提交的行，所以配额是按 1+N 张算的。超额时第 k 张返回 `IssueLimitReachedError`，整组回滚 —— **配额不够就一张都不建**，这是对的：半组单比没有单更糟。 |
| `NextTopPosition`（`server/internal/issueposition/position.go:21`） | 调 1 次 | 调 1+N 次 | 正确但要知道结果：它读 `MIN(position)`，本事务已插入的行可见，所以 1+N 张拿到递减的 position，按创建顺序排在列顶。父单在最上面，子单按数组顺序往下。符合直觉，不用特殊处理。 |
| `issueguard.LockAndFindActiveDuplicate`（`server/internal/issueguard/duplicate.go:45`） | 取 1 个事务级 advisory 锁 | 取 1+N 个，持到 COMMIT | 可接受。`AllowDuplicate=true` 在取锁之后才短路（`duplicate.go:58-62`），所以锁照取。N ≤ 20，advisory 锁很便宜；但这是把 §2.4 的上限定在 20 而不是 200 的理由之一。 |

### 4.5 提交后的顺序

```
COMMIT
├─ 父单 afterCommit   （先广播父单：客户端收到子单的 issue:created 时，
│                       它的 parent_issue_id 已经能解析）
└─ 子单 afterCommit × N（按 stage 升序，同 stage 按数组顺序）
```

入队走的还是 `maybeEnqueueOnAssign`（`service/issue.go:731`），**不新增入队逻辑**：它本来就跳过 `backlog`（`shouldEnqueueAgentTaskWithQueries:781`），所以 §6 的派单策略完全由载荷里的 `status` 表达，服务端一行分支都不用加。

`afterCommit` 里任何一步失败都只记 warn 不回滚——事务已经提交，组已经存在，把一次成功的创建报成失败会让客户端重试，而重试会走认领路径拿到同一组单。这与今天 `linkAttachments`（`service/issue.go:561`）的既有姿态一致。

## 5. finalize 的决策流程

### 5.1 完整流程

```
FinalizeIssueDraft(sessionId, {expected_revision})
│
├─ loadIssueDraftSession            会话归属 + carrier 校验（不变）
│
├─ [tx1] admitIssueDraftForFinalize（不变）
│   ├─ completed → 返回既有组（§5.2）───────────────► 200
│   ├─ abandoned → 409
│   ├─ 非 ready   → 409
│   ├─ revision 不符 → 409
│   └─ ready 且 revision 相符 → 继续
│
├─ issueGroupParamsFromDraft        解析 + 校验整组（事务外）
│   ├─ 反序列化 payload（含 children）
│   ├─ §2.4 的六条边界
│   ├─ 根节点：沿用今天 issueParamsFromDraft:805 的全部校验
│   ├─ 子节点 × N：status / priority / assignee 同样的校验
│   │   （project_id 不校验：IssueService.Create 会从父单回填）
│   └─ 推导每个节点的 origin_id（§3.1）
│
├─ [tx2] createIssueGroupForDraft
│   ├─ 先查 GetIssueByOrigin(chat_session_id)
│   │   └─ 命中 → 认领（§3.4），跳过建组
│   ├─ CreateGroup(...)
│   │   └─ 23505 → 认领（§3.4）
│   └─ 其它错误 → writeIssueDraftCreateError（不变，`issue_draft.go:967`）
│
├─ carryIssueDraftAttachments       会话附件 → 根单（§5.4，DENE-453）
│   ├─ ListAttachmentsByChatSession 只取还没有主的行
│   └─ LinkAttachmentsToIssue(根单) 失败只记 error，不改响应
│
└─ [tx3] completeIssueDraft（不变，issue_id = 根节点 id）───► 200
```

### 5.2 `completed` 分支要回传整组

今天这个分支只有 `issue_id`（`issue_draft.go:719-727`）。拆组之后它要回传整组，否则「双击确认」的第二次和第一次给客户端的东西不一样，确认页会从「这是刚建的 5 张单」退化成「这是刚建的 1 张单」。读法和 §3.4 的认领完全相同：`GetIssueByOrigin` 取父单，`ListChildIssues` 取子单。

### 5.3 响应形状

```ts
/** 一组单里的一张，够确认页把它渲染成一行。 */
export interface IssueDraftCreatedIssue {
  id: string;
  /** MUL-123。确认页要给人看的是编号，不是 UUID。 */
  identifier: string;
  title: string;
  status: string;
  stage?: number | null;
  assignee_type?: string | null;
  assignee_id?: string | null;
  parent_issue_id?: string | null;
}

export interface IssueDraftFinalizeResult {
  draft: IssueDraft;
  /** 根（父）单。字段没动，含义没动：仍然是「导航到哪里」的答案。 */
  issue_id: string;
  /**
   * 整组，根在最前。旧后端不会发这个字段——那时候组就是 [issue_id]，
   * 客户端按这个退化处理，不额外提示。
   */
  issues?: IssueDraftCreatedIssue[];
}
```

zod（`packages/core/api/schemas.ts:2402`）：

```ts
export const IssueDraftCreatedIssueSchema = z.object({
  id: z.string().min(1),
  identifier: z.string().catch(""),
  title: z.string().catch(""),
  status: z.string().catch(""),
  stage: z.number().int().nullish().catch(null),
  assignee_type: z.string().nullish().catch(null),
  assignee_id: z.string().nullish().catch(null),
  parent_issue_id: z.string().nullish().catch(null),
}).loose();

export const IssueDraftFinalizeSchema = z.object({
  draft: IssueDraftSchema,
  // 保持没有 fallback：2xx 意味着组已存在，客户端要导航过去，
  // 空 id 会把人送到一条解析不了的路由上（现有注释 schemas.ts:2395-2401）。
  issue_id: z.string().min(1),
  // 有 fallback：旧后端不发，坏行不该让整次成功的确认解析失败。
  // 客户端在空数组时退化成「只知道父单」。
  issues: z.array(IssueDraftCreatedIssueSchema).catch([]),
}).loose();
```

`issues` 里单张的 `id` 用 `min(1)` 而不是 `.catch("")`：一行没有 id 的记录在确认页上是一个点不开的幽灵行，整条丢掉比留着好——但 `z.array(...).catch([])` 会在任意一行坏掉时丢掉**整个数组**。这是刻意的：组是一个整体判断，「5 张里有 1 张解析不了」和「不知道有几张」应该给用户同一个界面（退化成只显示父单），而不是一个少了一行、看起来完整的列表。

### 5.4 会话附件挂在根单上（DENE-453）

对齐会话里产出的原型（用户丢进去的截图、载体自己传的 HTML）落在 `attachment` 表上，先绑在会话的消息上。确认时这些行要改挂到**根单**：

- **为什么必须挂**：`attachment.chat_message_id` 是 `ON DELETE CASCADE`，会话一删消息一删，文件跟着没；而 description 里的 markdown 链接会一直留在单上。挂到根单是让那条链接活得比会话久。
- **为什么只挂根单**：子单的 description 引用同一个 URL 就够了，markdown 链接不需要第二份字节。按节点各传一份会把一份产物拆成 N 份互不相干的附件。
- **读法**：`ListAttachmentsByChatSession`（`pkg/db/queries/attachment.sql`）取本会话还没主的行，两个方向都算——用户上传的带 `chat_session_id`，载体上传的只带 `chat_message_id`，只看前者会漏掉载体自己的原型。它同时是幂等的来源：已经挂过的行 `issue_id` 不再为 NULL，重复确认取不到任何东西。
- **姿态**：best-effort，与 §4.5 的 `linkAttachments` 一致。组这时已经提交，附件挂失败不能把一次成功的确认报成失败——客户端会重试一次已经发生的确认。成功时补一条 `issue_attachments:changed`，让别处的 issue 详情页知道附件变了。

## 6. 派单语义

### 6.1 三个候选

| 方案 | 确认那一刻发生什么 | 代价 |
| --- | --- | --- |
| 全 `todo` | N 个 agent 同时被点着 | 阶段 2 依赖阶段 1 的产出，同时开跑就是 N 份白跑的 run 加一堆合并冲突；而且一次确认烧掉 N 份配额 |
| 全 `backlog` | 什么都不跑，等人逐张点 | 直接违背 kk zi 的原话「最后完成时变成拆好的 issue 单，**指派给对应的 agent 去创建任务**」——拆完还要点 N 次，等于没拆 |
| 按 stage 只点第 1 阶段 | stage 1 的子单跑，其余停在 backlog | 需要有人把 stage 2 从 backlog 提上来（§6.3） |

### 6.2 推荐

**按 stage 只点第 1 阶段。** 具体默认值（DENE-755 / 758 落地后的版本）：

| 节点 | status | assignee |
| --- | --- | --- |
| 父单（有子单时） | `in_progress`（协调状态，见 §6.3） | 对齐里定下的 agent 或小队；保留但不立即派实现 run |
| 父单（无子单） | 载荷给什么是什么 | 走普通单票规则 |
| stage 1 子单 | `todo` | 对齐里定下的 agent / 小队；没匹配到就留未分配 |
| stage ≥ 2 子单 | `backlog` | 对齐里定下的 agent（先绑定，不起跑） |
| 无 stage 的子单 | `todo` | 对齐里定下的 agent |

子单的 status **由 stage 推导，不由载荷携带**：面板保存时归一化一次
（`packages/core/issue-drafts/group.ts` 的 `issueDraftChildStatus`），服务端在最终写入处再推导一次
（`issueDraftChildStatusForCreate`）。确认只发一个 revision，服务端是最后一个能拦住旧客户端原始载荷的地方。

没有负责人 = 未分配，不是「校验失败」：子单以未分配写入、照常创建、不起任何 run，面板上显示成待分配，
后续用普通指派补齐即可。它**不**被改写成 `backlog` —— 未分配是板上看得见的一个洞，
停进停车场只会把这个洞藏起来（DENE-757 定稿）。DENE-694（#285）落地的「某一格指派不上就把那一格丢成未分配并回一条 warning、
其余照常创建」正好落在同一条规则上：丢掉的格子变成一条待分配的子单加一条看得见的提示，而不是一张静默停在停车场的单。

理由：

1. **屏障机制本来就是为这件事造的。** `stageBarrierClosed`（`issue_child_done.go:512`）在最低未完成阶段全部终结时唤醒父单的 assignee，注释写得很直白：「The woken assignee decides whether to promote the next stage (agent-driven advancement); the server only detects the barrier and wakes.」服务端只检测和唤醒，提阶段是被唤醒的人/agent 的动作。这条设计不需要新增任何东西。
2. **backlog 的语义正好对得上。** `123_issue_stage.up.sql` 和 `shouldEnqueueAgentTaskWithQueries` 都把 backlog 定义成「预先指派但不立即执行的停车场」。stage ≥ 2 的子单就是这个状态：谁做已经定了，什么时候做没到。
3. **服务端只多一条镜像规则。** 子单的派单仍由载荷里的 `status` 表达，`maybeEnqueueOnAssign` 的既有 backlog 跳过就是执行器；新增的只有「子单 status 从 stage 推导」和「协调父单的状态与首轮入队被镜像」两条，都写在同一个写入边界上。

### 6.3 这个推荐有一个真的洞：父单必须有人能被叫醒

`notifyParentOfChildDone`（`issue_child_done.go:117-133`）有三道闸，都会让屏障唤醒**整条不发生**：

- 父单状态是 `done` / `cancelled` → 返回；
- 父单状态是 `backlog` → 返回（#4320 / MUL-3497 的防意外激活）；
- **父单 assignee 是 member（人）→ 返回**（MUL-2538）。

所以父单如果不指派、或者指派给人，stage 1 全部做完之后**不会有任何唤醒**，stage 2 就永远停在 backlog，而且是**静默地**停着——没有评论，没有 inbox 行。这不是实现 bug，是既有的产品决定（人自己看自己的时间线），但它和「拆一组单交给 agent 自动往下跑」是冲突的。

**已拍板的做法（DENE-755 / DENE-757 / DENE-758）：父单当协调者，但换一个「会醒」的状态。**

- **状态用 `in_progress`，不用 `backlog`。** 有子单时服务端在写入处把父单状态镜像成
  `ISSUE_DRAFT_COORDINATOR_STATUS`（`issue_draft_group.go` 的 `issueDraftCoordinatorStatus`）。
  这三道闸里只有 backlog 这道能靠选状态绕开，而它正好是 §6.2 里最容易顺手选的那个。
- **不入队由创建路径单独抑制。** 父单保留 agent / 小队负责人（那是屏障要唤醒的席位），
  但这一次确认不给它派实现 run：`IssueCreateOpts.SuppressAssigneeRun` 只跳过 `maybeEnqueueOnAssign`，
  issue 本身、负责人、广播、analytics 全都照常写入。这是一次创建的属性，不是 issue 被停在某个状态——
  之后对父单的每一次普通写入（改派、改状态、阶段收口）都按普通规则入队。
- **不新增组专用调度器。** 唤醒仍然只走 `dispatchParentAssigneeTrigger`；服务端不自动把下一阶段改成 `todo`，
  提阶段仍是被唤醒的协调者的一次普通 status 写入。
- **父单没有 agent / 小队负责人时仍然可以确认**，只是阶段结束后没人被叫醒；这一点由确认面板明确写出，
  而不是靠一个永远不触发的唤醒假装链路是闭环的。

被否掉的两条：父单 `backlog`（屏障整段跳过，链路断在阶段 1）、以及「阶段 2 只做人工提阶段」（§9 第 2 条的旧推荐 (c)，把闭环留给了人）。

### 6.4 `IssueDraftChild.stage` 的取值

模型给 stage 的能力有限，前端 preview 面板必须让人能改。三条规则：

- 全部子单都没给 stage → 不分阶段，全部 `todo`。`stageBarrierClosed`（`issue_child_done.go:512`）把「没有任何子单带 stage」的集合当成一个隐式阶段，最后一张做完时唤醒一次。这是最常见的小需求该有的行为，不该强行编排。
- 有任何一个给了 stage → **没给的必须补成 stage 1**。理由不是屏障会卡住（它不会），而是这样的子单会从屏障里**整个消失**：在一个分阶段的集合里，`stageBarrierClosed` 对未分阶段的兄弟 `continue`（`issue_child_done.go:528-530`），所以它既不会把任何阶段拖住，它自己做完时也 `return false` 什么都不唤醒（`:523-525`）。混着放的结果是一张单静默地不参与编排 —— 比卡住更难发现。
- stage 从 1 开始。跳号（1,3）不会坏：屏障是 frontier 判定（「stage ≤ S 的分阶段兄弟全部终结」），跳号自然成立；但前端仍归一化成 1,2，因为 `stageProgressSummary`（`issue_child_done.go:543`）会把实际的 stage 号打进唤醒评论，跳号会让人看到「Stage 1 / Stage 3」。

顺带一条对 §6.2 的加强：`stageProgressSummary` 除了渲染进度，还会返回「下一个该提的阶段」，写进唤醒评论里。也就是说被唤醒的一方不需要自己算该提哪一批 —— 服务端已经告诉它了。这是 §6.3 方案 (a) 的可行性证据。

## 7. 迁移清单

### 7.1 阶段 2：0 个迁移

逐条核对为什么不需要：

| 需要的东西 | 已有 | 出处 |
| --- | --- | --- |
| 子单挂父单 | `issue.parent_issue_id` | `001_init.up.sql` |
| 子单分阶段 | `issue.stage INTEGER CHECK (NULL OR >= 1)` | `123_issue_stage.up.sql` |
| 子单打 issue_draft origin | `origin_type` CHECK 已含 `'issue_draft'`，且已 VALIDATE | `484` + `485` |
| 一个节点至多一张单 | `idx_issue_origin_issue_draft_unique` | `486`（**不动**） |
| 按 origin 反查 | `idx_issue_origin ON issue(origin_type, origin_id)` | `042_autopilot.up.sql:77` |
| 按父单列子单 | `idx_issue_parent`、`idx_issue_workspace_parent` | `001:171`、`204` |
| draft 载荷放 children | `issue_draft.draft JSONB` | `483` |

`server/cmd/migrate/main.go:311` 的 `486 → idx_issue_origin_issue_draft_unique` 登记项也不动。

**这是本设计最实质的收益**：阶段 2 是一次纯应用层改动，回滚就是 revert，没有 DDL 需要倒回去。

### 7.2 阶段 3（重开一轮）需要的迁移

不在本单实现范围内，列出来是为了证明身份模型没有把这条路堵死，以及给阶段 3 一个起点。

**迁移 1：`4xx_issue_draft_finalize_round.up.sql`**

```sql
-- 这个 draft 已经确认过几轮，以及最后一轮确认时的 revision。
--
-- 轮次不参与节点 id 的推导（见 docs/design/issue-draft-group-finalize.md §3.1），
-- 所以它不是幂等键，而是「重开之后哪些内容是新的」的锚：第 N+1 轮只需要建
-- payload 里那些 key 还没有对应 issue 的节点，而 finalized_revision 让页面
-- 知道从哪一版开始算增量。
--
-- 默认值回填已有行：每一行都最多确认过一轮。
ALTER TABLE issue_draft
    ADD COLUMN IF NOT EXISTS finalize_round     INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS finalized_revision BIGINT;
```

单语句、无索引、无 CHECK 变更，所以不需要 `CONCURRENTLY`，也不需要 `NOT VALID` + `VALIDATE` 的拆分。

**迁移 2（仅当需要按轮次查询时）：`4xx_issue_draft_reopened_index.up.sql`**

```sql
-- 单文件单语句，CONCURRENTLY，按仓库规矩登记进
-- server/cmd/migrate/main.go 的 concurrentIndexMigrations。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_draft_reopened
    ON issue_draft (workspace_id, updated_at)
    WHERE finalize_round > 0;
```

**不需要的迁移**：`issue_draft.status` 的 CHECK 已经含 `'draft' | 'ready' | 'completed' | 'abandoned'` 四个值（`483`），从 `completed` 回到 `ready` 不需要改 CHECK。

### 7.3 顺序与规矩核对

- 每个并发索引一个文件、一条语句 —— §7.2 迁移 2 已满足；
- 建索引的迁移要登记进 `concurrentIndexMigrations`（`server/cmd/migrate/main.go:236-312`）—— §7.2 迁移 2 需要；
- 约束变更走 `NOT VALID` + 独立 `VALIDATE` 文件 —— 本设计**没有**约束变更；
- 条件跳过的迁移仍会进 `schema_migrations`，后续迁移要用 `IF EXISTS` / `IF NOT EXISTS` —— §7.2 两个迁移都用了。

### 7.4 如果拍板改成候选 A（复合唯一键）

留在这里是为了让这个取舍有价格。候选 A 需要 4 个迁移文件，顺序不能换：

```
4xx_issue_origin_seq.up.sql
  ALTER TABLE issue ADD COLUMN IF NOT EXISTS origin_seq INTEGER;

4xx_issue_origin_issue_draft_seq_unique.up.sql        （单语句）
  CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_origin_issue_draft_seq_unique
      ON issue (origin_id, origin_seq)
      WHERE origin_type = 'issue_draft';
  -- 并登记进 concurrentIndexMigrations

4xx_issue_origin_seq_not_null.up.sql                  （NOT VALID）
  ALTER TABLE issue ADD CONSTRAINT issue_origin_seq_present
      CHECK (origin_type <> 'issue_draft' OR origin_seq IS NOT NULL) NOT VALID;
  -- 回填已有 issue_draft 行的 origin_seq = 0 必须排在这个文件之前，
  -- 否则 VALIDATE 会在旧数据上失败

4xx_issue_origin_seq_validate.up.sql                  （独立 VALIDATE）
  ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_seq_present;

4xx_drop_issue_draft_origin_unique.up.sql             （单语句）
  DROP INDEX CONCURRENTLY IF EXISTS idx_issue_origin_issue_draft_unique;
  -- 并登记进 concurrentDownIndexCleanups（down 方向会重建）
```

外加 `GetIssueByOrigin` 的 `LIMIT 1` 改写、`createIssueForDraft` 的认领路径改写、一次在 `issue` 热表上的加列，以及 §3.2 说的「第二轮 seq 怎么幂等分配」这个没有答案的问题。

## 8. 测试契约

按仓库的分层规矩，每条行为只放在一层。

**`server/internal/handler/`（Go，用 `dbfx` + `testutil.Call`）**

| 用例 | 断言 |
| --- | --- |
| 单节点 payload（无 children） | 建 1 张，`issue_id` = 那张，行为与今天逐字相同 |
| 父 + 3 子 | 建 4 张，父单 `origin_id` = `chat_session_id`，3 张子单各有不同 `origin_id`，`parent_issue_id` 全部指向父单 |
| 同一 payload 确认两次 | 第二次不新建，返回同一组；`issue` 表里 `origin_type='issue_draft'` 的行数不变 |
| admit 之后、建组之前，draft 被保存成另一套 children key，再确认一次 | 只有一组（第一次那组），第二次认领它而不是建第二组（§3.3 时序 B）。这是本设计唯一一个 revision 挡不住、只有根节点 id 挡得住的用例，**不能漏** |
| 并发两次确认 | 只有一组；两次响应的 `issue_id` 相同 |
| 组里第 3 张的 assignee 无权调用 | 4 张单都建出来，第 3 张**未分配**，响应里的 `assignment_warnings` 指名它（DENE-694：校验照旧拒绝写这个 assignee，但降级成警告，不再拿整组陪葬） |
| 配额只剩 2 张，payload 要 4 张 | `IssueLimitReachedError`，一张都没建 |
| 重复 key | 400 `duplicate sub-issue key`，不到数据库 |
| children 长度 21 | 400 |
| stage = 0 / -1 | 400 |
| stage 1 `todo` + stage 2 `backlog` | 只有 stage 1 的子单入队（查 `agent_task_queue`） |
| 建组成功后 draft 未 completed，再确认一次 | 认领已有组并写回 `issue_id`（§3.3 时序 C） |

**`packages/core/`（`.test.ts`，首行 `// @vitest-environment node`）**

| 文件 | 用例 |
| --- | --- |
| `issue-drafts/protocol.test.ts` | `children` 块解析；坏 JSON = 不更新而不是清空；key 原样保留；`mergeIssueDraftPayload` 对 children 整体替换 |
| `issue-drafts/group.test.ts`（新） | stage 归一化（全空 → 不分阶段；部分空 → 补 1；跳号 → 压实）；派单策略从 stage 推 status |
| `api/schemas.test.ts` | `IssueDraftFinalizeSchema`：缺 `issues` → `[]`；`issues` 里有坏行 → 整个数组退化成 `[]`；`issue_id` 为空 → 硬失败；缺 `assignment_warnings` → `[]`，坏行仍可读（DENE-694） |
| `api/client.test.ts` | finalize 的 malformed-response 用例（仓库 API 兼容规矩的硬要求） |

**`packages/views/`（`.test.tsx`）** —— 只留确认页的 happy path、接线和无障碍，矩阵不重跑，注释指回上面的 `.test.ts`。

**不测的**：`stageBarrierClosed` 的阶段推进矩阵。它是既有行为，`issue_child_done_stage_test.go` 已经覆盖，本单一行没改它。

## 9. 需要 kk zi 拍板的开放问题

1. **派单默认档。** 推荐「按 stage 只点第 1 阶段」（§6.2）。备选：全 `todo`（快，但烧 N 份配额且产出会打架）、全 `backlog`（安全，但拆完还要点 N 次）。
2. **谁负责把 stage 2 从 backlog 提上来。**（§6.3）**已定：父单当协调者，用 `in_progress`，创建时抑制它自己的 run。**
   - (a) 父单指派给调度席，屏障唤醒它去提 —— 链路闭环；「创建时多起一次没必要的 run」由 `SuppressAssigneeRun` 消掉，所以这是最终选择；
   - (b) 放弃编排，全部子单一次性 `todo`；
   - (c) ~~阶段 2 先做「stage 1 自动跑 + 人工提阶段」~~ —— 被否，等于把闭环留给人工。
3. **拆单上限。** 推荐 prompt 里建议 8、服务端硬上限 20（§2.4）。上限决定了 §4.4 里 advisory 锁的持有量和 §3.3 时序 A 里并发确认的阻塞时长。
4. **父单要不要参与配额。** 现在的算法是父单也占一张（§4.4）。如果 kk zi 认为「容器单不该占额度」，那是一次 `AllocateIssueNumber` 的语义改动，会影响所有子单创建路径，不只是对齐 —— 我的建议是**不要改**，父单是一张真的 issue，它有标题、有讨论、有状态。
5. **文档位置。** 本页按 DENE-368 原文放在 `docs/design/`；仓库里既有的设计稿都在 `docs/kun/`（`blocker-attribution-design.md`、`config-transfer-v2.md` 等）。要统一的话我改路径，一条 `git mv` 的事。

## 10. 阶段 2 的落地顺序

拆成 4 个可独立审查的提交，前 3 个都不改变任何现有行为：

1. `refactor(issue)`：把 `IssueService.Create` 拆成 `createInTx` / `afterCommit`，`Create` 的外部行为一字不变。既有 Go 测试全绿就是证据。
2. `feat(issue)`：加 `IssueService.CreateGroup`，加 §8 的服务层测试。此时还没有人调用它。
3. `feat(issue-draft)`：`issueDraftNodeID` 推导、`issueGroupParamsFromDraft` 校验、`createIssueGroupForDraft` 认领、finalize 接上 `CreateGroup`、`completed` 分支回传整组。载荷里没有 `children` 时走的是 1 个节点的组，所以**这一步合进去时行为仍然不变**。
4. `feat(issue-draft)`：prompt 契约扩 `children` + 两个 policy 的 `Version` bump、TS 类型与 zod、preview 面板的拆单编辑、确认页的整组展示。这一步是行为真正改变的地方，也是唯一需要人工验收的地方。
