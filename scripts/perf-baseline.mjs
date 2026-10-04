#!/usr/bin/env node
/**
 * Measure the page-load baseline used by DENE-1278.
 *
 * The script deliberately measures a running app instead of starting one: Web,
 * Desktop's renderer and the mobile web build can all be pointed at the same
 * harness with --url. Authentication is supplied by Playwright storage state,
 * so credentials never belong in this script or its report.
 *
 *   node scripts/perf-baseline.mjs --url http://localhost:3000 \
 *     --routes issues=/issues,chat=/chat,detail=/issues/<id> --runs 3 \
 *     --out perf-report/baseline.json
 */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import os from "node:os";
import process from "node:process";
import { chromium } from "playwright";

const args = process.argv.slice(2);
const flag = (name, fallback) => {
  const index = args.indexOf(`--${name}`);
  return index >= 0 && args[index + 1] ? args[index + 1] : fallback;
};
const url = flag("url", process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:3000");
const runs = Math.max(1, Number(flag("runs", "3")) || 3);
const out = resolve(flag("out", "perf-report/baseline.json"));
const routeSpec = flag("routes", "issues=/issues,chat=/chat");
const storageStatePath = flag("storage-state", process.env.PERF_STORAGE_STATE);

const routes = routeSpec.split(",").map((entry) => {
  const separator = entry.indexOf("=");
  if (separator <= 0 || separator === entry.length - 1) {
    throw new Error(`invalid route ${entry}; expected name=/path`);
  }
  return { name: entry.slice(0, separator), path: entry.slice(separator + 1) };
});

const target = (path) => new URL(path, url).toString();

async function measureNavigation(page, path) {
  const requests = [];
  const startedAt = Date.now();
  const onRequest = (request) => requests.push(request);
  page.on("request", onRequest);
  try {
    await page.goto(target(path), { waitUntil: "domcontentloaded" });
    await page.waitForLoadState("networkidle", { timeout: 15_000 }).catch(() => {});
    const timing = await page.evaluate(() => {
      const entry = performance.getEntriesByType("navigation")[0];
      return entry
        ? { domContentLoadedMs: entry.domContentLoadedEventEnd, loadEventMs: entry.loadEventEnd }
        : null;
    });
    const apiRequests = requests.filter((request) => new URL(request.url()).pathname.startsWith("/api/"));
    return {
      elapsedMs: Date.now() - startedAt,
      domContentLoadedMs: timing?.domContentLoadedMs ?? null,
      loadEventMs: timing?.loadEventMs ?? null,
      requestCount: requests.length,
      apiRequestCount: apiRequests.length,
      apiRequests: apiRequests.map((request) => `${request.method()} ${new URL(request.url()).pathname}`),
    };
  } finally {
    page.off("request", onRequest);
  }
}

async function measureForeground(page) {
  const startedAt = Date.now();
  await page.bringToFront();
  const state = await page.evaluate(() => ({
    visibilityState: document.visibilityState,
    hidden: document.hidden,
  }));
  return {
    elapsedMs: Date.now() - startedAt,
    ...state,
  };
}

async function main() {
  const browser = await chromium.launch({ headless: true });
  const contextOptions = storageStatePath ? { storageState: JSON.parse(readFileSync(storageStatePath, "utf8")) } : {};
  const samples = [];
  try {
    for (const route of routes) {
      for (let run = 1; run <= runs; run += 1) {
        // A fresh context preserves the supplied auth state while modelling a
        // cold browser start. The following navigation in that same context
        // is the hot/re-entry sample.
        const context = await browser.newContext(contextOptions);
        const page = await context.newPage();
        const cold = await measureNavigation(page, route.path);
        // A second visit in the same context models returning to an already
        // loaded page. The request delta is the signal used by the optimization.
        const hot = await measureNavigation(page, route.path);
        // Keep a second page in front briefly, then bring the measured page
        // back. This exercises the same visibility/focus boundary as a user
        // switching back to the app without adding another navigation.
        const background = await context.newPage();
        await background.goto("about:blank");
        const foreground = await measureForeground(page);
        await background.close();
        samples.push({ route: route.name, path: route.path, run, cold, hot, foreground });
        await context.close();
      }
    }
  } finally {
    await browser.close();
  }

  const report = {
    schemaVersion: 1,
    generatedAt: new Date().toISOString(),
    url,
    runs,
    routes,
    environment: {
      node: process.version,
      platform: process.platform,
      arch: process.arch,
      os: `${os.type()} ${os.release()}`,
      browser: browser.version(),
    },
    samples,
    notes: [
      "cold is a new navigation in a cleared browser context; hot is the immediate second visit in the same context",
      "networkidle is best effort and is not used as the primary timing; use request counts and DOMContentLoaded for comparisons",
      "repeat on an idle machine and compare reports with the same routes, run count and fixture data",
    ],
  };
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, `${JSON.stringify(report, null, 2)}\n`);
  console.log(JSON.stringify({ out, samples: samples.length, routes: routes.map((route) => route.name) }));
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});
