import { chromium } from "playwright";
import fs from "node:fs";
const out = process.argv[2];
const W = "http://localhost:13920";
const b = await chromium.launch({ channel: "chrome" });
async function ctxFor(who, w, h) {
  const ctx = await b.newContext({ viewport: { width: w, height: h } });
  await ctx.addInitScript(t => localStorage.setItem("multica_token", t), fs.readFileSync(`/tmp/dene1641_${who}`, "utf8").trim());
  return ctx;
}
// dev accepts on a phone
let ctx = await ctxFor("dev", 390, 844); let p = await ctx.newPage();
await p.goto(W + "/dev/settings?tab=workspace-links&section=incoming", { waitUntil: "domcontentloaded", timeout: 120000 });
await p.waitForTimeout(6000);
await p.getByRole("button", { name: "Accept", exact: true }).click();
await p.waitForTimeout(3000);
await p.screenshot({ path: `${out}/accepted-settings-390.png` });
await p.goto(W + "/dev/inbox?layer=activity", { waitUntil: "domcontentloaded", timeout: 120000 });
await p.waitForTimeout(5000);
await p.screenshot({ path: `${out}/after-accept-inbox-390.png` });
await ctx.close();
// alice sees the receipt
for (const [w, h] of [[390, 844], [1280, 800]]) {
  ctx = await ctxFor("alice", w, h); p = await ctx.newPage();
  await p.goto(W + "/acme/inbox?layer=activity", { waitUntil: "domcontentloaded", timeout: 120000 });
  await p.waitForTimeout(6000);
  await p.getByText(/accepted/).first().click();
  await p.waitForTimeout(1500);
  await p.screenshot({ path: `${out}/receipt-${w}.png` });
  await ctx.close();
}
await b.close();
