import "./env";

import { expect, test, type Page } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";
import { createTestApi, waitForPageText } from "./helpers";

// Agent borrowing (DENE-808): the doorbell and timed passes, driven through the
// real API and the real inbox / agent-settings pages. The admission matrix is
// covered in Go (agent_doorbell_test.go); this walks the two owner surfaces.

const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
const SHOTS = process.env.DOORBELL_SHOTS_DIR ?? "";

// Lets a machine without the bundled chromium run this spec against an
// installed Chrome (PLAYWRIGHT_CHANNEL=chrome); unset means the default.
test.use({ channel: process.env.PLAYWRIGHT_CHANNEL });

async function shot(page: Page, name: string) {
  if (!SHOTS) return;
  await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: false });
}

async function postMention(
  token: string,
  slug: string,
  issueId: string,
  agentId: string,
  agentName: string,
  text: string,
) {
  const res = await fetch(`${API_BASE}/api/issues/${issueId}/comments`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
      "X-Workspace-Slug": slug,
    },
    body: JSON.stringify({ content: `[@${agentName}](mention://agent/${agentId}) ${text}` }),
  });
  if (res.status !== 201) throw new Error(`comment failed: ${res.status} ${await res.text()}`);
  const body = (await res.json()) as {
    trigger_outcomes?: { target_id: string; status: string; reason_code: string }[];
  };
  const out = body.trigger_outcomes?.find((o) => o.target_id === agentId);
  if (!out) throw new Error(`no outcome for agent in ${JSON.stringify(body.trigger_outcomes)}`);
  return out;
}

async function openInboxAs(page: Page, token: string, slug: string) {
  await page.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, token);
  await page.goto(`/${slug}/inbox`, { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "Inbox");
}

