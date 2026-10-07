// DENE-1341 有数据走查的造数脚本：长中文标题、长智能体名、多子票、长评论。
// 用法（仓库根目录，make up 之后）：
//   set -a; . ./.env.worktree; set +a; node docs/evidence/DENE-1341/tools/seed.mjs
// 输出 JSON（slug、各对象 id）给 capture.mjs 用。
import pg from "pg";
import fs from "node:fs";

const API = `http://localhost:${process.env.PORT}`;
const db = new pg.Client(process.env.DATABASE_URL);
await db.connect();

const EMAIL = "dev@localhost";
let token = "";
async function call(method, path, body, ws) {
  const res = await fetch(API + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(ws ? { "X-Workspace-Slug": ws.slug, "X-Workspace-ID": ws.id } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path} -> ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

await call("POST", "/auth/send-code", { email: EMAIL });
const auth = await call("POST", "/auth/verify-code", { email: EMAIL, code: "888888" });
token = auth.token;
await call("PATCH", "/api/me", { name: "孔昆昆（产品负责人 · 移动端验收）" });
await db.query(
  `UPDATE "user" SET onboarded_at = COALESCE(onboarded_at, now()),
     onboarding_questionnaire = COALESCE(onboarding_questionnaire,'{}'::jsonb) || '{"source":["friends_colleagues"],"source_skipped":false}'::jsonb
   WHERE email=$1`,
  [EMAIL],
);
let ws = (await call("GET", "/api/workspaces")).find((w) => w.slug === "dev");
if (!ws) ws = await call("POST", "/api/workspaces", { name: "开发工作区 · 手机端走查专用的超长工作区名字", slug: "dev" });
const userId = (await db.query(`SELECT id FROM "user" WHERE email=$1`, [EMAIL])).rows[0].id;

// 运行时与智能体：服务端只接受 daemon 注册运行时，这里直接写库。
const rt = (
  await db.query(
    `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, owner_id)
     VALUES ($1,'kunkun-MacBook-Pro-M4-Max.local（Claude Code 主力机）','local','claude','online',$2) RETURNING id`,
    [ws.id, userId],
  )
).rows[0].id;
const rt2 = (
  await db.query(
    `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, owner_id)
     VALUES ($1,'omarchy-linux-gpu-server（Codex 备用）','local','codex','offline',$2) RETURNING id`,
    [ws.id, userId],
  )
).rows[0].id;
// 额度快照：底部额度条只在运行时上报了 plan_limits 时出现。第三台 Gemini
// 机器让额度条在 390px 下放不下一行。capture.mjs 每次开跑会刷新 observed_at。
const rt3 = (
  await db.query(
    `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, owner_id)
     VALUES ($1,'nexus-gpu-workstation（Gemini CLI 试用）','local','gemini','online',$2) RETURNING id`,
    [ws.id, userId],
  )
).rows[0].id;
const reset = (h) => Math.floor(Date.now() / 1000) + h * 3600;
const limits = {
  [rt]: { provider: "claude", status: "available", windows: [{ name: "five_hour", used_percent: 63, window_minutes: 300, resets_at: reset(3) }, { name: "seven_day", used_percent: 41, window_minutes: 10080, resets_at: reset(96) }] },
  [rt2]: { provider: "codex", status: "exhausted", windows: [{ name: "five_hour", used_percent: 100, window_minutes: 300, resets_at: reset(2) }, { name: "seven_day", used_percent: 88, window_minutes: 10080, resets_at: reset(50) }] },
  [rt3]: { provider: "gemini", status: "available", windows: [{ name: "gemini_pro", used_percent: 22, resets_at: reset(20) }, { name: "gemini_flash", used_percent: 5, resets_at: reset(20) }] },
};
for (const [id, snap] of Object.entries(limits)) {
  await db.query(`UPDATE agent_runtime SET plan_limits=$2 WHERE id=$1`, [id, { ...snap, observed_at: Math.floor(Date.now() / 1000) }]);
}
const agentNames = [
  ["孙悟空 · 强档全栈执行（Claude Opus）", "负责跨模块的大改动，从服务端到前端一次交付，并附带真实浏览器截图证据。"],
  ["孙悟饭 · 验收席（同档换一家模型做终审）", "审查 PR 并给出 verdict，确认测试和截图都齐了再合入。"],
  ["布尔玛 · 前端界面与手机端适配专员", "专门处理 390px 手机网页、响应式布局与 apps/mobile 同屏改动。"],
  ["比克", "短名字对照组。"],
];
const agents = [];
for (const [i, [name, description]] of agentNames.entries()) {
  const r = await db.query(
    `INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_id, owner_id, status, visibility, instructions)
     VALUES ($1,$2,$3,'local',$4,$5,$6,'workspace',$7) RETURNING id`,
    [ws.id, name, description, i % 2 ? rt2 : rt, userId, ["working", "idle", "idle", "offline"][i], "你是 Multica 魔改的执行智能体。".repeat(20)],
  );
  agents.push(r.rows[0].id);
}

const projects = [];
for (const title of [
  "Multica 魔改：手机端全站有数据走查与排版修复（第二期）",
  "kun-agent-mono 自托管登录与多账号槽位改造",
  "官网",
]) {
  projects.push(await call("POST", "/api/projects", { title, description: "项目说明：".repeat(12), lead_type: "member", lead_id: userId }, ws));
}

const long = "收件箱「等你」卡片标题在 390px 下被固定宽度列挤成一字一行，需要排查所有页面同类问题并修复";
const titles = [
  long,
  "任务详情面包屑在 375 档下项目名只剩一个字母，改成带省略号的截断",
  "底部额度条横向溢出、聊天浮钮挡住列表最后一行内容",
  "软键盘弹出时输入框被遮挡，刘海屏和 Home 条没有留安全边距",
  "AGY 多账号槽位：额度耗尽自动接力到同档位同方向的下一席位并在界面上说明原因",
  "小队筛选与分组：按方向和档位分组，支持把智能体拖进拖出小队",
  "修 bug",
  "SupercalifragilisticexpialidociousVeryLongEnglishIdentifierWithoutSpacesThatMustWrap",
  "自托管实例上传链路：分片续传，断网恢复后从断点继续，进度条在手机上也要能看清",
  "Goal 模式：完成线锁定后执行人不能自行降低标准",
  "Desktop 双通道发版：测试通道 vX.Y.Z-test.N 与正式通道并存",
  "聊天会话列表在有 50 条历史会话时滚动卡顿，需要虚拟列表",
];
const statuses = ["todo", "in_progress", "in_review", "blocked", "done", "backlog"];
const prios = ["urgent", "high", "medium", "low", "none"];
const issues = [];
for (const [i, title] of titles.entries()) {
  issues.push(
    await call(
      "POST",
      "/api/issues",
      {
        title,
        description: `## 背景\n\n${"这是一段很长的中文描述，用来检查正文在窄屏下是否正常换行、不会撑破容器。".repeat(6)}\n\n- 列表项一：\`packages/views/inbox/components/inbox-list-item.tsx\`\n- 列表项二：https://github.com/jeff-kunkun/multica/pull/545/files#diff-0123456789abcdef0123456789abcdef\n\n\`\`\`ts\nconst veryLongVariableNameForTesting = someFunctionCall(argumentOne, argumentTwo, argumentThree);\n\`\`\``,
        status: statuses[i % statuses.length],
        priority: prios[i % prios.length],
        project_id: projects[i % 2].id,
        assignee_type: i % 3 === 2 ? "member" : "agent",
        assignee_id: i % 3 === 2 ? userId : agents[i % agents.length],
        due_date: i % 2 ? "2026-10-20T00:00:00Z" : undefined,
      },
      ws,
    ),
  );
}
const parent = issues[0];
for (let i = 0; i < 8; i++) {
  issues.push(
    await call(
      "POST",
      "/api/issues",
      {
        title: `子任务 ${i + 1}：${["首页与收件箱", "任务列表与看板", "任务详情面包屑", "聊天列表与会话", "项目列表与详情", "智能体与小队", "自动化与运行时", "设置各页与附件预览"][i]}的有数据走查`,
        status: statuses[i % statuses.length],
        priority: prios[i % prios.length],
        parent_issue_id: parent.id,
        project_id: projects[0].id,
        assignee_type: "agent",
        assignee_id: agents[i % agents.length],
      },
      ws,
    ),
  );
}
const comments = [
  "我在手机上看了一下，收件箱、任务详情、聊天都有问题，显示问题已经好多个了，所有页面都过一遍吧。".repeat(3),
  "根因是 `grid-cols-[minmax(0,1fr)_96px_auto]` 这类固定列在窄屏没有变体，正文被挤成一字一行。长链接：https://github.com/jeff-kunkun/multica/blob/kun/packages/views/inbox/components/inbox-list-item.tsx#L120-L180",
  "短评论。",
  "```bash\nset -a; . ./.env.worktree; set +a; node docs/evidence/DENE-1341/tools/capture.mjs --phase before --viewport 390\n```",
];
for (const c of comments) await call("POST", `/api/issues/${parent.id}/comments`, { content: c }, ws);

const squad = await call(
  "POST",
  "/api/squads",
  { name: "前端与手机端适配小队（Web / Desktop / apps/mobile 三面）", description: "负责全站响应式与手机端排版问题。", leader_id: agents[2] },
  ws,
).catch((e) => ({ error: String(e) }));
if (squad.id) {
  for (const a of [agents[0], agents[1]]) {
    await db.query(`INSERT INTO squad_member (squad_id, member_type, member_id) VALUES ($1,'agent',$2) ON CONFLICT DO NOTHING`, [squad.id, a]);
  }
}
const autopilot = await call(
  "POST",
  "/api/autopilots",
  {
    title: "每天早上九点巡检全站手机端截图并把有回退的页面开成任务（长标题对照组）",
    description: "巡检说明".repeat(10),
    assignee_id: agents[0],
    execution_mode: "create_issue",
    project_id: projects[0].id,
  },
  ws,
).catch((e) => ({ error: String(e) }));

// 聊天：会话 + 消息直写库（服务端创建会话会尝试起 run）。
const chats = [];
for (const [i, title] of [
  "Multica 魔改 · 手机端全站有数据走查与排版修复的方案讨论（很长的会话标题）",
  "官网 · 改首页",
  "kun-agent-mono · 自托管登录",
].entries()) {
  const s = (
    await db.query(
      `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, explicitly_created_at)
       VALUES ($1,$2,$3,$4,'active', now() - ($5 || ' minutes')::interval) RETURNING id`,
      [ws.id, agents[i], userId, title, String(i * 30)],
    )
  ).rows[0].id;
  await db.query(`INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1,'user',$2)`, [
    s,
    "帮我看看手机上收件箱和任务详情的显示问题，标题被挤成一字一行了，还有面包屑项目名只剩一个字母。",
  ]);
  await db.query(`INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1,'assistant',$2)`, [
    s,
    `好的，我先列一下涉及的模块：\n\n1. 收件箱列表 \`packages/views/inbox\`\n2. 任务详情头部面包屑\n3. 共享移动外壳\n\n${"排查结论会附 390 / 768 / 1280 三档截图。".repeat(4)}\n\n| 页面 | 问题 | 修法 |\n| --- | --- | --- |\n| 收件箱 | 标题竖排 | 去掉固定列 |\n| 任务详情 | 面包屑被压 | 截断加省略号 |\n| 设置 | 未知 | 待查 |`,
  ]);
  chats.push(s);
}

const out = { slug: ws.slug, wsId: ws.id, token, userId, agents, projects: projects.map((p) => p.id), issues: issues.map((i) => i.id), parent: parent.id, squad: squad.id ?? squad, autopilot: autopilot.id ?? autopilot, chats, runtimes: [rt, rt2, rt3] };
fs.writeFileSync(new URL("./seed-output.json", import.meta.url), JSON.stringify(out, null, 2));
console.log(JSON.stringify(out, null, 2));
await db.end();
