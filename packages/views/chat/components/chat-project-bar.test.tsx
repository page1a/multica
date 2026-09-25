// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
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

  it("keeps one row, and more can search, pin, and reorder", async () => {
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
});
