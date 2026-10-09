import { chromium } from "playwright";
import fs from "node:fs";
const [token, out] = [fs.readFileSync("/tmp/dene1641_dev","utf8").trim(), process.argv[2]];
const W = "http://localhost:13920";
const browser = await chromium.launch({ channel: "chrome" });
async function shot(name, w, h, url, act, keepScroll) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h } });
  await ctx.addInitScript(t => { localStorage.setItem("multica_token", t); localStorage.setItem("i18nextLng","zh-Hans"); }, token);
  const page = await ctx.newPage();
  await page.goto(W + url, { waitUntil: "domcontentloaded", timeout: 120000 });
  await page.waitForTimeout(6000);
  if (act) await act(page);
  await page.waitForTimeout(800);
  if (!keepScroll) await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: `${out}/${name}.png` });
  console.log(name, page.url());
  await ctx.close();
}
const openItem = async p => { await p.getByText(/Acme Studio/).first().click(); };
await shot("inbox-1280", 1280, 800, "/dev/inbox", openItem);
await shot("inbox-list-390", 390, 844, "/dev/inbox", async p => { await p.getByRole("tab", { name: "All activity" }).click(); await p.waitForTimeout(1500); });
await shot("inbox-detail-390", 390, 844, "/dev/inbox?layer=activity", openItem);
await shot("settings-1280", 1280, 800, "/dev/settings?tab=workspace-links&section=incoming");
await shot("settings-390", 390, 844, "/dev/settings?tab=workspace-links&section=incoming");
// follow the jump from the detail pane on mobile
await shot("jump-390", 390, 844, "/dev/inbox?layer=activity", async p => { await openItem(p); await p.waitForTimeout(800); await p.getByRole("link", { name: /Review in Linked workspaces/ }).click(); await p.waitForTimeout(8000); }, true);
await browser.close();
