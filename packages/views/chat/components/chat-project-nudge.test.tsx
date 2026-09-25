// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ChatSession } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enChat from "../../locales/en/chat.json";
import { ChatProjectNudge } from "./chat-project-nudge";

vi.mock("../../projects/components/project-picker", () => ({
  ProjectPicker: ({
    onUpdate,
    triggerRender,
  }: {
    onUpdate: (updates: { project_id?: string | null }) => void;
    triggerRender: React.ReactElement;
  }) => (
    <div>
      {triggerRender}
      <button type="button" onClick={() => onUpdate({ project_id: "project-1" })}>
        pick-project
      </button>
    </div>
  ),
}));

const RESOURCES = { en: { common: enCommon, chat: enChat } };

function session(overrides: Partial<ChatSession> = {}): ChatSession {
  return {
    id: "session-1",
    workspace_id: "ws-1",
    agent_id: "agent-1",
    creator_id: "user-1",
    title: "Loose notes",
    status: "active",
    has_unread: false,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

function renderNudge(value: ChatSession, onBind = vi.fn(), onDismiss = vi.fn()) {
  render(
    <I18nProvider locale="en" resources={RESOURCES}>
      <ChatProjectNudge session={value} onBind={onBind} onDismiss={onDismiss} />
    </I18nProvider>,
  );
  return { onBind, onDismiss };
}

describe("ChatProjectNudge", () => {
  it("asks an unbound chat to bind, and offers to stop asking", async () => {
    const user = userEvent.setup();
    const { onBind, onDismiss } = renderNudge(session());

    expect(screen.getByRole("button", { name: "Bind to a project" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "pick-project" }));
    expect(onBind).toHaveBeenCalledWith(["project-1"]);

    await user.click(
      screen.getByRole("button", { name: "This is a casual chat and doesn't need a project" }),
    );
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("stays quiet once the chat has a project or the reminder was dismissed", () => {
    const { rerender } = render(
      <I18nProvider locale="en" resources={RESOURCES}>
        <ChatProjectNudge
          session={session({ project_ids: ["project-1"], project_id: "project-1" })}
          onBind={vi.fn()}
          onDismiss={vi.fn()}
        />
      </I18nProvider>,
    );
    expect(screen.queryByRole("button", { name: "Bind to a project" })).not.toBeInTheDocument();

    rerender(
      <I18nProvider locale="en" resources={RESOURCES}>
        <ChatProjectNudge
          session={session({ project_nudge_dismissed: true })}
          onBind={vi.fn()}
          onDismiss={vi.fn()}
        />
      </I18nProvider>,
    );
    expect(screen.queryByRole("button", { name: "Bind to a project" })).not.toBeInTheDocument();
  });
});
