/**
 * DENE-1643 live evidence: the real chat page, linked (read-only) projects in
 * the 项目上下文 menu — select, remove, then stale after the source unticks /
 * revokes, removing stale chips one at a time. Writes screenshots and a step
 * log to docs/evidence/DENE-1643/live/. Not part of the regular suite: to
 * rerun, copy it into e2e/ against a `make up C=api,web` worktree.
 */
import "./env";
import { test, expect, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import pg from "pg";
import { TestApiClient } from "./fixtures";

const API_BASE = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
const DATABASE_URL = process.env.DATABASE_URL!;
const OUT = path.resolve(process.cwd(), "docs/evidence/DENE-1643/live");
const LONG_SOURCE = "北辰联合研究院·跨区域数据治理与智能体协作平台二期工作区";
const TITLES = [
  "长期运行的跨团队知识库迁移与项目记忆整理（含历史决策回填）",
  "Q4 客户交付",
  "全渠道订单履约系统重构——库存、物流与售后一体化",
];

const VIEWPORTS = [
  { name: "375", width: 375, height: 812, mobile: true },
  { name: "390", width: 390, height: 844, mobile: true },
  { name: "768", width: 768, height: 1024, mobile: false },
  { name: "1280", width: 1280, height: 800, mobile: false },
];

const log: string[] = [];

async function call(api: TestApiClient, slug: string, p: string, init: RequestInit = {}) {
  const res = await fetch(`${API_BASE}${p}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${api.getToken()}`,
      "Content-Type": "application/json",
      "X-Workspace-Slug": slug,
    },
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${init.method ?? "GET"} ${p} -> ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

async function linkedOnServer(api: TestApiClient, slug: string, sessionId: string) {
  const s = await call(api, slug, `/api/chat/sessions/${sessionId}`);
  return (s.linked_projects ?? []).map(
    (p: { title: string; available: boolean }) => `${p.title.slice(0, 8)}${p.available ? "" : "(失效)"}`,
  );
}

async function openProjectMenu(page: Page) {
  // Start from a closed menu: after a pick the outer menu may still be open.
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toHaveCount(0);
  await page.getByRole("button", { name: /^(添加|Add)$/ }).last().click();
  await page.getByRole("menuitem", { name: /项目上下文|Project context/ }).click();
  await expect(page.getByText(/连通的项目（只读）|Linked projects \(read-only\)/)).toBeVisible();
  await page.waitForTimeout(400); // let the open animation finish before a screenshot
}

function chipClear(page: Page, title: string) {
  return page
    .locator("button[title]", { hasText: title })
    .locator("xpath=..")
    .getByRole("button", { name: /移除项目上下文|Remove project context/ });
}

for (const vp of VIEWPORTS) {
  test(`linked projects in the real chat page @${vp.name}`, async ({ browser }) => {
    const shot = async (page: Page, step: string) => {
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(OUT, `${vp.name}-${step}.png`) });
    };
    const note = (s: string) => log.push(`[${vp.name}] ${s}`);

    const api = new TestApiClient();
    const run = `${vp.name}-${Date.now().toString(36)}`;
    await api.login(`dene1643-${run}@multica.ai`, "Kun 验收");
    const viewer = await api.ensureWorkspace("我的工作区", `dene1643-v-${run}`);
    await api.markUserOnboarded();
    const source = await call(api, viewer.slug, "/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ name: LONG_SOURCE, slug: `dene1643-s-${run}` }),
    });
    const projects = [];
    for (const title of TITLES) {
      projects.push(await call(api, source.slug, "/api/projects", { method: "POST", body: JSON.stringify({ title, visibility: "workspace" }) }));
    }
    await call(api, viewer.slug, "/api/projects", { method: "POST", body: JSON.stringify({ title: "本工作区项目" }) });
    // Projects are created private; only workspace-visible ones can be shared.
    const vis = new pg.Client(DATABASE_URL);
    await vis.connect();
    await vis.query(`UPDATE project SET visibility = 'workspace' WHERE workspace_id = $1`, [source.id]);
    await vis.end();
    const link = await call(api, source.slug, "/api/workspace-links", {
      method: "POST",
      body: JSON.stringify({ target_slug: viewer.slug, project_ids: projects.map((p) => p.id) }),
    });
    await call(api, viewer.slug, `/api/workspace-links/${link.id}`, { method: "PATCH", body: JSON.stringify({ accept: true }) });

    // Agent + session seeded directly: no daemon runs in an agent worktree.
    const db = new pg.Client(DATABASE_URL);
    await db.connect();
    const user = (await db.query(`SELECT id FROM "user" WHERE email = $1`, [api.getEmail()])).rows[0].id;
    const rt = (await db.query(
      `INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at)
       VALUES ($1, NULL, 'evidence runtime', 'cloud', 'claude', 'online', 'evidence', '{"cli_version":"0.5.0"}'::jsonb, now()) RETURNING id`,
      [viewer.id],
    )).rows[0].id;
    const agent = (await db.query(
      `INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
       VALUES ($1, '孙悟空', '', 'cloud', '{}'::jsonb, $2, 'workspace', 1, $3) RETURNING id`,
      [viewer.id, rt, user],
    )).rows[0].id;
    const session = (await db.query(
      `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, explicitly_created_at)
       VALUES ($1, $2, $3, '看看连通工作区的项目', 'active', now()) RETURNING id`,
      [viewer.id, agent, user],
    )).rows[0].id;
    await db.query(
      `INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1, 'user', '帮我参考一下对方的项目')`,
      [session],
    );
    await db.end();

    const context = await browser.newContext({
      viewport: { width: vp.width, height: vp.height },
      isMobile: vp.mobile,
      hasTouch: vp.mobile,
      locale: "zh-CN",
    });
    const page = await context.newPage();
    await page.addInitScript((t) => localStorage.setItem("multica_token", t), api.getToken()!);
    await page.goto(`/${viewer.slug}/chat/${session}`, { waitUntil: "domcontentloaded" });
    // The dev-server badge sits over the composer on narrow screens; dev only.
    await page.addStyleTag({ content: "nextjs-portal { display: none !important; }" });
    await expect(page.getByTestId("virtuoso-item-list").getByText("帮我参考一下对方的项目")).toBeVisible({ timeout: 30000 });

    // 1. Menu: the linked group with long titles and the long source name.
    await openProjectMenu(page);
    await shot(page, "1-menu");
    note("打开 + → 项目上下文：看到「连通的项目（只读）」分组，3 个来源项目");

    // 2. Select all three linked projects (checkbox items keep the menu open).
    // Each tick saves the session and the submenu folds back (also true for
    // this workspace's own projects), so reopen it per pick.
    for (const [i, title] of TITLES.entries()) {
      if (i > 0) await openProjectMenu(page);
      await page.getByRole("menuitemcheckbox", { name: new RegExp(title.slice(0, 6)) }).click();
      await expect.poll(() => linkedOnServer(api, viewer.slug, session)).toHaveLength(i + 1);
    }
    await openProjectMenu(page);
    await shot(page, "2-selected-menu");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await expect.poll(() => linkedOnServer(api, viewer.slug, session)).toHaveLength(3);
    await shot(page, "3-chips");
    note(`勾选 3 个 → 服务端 ${JSON.stringify(await linkedOnServer(api, viewer.slug, session))}`);

    // 3. Remove a valid one via its chip ×.
    await chipClear(page, TITLES[2]).click();
    await expect.poll(() => linkedOnServer(api, viewer.slug, session)).toHaveLength(2);
    await shot(page, "4-removed-valid");
    note(`点第 3 个的 × → 服务端 ${JSON.stringify(await linkedOnServer(api, viewer.slug, session))}`);
    await openProjectMenu(page);
    await page.getByRole("menuitemcheckbox", { name: new RegExp(TITLES[2].slice(0, 6)) }).click();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await expect.poll(() => linkedOnServer(api, viewer.slug, session)).toHaveLength(3);
    note(`菜单里再勾回第 3 个 → 服务端 ${JSON.stringify(await linkedOnServer(api, viewer.slug, session))}`);

    // 4. Source unticks the first two: they go stale, the third stays live.
    await call(api, source.slug, `/api/workspace-links/${link.id}`, {
      method: "PATCH",
      body: JSON.stringify({ project_ids: [projects[2].id] }),
    });
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.addStyleTag({ content: "nextjs-portal { display: none !important; }" });
    await expect(page.getByText("失效").first()).toBeVisible({ timeout: 30000 });
    await shot(page, "5-stale-chips");
    note(`来源方取消共享前两个 → 刷新后服务端 ${JSON.stringify(await linkedOnServer(api, viewer.slug, session))}`);
    await openProjectMenu(page);
    await shot(page, "6-stale-menu");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");

    // 5. Remove one stale chip; the other stale one and the live one stay.
    await chipClear(page, TITLES[0]).click();
    await expect.poll(() => linkedOnServer(api, viewer.slug, session)).toHaveLength(2);
    await shot(page, "7-removed-one-stale");
    note(`点第 1 个（失效）的 × → 服务端 ${JSON.stringify(await linkedOnServer(api, viewer.slug, session))}`);

    // 6. Revoke the whole link: the live one goes stale too; remove the other stale.
    await call(api, source.slug, `/api/workspace-links/${link.id}`, { method: "DELETE" });
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.addStyleTag({ content: "nextjs-portal { display: none !important; }" });
    await expect(page.getByText("失效")).toHaveCount(2, { timeout: 30000 });
    await shot(page, "8-revoked");
    note(`来源方撤销连通 → 刷新后服务端 ${JSON.stringify(await linkedOnServer(api, viewer.slug, session))}`);
    await chipClear(page, TITLES[1]).click();
    await expect.poll(() => linkedOnServer(api, viewer.slug, session)).toHaveLength(1);
    await shot(page, "9-removed-second-stale");
    note(`撤销后再点第 2 个（失效）的 × → 服务端 ${JSON.stringify(await linkedOnServer(api, viewer.slug, session))}`);

    // 7. A forged stale ref is still refused.
    const forged = await fetch(`${API_BASE}/api/chat/sessions/${session}`, {
      method: "PATCH",
      headers: { Authorization: `Bearer ${api.getToken()}`, "Content-Type": "application/json", "X-Workspace-Slug": viewer.slug },
      body: JSON.stringify({ linked_projects: [{ link_id: link.id, project_id: projects[2].id }, { link_id: link.id, project_id: projects[0].id }] }),
    });
    note(`再提交一个本会话没挂过的失效项目 → HTTP ${forged.status}`);
    expect(forged.status).toBe(404);

    await context.close();
    fs.writeFileSync(path.join(OUT, "steps.txt"), log.join("\n") + "\n");
  });
}