test("doorbell: ring, approve with pass, decline receipt, settings passes", async ({ page, browser }) => {
  test.setTimeout(180_000);
  const owner = await createTestApi();
  const db = new pg.Client(process.env.DATABASE_URL);
  await db.connect();
  const runId = `${Date.now().toString(36)}-${process.pid.toString(36)}`;
  let runtimeId: string | undefined;
  let agentId: string | undefined;
  let requesterId: string | undefined;
  const agentName = `Doorbell Agent ${runId}`;
  try {
    const workspace = (await owner.getWorkspaces())[0]!;
    const ownerRow = await db.query<{ id: string }>(`SELECT id FROM "user" WHERE email = $1`, [owner.getEmail()]);
    const ownerId = ownerRow.rows[0]!.id;

    // A second member of the same workspace who is NOT on the agent's list.
    const requester = new TestApiClient();
    await requester.login(`doorbell-lead-${runId}@multica.ai`, "Lead Li");
    await requester.markUserOnboarded();
    const reqRow = await db.query<{ id: string }>(`SELECT id FROM "user" WHERE email = $1`, [requester.getEmail()]);
    requesterId = reqRow.rows[0]!.id;
    await db.query(`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, [workspace.id, requesterId]);
    await requester.ensureWorkspace(workspace.name, workspace.slug);

    // Owner's private agent on an online cloud runtime, doorbell on.
    const runtime = await db.query<{ id: string }>(
      `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
       VALUES ($1, 'Doorbell verification', 'cloud', 'e2e_doorbell', 'online', 'E2E fixture', '{}'::jsonb, $2, now()) RETURNING id`,
      [workspace.id, ownerId],
    );
    runtimeId = runtime.rows[0]!.id;
    const agent = await db.query<{ id: string }>(
      `INSERT INTO agent (workspace_id, name, description, instructions, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id, doorbell_enabled)
       VALUES ($1, $2, '', '', 'cloud', '{}'::jsonb, $3, 'private', 'private', 1, $4, true) RETURNING id`,
      [workspace.id, agentName, runtimeId, ownerId],
    );
    agentId = agent.rows[0]!.id;

    const issueA = await owner.createIssue(`Borrow for the launch checklist ${runId}`);
    const issueB = await owner.createIssue(`Borrow for the release notes ${runId}`);
    const issueC = await owner.createIssue(`Borrow with a pass ${runId}`);
    await db.query(`UPDATE issue SET visibility = 'workspace' WHERE id = ANY($1::uuid[])`, [[issueA.id, issueB.id, issueC.id]]);

    const reqToken = requester.getToken()!;
    const ownerToken = owner.getToken()!;

    // 1. The requester rings: blocked with access_requested, no task yet.
    const ring = await postMention(reqToken, workspace.slug, issueA.id, agentId, agentName, "please draft the launch checklist");
    expect(ring.status).toBe("blocked");
    expect(ring.reason_code).toBe("access_requested");
    const tasksBefore = await db.query(`SELECT count(*)::int AS n FROM agent_task_queue WHERE agent_id = $1`, [agentId]);
    expect(tasksBefore.rows[0].n).toBe(0);

    // 2. Owner sees the ring in the inbox and approves it with a 2-hour pass.
    await openInboxAs(page, ownerToken, workspace.slug);
    await waitForPageText(page, "Lead Li");
    await page.getByRole("button", { name: /Lead Li/ }).first().click();
    const notice = page.getByTestId("agent-access-request-notice");
    await expect(notice).toBeVisible({ timeout: 15000 });
    await expect(notice).toContainText("local environment");
    await shot(page, "01-inbox-ring");
    await notice.getByRole("button", { name: "2 hours" }).click();
    await notice.getByTestId("access-approve").click();
    await expect(notice.getByTestId("access-request-status")).toHaveText("Approved", { timeout: 15000 });
    await shot(page, "02-inbox-approved");

    const tasksAfter = await db.query(`SELECT count(*)::int AS n FROM agent_task_queue WHERE agent_id = $1 AND issue_id = $2`, [agentId, issueA.id]);
    expect(tasksAfter.rows[0].n).toBe(1);
    const pass = await db.query<{ id: string }>(
      `SELECT id FROM agent_access_pass WHERE agent_id = $1 AND user_id = $2 AND revoked_at IS NULL AND expires_at > now()`,
      [agentId, requesterId],
    );
    expect(pass.rows).toHaveLength(1);

    // 3. With the pass in hand the next mention goes straight through.
    const withPass = await postMention(reqToken, workspace.slug, issueC.id, agentId, agentName, "and the pass lets me in");
    expect(withPass.status).toBe("queued");

    // 4. Settings: doorbell switch on, the pass listed, revoke it, issue a new one.
    await page.goto(`/${workspace.slug}/agents/${agentId}?view=access`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("doorbell-switch")).toHaveAttribute("aria-checked", "true", { timeout: 15000 });
    await expect(page.getByTestId("passes-list")).toContainText("Lead Li");
    await shot(page, "03-settings-passes");
    await page.getByTestId(`revoke-pass-${pass.rows[0]!.id}`).click();
    await expect(page.getByTestId("passes-empty")).toBeVisible({ timeout: 15000 });
    await shot(page, "04-settings-revoked");

    // Revoked: the next mention rings again instead of running.
    const afterRevoke = await postMention(reqToken, workspace.slug, issueB.id, agentId, agentName, "write the release notes");
    expect(afterRevoke.reason_code).toBe("access_requested");

    // Issue a fresh pass proactively for the rest of today.
    await page.getByRole("combobox", { name: "Member" }).click();
    await page.getByRole("option", { name: "Lead Li" }).click();
    await page.getByRole("button", { name: "Rest of today" }).click();
    await page.getByTestId("issue-pass").click();
    await expect(page.getByTestId("passes-list")).toContainText("Lead Li", { timeout: 15000 });
    await shot(page, "05-settings-issued");
    const activePasses = await db.query(
      `SELECT count(*)::int AS n FROM agent_access_pass WHERE agent_id = $1 AND user_id = $2 AND revoked_at IS NULL AND expires_at > now()`,
      [agentId, requesterId],
    );
    expect(activePasses.rows[0].n).toBe(1);
    await db.query(`UPDATE agent_access_pass SET revoked_at = now() WHERE agent_id = $1 AND revoked_at IS NULL`, [agentId]);

    // 5. Decline the second ring; the requester gets a receipt in their inbox.
    // Rows carry the ring's title ("Lead Li wants to use …"), newest first;
    // the summary only shows on the card, so pick the top row and check there.
    await page.goto(`/${workspace.slug}/inbox`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "Lead Li");
    await page.getByRole("button", { name: /Lead Li/ }).first().click();
    const notice2 = page.getByTestId("agent-access-request-notice");
    await expect(notice2).toBeVisible({ timeout: 15000 });
    await expect(notice2).toContainText("release notes");
    await notice2.getByTestId("access-decline").click();
    await expect(notice2.getByTestId("access-request-status")).toHaveText("Declined", { timeout: 15000 });
    await shot(page, "06-inbox-declined");

    const reqPage = await browser.newPage();
    try {
      await openInboxAs(reqPage, reqToken, workspace.slug);
      await waitForPageText(reqPage, `Your request to use ${agentName} was declined`);
      await reqPage.getByRole("button", { name: /request declined/ }).first().click();
      await waitForPageText(reqPage, "release notes");
      await shot(reqPage, "07-requester-declined-receipt");
    } finally {
      await reqPage.close();
    }
    const tasksB = await db.query(`SELECT count(*)::int AS n FROM agent_task_queue WHERE agent_id = $1 AND issue_id = $2`, [agentId, issueB.id]);
    expect(tasksB.rows[0].n).toBe(0);
  } finally {
    await owner.cleanup();
    if (agentId) await db.query(`DELETE FROM agent WHERE id = $1`, [agentId]);
    if (runtimeId) await db.query(`DELETE FROM agent_runtime WHERE id = $1`, [runtimeId]);
    if (requesterId) await db.query(`DELETE FROM "user" WHERE id = $1`, [requesterId]);
    await db.end();
  }
});
