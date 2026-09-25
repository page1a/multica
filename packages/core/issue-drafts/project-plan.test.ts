import { describe, expect, it } from "vitest";
import { ApiError } from "../api/client";
import type { IssueDraftPayload } from "../types";
import {
  inferNewProjectExecutionMode,
  provisionedDirectoryShouldBeRemoved,
  resolveAlignmentProjectPlan,
  sanitizeProjectDirectoryName,
} from "./project-plan";

const EMPTY: IssueDraftPayload = {
  title: "父任务",
  description: "业务定位写在这里。",
  status: "",
  priority: "",
};

const PROJECTS = [
  { id: "p-multica", title: "Multica 魔改" },
  { id: "p-other", title: "别的项目" },
];

describe("inferNewProjectExecutionMode", () => {
  it("gives a freshly initialised repository its own worktree", () => {
    expect(inferNewProjectExecutionMode({ gitInit: true, container: false })).toBe(
      "worktree",
    );
  });

  it("shares a container or a folder that is not a repository", () => {
    expect(inferNewProjectExecutionMode({ gitInit: false, container: false })).toBe(
      "shared",
    );
    expect(inferNewProjectExecutionMode({ gitInit: true, container: true })).toBe(
      "shared",
    );
  });
});

describe("sanitizeProjectDirectoryName", () => {
  it("keeps a Chinese title as one path segment", () => {
    expect(sanitizeProjectDirectoryName("通力电梯图像追溯")).toBe("通力电梯图像追溯");
  });

  it("refuses a name that would leave the root", () => {
    expect(sanitizeProjectDirectoryName("../secret")).toBe(".. secret");
    expect(sanitizeProjectDirectoryName("..")).toBe("");
    expect(sanitizeProjectDirectoryName("a/b")).toBe("a b");
  });
});

describe("resolveAlignmentProjectPlan", () => {
  it("selects an existing project by the exact name the carrier gave", () => {
    const plan = resolveAlignmentProjectPlan({
      draft: {
        ...EMPTY,
        project_proposal: { action: "existing", name: "Multica 魔改" },
      },
      projects: PROJECTS,
    });
    expect(plan).toEqual({
      kind: "existing",
      projectId: "p-multica",
      suggested: true,
    });
  });

  it("turns an unmatched name into a new project when the draft is a group", () => {
    const plan = resolveAlignmentProjectPlan({
      draft: {
        ...EMPTY,
        children: [
          {
            key: "c1",
            title: "子任务",
            description: "",
            status: "",
            priority: "",
          },
        ],
        project_proposal: { action: "existing", name: "通力电梯" },
      },
      projects: PROJECTS,
    });
    expect(plan.kind).toBe("create");
    expect(plan.name).toBe("通力电梯");
    expect(plan.suggested).toBe(true);
    expect(plan.description).toBe("业务定位写在这里。");
  });

  it("does not start a project for a single issue", () => {
    const plan = resolveAlignmentProjectPlan({
      draft: {
        ...EMPTY,
        project_proposal: {
          action: "create",
          name: "通力电梯",
          description: "定位",
        },
      },
      projects: PROJECTS,
    });
    expect(plan).toEqual({ kind: "none" });
  });

  it("keeps a create proposal, including its positioning, for a group", () => {
    const plan = resolveAlignmentProjectPlan({
      draft: {
        ...EMPTY,
        children: [
          { key: "c1", title: "子任务", description: "", status: "", priority: "" },
        ],
        project_proposal: {
          action: "create",
          name: "通力电梯",
          icon: "🛗",
          description: "图像追溯的安全加固",
        },
      },
      projects: PROJECTS,
    });
    expect(plan).toMatchObject({
      kind: "create",
      name: "通力电梯",
      icon: "🛗",
      description: "图像追溯的安全加固",
    });
  });

  it("lets the person clear the proposal", () => {
    const plan = resolveAlignmentProjectPlan({
      draft: {
        ...EMPTY,
        children: [
          { key: "c1", title: "子任务", description: "", status: "", priority: "" },
        ],
        project_proposal: { action: "create", name: "通力电梯" },
        project_choice: { kind: "none" },
      },
      projects: PROJECTS,
    });
    expect(plan).toEqual({ kind: "none" });
  });

  it("keeps a project the person already picked", () => {
    const plan = resolveAlignmentProjectPlan({
      draft: {
        ...EMPTY,
        project_id: "p-other",
        project_proposal: { action: "create", name: "通力电梯" },
        children: [
          { key: "c1", title: "子任务", description: "", status: "", priority: "" },
        ],
      },
      projects: PROJECTS,
    });
    expect(plan).toEqual({ kind: "existing", projectId: "p-other" });
  });
});

describe("provisionedDirectoryShouldBeRemoved", () => {
  it("removes the directory when the server rolled the project back", () => {
    const err = new ApiError("no", 500, "error", { code: "rolled_back" });
    expect(provisionedDirectoryShouldBeRemoved(err)).toBe(true);
  });

  it("removes the directory on a refusal that committed nothing", () => {
    const err = new ApiError("upgrade the daemon", 422, "error");
    expect(provisionedDirectoryShouldBeRemoved(err)).toBe(true);
  });

  it("keeps the directory once the project has committed", () => {
    const err = new ApiError("failed to complete", 500, "error", {
      code: "committed",
    });
    expect(provisionedDirectoryShouldBeRemoved(err)).toBe(false);
    expect(provisionedDirectoryShouldBeRemoved(new Error("network"))).toBe(false);
  });
});
