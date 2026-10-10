import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import type { ProjectMember } from "@multica/core/types";

function pm(over: Partial<ProjectMember> & Pick<ProjectMember, "member_id" | "name">): ProjectMember {
  return {
    id: `pm-${over.member_id}`,
    workspace_id: "ws-1",
    project_id: "p-1",
    added_by: null,
    created_at: "2026-01-01T00:00:00Z",
    email: "",
    avatar_url: null,
    role: "",
    is_lead: false,
    ...over,
  };
}

// Server order and redaction as a non-owner sees them: lead first, own role.
const MEMBERS = [
  pm({ member_id: "u-lead", name: "Lia Lead", is_lead: true }),
  pm({ member_id: "u-self", name: "Sam Self", role: "member" }),
  pm({ member_id: "u-other", name: "Oz Other" }),
];

vi.mock("@multica/core/projects", () => ({
  projectMembersOptions: () => ({ queryKey: ["pm"], queryFn: async () => MEMBERS }),
  useAddProjectMember: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useRemoveProjectMember: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));

import { ProjectMembersSection } from "./project-members-section";

describe("ProjectMembersSection (DENE-1706)", () => {
  it("explains what membership grants and labels roles in server order", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithI18n(
      <QueryClientProvider client={qc}>
        <ProjectMembersSection projectId="p-1" canManage={false} />
      </QueryClientProvider>,
    );

    expect(screen.getByText("Project members can see this project's issues.")).toBeInTheDocument();
    const lead = (await screen.findByText("Lia Lead")).parentElement!;
    expect(within(lead).getByText("Lead")).toBeInTheDocument();
    const self = screen.getByText("Sam Self").parentElement!;
    expect(within(self).getByText("Member")).toBeInTheDocument();
    // A role the server blanked shows nothing rather than a guess.
    expect(screen.getByText("Oz Other").parentElement!.children).toHaveLength(1);

    const names = screen.getAllByText(/Lia Lead|Sam Self|Oz Other/).map((n) => n.textContent);
    expect(names).toEqual(["Lia Lead", "Sam Self", "Oz Other"]);
  });
});
