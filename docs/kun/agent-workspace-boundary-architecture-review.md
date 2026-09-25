# Agent 工作区边界与确定性失败架构评估

这份评估围绕 DENE-763 / DENE-814 的现场，检查五个边界：用户工作区与 Agent 交付分支、失败重试、迁移编号、DB 测试、同票多分支。结论是：当前代码已经有几处护栏，但它们分散在执行器、脚本和迁移测试中，尚未形成统一的“输入—产出—失败出口”契约。

## 结论与优先级

| 优先级 | 结论 | 推荐动作 |
| --- | --- | --- |
| P0 | 用户快照仍把“用户当前整棵树”当作可重放输入；`priorState` 没有把用户 HEAD 与未提交 delta 分开。冲突后 `snapshotPending` 保留同一输入，因而可以无限重演。 | 先做 DENE-814：把用户状态变成只读输入，记录 `user_head` 与 `dirty_snapshot`，只重放 HEAD 之后真实未提交的变化；给确定性 replay 冲突增加升级出口。 |
| P1 | 迁移双编号已经有 lint，但它把 468–501 的历史双编号白名单硬编码在测试里；新编号靠开发者自觉，合入时没有独立 reservation。 | 保留现有历史兼容，增加提交/PR 阶段的冲突检查与明确的编号分配责任；不要再用时间戳替换已发布编号。 |
| P1 | DB 测试已有 `scripts/test-db.sh` 的强制模式，但 package 级 `go test` 仍可走 `t.Skip`，测试是否真连库取决于入口。 | CI 只允许通过 `scripts/test-go.sh` 跑 DB 套件，并把“DB-backed package 零连接”作为失败；保留本地无库可跳过的开发体验。 |
| P2 | `retryableReasons` 已区分部分确定性失败，但没有统一的 failure fingerprint / attempt budget；执行器和巡检仍可能把同一输入交回同一 Agent。 | 在任务失败记录中持久化稳定指纹和输入版本，重复指纹达到阈值后转人工/阻塞，并把下一步写入 issue。 |
| P2 | 分支命名和 worktree 归属校验已有 `WorkspaceID/AgentID/ConversationID`，但一张票仍可有多个 task-scoped 分支和互不包含的救援线；平台没有交付真相源。 | 以 issue 为交付聚合根：一个 canonical delivery branch/PR，其他 worktree 必须标为尝试或救援，并在关闭/合并后可审计清理。 |

## 1. 用户工作区与 Agent 工作区

### 现状证据

- `server/internal/daemon/execenv/local_worktree.go:30-37` 明确说 worktree 要重放用户目录，快照包含 tracked edits 和 untracked files。
- `captureUserSnapshot`（约 `:1157-1218`）把用户目录写成一个 parent 为用户 HEAD 的 tree commit；它没有把“用户 HEAD 的移动”和“HEAD 之后的未提交 delta”作为两个字段保存。
- `replayUserState`（约 `:1612-1700`）用 `priorState → snapshot` 生成 commit 再 cherry-pick。若用户 HEAD 在两轮间前进，这段前进会被解释为本地编辑。
- `Finalize`（约 `:699-820`）在未合并文件存在时拒绝提交，并保持 worktree；`snapshotPending` 不推进状态，因此下一轮可得到同一个冲突输入。
- 已有保护：私有 `GIT_INDEX_FILE`、`refs/multica/local-state/*`、branch owner 校验和 worktree 保留，防止直接写用户目录或静默丢失工作。

### 风险

用户提交的前进会进入 Agent 分支，造成大范围伪变更；冲突时“保持现场”保护了数据，却没有改变下一轮输入，因而形成确定性死循环。用户看到的是主目录干净、Agent 分支却有 254 个文件，无法判断谁是变更来源。

### 方案对比

| 方案 | 做法 | 优点 | 代价 |
| --- | --- | --- | --- |
| A. 现状加重试次数 | 保留整树快照，冲突 N 次后放弃 | 改动小 | 仍会污染分支，N 次前浪费运行；无法证明哪些是用户输入 |
| B. 双层用户输入（推荐） | 每轮记录 `user_head`、未提交 tracked/untracked snapshot；只将“上轮 dirty → 本轮 dirty”的 delta 重放，用户 HEAD 移动只作为 branch 起点/只读参照 | 边界可解释，replay 可幂等；冲突只代表真实的用户与 Agent 同改 | 需要升级旧 `local-state` 记录并补迁移测试 |
| C. Overlay/只读 ref | Agent 永远从纯基线分支运行，用户目录作为只读 overlay，交付时由人/合并器选择 | 最强隔离，用户状态永不进交付分支 | 构建工具和 Agent 读取路径要适配 overlay；当前 worktree/CLI 契约变化大 |

