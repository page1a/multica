# Desktop 发版手册（kun 魔改线）

发一版 Desktop 给 kk zi 真机用，要走完的全流程与红线。写这份文档的直接原因是 DENE-276：那一轮打包实际只花 7 分钟，却在用户那边表现为「一个小时没动静」，最后产物躺在本地硬盘上没有发出去。下面每条纪律都对应一次真实踩坑。

DENE-352 补上了第二类踩坑：**上传到一半断了，命令行却报成功**。本机到 `uploads.github.com` 的出站约 90–110 KB/s，230 MB 的 dmg 要 40 分钟不断线；`POST .../assets` 被 EOF 掐断时 `gh release upload` **退出码仍是 0**，失败的两个文件在 Release 上留成 `state=starter` 的僵尸资产（`gh release view` 看不见），而 `latest-mac.yml` 已经指向它们——0.4.55 用户的自动更新链路就是这样断的。结论：**发版搬到 GitHub Actions runner，并且上传后逐个资产核对**，见红线 1、2。

## 红线（违反其中任何一条，这一版等于没发）

1. **tag → build → release 走 GitHub Actions（`.github/workflows/desktop-release.yml`），不从本机上传大文件。** runner 在 GitHub 内网上传，不吃本机带宽；本机路径发不出 230 MB 的 dmg，而且失败时命令仍返回 0。
2. **上传完成不等于发布成功：必须逐个资产核对。** 判据是每个资产 `state=uploaded` 且 size 与本地一致（GitHub 报 digest 时还要比对 sha256）；只信 `gh` 的退出码等于没核对。核对命令就是 CI 用的那条 `node scripts/desktop-release-assets.mjs check`，结果要贴进票里，不许写「应该上传好了」。
3. **产物留在本地硬盘 = 没有发布。** 一次 run 必须把 tag 推送、Release 建好、下载链接贴回票里；只贴「构建成功」而没有下载入口，用户拿不到任何东西。
4. **一次 run 内跑完 tag → build → release → 评论。** run 退出时所有未完成的工作都会丢失，没有后台唤醒。不要把「等构建完再发」留到下一轮。
5. **构建基线必须是当下的 `kun` tip，且构建结束后要复核 tip 有没有前进。** DENE-276 的第一次构建出自 `b4865a77d`，落后 tip 4 个 commit，其中 `da4bc9bbc`(DENE-289) 恰好重写了导出路径 302 行——照那份产物发出去，用户重测会再次踩到同一个缺陷。
6. **（兜底路径）构建一出炉就把产物挪出 worktree。** run 结束时 worktree 会被回收，产物跟着一起没。DENE-329 连栽两次：两轮都成功构建出 DMG/ZIP，都因为 run 在上传途中终止而被清理掉，Release 上只剩 `latest-mac.yml` 和 blockmap。先 `cp` 到 `~/multica-releases/<tag>/`，再开始上传。
7. **（兜底路径）先传两个大产物，最后传 `latest-mac.yml`。** 清单先上去而产物没传完，等于对着所有已装客户端广播一个指向 404 的自动更新地址——比不发版更糟。顺序错了就先把清单删掉，传完产物再补。CI 路径已把这条顺序固定在 `desktop-release-assets.mjs upload` 里，不要手工 `gh release upload` 拼顺序。**注意**：`gh release upload` 对传给它的文件用 5 个并发 worker 上传，一条命令里排参数顺序是没用的（537 字节的 `latest-mac.yml` 必然先于 230 MB 的 dmg 落地）。脚本因此按「产物 → blockmap → 清单」分三批、每批一条 `gh release upload`，前一批失败就不发下一批。
8. **不在 Release 说明、评论、产物里写任何 token / 凭据 / 环境变量值。**
9. **版本号只由 tag 决定。** `apps/desktop/scripts/package.mjs` 从 `git describe --tags --match 'v[0-9]*'` 推导版本，与 GoReleaser 给 CLI 的 `main.version` 同源。不要手改 `apps/desktop/package.json` 的 `version`。
10. **macOS 的「原地静默安装」要求所有包用同一张证书签，但发版不以此为前提。** ad-hoc 签名的 designated requirement 锁在这一份二进制的哈希上，下一版必然对不上，Squirrel 不会替换应用——默认发版就是这种包，走下面的「自带安装包」路径：应用自己把 `.dmg` 下下来，用户只负责拖进「应用程序」。要更省事的原地安装才需要那张证书；证书只生成一次，换一张，已经装出去的客户端就再也收不到原地更新。私钥、`.p12`、密码不进仓库、不进票、不进评论。

