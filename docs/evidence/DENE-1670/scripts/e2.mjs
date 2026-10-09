const task = await taskSpace("DENE-1670 沉淀实页验收");
const B = "http://localhost:13784/dev";
const PROJ = B + "/projects/e1e794bb-ea60-47c9-860a-a01a5f2f9c2a";
const PARENT = B + "/issues/01a11bb8-75bd-702a-9e83-c5d299e51a39";
const S = "/tmp/dene1670/shots/";
const go = async (page, u) => { try { await page.goto(u, { timeout: 90000, waitUntil: "domcontentloaded" }); } catch (e) { console.log("goto:", e.message); } };
const vp = async (page, w, h, mobile) => {
  await page.cdp("Emulation.setDeviceMetricsOverride", { width: w, height: h, deviceScaleFactor: 2, mobile });
  const iw = await page.evaluate(() => innerWidth);
  if (iw !== w) { const f = w / iw; await page.cdp("Emulation.setDeviceMetricsOverride", { width: Math.round(w * f), height: Math.round(h * f), deviceScaleFactor: 2, mobile }); }
};
const waitText = (page, txt) => page.waitForFunction((t) => document.body.innerText.includes(t), txt, { timeout: 90000 });
const overflow = (page) => page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: innerWidth, bodySW: document.body.scrollWidth }));
const report = {};
const shot = async (page, path) => { const r = await page.cdp("Page.captureScreenshot", { format: "png" }); const fs = await import("node:fs"); fs.writeFileSync(path, Buffer.from(r.data, "base64")); };
try {
  const page = task.page("p1");
  await go(page, B.replace("/dev", "/"));
  await page.evaluate(() => { localStorage.setItem("multica_token", "<dev 登录拿到的 token>"); });
  for (const [w, h] of [[1280, 860], [768, 1024], [390, 844], [375, 812]]) {
    const r = report[w] = {};
    await go(page, PROJ);
    await vp(page, w, h, w < 768);
    r.innerWidth = await page.evaluate(() => innerWidth);
    await waitText(page, "沉淀验收项目");
    await page.waitForTimeout(1500);
    let visible = await page.evaluate(() => document.body.innerText.includes("最近沉淀"));
    r.cardVisibleWithoutOpening = visible;
    if (!visible) {
      await page.evaluate(() => document.querySelector("svg.lucide-panel-right")?.closest("button")?.click());
      await waitText(page, "最近沉淀");
      await page.waitForTimeout(800);
      r.openedSheet = true;
    }
    await page.evaluate(() => { const el = [...document.querySelectorAll("p,div,span,h3,h4")].find(e => e.childElementCount === 0 && e.textContent.trim() === "最近沉淀"); el?.scrollIntoView({ block: "start" }); });
    await page.waitForTimeout(500);
    r.card = await page.evaluate(() => {
      const head = [...document.querySelectorAll("*")].find(e => e.childElementCount === 0 && e.textContent.trim() === "最近沉淀");
      const list = head?.parentElement;
      const rows = list ? [...list.children].filter(c => c.tagName === "DIV").map(row => {
        const a = row.querySelector("a");
        const f = row.querySelector("p");
        return { source: a?.textContent, href: a?.getAttribute("href"), files: f?.textContent, filesTruncated: f ? f.scrollWidth > f.clientWidth : null, filesLines: f ? Math.round(f.getBoundingClientRect().height / parseFloat(getComputedStyle(f).lineHeight)) : null,
                 rowFits: row.scrollWidth <= row.clientWidth + 1, unverified: row.textContent.includes("未核对"), right: Math.round(row.getBoundingClientRect().right) };
      }) : [];
      return rows;
    });
    r.overflowOpen = await overflow(page);
    await shot(page, S + `project-memory-${w}.png`);
    if (r.openedSheet) {
      await page.keyboard.press("Escape");
      await page.waitForTimeout(2000);
      r.closedByEscape = !(await page.evaluate(() => document.body.innerText.includes("最近沉淀")));
      // reopen, close by tapping backdrop
      await page.evaluate(() => document.querySelector("svg.lucide-panel-right")?.closest("button")?.click());
      await waitText(page, "最近沉淀");
      await page.waitForTimeout(600);
      await page.mouse.click(10, Math.round(h / 2));
      await page.waitForTimeout(2000);
      r.closedByBackdrop = !(await page.evaluate(() => document.body.innerText.includes("最近沉淀")));
      await shot(page, S + `project-closed-${w}.png`);
    }
    r.overflowClosed = await overflow(page);
    // close strip on parent issue
    await go(page, PARENT);
    await waitText(page, "收口条：读到交付文件");
    await page.waitForTimeout(1500);
    r.strips = await page.evaluate(() => [...document.querySelectorAll('[data-testid="sub-issue-close-strip"]')].map(s => {
      const k = s.querySelector('[data-kind="close.knowledge_audit"]') || [...s.children].find(c => /知识|记忆|未核对|沉淀/.test(c.textContent));
      s.scrollIntoView({ block: "center" });
      return { text: s.innerText.replace(/\n/g, " | "), knowledge: k?.textContent, fits: s.scrollWidth <= s.clientWidth + 1 };
    }));
    r.overflowIssue = await overflow(page);
    await shot(page, S + `close-strip-${w}.png`);
  }
  // source jumps at 390
  await vp(page, 390, 844, true);
  const jumps = {};
  for (const [name, match] of [["chat", "聊天"], ["issue", "SED-3"]]) {
    await go(page, PROJ);
    await waitText(page, "沉淀验收项目");
    await page.waitForTimeout(1200);
    await page.evaluate(() => document.querySelector("svg.lucide-panel-right")?.closest("button")?.click());
    await waitText(page, "最近沉淀");
    await page.waitForTimeout(600);
    const clicked = await page.evaluate((m) => { const a = [...document.querySelectorAll("a")].find(a => a.textContent.startsWith(m) && a.closest('[aria-labelledby="project-memory-recent-heading"]')); if (!a) return null; const t = a.textContent; a.click(); return t; }, match);
    await page.waitForFunction(() => !location.pathname.includes("/projects/"), null, { timeout: 120000 }).catch(() => {});
    await page.waitForTimeout(5000);
    jumps[name] = { clicked, url: await page.evaluate(() => location.pathname), title: await page.evaluate(() => document.title), sheetGone: !(await page.evaluate(() => document.body.innerText.includes("最近沉淀"))) };
    await shot(page, S + `jump-${name}-390.png`);
  }
  report.jumps = jumps;
  console.log(JSON.stringify(report, null, 1));
} catch (e) { console.log("ERR", e.message); console.log(JSON.stringify(report, null, 1)); }
finally { await task.finish({ keep: [] }); }
