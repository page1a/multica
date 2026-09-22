# 对齐能力内置：能力与策略两条轴、按入参组装、mono 只留指针（DENE-513 / 设计）

本页是 DENE-513 的落地记录，属于 DENE-512 的阶段 1。它**改**生产代码：把
`wayfinder`、`grill`、`grilling`、`grill-frontend-look` 四个方法内置进 Multica 的
对齐运行时，由创建入参按会话选择并组装进载体 prompt；同时在 `kun-agent-mono`
把四份 skill 定义的正文换成重定向指针。

核对基线：`origin/kun` @ `61affee412`（2026-09-17）。

## 0. 一页结论

1. **策略与能力是两条正交的轴，不是一个维度的两档。** 策略回答「怎么问」（引导采访
   / 平铺对话 / 看屏一轮），一场对齐跑且只跑一条；能力回答「可以动用哪些方法」，
   一场对齐跑其中任意子集。两者各自有版本、各自记录在 `issue_draft` 行上。
2. **组装发生在创建时，入参是勾选集合。** `POST /api/issue-drafts` 新增
   `capabilities`；缺省 = 内置默认集，显式空数组 = 一个都不要（勾选框全空的
   状态）。未知 key 直接 400，不静默丢弃。
3. **`grill-frontend-look` 的方法正文从 `frontend` 策略里搬进能力。** 之前它住在
   策略正文里，现在方法只有一份；`frontend` 策略用 `Requires` 声明它，所以无论
   勾选框怎么点，看屏一轮都带着自己的方法。`frontend` 因此 1 → 2。
4. **审计记录是三元组，不是二元组。** prompt = 契约 + 能力片段 + 策略正文，
   `policy_version` 只钉住两头，所以 `issue_draft` 同时记录
   `capability_keys` 与 `capability_version`。
5. **载体就是唯一读者，所以方法重写而不是照搬。** 原 skill 的收成物落在仓库路径
   （`docs/design/prototypes/<screen>.html`、`INTERACTION.md`），对齐载体没有项目
   也没有仓库；wayfinder 的 tracker adapter、grilling 的 `/domain-modeling` 落盘
   同样够不着。内置的是**判据与考法**，落点是 draft 与回复附件。
6. **mono 侧正文搬走，名字留下**：四份 `SKILL.md` 留在 `skills/_shared/` 原目录，正文
   换成重定向指针。名字的回收（移入 `skills/_retired/`、收 `skill-map.html` 与角色绑定）
   要先把线上 Agent 的角色绑定改掉，是独立的一步——理由与那一步的四条动作见 §5。

## 1. 为什么是「能力」，不是「第四个策略」

`issue_draft_policy.go` 的注册表已经把「一场对齐一条记录」写死成审计契约：`policy_key`
/ `policy_version` 是覆盖写，切一次就只剩最后一条。四份方法里有三份**不是**「怎么问」：
wayfinder 是画决策地图，grilling 是分轮纪律，grill-frontend-look 是看屏考法。把它们做成
策略键意味着：

- 想同时「按决策树盘问」和「先看一屏」时无解——两个键只能有一个生效；
- 每加一个方法都要新增一档，用户在策略药丸里看到的是四选一，而不是「问法」的选择。

能力轴让二者相乘：`question` + `[wayfinder, grill-frontend-look]` 是合法且可表达的组合。

## 2. 契约

### 2.1 请求

```jsonc
POST /api/issue-drafts
{
  "runtime_id": "...",
  "policy": "question",              // 省略 = guided 默认
  "capabilities": ["wayfinder", "grill", "grill-frontend-look"]  // 省略 = 内置默认集
}
```

三条解析规则：

| 入参 | 含义 |
| --- | --- |
| 字段缺席（`nil`） | 服务端内置默认集：三个可勾选项，`grill` 顺带拉入 `grilling` |
| `[]` | 一个都不要——勾选框全空时前端就该发这个，而不是省略字段 |
| 含未知 key | 400，`capabilities must be one of: …` |

`Requires` 在解析期展开一次：`grill` → `grilling`。记录进 `issue_draft` 的是**展开后的
并集**（含策略自身的 `Requires`），不是请求原文。

### 2.2 响应

`issueDraftResponse.capabilities = { keys: string[], version: string }`。`keys` 是**行上
记录的原值**，不是本端认识的键的交集——某次构建退役掉的 key 依然照实回报，客户端自己
求交集。这与 `policy` 对未知 key 的处理是同一套写法。

### 2.3 组装顺序

```
issueDraftContract        // 线格式与永不弯折的规则
issueDraftCapabilityPreamble + 各能力片段（注册表顺序，非请求顺序）
policy.Behaviour          // 这一轮的任务，放最后
```

