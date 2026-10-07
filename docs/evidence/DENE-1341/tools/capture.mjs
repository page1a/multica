// DENE-1341 三档截图 + 排版问题探测。
// 用法（仓库根目录，seed.mjs 跑过之后）：
//   set -a; . ./.env.worktree; set +a
//   node docs/evidence/DENE-1341/tools/capture.mjs --phase before [--widths 390,768,1280] [--only inbox,issue]
// 截图写到 docs/evidence/DENE-1341/raw/<phase>/<page>-<width>.png（不提交），
// 探测结果写到 docs/evidence/DENE-1341/raw/<phase>/findings.json。
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, all) => (cur.startsWith("--") ? [...acc, [cur.slice(2), all[i + 1]]] : acc), []),
);
const phase = args.phase ?? "before";
const widths = (args.widths ?? "390,768,1280").split(",").map(Number);
const only = args.only ? args.only.split(",") : null;
const here = path.dirname(new URL(import.meta.url).pathname);
const seed = JSON.parse(fs.readFileSync(path.join(here, "seed-output.json"), "utf8"));
const outDir = path.join(here, "..", "raw", phase);
fs.mkdirSync(outDir, { recursive: true });
const base = process.env.FRONTEND_ORIGIN;
const s = seed.slug;

const settingsTabs = [
  "profile", "preferences", "notifications", "tokens", "workspace", "members", "labels", "issue-statuses",
  "properties", "routing", "wakeups", "quick-actions", "shortcuts", "code", "connections", "apps",
  "agent-permissions", "workspace-links", "project-sharing", "config-transfer", "billing",
];
const pages = [
  ["home", `/${s}`],
  ["inbox", `/${s}/inbox`],
  ["my-issues", `/${s}/my-issues`],
  ["issues", `/${s}/issues`],
  ["issue-detail", `/${s}/issues/${seed.parent}`],
  ["issue-child", `/${s}/issues/${seed.issues[12]}`],
  ["chat", `/${s}/chat`],
  ["chat-session", `/${s}/chat/${seed.chats[0]}`],
  ["projects", `/${s}/projects`],
  ["project-detail", `/${s}/projects/${seed.projects[0]}`],
  ["agents", `/${s}/agents`],
  ["agent-detail", `/${s}/agents/${seed.agents[0]}`],
  ["squads", `/${s}/squads`],
  ["squad-detail", `/${s}/squads/${seed.squad}`],
  ["autopilots", `/${s}/autopilots`],
  ["autopilot-detail", `/${s}/autopilots/${seed.autopilot}`],
  ["runtimes", `/${s}/runtimes`],
  ["runtime-detail", `/${s}/runtimes/${seed.runtimes[0]}`],
  ["skills", `/${s}/skills`],
  ["usage", `/${s}/usage`],
  ["member-detail", `/${s}/members/${seed.userId}`],
  ["linked", `/${s}/linked`],
  ...settingsTabs.map((t) => [`settings-${t}`, `/${s}/settings?tab=${t}`]),
].filter(([name]) => !only || only.some((o) => name === o || name.startsWith(o + "-")));

// 在页面里找三类问题：横向溢出、一字一行的竖排文本、被压到极窄的文本。
function detect() {
  const vw = window.innerWidth;
  const out = { docOverflow: document.documentElement.scrollWidth - vw, items: [] };
  const seen = new Set();
  const label = (el) => {
    const cls = typeof el.className === "string" ? el.className.split(/\s+/).slice(0, 6).join(".") : "";
    return `${el.tagName.toLowerCase()}${el.dataset.slot ? `[slot=${el.dataset.slot}]` : ""}.${cls}`;
  };
  const scrollsX = (el) => {
    for (let p = el.parentElement; p; p = p.parentElement) {
      const ox = getComputedStyle(p).overflowX;
      if (ox === "auto" || ox === "scroll" || ox === "hidden" || ox === "clip") return p;
    }
    return null;
  };
  for (const el of document.querySelectorAll("body *")) {
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    const cs = getComputedStyle(el);
    const ownText = [...el.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join("").trim();
    if (ownText.length >= 4) {
      const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.4;
      const charsPerLine = ownText.length / Math.max(1, Math.round(r.height / lh));
      if (r.height > lh * 2.5 && charsPerLine <= 2.2 && cs.writingMode === "horizontal-tb") {
        out.items.push({ kind: "vertical-text", el: label(el), text: ownText.slice(0, 30), w: Math.round(r.width), h: Math.round(r.height) });
      }
    }
    if (r.right > vw + 1 && r.left < vw) {
      const clip = scrollsX(el);
      const clipR = clip ? clip.getBoundingClientRect() : null;
      if (!clip || clipR.right > vw + 1) {
        const key = label(el);
        if (!seen.has(key)) {
          seen.add(key);
          out.items.push({ kind: "overflow-x", el: key, text: (el.textContent || "").trim().slice(0, 30), right: Math.round(r.right) });
        }
      }
    }
  }
  out.items = out.items.slice(0, 40);
  return out;
}

const browser = await chromium.launch({ channel: "chrome", headless: true });
const findings = fs.existsSync(path.join(outDir, "findings.json"))
  ? JSON.parse(fs.readFileSync(path.join(outDir, "findings.json"), "utf8"))
  : {};
for (const width of widths) {
  const mobile = width < 768;
  const ctx = await browser.newContext({
    viewport: { width, height: mobile ? 844 : width === 768 ? 1024 : 800 },
    deviceScaleFactor: mobile ? 2 : 1,
    isMobile: mobile,
    hasTouch: mobile,
    locale: "zh-CN",
  });
  await ctx.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
  }, seed.token);
  const page = await ctx.newPage();
  for (const [name, url] of pages) {
   try {
    await page.goto(base + url, { waitUntil: "domcontentloaded", timeout: 180000 });
    await page.waitForLoadState("load", { timeout: 120000 }).catch(() => {});
    await page.waitForTimeout(2000);
    // 骨架屏或加载转圈还在就继续等，避免截到半成品。
    await page
      .waitForFunction(() => !document.querySelector('[data-slot="skeleton"], .animate-pulse, [aria-busy="true"]'), null, { timeout: 45000 })
      .catch(() => {});
    await page.waitForTimeout(800);
    // Next.js 开发角标只在 dev 出现，截图里隐藏掉。
    await page.addStyleTag({ content: "nextjs-portal{display:none!important}" });
    // 有的设置页加载后会再跳一次地址，探测撞上就等它落定再试。
    let f;
    for (let attempt = 0; ; attempt++) {
      try {
        f = await page.evaluate(detect);
        break;
      } catch (err) {
        if (attempt >= 2) throw err;
        await page.waitForLoadState("load");
        await page.waitForTimeout(1500);
      }
    }
    findings[`${name}-${width}`] = f;
    await page.screenshot({ path: path.join(outDir, `${name}-${width}.png`), fullPage: false, timeout: 90000 });
    console.log(`${name}-${width}: docOverflow=${f.docOverflow} items=${f.items.length}`);
   } catch (err) {
    // dev 服务器编译慢时单页超时，记下来接着截下一页，最后补跑。
    console.log(`${name}-${width}: FAILED ${err.message.split("\n")[0]}`);
   }
  }
  await ctx.close();
}
fs.writeFileSync(path.join(outDir, "findings.json"), JSON.stringify(findings, null, 2));
await browser.close();
