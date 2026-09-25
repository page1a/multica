# 迁移编号分配与撞号门禁

`server/migrations/` 里的文件按 `NNN_name.up.sql` / `NNN_name.down.sql` 命名。golang-migrate 按**完整文件名**排序和记账，不按编号：两个 `518_*.up.sql` 会被当成两条独立迁移先后执行，不报错。所以「同号」在运行时是静默的，只能靠门禁在合入前拦下。

## 为什么会撞号

`kun` 上多条 agent 分支并行开发，每条都从当前最大编号 +1 取号。先合入的那条占了号，后合入的那条就和它同号（DENE-730、DENE-763 都栽在这里）。这不是谁的操作失误，是并行分支取号的必然结果，所以解决方式是「合入前必检」而不是「下次小心」。

## 门禁在哪里

| 位置 | 触发 | 命令 |
| --- | --- | --- |
| 本地 | 手动 | `make migration-lint` |
| PR CI | 改到 `server/migrations/**`、lint 测试本身、`Makefile` 或 CI 配置 | `.github/workflows/ci.yml` 的 `migration-lint` job，进 `backend` 聚合门禁 |
| release | 任何 `vX.Y.Z*` tag | `.github/workflows/release.yml` 的 `verify` job，在整套 Go 测试之前先跑 |

三处跑的是同一条命令：`cd server && go test ./internal/migrations -run '^TestMigration' -count=1`。它不需要数据库，一分钟内出结果，失败信息直接点名两个同号文件。

同一组测试也仍然包含在 `backend-tests` 的完整 Go 套件里，`migration-lint` 是把它前置并单列，不是替代。

## 检查内容

测试源文件：`server/internal/migrations/migrations_lint_test.go`。

1. **编号唯一**（`TestMigrationNumericPrefixesAreUnique`）：129 号起，每个数字前缀只允许一个 up 文件。128 及以前是上游历史遗留，不管。
2. **历史白名单冻结**（`TestMigrationKnownDuplicateWhitelistIsFrozen`）：468–501 这 34 个号，上游镜像和本 fork 各自加过一条，两条都已经在现网跑过，谁都不能改名。白名单精确到文件名：
   - 白名单里的号上出现**第三个**文件 → 失败；
   - 白名单里的一对被改名或删掉一个 → 失败；
   - 白名单本身多出 468–501 之外的号、或某号不是恰好两条 → 失败。
   所以想通过「往白名单里加一行」放过一次新撞号是做不到的，只能改号。
3. **up/down 配对**（`TestMigrationFilesHaveMatchingDirections`）：每个 stem 必须同时有 `.up.sql` 和 `.down.sql`。
4. **门禁自检**（`TestMigrationPrefixCollisionsDetectsNewCollisions`、`TestMigrationUnpairedStemsDetectsMissingDirection`）：用合成文件名喂给纯函数，证明「新撞号 / 第三个同号 / 缺 down」确实会被判失败。这两条保证门禁自身不会在某次重构里被改哑。

## 怎么取号

1. 取号与建文件是一条命令：

   ```bash
   make migration-new NAME=issue_status_icon
   ```

   它做三件事：

   - `git fetch origin kun`（取不到时打 warning，退回本地已有的 `origin/kun` ref：号可能偏旧，合入前的门禁就是这种时候的兜底）；
   - 取 `origin/kun:server/migrations` 和本地 `server/migrations` **两边**的最大编号，+1 作为你的号；
   - 建出 `server/migrations/NNN_<name>.up.sql` 和 `NNN_<name>.down.sql` 两个空文件。已存在的文件绝不覆盖；`<name>` 用全小写下划线（字母、数字、单下划线）。

   两条空文件已经成对，所以 `make migration-lint` 在写 SQL 之前就是绿的。
2. 写 up/down SQL。改到查询就跑 `make sqlc` 重新生成。
3. **合入前再核一次。** 分支开得久，`kun` 上可能已经有人用掉了你的号。PR 的 `migration-lint` job 红了、报 `share numeric prefix N`，就是这种情况。
4. 撞号了怎么改：把自己这条迁移改成新的最大号 +1（up/down 一起改名），如果 sqlc 生成代码或 Go 里引用了旧文件名，一并改。**不要**动 `kun` 上已经合入的那条，它可能已经在某个环境跑过了。
5. 这条迁移还没在任何环境跑过（只在自己 worktree 的库里跑过）就可以放心改名；本地库用 `make db-reset` 重建即可。

## 明确不做的事

- **不扩白名单。** 468–501 是一次性的上游/fork 平行历史，窗口已关。任何新的同号都要改号，没有第二个例外。
- **不改成时间戳编号。** 上游用顺序号，改成时间戳会让每次同步上游都要重命名，得不偿失。合入前的门禁已经够用。
- **不在 desktop-release 里跑。** Desktop 包只带 daemon，不带服务端迁移；服务端 schema 走 `release.yml`。
