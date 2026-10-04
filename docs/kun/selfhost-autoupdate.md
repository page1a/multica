# 自建实例自动跟随上游分支

自建实例（`ai.ferryway.cc`）跑的是本机 checkout 构建出来的镜像。2026-09-17 那次迁移失败就是因为它停在 9 月 15 日的 `a5d3d808`，落后 `kun` 70 个提交：目标端版本太旧，读不了带任务的迁移包。手工 ssh 升级后问题消失，但升级完 4 小时，`origin/kun` 又往前走了 —— 靠人记着 pull 修不了这件事。

`scripts/selfhost-autoupdate.sh` + systemd timer 就是那台机器上的「记性」：每 15 分钟比一次「正在服务的后端自报的 commit」和「`origin/kun` 的 tip」，不一样就重建、重建失败就回滚，并把结果写进一个能被机器读的状态文件。

## 装

前置条件：

- 机器上有一份完整的 checkout（`/opt/multica`），`origin` 指向本 fork，免交互 `git fetch origin kun` 可用；
- 部署方式是本机构建（`docker-compose.selfhost.yml` + `docker-compose.selfhost.build.yml`），不是 GHCR 拉取；
- systemd 可用，安装时用 root。

```bash
cd /opt/multica
sudo scripts/install-selfhost-autoupdate.sh
```

装完会：

- 把 `deploy/systemd/multica-autoupdate.{service,timer}` 按当前 checkout 路径渲染进 `/etc/systemd/system/`；
- 建好 `/var/lib/multica/`（状态文件目录）；
- `systemctl enable --now multica-autoupdate.timer`。

幂等：unit 内容没变就不重写、不重启 timer，重复执行只会打印 `unchanged:`。

常用参数：

```bash
sudo scripts/install-selfhost-autoupdate.sh --repo-dir /srv/multica --branch main
sudo scripts/install-selfhost-autoupdate.sh --no-enable        # 只写 unit，先不启用
```

## 镜像来源：本机编译或拉 CI 镜像

`/etc/multica/autoupdate.env` 里的 `MULTICA_AUTOUPDATE_IMAGE_SOURCE` 决定新版本从哪来：

- `build`（默认）—— 在本机 `docker compose build`。2 核机器上一次要 10 分钟以上，期间访问明显变慢（DENE-1182）。
- `registry` —— `.github/workflows/selfhost-images.yml` 在 `kun` 每次 push 后用 GitHub Actions 编好 amd64 镜像，推到 `ghcr.io/jeff-kunkun/multica-{backend,web}:sha-<完整 commit>`（另带一个 `:kun` 指向最新）。脚本只 `docker pull` 这两个 tag，再改名成本地的 `multica-backend:dev` / `multica-web:dev`，所以 compose 文件、`:prev` 回滚和 `/health` 校验都和编译模式一样。部署成功后会删掉拉下来的 `sha-*` 标签并清理悬空镜像。

`ai.ferryway.cc` 用的是 `registry`：

```bash
# /etc/multica/autoupdate.env
MULTICA_AUTOUPDATE_IMAGE_SOURCE=registry
# 可选，默认就是 fork 自己的命名空间；不要指向 ghcr.io/multica-ai
# MULTICA_AUTOUPDATE_REGISTRY=ghcr.io/jeff-kunkun
```

两个包在 GHCR 上是公开的，服务器拉取不需要登录。仓库公开，Actions 分钟数和公开包的存储、流量都不收费。

`kun` 刚 push 时镜像还没编完，这一轮会记成 `result=pending`、什么都不动，等下一轮再试；一般 push 后 10–20 分钟上线。一直 `pending` 就去看 Actions 里 `Self-host images` 那次运行是不是失败了。

## 确认它在跑

```bash
systemctl list-timers multica-autoupdate.timer    # NEXT/LAST 要有时间
journalctl -u multica-autoupdate.service -n 200   # 每次跑的日志
```

timer 是 `OnActiveSec=5min` + `OnUnitActiveSec=15min`：启用后 5 分钟跑第一次，之后每 15 分钟一次。这里刻意没用 `OnBootSec`——它相对开机时间算，在一台已经开了几天的机器上启用 timer 会立刻触发一次升级。

想立刻升一次（前台，日志直接看得到）：

```bash
sudo systemctl start multica-autoupdate.service
```

## 查状态

`/var/lib/multica/autoupdate.json` 每次跑完都会原子重写：