推荐 B。它保留 Agent 能看到用户未提交工作这一产品行为，同时把“用户已提交前进”从 Agent 交付分支移出。DENE-814 是 P0 实施票。

## 2. 确定性失败与出口

### 现状证据

- `server/internal/service/task.go:5362-5405` 用 `retryableReasons` 做失败分类；测试已明确本地 stall、磁盘/权限失败不应进入可重试集合（`server/internal/service/task_complete_race_test.go:539-565`）。
- `MaybeRetryFailedTask`（约 `:5654`）按 reason 决定自动重试；失败处理仍是 task 级别，未保存“同一输入版本 + 同一失败指纹”的统一键。
- `Finalize` 的 replay conflict 路径是可重复的输入错误，却只以 worktree 保留和错误文本告知，没有自动升级为 blocked/人工处理。
- completion stall 只按 30 分钟 marker 去重并发一次恢复任务（`server/internal/service/task_completion_stall.go`），它解决“完成但 issue 未收口”，不等于确定性失败出口。

### 风险

同一 `input_version + failure_class + normalized_error` 若被无限重新入队，巡检只会再次叫醒同一执行人；失败次数消耗 seat 和时间，也会让“正在工作”掩盖“需要人决策”。

### 方案对比

1. **固定最大重试次数**：简单，但不识别同一失败；不同失败共享预算，误伤可恢复故障。
2. **失败指纹 + 输入版本（推荐）**：任务记录 `failure_fingerprint`、`input_version`、`attempt_count`；相同二元组连续命中阈值后停止自动重试，写 issue system comment 并转 `blocked` 或 `in_review/awaiting_human`。
3. **只依赖人工 rerun**：最安全但把所有暂时性网络故障也推给人，失去现有自动恢复价值。

推荐 2，并把“冲突/权限/磁盘/迁移校验”等确定性类列为默认不自动重试；同一 fingerprint 的第二次命中就升级，网络/容量类仍按现有有限重试。

## 3. 迁移编号

### 现状证据

- `server/internal/migrations/migrations_lint_test.go:12-68` 已记录 468–501 的历史双编号白名单，并在 `TestMigrationNumericPrefixesAreUnique` 中拒绝新的冲突。
- 迁移 runner `server/cmd/migrate/main.go:893-1030` 以完整 filename stem 写入 `schema_migrations.version`，因此同号不同 stem 会都执行；advisory lock 只串行执行，不能解决“两个分支各自取同号”。
- 当前 `server/migrations/` 仍有历史同号文件，例如 499、500、501 各两条；这正是必须兼容而不能简单 renumber 的发布事实。

### 风险

“本地测试通过”不能证明合入后的编号唯一；同号 SQL 可能按文件名都落库，也可能给运维造成无法用一个数字定位变更的歧义。时间戳编号虽然减少碰撞，却失去顺序可读性、和现有工具/文档兼容性差。

### 方案对比

- **合入时重新编号**：最终线干净，但需要重写 down/up、文档和已存在分支，容易把已发布编号重新解释。
- **保留序号 + CI collision gate（推荐）**：新编号由合入队列/维护者分配；lint 在 PR 与 release 阶段运行，历史白名单只允许既有集合，第三个同号立即失败。
- **时间戳/UUID**：天然唯一，但需要改 runner、运维和文档，不能解决已发布历史。

推荐第二项：把现有 lint 从“测试里有”升级成 CI 必经门禁，并为 PR 模板明确“从 kun 最新 tip 分配下一个编号”。

## 4. DB 测试护栏

### 现状证据

- `server/internal/testutil/database.go:13-55` 统一了 `TEST_DATABASE_URL`、`MULTICA_REQUIRE_TEST_DB=1` 和 `SkipDatabase`；强制模式下连不上库会 `t.Fatalf`。
- `scripts/test-go.sh:42-61` 通过 `scripts/test-db.sh` 创建隔离库；`scripts/test-db.sh:143-170` 导出强制变量，并比较 `pg_stat_database.xact_commit`，整个 DB 套件零连接则失败。
- 但 `server/internal/service/main_test.go:44-96` 仍自行读取 `DATABASE_URL`，连不上直接 `t.Skip`，且有 localhost 默认值；其它 package 也存在同样的直接 skip（例如 `server/cmd/server/*_test.go`）。

### 风险

直接运行 `go test ./internal/...` 可能在没有数据库时全绿但没有执行 handler/DB 断言；不同 package 的数据库选择和 skip 语义不一致，类型错误要到真库才暴露。

### 方案对比