## macOS 默认更新路径（没有证书，也不需要）

默认发版不带任何证书：CI 没有 `CSC_LINK`，macOS 包是 ad-hoc 签名。这种包不能原地替换自己，但更新链路是通的——**下载由应用完成，只有最后一步要人动手**。

应用启动 5 秒后照常检查更新。发现新版本时 `updater.ts` 不让 electron-updater 下载（它只会暂存一个 Squirrel 装不上的包），改走 `mac-installer.ts`：从打包进应用的那份 `app-update.yml`（即 electron-updater 自己读的那个 feed）推出本版本对应的 `.dmg` 直链，流式下到 `userData/installers/`，进度走和普通下载同一条渲染进程通道。下完了弹「安装包已下载 / 打开安装包 / 在访达中显示」，文案直说：打开它，把 Multica 拖进「应用程序」覆盖旧版本。

几个刻意的选择：

- **落盘位置是 `userData/installers/`，不是 `~/Downloads`。** 下载目录在新版 macOS 受 TCC 保护，下载到一半弹权限框正是这条路径要避免的打断。界面直接给绝对路径，并提供「在访达中显示」。
- **每次只留当前这一份 `.dmg`。** 一版 220 MB，不清理就按用户跳过的版本数线性堆积。
- **`.dmg` 直链是从 feed 推的，不是写死 GitHub。** 文件名跟 `electron-builder.yml` 的 `mac.artifactName` 一致——改了那边没改 `installerFileName()`，每次自动下载都会 404。provider 推不出资产地址时返回 null，界面退回「打开 Release 页面」，绝不猜一个 URL 去下。
- **手动拖进「应用程序」不走 designated requirement 校验。** 那道同源校验是 Squirrel 在静默替换时做的；用户自己替换文件是系统认可的显式操作，所以这条路径对签名没有任何要求。

Gatekeeper 仍然会拦第一次启动（包没有公证）。放行一次即可：系统设置 → 隐私与安全性 → 仍要打开，或 `xattr -dr com.apple.quarantine /Applications/Multica.app`。

### 实测（2026-09-23）

在本机用 `CSC_IDENTITY_AUTO_DISCOVERY=false -c.mac.identity=null` 打了两个 ad-hoc 包（复现 CI 上没有任何证书的情形），`codesign -dv` 两边都是 `Signature=adhoc`、`TeamIdentifier=not set`：`0.5.90-test.3` 与 `0.5.90-test.4`。把 test.4 的 `latest-mac.yml`、`.dmg`、`.zip` 放到一个本机 generic feed（`http://127.0.0.1:8765`），把 test.3 的 `app-update.yml` 指过去后重新 ad-hoc 签名，用独立 `--user-data-dir` 启动 test.3。

日志按顺序记下这三条：`[updater] in-place install unavailable (mac-unsigned); downloading the installer for a manual drag into Applications` → `Found version 0.5.90-test.4` → `[updater] installer ready for manual install: .../installers/multica-desktop-0.5.90-test.4-mac-arm64.dmg`。落盘的 `.dmg` 是 237,908,431 字节，sha512 与 feed 上的那份一致。界面右下角出现「Installer downloaded / Show in Finder / Open installer」。

把这份下载来的 `.dmg` `hdiutil attach` 后，卷里是标准的 `Multica.app` + `Applications` 快捷方式；拷出来的 app `CFBundleShortVersionString` 是 `0.5.90-test.4`。即：没有任何证书，应用自己完成了下载，用户拖一次就升到新版本。

