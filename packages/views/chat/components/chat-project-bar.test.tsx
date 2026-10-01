// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ChatSession, Project } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import { useChatProjectBarStore } from "@multica/core/chat/project-bar-store";
import enCommon from "../../locales/en/common.json";
import enChat from "../../locales/en/chat.json";
import { ChatProjectBar } from "./chat-project-bar";

const RESOURCES = { en: { common: enCommon, chat: enChat } };

function project(id: string, title: string): Project {
  return {
    id,
    workspace_id: "ws-1",
    title,
    description: null,
    icon: null,
    status: "in_progress",
    priority: "none",
    lead_type: null,
    lead_id: null,
    start_date: null,
    due_date: null,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
  };
}

function session(id: string, projectIds: string[], updatedAt: string): ChatSession {
  return {
    id,
    workspace_id: "ws-1",
    agent_id: "agent-1",
    creator_id: "user-1",
    project_id: projectIds[0] ?? null,
    project_ids: projectIds,
    title: id,
    status: "active",
    has_unread: false,
    created_at: updatedAt,
    updated_at: updatedAt,
  };
}

const projects = Array.from({ length: 20 }, (_, index) =>
  project(`p${index + 1}`, `Project ${index + 1}`),
);

describe("ChatProjectBar", () => {
  beforeEach(() => {
    localStorage.clear();
    useChatProjectBarStore.setState({ byUser: {} });
  });

  it("more can search, pin, and reorder", async () => {
    const user = userEvent.setup();
    const onFilterChange = vi.fn();
    render(
      <I18nProvider locale="en" resources={RESOURCES}>
        <ChatProjectBar
          projects={projects}
          sessions={[
            session("s1", ["p3"], "2026-09-02T00:00:00Z"),
            session("s2", ["p1"], "2026-09-20T00:00:00Z"),
          ]}
          userId="user-1"
          filter={{ type: "all" }}
          onFilterChange={onFilterChange}
        />
      </I18nProvider>,
    );

    expect(screen.getByRole("button", { name: "All" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /More \(20\)/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /More \(20\)/ }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Project 1")).toBeInTheDocument();
    expect(within(dialog).getByText("Project 20")).toBeInTheDocument();

    await user.type(within(dialog).getByRole("textbox", { name: "Search projects…" }), "Project 17");
    expect(within(dialog).getByText("Project 17")).toBeInTheDocument();
    expect(within(dialog).queryByText("Project 1")).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Pin" }));
    expect(useChatProjectBarStore.getState().byUser["user-1"]).toEqual(["p17"]);

    const saved = JSON.parse(localStorage.getItem("multica_chat_project_bar") ?? "{}") as {
      state?: { byUser?: Record<string, string[]> };
    };
    expect(saved.state?.byUser?.["user-1"]).toEqual(["p17"]);

    await user.clear(within(dialog).getByRole("textbox", { name: "Search projects…" }));
    await user.click(within(dialog).getAllByRole("button", { name: "Pin" })[0]!);
    useChatProjectBarStore.getState().move("user-1", "p17", useChatProjectBarStore.getState().byUser["user-1"]![0]!);
    const order = useChatProjectBarStore.getState().byUser["user-1"]!;
    expect(order[0]).toBe("p17");
    expect(order).toHaveLength(2);

    await user.click(within(dialog).getByRole("button", { name: /Project 17/ }));
    expect(onFilterChange).toHaveBeenCalledWith({ type: "project", id: "p17" });
  });

  describe("with measured chips", () => {
    // jsdom has no layout: every chip measures CHIP px wide and the chip
    // area measures `areaWidth`, which is what the fit reads.
    const CHIP = 100;
    let areaWidth = 0;
    const original = {
      clientWidth: Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth"),
      offsetWidth: Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth"),
      rect: HTMLElement.prototype.getBoundingClientRect,
    };

    beforeEach(() => {
      Object.defineProperty(HTMLElement.prototype, "clientWidth", {
        configurable: true,
        get: () => areaWidth,
      });
      // The bar root: chip area plus 120px of padding and trailing buttons.
      Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
        configurable: true,
        get: () => areaWidth + 120,
      });
      HTMLElement.prototype.getBoundingClientRect = () => ({ width: CHIP }) as DOMRect;
    });

    afterEach(() => {
      if (original.clientWidth) {
        Object.defineProperty(HTMLElement.prototype, "clientWidth", original.clientWidth);
      }
      if (original.offsetWidth) {
        Object.defineProperty(HTMLElement.prototype, "offsetWidth", original.offsetWidth);
      }
      HTMLElement.prototype.getBoundingClientRect = original.rect;
    });

    // p1..p8 each have a chat; p1 is the most recent.
    const sessions = Array.from({ length: 8 }, (_, index) =>
      session(`s${index + 1}`, [`p${index + 1}`], `2026-09-${String(28 - index).padStart(2, "0")}T00:00:00Z`),
    );

    function renderBar(props: Partial<React.ComponentProps<typeof ChatProjectBar>> = {}) {
      const onFilterChange = vi.fn();
      const onFitWidthChange = vi.fn();
      const view = render(
        <I18nProvider locale="en" resources={RESOURCES}>
          <ChatProjectBar
            projects={projects}
            sessions={sessions}
            userId="user-1"
            filter={{ type: "all" }}
            onFilterChange={onFilterChange}
            onFitWidthChange={onFitWidthChange}
            {...props}
          />
        </I18nProvider>,
      );
      const chips = () =>
        within(view.container.querySelector<HTMLElement>("[data-slot='chat-project-chips']")!)
          .getAllByRole("button")
          .map((button) => button.textContent);
      return { ...view, chips, onFilterChange, onFitWidthChange };
    }

    it("fills two rows before anything goes into More", () => {
      // 3 chips per row (100 + 6 + 100 + 6 + 100 = 312): All + 5 projects.
      areaWidth = 320;
      const { chips } = renderBar();
      expect(chips()).toEqual([
        "All",
        "Project 11",
        "Project 21",
        "Project 31",
        "Project 41",
        "Project 51",
      ]);
      // 20 projects, 5 on the bar.
      expect(screen.getByRole("button", { name: /More \(15\)/ })).toBeInTheDocument();
    });

    it("keeps pinned projects on the bar when space runs out", () => {
      areaWidth = 320;
      useChatProjectBarStore.setState({ byUser: { "user-1": ["p8", "p20"] } });
      const { chips } = renderBar();
      expect(chips()).toEqual([
        "All",
        "Project 81",
        "Project 200",
        "Project 11",
        "Project 21",
        "Project 31",
      ]);
    });

    it("lists the collapsed projects with their chat counts in More", async () => {
      areaWidth = 320;
      const user = userEvent.setup();
      const { onFilterChange } = renderBar();

      await user.click(screen.getByRole("button", { name: /More/ }));
      const collapsed = within(screen.getByRole("group", { name: "Collapsed projects" }));
      expect(
        collapsed.getAllByRole("button", { name: /Project \d+, 1 chat/ }).map((row) =>
          row.getAttribute("aria-label"),
        ),
      ).toEqual(["Project 6, 1 chat", "Project 7, 1 chat", "Project 8, 1 chat"]);
      // Listed once: not repeated under "Other projects".
      expect(within(screen.getByRole("dialog")).getAllByText("Project 7")).toHaveLength(1);

      await user.click(collapsed.getByRole("button", { name: "Project 7, 1 chat" }));
      expect(onFilterChange).toHaveBeenCalledWith({ type: "project", id: "p7" });
    });

    it("moves the project picked from More to a visible slot right after the pins", () => {
      areaWidth = 320;
      useChatProjectBarStore.setState({ byUser: { "user-1": ["p8"] } });
      const { chips } = renderBar({ filter: { type: "project", id: "p7" } });
      expect(chips()).toEqual([
        "All",
        "Project 81",
        "Project 71",
        "Project 11",
        "Project 21",
        "Project 31",
      ]);
      expect(screen.getByRole("button", { name: /Project 7/, pressed: true })).toBeInTheDocument();
    });

    it("reports the bar width at which every project fits in two rows", () => {
      areaWidth = 320;
      const { onFitWidthChange } = renderBar();
      // All + 8 projects = 9 chips; 5 on row one needs 5 * 100 + 4 * 6 = 524,
      // plus the 120px beside the chips and 2px of slack.
      expect(onFitWidthChange).toHaveBeenLastCalledWith(524 + 120 + 2);
    });
  });
});