1. **所有测试永不 skip**：本地开发需要 Postgres，反馈快但会让无库的纯单元测试不可用。
2. **CI 强制、开发可跳过（推荐）**：所有 DB 套件统一 `OpenTestDatabase/SkipDatabase`；CI 只走 `scripts/test-go.sh`，并保留零连接检查。
3. **容器化每 package**：隔离最强，但启动慢、重复迁移且不能替代统一入口。

推荐 2。验收必须包含：坏 migration 在 `scripts/test-go.sh` 中失败；一个故意未连接 DB 的 package 在 `MULTICA_REQUIRE_TEST_DB=1` 下失败；无库本地的纯单元测试仍可运行。

## 5. 一票多分支 / worktree

### 现状证据

- `LocalWorktreeParams` 和 branch owner（`local_worktree.go:80-150, 179-223`）已用 workspace/agent/conversation identity 防止误认同名分支；`ConversationKey` 让同一会话复用一条分支。
- 但无 conversation 的 task 仍生成 task-scoped branch；同一 issue 可以被多次 rerun、救援和并行子任务分别创建 worktree。GC 只能扫描和清理 worktree，不能决定哪条分支是交付真相。
- DENE-763 的现场出现 `agent/agent/dene-763`、多个 `dene-763-<task>` 和两条不相包含的提交线，救援必须人工逐条对账。

### 风险

PR 可能漏掉某条分支上的修复，也可能把救援分支的旧提交一起合入；任务完成后遗留 worktree 和 hidden ref 还会继续成为下一轮候选输入。

### 方案对比

- **只靠命名约定**：成本低，但无法防止并行 rerun 和人工救援产生第二条线。
- **issue 级 canonical delivery（推荐）**：首次 issue task 创建 delivery record（canonical branch + PR）；后续 rerun 默认续接该线，其他 worktree 必须显式标记 rescue/experiment；合并或关闭时生成可审计清理清单。
- **强制单 worktree 锁**：最简单地消除分叉，但牺牲并行任务和故障现场保留，不适合 worktree 模式。

推荐第二项。唯一真相源应是 issue → canonical PR，而不是某个本地目录；本地 worktree 只是执行载体，保留期和清理动作要写入任务/PR 元数据。

## 子票拆分与验收

1. **DENE-814（P0，已有）**：用户 HEAD 前进不应进入 Agent replay；增加两轮干净主目录、HEAD 前进、同文件改动三组回归测试；确定性冲突二次命中要有出口；说明旧 `refs/multica/local-state/*` 的兼容/清理策略。
2. **P1：迁移编号 collision gate**：把现有 `migrations_lint_test.go` 接入 PR/release 必经检查；新同号失败、历史 468–501 白名单稳定、up/down 配对仍受检；补一份编号分配 runbook。
3. **P1：DB 测试必须真跑**：统一所有 DB package 使用 `testutil.OpenTestDatabase`；CI 入口只用 `scripts/test-go.sh`；断言无连接和 migration/schema 错误都会失败；本地无库的非 DB 测试仍可单独运行。
4. **P2：确定性失败出口**：为 task 持久化输入版本与 failure fingerprint；同 fingerprint 连续命中达到阈值后停止自动 retry、写可操作 system comment 并转人工/blocked；网络/容量等 transient reason 保留有限重试。
5. **P2：issue canonical delivery**：设计 issue → canonical branch/PR 的记录与状态转移；rerun/rescue 的分支归属、PR 链接、合并/关闭后的 worktree/ref 清理均有可查询证据；并行 task 不得静默成为第二个交付真相。

DENE-814 应先落地；迁移 gate 与 DB 测试护栏可并行；确定性失败出口依赖失败记录字段，排在其后；canonical delivery 依赖对前面分支语义的稳定，最后实施。

## 落地状态（2026-09-24）

| 子票 | 交付 PR | 落在哪里 |
| --- | --- | --- |
| DENE-814 用户已提交前进不进 replay | #330 | `server/internal/daemon/execenv/local_worktree.go`：续跑只重放未提交改动，冲突二次命中有出口 |
| DENE-817 迁移编号撞号门禁 | #340 | `migration-lint` 接入 PR CI 与 release verify；`make migration-new` 取号见 `docs/kun/migration-numbering.md` |
| DENE-818 DB 测试必须真跑 | #345 | 所有 DB 测试统一走 `server/internal/testutil`；`MULTICA_REQUIRE_TEST_DB=1` 下跳过算失败，`scripts/test-db.sh` 通过维护库核对套件真连过库 |
| DENE-819 确定性失败 fingerprint 与人工出口 | #347 | 任务失败记录持久化 fingerprint，重复命中达到阈值转人工 |
| DENE-820 issue canonical delivery 与现场清理 | #353 | issue 级 canonical 交付线，合并后现场清理可审计 |

五张子票均已合入 `kun`。本评估到此收口，后续如再出现同类现场，先对照上表确认护栏是否被绕过。