本机没有屏幕录制权限，`screencapture` 取不到画面；截图是通过 Electron 的远程调试端口用 CDP `Page.captureScreenshot` 抓的，画的是应用窗口本身。

## （可选）macOS 自签证书：换取原地静默安装

有了上面的默认路径，这一节是**可选优化**，不是发版前提：它把「下载 + 拖一次」变成「下载完重启就装好」。owner 不使用 Apple Developer ID。自动更新靠一张长期自签的 Code Signing 证书：本机生成一次，导出 `.p12`，由 owner 自己写进仓库 secret，GitHub Actions 用同一张签每一个 mac 包。只在自己电脑上签、CI 仍打 ad-hoc，包和包之间还是对不上。

没有 `CSC_LINK` 时，workflow 保持 ad-hoc 行为，缺 secret 不会让这条流水线变红，用户拿到的是上面那条自带安装包的更新路径。Windows / Linux 不读这张证书，继续不签名。

有 `CSC_LINK` 时，macOS job 先跑 `scripts/macos-ci-keychain.sh`：把 `.p12` 导入一把临时钥匙串，并只在这把钥匙串里把证书标成代码签名可信任。electron-builder 用 `security find-identity -v` 挑证书，不信任的自签证书会被跳过，然后 arm64 悄悄退回 ad-hoc。脚本跑完会清掉 `CSC_LINK`，改把 `CSC_KEYCHAIN` 交给 electron-builder，避免它再导入一把没有信任的钥匙串。这把信任只活在这次构建里，不会打进安装包。证书不可用时 `forceCodeSigning` 让 job 变红，而不是发出一个签坏的包。

公证（notarization）和 Hardened Runtime 在没有 `APPLE_TEAM_ID` 时关闭。自签证书过不了公证；Hardened Runtime 配自签证书，应用启动就会被系统杀掉。

### 生成（owner 自己跑，agent 不碰私钥）

钥匙串助理也能建这张证书：钥匙串访问 → 证书助理 → 创建证书 → 名称 `Multica Kun Self-Signed`，身份类型「自签名根证书」，证书类型「代码签名」，覆盖默认值把有效期改成 3650 天。然后选中证书导出 `.p12`。下面这条脚本做同一件事，并且顺便写出 CI 要的 base64，避免手改有效期或用途时漏掉。

```bash
scripts/macos-self-sign-cert.sh
```

默认写到 `~/Library/Application Support/multica-signing/`（仓库外面，权限 0700）。已经有私钥时脚本拒绝覆盖：换钥匙等于把已安装的客户端锁死在旧版本上。

脚本结束会打印这两条，由 owner 自己执行。密码只在 `gh` 的提示里输入。

```bash
gh secret set CSC_LINK --repo jeff-kunkun/multica \
  < "$HOME/Library/Application Support/multica-signing/csc-link.b64"
gh secret set CSC_KEY_PASSWORD --repo jeff-kunkun/multica
```

`CSC_LINK` 是 `.p12` 的单行 base64。`CSC_KEY_PASSWORD` 是导出时设的密码。两个都不要回贴到票上。

### 装过一次的 Mac 要做的事

包没有公证。从浏览器或 GitHub 下载后，Gatekeeper 会拦（「未识别的开发者」或「已损坏」）。放行一次即可：

- 系统设置 → 隐私与安全性 → 仍要打开
- 或 `xattr -dr com.apple.quarantine /Applications/Multica.app`

Squirrel 在安装更新前会用当前应用的 designated requirement 校验新包。2026-09-23 的实测里，证书不在登录钥匙串的信任设置中，`SecStaticCodeCheckValidityWithErrors` 仍然返回成功，退出后新版本装上了。安装端不用把证书设成「代码签名 / 始终信任」。

### 实测（自签证书路径）

