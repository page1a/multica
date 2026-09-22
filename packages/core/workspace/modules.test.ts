// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWithFallback } from "../api/schema";
import {
  EMPTY_MODULE_VISIBILITY_LIST,
  ModuleVisibilityListSchema,
} from "../api/schemas";
import type { ModuleVisibility } from "../types";
import { canAccessModule, navItemModule } from "./modules";

const deniedIssues: ModuleVisibility[] = [
  { key: "issues", visibility: "private", project_id: null, allowed: false },
  { key: "projects", visibility: "workspace", project_id: null, allowed: true },
  { key: "repos", visibility: "workspace", project_id: null, allowed: true },
  { key: "runtimes", visibility: "workspace", project_id: null, allowed: true },
];

describe("canAccessModule", () => {
  it("defaults to allowed while the list is still loading", () => {
    expect(canAccessModule(undefined, "issues")).toBe(true);
  });

  it("hides a module the server marked not allowed", () => {
    expect(canAccessModule(deniedIssues, "issues")).toBe(false);
    expect(canAccessModule(deniedIssues, "projects")).toBe(true);
  });

  it("treats a missing row as allowed so a partial payload does not lock the product", () => {
    expect(canAccessModule([], "runtimes")).toBe(true);
  });
});

describe("navItemModule", () => {
  it("gates Issues, My Issues, Projects and Runtimes", () => {
    expect(navItemModule("issues")).toBe("issues");
    expect(navItemModule("myIssues")).toBe("issues");
    expect(navItemModule("projects")).toBe("projects");
    expect(navItemModule("runtimes")).toBe("runtimes");
  });

  it("leaves Agents, Squads and settings out of the module list", () => {
    expect(navItemModule("agents")).toBeNull();
    expect(navItemModule("squads")).toBeNull();
    expect(navItemModule("settings")).toBeNull();
    expect(navItemModule("usage")).toBeNull();
  });
});

describe("GET /api/modules contract", () => {
  it("falls back to every module open when the payload is malformed", () => {
    const out = parseWithFallback(
      null,
      ModuleVisibilityListSchema,
      EMPTY_MODULE_VISIBILITY_LIST,
      { endpoint: "GET /api/modules" },
    );
    expect(out.modules).toHaveLength(4);
    expect(out.modules.every((row) => row.allowed)).toBe(true);
  });
});
