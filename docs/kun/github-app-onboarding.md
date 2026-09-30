# 新成员接 GitHub 仓库

目标：工作区里任何人的 GitHub 仓库（个人账号或组织都行）都能接进来，PR 自动挂到对应的票上。背景见 DENE-959。

## 先弄清三件事

- **PR 是怎么进来的**：GitHub App 装在某个账号或组织上，这个安装就叫 installation。GitHub 只推送这个安装授权范围内的仓库事件；服务端收到后，发给绑定了该安装的所有工作区，再按标题、分支名里的票号（例如 `DENE-123`）挂票。
- **设置 → 连接与扩展 → 代码仓库管的是另一件事**：它决定智能体能 clone 哪些仓库，不影响 PR 能不能进来。一个仓库要「能挂 PR」又要「能被智能体选到」，两边都得配。
- **一个 GitHub 账号 = 一个安装**：kkunkunya 的仓库要装在 kkunkunya 上，jeff-kunkun 的仓库要装在 jeff-kunkun 上。同一个工作区可以同时绑多个安装，列表里是各自独立的连接。同一个安装不能再挂到第二个工作区，跳回来时会说明它已经挂在别处。以前已经挂在多个工作区上的旧数据还在，事件仍发给这些工作区（迁移 133 放宽了唯一约束，新的连接在应用层拒绝）。

## 新成员接自己的仓库（日常）

1. **找能点连接的人**。只有工作区 owner / admin 能发起连接。普通成员请工作区 owner / admin 来点。
2. **admin 点连接**。在 **设置 → 连接与扩展 → 连接** 点 **添加连接**，类型选 **GitHub App**。浏览器会打开 GitHub 的安装页。
3. **在 GitHub 上选账号并授权仓库**：
   - admin 自己就是仓库主人：在安装页选对应的账号或组织，勾 *All repositories* 或者只勾要接的仓库，然后点 Install。
   - 仓库在别人的 GitHub 账号下：把第 2 步打开的地址（`https://github.com/apps/<slug>/installations/new?state=…`）原样发给对方。对方登录自己的 GitHub 打开这个链接，完成安装后会自动跳回来，绑到这个工作区。这个链接只绑工作区，不过期，别往工作区外发。
   - 组织仓库：只有组织 owner 能直接安装。普通组织成员点完会变成一条「请求安装」，要等组织 owner 在 GitHub 上批准。
4. **回到设置页核对**。**设置 → 连接与扩展 → 连接** 里会多出这个账号的 GitHub App 连接。再打开 **代码仓库**，每个仓库一行状态：
   - **已接通**：PR 会挂票，关单时查得到。
   - **待安装 / 未接通**：这个仓库所属账号还没装 App，或者装了但没勾这个仓库。要么连上它所属的账号，要么去 GitHub 上把它加进已有的安装（GitHub → Settings → Applications → 这个 App → Configure → Repository access）。
   - 被覆盖但没登记的仓库：PR 照常挂票，但智能体选不到它。需要的话在 **代码仓库** 里登记。
5. **验证**。在仓库里开一个标题带票号的 PR（例如 `DENE-123 修一下登录`），几秒后 `multica issue pull-requests DENE-123` 能看到它。

补充授权范围（给已有安装加仓库）不用在 Multica 里再点连接，在 GitHub 上改完就生效。服务端不存仓库清单，事件里带着哪个安装就按哪个安装分发。

## 存量 PR 补挂

App 只收接通之后的事件，接通前开的 PR 不会自己出现。要补挂的话，在 GitHub 上把 PR 标题随便改一个字再改回来（或者直接保存一次标题），这会触发一次 `edited` 事件，PR 就挂上了。

## 服务器侧（一台实例做一次）

App 用 GitHub 的 manifest 流程创建，字段和以前一样：

- 可安装范围：**Any account**（`public: true`）。选 "Only on this account" 的话，别的 GitHub 账号都装不上。
- Setup URL `{实例}/api/github/setup`，打开 Redirect on update；Webhook URL `{实例}/api/webhooks/github`。
- 权限全部只读：Metadata、Contents、Pull requests、Checks、Commit statuses。订阅的事件：Pull request、Check suite、Check run、Status。

有两条路，走一条就够。环境变量里已经配过 App 时，以环境变量为准，设置页只显示「由服务器配置，只读」，不会被界面覆盖。

### 设置页（默认）

工作区所有者打开 **设置 → 连接与扩展 → 连接**。还没有 App 时，这里直接显示「先创建 GitHub App」，可选填组织名。点下去之后浏览器把清单提交到 GitHub，人输入一次密码并创建。GitHub 带着一次性 `code` 回到服务端（`/api/github/app/callback`），服务端换成 App 身份、加密存库、马上生效，然后打开安装页。接着按「新成员接自己的仓库」选账号和仓库即可。

不是所有者的人看到的是：需要工作区所有者来开通。

智能体不打开浏览器时，用 `multica github-app status` 看现在是未配置、环境变量还是设置页创建的，用 `multica github-app setup-link`（可加 `--org`）拿到一个链接。人用已登录 GitHub 的浏览器打开这个链接，完成那一次密码确认。链接只能打开一次、10 分钟内有效，过期或重复打开会显示错误页，重新生成即可。桌面 App 里点设置页的「先创建 GitHub App」也是用系统浏览器打开同一种链接（桌面窗口不允许直接跳到 GitHub）。两条命令都支持 `--output json`。

### 没有界面时

`scripts/selfhost-github-app.sh` 仍是兜底：人自己向 `https://github.com/settings/apps/new`（或组织的 `/organizations/<org>/settings/apps/new`）POST 同一份清单，`redirect_url` 指回 **设置 → 连接**（`/{工作区}/settings?tab=git-connections`，不要再用 `/settings?tab=github`）。地址栏里的 `code` 交给脚本。脚本把它写进服务器 `.env` 并重建 backend。这条路的密钥在环境变量里，优先级高于设置页存的那一份。

App 的拥有者账号不影响谁能安装（范围是 Any account）；以后要换拥有者，可以在 App 设置里 Transfer ownership。

## 排查

| 现象 | 先查 |
| --- | --- |
| 连接页让所有者先创建 GitHub App | 这台实例还没有 App。所有者点创建；其他人要等所有者开通 |
| 显示「由服务器配置，只读」 | `.env` 里已经有 App。改配置要改环境变量并重建 backend，设置页不能覆盖 |
| 添加连接里不能浏览仓库 | `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY`（或设置页存下的同一对）没配齐 |
| 跳回后说这个安装已经挂在另一个工作区 | 同一个 GitHub 安装不能同时挂两个工作区。用另一个账号或组织再装一次 |
| 代码仓库里某账号的仓库全是「未接通」 | 这个安装在 GitHub 上被卸载或暂停了，重新连接 |
| PR 没挂上 | 先看代码仓库那一行的状态；再看标题 / 分支名里的票号前缀是不是本工作区的；最后到 GitHub App → Advanced → Recent Deliveries 看投递是不是 2xx |
| 投递返回 401 | 服务器上的 webhook secret 和 App 里的不一致 |