2026-09-23，同一张自签证书（CN `Multica Kun Self-Signed`，没有放进登录钥匙串）打出 `0.5.90-test.1-dirty` 和 `0.5.90-test.2-dirty`。两边的 designated requirement 都是 `identifier "ai.multica.desktop" and certificate leaf = H"16ff7c1a8849390b6b77015b617166300fa7016e"`，不是 `cdhash`。

用 Squirrel 那组参数（`kSecCSCheckAllArchitectures`，加上正在运行的包的 designated requirement）去校验新包，返回 0，没有 `errSecCSSignatureUntrusted`。

把第一版放到实验室目录里启动（没有替换 `/Applications/Multica.app`）。它向本机 feed 拉第二版，electron-log 记下 216 MB 的 zip 下载完成，以及 `nativeUpdater.update-downloaded`。退出后实验室里的版本变成 `0.5.90-test.2-dirty`，签名仍是同一张证书。正在使用的 `/Applications/Multica.app` 保持 0.5.4。

本机磁盘上的 feed 大约 0.3 秒传完，没有逐条进度日志，当前环境也没有屏幕录制权限，所以没有截到进度条。下载完成和版本变化由日志和安装后的版本号证明。

## 主路径：CI 发版（默认走这条）

```bash
# 1. 确认要发的 commit 已经在 kun（CI 从 tag 指向的 commit 构建）
git fetch origin --prune && git log --oneline origin/kun -1

# 2. 打 tag 并推送：上一版 patch +1
git tag v0.4.58 && git push origin v0.4.58
```

推送 tag 后 `.github/workflows/desktop-release.yml` 会自动：

1. 校验 tag 形状（`vX.Y.Z` / `vX.Y.Z-suffix`，拒绝 `-dirty`）；
2. 三个平台并行打包。macOS 在 `CSC_LINK` 有值时用那张自签证书签，没有 secret 时退回 ad-hoc；Windows 与 Linux 保持不签名。`package.mjs` 在没有 `APPLE_TEAM_ID` 时关掉公证和 Hardened Runtime；
3. 用 `desktop-release-assets.mjs clean` 清掉旧僵尸资产；
4. Release 不存在时用 `gh release create --verify-tag --generate-notes` 建好（说明里写明自签或 ad-hoc，以及首次打开怎么放行）；
5. 用 `desktop-release-assets.mjs upload` 分三批上传全部 5 个资产，**`latest-mac.yml` 单独一批、最后传**（即红线 7）；
6. 用 `desktop-release-assets.mjs check --attempts 6` 逐个核对 `state=uploaded` + size + sha256，不通过就红。

`--publish never` 是故意的：产物上传交给上面的脚本，才能在上传后核对；electron-builder 在 `never` 下照样写出 `latest-mac.yml` 与两个 `.blockmap`，只是不自己传。macOS 之外的 Desktop 目标仍由 `release.yml` 的 `desktop` job 负责。

**为什么没有并进 `release.yml`**（DENE-352 的评估结论）：`release.yml` 的发布类 job 都带 `github.repository_owner == 'multica-ai'` 守卫，`desktop` 又 `needs: release`；在本 fork 上 `release` 被跳过，`desktop` 随之被跳过，tag 推上去也永远不会构建。它的 `verify` job 还要跑整套 Go 测试 + `govulncheck`，桌面安装包没必要被那条流水线阻塞。所以 Desktop 发版独立成一条 workflow。

## 回填：给已有 tag 补资产

Release 已经有 tag、但资产残缺（典型：只剩两个 blockmap 与 `latest-mac.yml`）时，不用重新打版本号，直接回填：

```bash
gh workflow run desktop-release.yml --repo jeff-kunkun/multica --ref kun -f tag=v0.4.57
```

workflow 会 checkout **那个 tag 指向的 commit**（不是 `kun` tip），重新构建，并把 5 个资产全部 `--clobber` 覆盖。**必须整组重传**：`latest-mac.yml` 里写的是本次构建产物的 sha512，只补 dmg/zip 而留用旧 yml，客户端校验会对不上。

