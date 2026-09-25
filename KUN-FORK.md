# jeff-kunkun / multica 魔改分支

这是 [jeff-kunkun/multica](https://github.com/jeff-kunkun/multica) 的魔改主线，fork 自官方 [multica-ai/multica](https://github.com/multica-ai/multica)。

本地工作副本：`~/.agents/multica`。

## 远端

- `origin` = https://github.com/jeff-kunkun/multica（我们的 fork）
- `upstream` = https://github.com/multica-ai/multica（官方）

## 分支约定

三条分支，各自一个职责，不要互换：

| 分支 | 职责 | 谁往里写 |
| --- | --- | --- |
| `main` | 官方镜像。永远不直接提交，只 fast-forward 到 `upstream/main`。 | 只有上游同步 |
| `kun` | 魔改主线 = **测试线**。所有功能分支从这里切出、PR 打回这里。也是 GitHub 默认分支。 | 所有开发 |
| `release` | **发布线**。只接受从 `kun` 合进来的快进合并，不直接开发、不 cherry-pick。 | 只有发版 |

- 永远不要 rebase `kun`，不要 force-push `kun`、`main` 或 `release`。
- 魔改增量随时可查：`git log --oneline upstream/main..kun`。
- 发布线落后多少：`git log --oneline release..kun`。

### 为什么发布线不叫 `main`

`main` 在这个 fork 里已经被官方镜像占住了，它永远只 fast-forward 到 `upstream/main`，不能放我们的代码。把发布线塞进 `main` 会连带改默认分支、CI 判断、自建实例自动升级脚本（盯的是 `origin/kun`）以及所有「PR 打回 kun」的约定。所以发布线单独叫 `release`，语义和「main = 正式版」完全一样。

## 上游同步流程

每次上游有更新时执行：

1. `git fetch upstream --prune && git fetch origin --prune`
2. 看有什么新东西：`git log --oneline kun..upstream/main`。为空则「上游无更新」，结束。
3. 先更新镜像：`git checkout main && git merge --ff-only upstream/main && git push origin main`
4. 冲突预检（不碰 kun）：`git checkout -b sync/upstream-$(date +%Y%m%d) kun && git merge --no-ff upstream/main`
   - 有冲突：`git diff --name-only --diff-filter=U` 列出冲突文件，`git merge --abort`，检查冲突是否能按既有代码与项目约定可靠解决；涉及尚未确定的产品选择时再交给人决策。
   - 无冲突：跑构建和测试（Go 与前端各跑一次，命令以仓库 README / Makefile 为准）。
5. 推送同步分支并开 PR：`git push -u origin sync/upstream-<日期>`，`gh pr create --base kun --title "sync: upstream main <日期>"`。Reviewer 通过后直接合并；没有 Reviewer 结论时，Agent 完成按风险自检并认为可接受也可合并，不等待本人再次确认。

### 已提前照搬到 `kun`、上游还没合并的 PR

官方 PR 还开着时我们就先照搬了，上游随后合并会在同一处产生 diff，同步时按这里处理：

- [upstream #8385](https://github.com/multica-ai/multica/pull/8385)（Antigravity 工具事件实时转发，DENE-723）：`server/pkg/agent/antigravity.go` 与官方逐行一致，不会冲突；`server/pkg/agent/antigravity_tools_test.go` 是 add/add 冲突点，取官方版本（官方那个 POSIX sh 版 live fixture 在 macOS 和 Linux 上都成立）。上游合并后删掉这一条。

## DeepSeek Harness

官方已支持 `dsh` 运行时，但桥接包还没上公共 npm（[upstream #6936](https://github.com/multica-ai/multica/issues/6936)）。自托管请用本 fork 的一键脚本：

```bash
bash scripts/setup-dsh-runtime.sh
```

模型能不能读图，由 `~/.dsh/settings.yaml` 的声明决定，而且 pi-ai 路由的键是 `input` 不是 `inputModalities`。用探测脚本实测，不要手写：

```bash
node scripts/dsh-vision-probe.mjs --apply
```

说明、已知坑、给上游的反馈建议见 [docs/kun/dsh-runtime.md](docs/kun/dsh-runtime.md)。

## Desktop 发版：双通道

打包发布给真机用的 Desktop 版本，走 [docs/kun/desktop-release.md](docs/kun/desktop-release.md)：版本号由 tag 推导、产物没推上 Release 就等于没发。

两条发布通道，客户端装上哪条就只收哪条的包：

| 通道 | 从哪个分支打 tag | tag 形状 | 谁装 |
| --- | --- | --- | --- |
| 测试版（test） | `kun` tip | `v0.5.5-test.1` | kun 本机；愿意先吃 bug 的用户 |
| 正式版（stable） | `release` tip | `v0.5.5` | kk、zi 以及默认用户 |

测试版在 `kun` 上跑稳之后，把 `kun` 快进合进 `release` 再打正式 tag——正式版的每一行代码都必须先在测试通道上出现过。用户在「设置 → 更新」里自己选通道，切换立即生效。

开任务时要写清这活进哪条线：默认都是进 `kun`（测试线），只有发版动作才碰 `release`。

## Desktop 默认入口 = 自建实例

本 fork 的 Desktop **入口默认连自建实例**：`~/.multica/desktop.json` 不存在时用 `DEFAULT_ENTRY_RUNTIME_CONFIG`（`apps/desktop/src/shared/runtime-config.ts`，即 `https://ai.ferryway.cc`），不再回落官方云。官方云仍然可达——设置 → 服务器里切到「官方云」会**显式写一份** `desktop.json`，而不是删文件（删文件已经等于自建）。想改默认入口只改这一个常量。

## 自建实例自动跟随 `kun`

自建实例（`ai.ferryway.cc`）曾经落后 `kun` 70 个提交才被发现。现在用 systemd timer 每 15 分钟比一次 `/health` 自报的 commit 和 `origin/kun`，有漂移就重建、失败就回滚：

```bash
sudo scripts/install-selfhost-autoupdate.sh
```

装、停、查状态、手工回滚见 [docs/kun/selfhost-autoupdate.md](docs/kun/selfhost-autoupdate.md)。

## 迁移编号

并行分支各自取号会撞号（DENE-730、DENE-763），撞了运行时不报错。`make migration-lint` 是 PR CI 与 release 必经门禁，468–501 的历史双号是冻结白名单、不再扩。取号、改号与门禁细节见 [docs/kun/migration-numbering.md](docs/kun/migration-numbering.md)。

## 边界

- 不向 `multica-ai/multica` 开 PR 或 push。
- 不把任何 token、凭据、环境变量值写进提交、评论或 PR。
- 不删除远端分支、不改 GitHub 默认分支。合并 PR 按上面的审查与自检规则执行，不要求本人再次确认。
