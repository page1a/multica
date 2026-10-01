import { cleanup, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (type: string, id: string) =>
      type === "member" && id === "user-1" ? "Kun" : `${type}:${id}`,
  }),
}));

import { AssigneeSourceNote } from "./assignee-source-note";

afterEach(cleanup);

function issue(overrides: Partial<Issue> = {}): Issue {
  return {
    id: "issue-1",
    assignee_type: "agent",
    assignee_id: "agent-1",
    ...overrides,
  } as Issue;
}

describe("AssigneeSourceNote", () => {
  it("credits a person's own pick to that person", () => {
    renderWithI18n(
      <AssigneeSourceNote
        issue={issue({ assignee_source: "human", assignee_source_user_id: "user-1" })}
      />,
    );
    expect(screen.getByTestId("assignee-source-note").textContent).toContain("Kun");
    expect(screen.queryByTestId("assignee-source-quote")).toBeNull();
  });

  it("shows the speaker and the words for a quoted pick", () => {
    renderWithI18n(
      <AssigneeSourceNote
        issue={issue({
          assignee_source: "quote",
          assignee_source_user_id: "user-1",
          assignee_quote: "交给贝吉塔游戏做",
        })}
      />,
    );
    expect(screen.getByTestId("assignee-source-note").dataset.source).toBe("quote");
    expect(screen.getByTestId("assignee-source-note").textContent).toContain("Kun");
    expect(screen.getByTestId("assignee-source-quote").textContent).toContain("交给贝吉塔游戏做");
  });

  it("says when routing or an agent made the pick", () => {
    renderWithI18n(<AssigneeSourceNote issue={issue({ assignee_source: "router" })} />);
    expect(screen.getByTestId("assignee-source-note").dataset.source).toBe("router");
    cleanup();
    renderWithI18n(<AssigneeSourceNote issue={issue({ assignee_source: "agent" })} />);
    expect(screen.getByTestId("assignee-source-note").dataset.source).toBe("agent");
  });

  it("stays silent without a record or without an executor", () => {
    const { container } = renderWithI18n(<AssigneeSourceNote issue={issue()} />);
    expect(container.textContent).toBe("");
    cleanup();
    const again = renderWithI18n(
      <AssigneeSourceNote issue={issue({ assignee_source: "human", assignee_id: null })} />,
    );
    expect(again.container.textContent).toBe("");
  });
});