回填成功后记得 `gh release edit <tag> --notes-file <file>` 把说明改成如实版本——资产齐全了就不该再写着「资产不完整」。

## 僵尸资产（`state=starter`）

失败的半截上传会留下 `state=starter` 的资产：`gh release view` 看不到它们，但它们会挡住同名重传。查与清：

```bash
# 查（能看到 starter）
node scripts/desktop-release-assets.mjs check --tag v0.4.57 --repo jeff-kunkun/multica --dist <本地 dist> --size-only
# 清（只删非 uploaded 状态的资产，可先加 --dry-run 看它准备删什么）
node scripts/desktop-release-assets.mjs clean --tag v0.4.57 --repo jeff-kunkun/multica
```

`clean` 不会碰已 `uploaded` 的资产，也不会动别的 Release。

## 本机兜底构建（只在 CI 跑不起来时用）

用本条时，红线 6、7 与红线 2 的核对一条都不能省。

### 时间预期（先说，别让用户干等）

| 阶段 | 冷启动 | 复用缓存 |
| --- | --- | --- |
| `pnpm install --frozen-lockfile`（11 个 workspace 包，store 全冷） | ~70 分钟 | 数分钟 |
| `pnpm --filter @multica/desktop package` 实际打包 | ~7 分钟 | ~6 分钟 |
| 上传 DMG + ZIP 到 GitHub Release（约 450 MB） | 40–60 分钟 | 40–60 分钟 |

上传不会因为缓存变快：走代理到 uploads.github.com 实测单连接只有 ~110 KB/s，两个大产物并行推才勉强到 ~200 KB/s。并行推两个文件比串行快近一倍，**一定要并行**。`gh release upload` 卡住时不会报错也不会有进度，用 `nettop -P -p <curl-pid> -l 1 -J bytes_out` 看真实字节数，别靠感觉判断它是慢还是死了。

绝大部分等待时间在装依赖，不是构建。**开工第一条评论就要把这个预期说出去**，否则用户看到的就是「打包了一个小时没打包好」。

不要为了「干净」对已有 checkout 用 `multica repo checkout --fresh`：那会丢掉 pnpm store 与 Electron/Go 缓存，把 6 分钟的活变回 70 分钟。

### 沙箱与缓存（macOS，已验证可绕过，不需要额外权限）

- Electron / Go 的默认缓存目录在工作区沙箱外，会被拒写。把缓存目录指到**工作区内**再构建，后续发版沿用同一套路径即可复用。
- `hdiutil` 建 DMG 会被 workspace-write 沙箱拒绝，需要一次提权构建。
- 这两点都不需要改 harness 配置或加 runtime 权限。

### 本机流程

```bash
# 1. 对齐基线
git fetch origin --prune
git checkout kun && git merge --ff-only origin/kun
git rev-parse --short HEAD          # 记下这个 sha，它要进评论

# 2. 依赖（冷装很慢，见上表）
pnpm install --frozen-lockfile

# 3. 打 tag：上一版 patch +1
git tag v0.4.58 && git push origin v0.4.58

# 4. 构建。有自签证书时走和 CI 相同的变量，不要再设
#    CSC_IDENTITY_AUTO_DISCOVERY=false，否则会退回 ad-hoc。
#    没有证书时才用下面这一行。
export CSC_LINK="$HOME/Library/Application Support/multica-signing/cert.p12"
export CSC_KEY_PASSWORD='(导出 p12 时的密码)'
unset CSC_IDENTITY_AUTO_DISCOVERY
pnpm --filter @multica/desktop package -- --mac --arm64
# 无证书的兜底：
# CSC_IDENTITY_AUTO_DISCOVERY=false pnpm --filter @multica/desktop package -- --mac --arm64

# 5. 产物挪出 worktree（红线 6）
mkdir -p ~/multica-releases/v0.4.58
cp apps/desktop/dist/* ~/multica-releases/v0.4.58/

# 6. 建 Release，清僵尸，整组上传（顺序由脚本保证，红线 7）
gh release create v0.4.58 --repo jeff-kunkun/multica --verify-tag \
  --title "Multica Desktop v0.4.58 (kun fork)" --notes-file <notes>
node scripts/desktop-release-assets.mjs clean  --tag v0.4.58 --repo jeff-kunkun/multica
node scripts/desktop-release-assets.mjs upload --tag v0.4.58 --repo jeff-kunkun/multica \
  --dist ~/multica-releases/v0.4.58

# 7. 上传后自检（红线 2）——不过就是没发出去
node scripts/desktop-release-assets.mjs check --tag v0.4.58 --repo jeff-kunkun/multica \
  --dist ~/multica-releases/v0.4.58
```

