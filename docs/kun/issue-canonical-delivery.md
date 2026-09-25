# Issue 级 canonical 交付线（DENE-820）

一张票只有一条交付真相：一条 canonical 分支及其 PR。后续 rerun 默认续接这条线；救援、实验的分支必须显式归类，不能静默成为第二条交付线；合并之后可以列出并清理非 canonical 的现场。这是 DENE-815 架构评审第 5 节的落地。

## 0. 一页结论

- 表 `issue_delivery_branch`（迁移 528）按 `(issue_id, branch_name)` 记录每条分支的角色：`canonical` / `rescue` / `experiment` / `unclassified`。部分唯一索引保证一张票同时只有一条 canonical。
- 记录是自动的：task 完成、失败或取消回执带 `branch_name` 时，第一条分支自动成为 canonical；之后出现的其它分支记为 `unclassified`，并在票上发一条系统评论要求归类。
- 续接是自动的：daemon 领任务时服务端把 `canonical_branch` 一起下发，worktree 模式下另一位执行人的 rerun 直接续接这条分支（要求同一 workspace、同一会话，且分支带 Multica 归属记录），不再各开一条 `agent/<自己>/<票>`。
- 验收 `verdict: pass` 之前会看交付线：存在 unclassified 分支、未 resolved 的 rescue、或开着的 PR 不在 canonical 分支上，票转 `blocked` 并写明 `交付线对齐：<原因>`，叫醒执行人；对齐后再推回验收。合并时优先合 canonical 分支上的 PR。
- 清理只在合并之后（canonical PR merged，或票 done / cancelled）：`multica issue delivery cleanup --apply` 按 git 合并证据删除非 canonical 的 worktree、分支和 `refs/multica/*` 状态引用，并把结果（`cleaned` / `kept`）记回服务端。

## 1. 命令

```bash
multica issue delivery <issue>                                   # 看现场：canonical、每条分支的任务/PR/清理状态、问题清单
multica issue delivery set-canonical <issue> <branch>            # 换 canonical，旧的自动降为 rescue
multica issue delivery classify <issue> <branch> --role rescue|experiment [--resolution absorbed|discarded]
multica issue delivery cleanup <issue>                           # 只列清理计划
multica issue delivery cleanup <issue> --apply [--git-root .] [--trunk origin/kun]
```

对应 API：`GET /api/issues/{id}/delivery`，`PUT …/delivery/canonical`，`POST …/delivery/classify`，`POST …/delivery/cleanup`。

## 2. 角色与归宿

| 角色 | 含义 | 何时能清理 |
| --- | --- | --- |
| `canonical` | 唯一的交付线；PR 打向它，rerun 续接它 | 永不由本机制清理 |
| `rescue` | 为救火另开的分支 | 标 `absorbed`（内容已并回 canonical）或 `discarded` 后，且票已合并 |
| `experiment` | 试验性分支，不参与交付 | 票已合并 |
| `unclassified` | 系统自动记录、还没有人归类 | 不能清理，且会阻塞验收合并 |

`--apply` 的门是 git 合并证据（ancestor 或 squash 内容匹配）：分支内容不在 trunk 里的一律 `kept` 并写原因，只有 `rescue --resolution discarded` 明确说过“丢掉”才强制删除。这样服务端的分类和仓库的真实状态互相校验，救援分支不会被遗漏也不会被误删。

## 3. 与其它协议的关系

- 收口协议（`scheduling-close-protocol.md`）不变：交付线对齐是验收合并前的守卫，不是新的 close 字段。
- worktree 回收（DENE-819 的 `execenv/worktree_cleanup.go`）处理“无人认领的目录”，本机制处理“票已经合并后，按分支归属清场”。两者都只走 git，不 `rm -rf`。