片段顺序取注册表而非请求，否则同一组勾选换个点击顺序就得到不同 prompt，记录也就
不再能唯一还原文本。前言里写死一句「方法与本轮任务冲突时以任务为准」，避免方法与
策略在节奏上互相打架（例如 `conversation` 明令不面试，而 `grilling` 要盘问）。

### 2.4 记录

迁移 `505_issue_draft_capabilities`：

```sql
ALTER TABLE issue_draft
    ADD COLUMN IF NOT EXISTS capability_keys TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS capability_version TEXT NOT NULL DEFAULT '1';
```

两列而不是一列的理由：同一份 `policy_version` 会被选了不同能力的草稿记录，它钉不住
能力片段的文本。老行回填成空集 + 惰性版本号——它们跑的时候还没有这套库。

`PATCH /api/issue-drafts/{id}/policy` 现在同时重写这两列：切换策略会把行上已记录的能力
并上新策略的 `Requires`，绝不替换——把引导调低不是让人丢掉自己勾的方法。行上若有本
次构建已不认识的 key，组装时丢弃、记录保留（审计轨迹不能改）。

## 3. 四个方法重写成什么

| 能力 | 原 skill 的落点 | 对齐里的落点 |
| --- | --- | --- |
| `wayfinder` | tracker adapter 的 map / child / blocking / frontier | 问题组本身就是地图：父单是 destination，子单是能精确陈述的决策票，`stage` 就是 blocking 顺序；fog 与出界方向写进父单 `description` 的 `## 决策地图` / `## Decision map` 段 |
| `grill` | 单会话内开一场盘问 | 一句话开场，方法在 `grilling`；依赖还看不清时它不是 grill |
| `grilling` | 每轮问完整个 frontier，事实自己查 | frontier 是载体的工作状态、不是问卷（本介质一轮一问）；载体没有文件系统，查不到的事实一律变成问题，绝不变成草稿里的静默假设 |
| `grill-frontend-look` | `docs/design/prototypes/<screen>.html` + `INTERACTION.md` 收线 | 回复附件里那一个自包含 HTML；「收成一条线」不接（它硬依赖仓库里的 `INTERACTION.md`） |

四段正文一律以英文写，与既有契约、策略正文同语言。

## 4. 版本与回滚

- `issueDraftCapabilityVersion = "1"`：任何片段改动都要 bump，语义与策略版本一致——
  「事后指得出这场对齐跑的是哪份文本」。
- `frontend` 策略 1 → 2：它自己的正文变了（不再复述方法，改为声明 `Requires`）。
  `question` / `conversation` 停在 4 / 3：它们的两半都没动，能力片段由
  `capability_version` 单独钉住。
- 回滚 = 还原常量 + 迁移 down。记了能力 key 的草稿在旧构建上会丢掉那些片段（旧构建
  不认识这列），这正是回滚应有的样子。

## 5. mono 侧：正文搬走，名字留下

`kun-agent-mono/skills/_shared/{wayfinder,grill,grilling,grill-frontend-look}/SKILL.md`
的正文换成重定向指针：新家文件与常量名、能力 key、选择入口，以及内置版本改写了哪几处。
目录与名字留在原处——**不**移入 `skills/_retired/`，**不**从 `skill-map.html` 的
`SKILLS` 数组与龙珠小队角色绑定里摘名。

理由是这四个名字仍在被引用：skill 索引、`architect` / `builder` / `scout` 三个角色的绑定，
以及 `anti-slop-frontend`、`interaction-graph`、`implement/references/frontend-readiness.md`
的正文。直接摘名会让 `check-skill-index.py` 报 stale、`multica-skills.py drift` 报
unresolved；而按本仓 `AGENTS.md`，角色契约要「先改线上 Agent 再 export」，那是动线上配置的
一步，不该混在代码票里静默做掉。本票要的「一份正文、一个单源」已经达成，名字的回收是独立
的一步：

- 先改线上 Agent 的角色绑定（去掉四个名字），再 `scripts/export-multica-agents.py` 回流；
- `skills/skill-map.html` 的 `SKILLS` 数组删掉四个名字——`check-skill-index.py` 会拿它
  和源目录对账；
- 四个目录移入 `skills/_retired/`；
- 仍在正文里点名 `/grill-frontend-look` 的 living 文档（`docs/contexts/skill-system/
  CONTEXT.md`、`docs/contexts/work-methods/CONTEXT.md` 等）改成指向新家；ADR 与研究
  记录是历史，保留原文。

## 6. 与相邻票据的边界

- **DENE-514**：底栏复合选择器（CLI / Model / Think Level / 三个勾选框）。本页给出它
  需要的全部入参契约与前端键表（`packages/core/issue-drafts/capabilities.ts`），
  UI 不在本页。
- **能力不在会话中途切换**：指令在建会话时写死一次，中途改需要与策略切换同样的两写
  事务。目前没人要求，本页不做。