`latest-mac.yml` 与两个 `.blockmap` 不是可选项——缺了自动更新链路就是断的。mac arm64 一版共 **5 个资产**。

## 发布后自检（每条都要贴进票里，而不是只说"已发布"）

```bash
# 0. 资产核对：5 个资产全部 state=uploaded，size（+sha256）与本地一致
node scripts/desktop-release-assets.mjs check --tag v0.4.58 --repo jeff-kunkun/multica \
  --dist <本地 dist>

# Release 不是 draft，tag 真的指向构建用的那个 commit
gh release view v0.4.58 --repo jeff-kunkun/multica --json isDraft,tagName,publishedAt
git ls-remote origin refs/tags/v0.4.58

# 本版声称修的 commit 确实在里面
git merge-base --is-ancestor <fix-sha> v0.4.58; echo exit=$?   # 期望 0

# 没有回退上一版的任何功能
git merge-base --is-ancestor v0.4.57 v0.4.58; echo exit=$?     # 期望 0

# 产物级证据：验装出来的 app，不是看源码
#（内置 CLI 随 asarUnpack 落在 Contents/Resources/app.asar.unpacked/resources/bin/ 下，
#  路径随打包配置变，别硬编码，find 一下）
CLI=$(find /Applications/Multica.app -type f -name multica -perm -u+x | head -1)
"$CLI" --version                    # 版本 + commit 应与上面一致
plutil -extract CFBundleShortVersionString raw /Applications/Multica.app/Contents/Info.plist
```

再加一条**产物级**（不是源码级）的证据：在打包出来的内置 CLI 二进制里 grep 本次修复引入的标记字串，证明修复真的进了包。例：DENE-274 的 `read_api_missing`、`resolve source workspace: %w`；DENE-289 的 `list_has_more`、`list_shape_unknown`。

自动更新链路是断是通，只看一件事：`latest-mac.yml` 里列出的文件名，是否在同一个 Release 上全部 `state=uploaded`。第 0 条命令判的就是这个（它内部比对本地产物与 Release 资产的 size 与 sha256）。

## 交付评论该写什么

顺序固定，用户只读前两屏：

1. **下载入口**（DMG / ZIP 直链），并说明是否 draft；
2. **本版修了什么**：一行一个 commit sha + 票号 + 一句人话；
3. **产物包含修复的证据**：上面自检的实际输出，尤其是资产核对那一屏；
4. **用户要重跑哪一步**：编号步骤，写清判定标准（例：「先看包体积，还是 KB 级就别导入，直接回我」）；
5. **未决风险 / 边界**：本版没改什么、哪些算待人工测试。

Gatekeeper 提示要写进步骤里：本包未公证。有 `CSC_LINK` 时是自签证书，没有时是 ad-hoc。首次打开需在「系统设置 → 隐私与安全性」放行，或 `xattr -dr com.apple.quarantine /Applications/Multica.app`。实测确认自动更新不要求把证书设为「始终信任」。

macOS 用户升级方式也要写清，按本版怎么签的分两种：ad-hoc 包由应用自己下 `.dmg`，弹窗提示后打开它、把 Multica 拖进「应用程序」覆盖旧版本；自签证书包下完重启即装好。