```json
{
  "last_check_at": "2026-09-17T04:10:02Z",
  "deployed_commit": "463c4ba4…",
  "target_commit": "463c4ba4…",
  "result": "updated",
  "error": "",
  "duration_seconds": 412
}
```

| 字段 | 含义 |
| --- | --- |
| `last_check_at` | 这一轮开始的时间（UTC） |
| `deployed_commit` | 跑完后 `/health` 报的 commit，也就是**真正在服务**的版本 |
| `target_commit` | 这一轮 `origin/<branch>` 的 SHA |
| `result` | `noop` / `updated` / `pending` / `rolled_back` / `failed` |
| `error` | 失败原文（构建日志尾部等），成功时为空 |
| `duration_seconds` | 这一轮耗时 |

`result` 怎么读：

- `noop` —— 没有漂移，什么都没做（不重建）。
- `pending` —— 只在 `registry` 模式出现：目标 commit 的镜像还拉不到（CI 没编完，或编失败了），什么都没动。`error` 里是 `docker pull` 的原文。
- `updated` —— 升级成功，`/health` 的 commit 已经等于目标 SHA。
- `rolled_back` —— 升级失败，但已经回滚：镜像 `:prev` 打回 `:dev`、checkout 回到旧 SHA、容器重建、健康检查重新通过。**退出码非零**，值得看一眼 `error`。
- `failed` —— 没能回到旧版本（或失败发生在动手之前，比如 `git fetch` 失败）。机器现在处于需要人看的状态。

一个必须知道的判据：**「构建了但 `/health` 没变成目标 SHA」也算失败**，会照常回滚。这条是防「构建产物没真正生效」，也是这个脚本存在的理由——不查这一条，机器和工作区可能各说各话。

## 停

```bash
sudo systemctl disable --now multica-autoupdate.timer
sudo systemctl stop multica-autoupdate.service     # 正在跑的那一次
```

停掉 timer 之后实例就停在当前版本，不再自动跟。要彻底卸掉：删掉 `/etc/systemd/system/multica-autoupdate.{service,timer}`，然后 `systemctl daemon-reload`。

## 失败了怎么手工回滚

脚本自己能回滚（`result=rolled_back`）。只有它报了 `failed`，或者你要回到更早的版本时才需要手工做：

```bash
cd /opt/multica

docker tag multica-backend:prev multica-backend:dev
docker tag multica-web:prev multica-web:dev

git log --oneline -5                     # 挑一个已知好的 SHA
git reset --hard <那个 SHA>

export VERSION=<那个 SHA> COMMIT=<那个 SHA> DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml build
docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --force-recreate backend frontend

curl -s localhost:8080/health            # commit 必须等于 <那个 SHA>
curl -s localhost:8080/readyz            # db / migrations 都要 ok
```

注意 `:prev` 是**上一次跑之前**的版本，不是「上一个好版本」；连跑几轮之后它会跟着往前走。

排查顺序：

1. `cat /var/lib/multica/autoupdate.json` 看 `result` 和 `error`；
2. `journalctl -u multica-autoupdate.service -n 200` 看失败那一步的完整输出；
3. `curl -s localhost:8080/health` 确认现在到底在跑什么；
4. 构建本身失败（`Reached heap limit` 之类）看 `Dockerfile.web` 的 `WEB_BUILD_NODE_OPTIONS`——4 GiB 机器上前端构建吃满内存是老问题；
5. checkout 里有未提交的改动时脚本会**拒绝**执行（`result=failed`，`refusing to reset --hard`），这是故意的：`git reset --hard` 会无声吃掉本地修改。清掉或 stash 之后再跑。

## 不做什么

- 不做 push 触发部署（GitHub Actions ssh 进盒子）。要把 SSH 私钥放进 GitHub secrets，是仓库授权级变更，而 15 分钟轮询已经够用。
- 不动数据库备份、不动 TLS、不改 compose 的服务拓扑。

## 相关文件

- `scripts/selfhost-autoupdate.sh` —— 主脚本
- `scripts/install-selfhost-autoupdate.sh` —— 安装器（幂等）
- `deploy/systemd/multica-autoupdate.{service,timer}` —— unit 模板
- `scripts/selfhost-autoupdate.test.sh` —— 打桩测试（noop / updated / rolled_back / 锁 / 安装器幂等）
- `SELF_HOSTING.md` → Auto-following an upstream branch
