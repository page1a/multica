import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ReviewerPicker } from "./reviewer-picker";

// 验收席 has three states where the assignee slot has two, and the third one
// ("needs no acceptance pass") is the whole reason the slot is a written value
// rather than an empty one — routing re-judges an empty slot forever. These
// tests pin the three states to three distinct writes. (DENE-633)

const members = [{ user_id: "u-1", name: "Alice", role: "member" }];
const agents = [
  { id: "a-1", name: "悟空", archived_at: null },
  { id: "a-2", name: "Archived One", archived_at: "2026-01-01T00:00:00Z" },
];

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (options: { queryKey: unknown[] }) => ({
    data: options.queryKey.includes("agents") ? agents : members,
  }),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members", "ws-1"] }),
  agentListOptions: () => ({ queryKey: ["agents", "ws-1"] }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (_type: string, id: string) => `Actor ${id}`,
    getActorInitials: () => "AA",
    getActorAvatarUrl: () => null,
  }),
}));

// Renders each label as its own key path, so an assertion names the string the
// component asked for rather than a translation that can be re-worded.
vi.mock("../../../i18n", () => {
  const path = (prefix: string): unknown =>
    new Proxy(() => prefix, {
      get: (_t, key) =>
        typeof key === "symbol" ? () => prefix : path(prefix ? `${prefix}.${key}` : key),
    });
  return {
    useT: () => ({ t: (pick: (d: unknown) => unknown) => String(pick(path(""))) }),
    useLocale: () => "en",
  };
});

vi.mock("../../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => <span data-testid={`avatar-${actorId}`} />,
}));

function renderPicker(
  props: Partial<React.ComponentProps<typeof ReviewerPicker>> = {},
) {
  const onUpdate = vi.fn();
  render(
    <ReviewerPicker
      reviewerType={null}
      reviewerId={null}
      onUpdate={onUpdate}
      open
      onOpenChange={() => {}}
      {...props}
    />,
  );
  return onUpdate;
}

describe("ReviewerPicker", () => {
  it("writes 'none' with a null id for 不需要验收", () => {
    const onUpdate = renderPicker();
    fireEvent.click(screen.getByText("pickers.reviewer.no_review"));
    expect(onUpdate).toHaveBeenCalledWith({ reviewer_type: "none", reviewer_id: null });
  });

  it("clears the pair back to undecided, which is NOT the same write as 'none'", () => {
    const onUpdate = renderPicker({ reviewerType: "none" });
    fireEvent.click(screen.getByText("pickers.reviewer.trigger_undecided"));
    expect(onUpdate).toHaveBeenCalledWith({ reviewer_type: null, reviewer_id: null });
  });

  it("writes a reference to the chosen agent, not its name", () => {
    const onUpdate = renderPicker();
    fireEvent.click(screen.getByText("悟空"));
    expect(onUpdate).toHaveBeenCalledWith({ reviewer_type: "agent", reviewer_id: "a-1" });
  });

  it("writes a reference to the chosen member", () => {
    const onUpdate = renderPicker();
    fireEvent.click(screen.getByText("Alice"));
    expect(onUpdate).toHaveBeenCalledWith({ reviewer_type: "member", reviewer_id: "u-1" });
  });

  it("does not offer an archived agent", () => {
    renderPicker();
    expect(screen.queryByText("Archived One")).toBeNull();
  });
});
