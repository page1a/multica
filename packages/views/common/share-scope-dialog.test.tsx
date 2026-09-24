// @vitest-environment jsdom

import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enLayout from "../locales/en/layout.json";
import { ShareScopeDialog, ShareScopeTrigger } from "./share-scope-dialog";
import { GuestReadOnlyScope, WriteAction } from "../layout/guest-readonly";

const mutateIssue = vi.fn();
const mutateRepo = vi.fn();
const mutateProject = vi.fn();
const addProjectMember = vi.fn();
const removeProjectMember = vi.fn();
const addShare = vi.fn();
const removeShare = vi.fn();
const previewData = { project_id: "project-1", visibility: "private", affected_count: 8, previously_private_count: 3 };
const data = vi.hoisted(() => ({
  projectMembers: [] as Array<{ member_id: string; name: string; email: string }>,
  shares: [] as Array<{ member_id: string; name: string; email: string }>,
  shareResources: [] as unknown[],
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "me" } }) }));
vi.mock("@multica/core/projects", () => ({
  projectMembersOptions: (_wsId: string, projectId: string) => ({ queryKey: ["project-members", projectId], queryFn: () => data.projectMembers }),
  useAddProjectMember: () => ({ mutateAsync: addProjectMember }),
  useRemoveProjectMember: () => ({ mutateAsync: removeProjectMember }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({
    queryKey: ["workspace-members"],
    queryFn: () => [
      { user_id: "me", role: "owner", name: "Me", email: "me@example.com" },
      { user_id: "jeff", role: "member", name: "JEFF", email: "jeff@example.com" },
      { user_id: "guest-1", role: "guest", name: "Guest", email: "guest@example.com" },
    ],
  }),
}));
vi.mock("@multica/core/visibility", () => ({
  useSetIssueVisibility: () => ({ isPending: false, mutateAsync: mutateIssue }),
  useSetRepoVisibility: () => ({ isPending: false, mutateAsync: mutateRepo }),
  useSetProjectVisibility: () => ({ isPending: false, mutateAsync: mutateProject }),
  useProjectVisibilityPreview: (_projectId: string, enabled: boolean) => ({
    isLoading: false,
    data: enabled ? previewData : undefined,
  }),
  resourceSharesOptions: (_wsId: string, resource: unknown) => {
    data.shareResources.push(resource);
    return { queryKey: ["shares", JSON.stringify(resource)], queryFn: () => data.shares };
  },
  useAddResourceShare: () => ({ mutateAsync: addShare }),
  useRemoveResourceShare: () => ({ mutateAsync: removeShare }),
}));
vi.mock("./actor-avatar", () => ({ ActorAvatar: () => <span data-testid="avatar" /> }));

const resources = { en: { common: enCommon, layout: enLayout } };

function renderDialog(
  target: Parameters<typeof ShareScopeDialog>[0]["target"],
  onOpenChange = vi.fn(),
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={resources}>
        <ShareScopeDialog open onOpenChange={onOpenChange} target={target} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("ShareScopeDialog", () => {
  beforeEach(() => {
    data.projectMembers = [];
    data.shares = [];
    data.shareResources = [];
    mutateIssue.mockReset().mockResolvedValue({ visibility: "workspace", audience_size: 4 });
    mutateRepo.mockReset().mockResolvedValue({ visibility: "workspace", audience_size: 4 });
    mutateProject.mockReset().mockResolvedValue({ visibility: "project", audience_size: 2 });
    addProjectMember.mockReset().mockResolvedValue({});
    removeProjectMember.mockReset().mockResolvedValue(undefined);
    addShare.mockReset().mockResolvedValue({});
    removeShare.mockReset().mockResolvedValue(undefined);
  });

  it("lets an issue without a project pick specific people directly", async () => {
    renderDialog({ kind: "issue", resourceId: "issue-1", currentScope: "private" });
    const specificRadio = screen.getByRole("radio", { name: /Specific people/ });
    expect(specificRadio).not.toBeDisabled();
    fireEvent.click(specificRadio);

    const jeff = await screen.findByRole("checkbox", { name: /JEFF/ });
    expect(screen.queryByRole("checkbox", { name: /Me/ })).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /Guest/ })).toBeInTheDocument();
    expect(screen.getByText("Read only")).toBeInTheDocument();
    expect(screen.getByText(/No one picked yet/)).toBeInTheDocument();

    fireEvent.click(jeff);
    await waitFor(() => expect(addShare).toHaveBeenCalledWith("jeff"));
    expect(data.shareResources).toContainEqual({ kind: "issue", id: "issue-1" });

    fireEvent.click(screen.getByRole("button", { name: /Save as Specific people/ }));
    await waitFor(() => expect(mutateIssue).toHaveBeenCalledWith({ issueId: "issue-1", visibility: "project" }));
  });

  it("shows existing picks as checked and removes them on uncheck", async () => {
    data.shares = [{ member_id: "jeff", name: "JEFF", email: "jeff@example.com" }];
    renderDialog({ kind: "issue", resourceId: "issue-1", currentScope: "project" });
    const jeff = await screen.findByRole("checkbox", { name: /JEFF/ });
    await waitFor(() => expect(jeff).toBeChecked());
    expect(screen.getByText("1 picked")).toBeInTheDocument();
    const specificOption = screen.getByRole("radio", { name: /Specific people/ }).closest("label");
    expect(specificOption).toHaveTextContent("2 people can see it");
    fireEvent.click(jeff);
    await waitFor(() => expect(removeShare).toHaveBeenCalledWith("jeff"));
  });

  it("shows toggle failures inline", async () => {
    addShare.mockRejectedValue(new Error("Nope"));
    renderDialog({ kind: "issue", resourceId: "issue-1", currentScope: "project" });
    fireEvent.click(await screen.findByRole("checkbox", { name: /JEFF/ }));
    expect(await screen.findByText("Nope")).toBeInTheDocument();
  });

  it("uses the project member list as the picked people for a project", async () => {
    data.projectMembers = [{ member_id: "guest-1", name: "Guest", email: "guest@example.com" }];
    renderDialog({ kind: "project", resourceId: "project-1", currentScope: "project" });
    const jeff = await screen.findByRole("checkbox", { name: /JEFF/ });
    await waitFor(() => expect(screen.getByRole("checkbox", { name: /Guest/ })).toBeChecked());
    fireEvent.click(jeff);
    await waitFor(() => expect(addProjectMember).toHaveBeenCalledWith("jeff"));
    fireEvent.click(screen.getByRole("checkbox", { name: /Guest/ }));
    await waitFor(() => expect(removeProjectMember).toHaveBeenCalledWith("guest-1"));
    expect(addShare).not.toHaveBeenCalled();
  });

  it("asks for confirmation before applying a project-wide scope", async () => {
    const onOpenChange = vi.fn();
    renderDialog({ kind: "project", resourceId: "project-1", currentScope: "private" }, onOpenChange);
    fireEvent.click(screen.getByRole("radio", { name: /Specific people/ }));
    fireEvent.click(screen.getByRole("button", { name: /Save as Specific people/ }));
    expect(await screen.findByText(/Apply to all project resources/)).toBeInTheDocument();
    expect(screen.getByText(/8 resources, including 3/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Apply sharing/ }));
    await waitFor(() => expect(mutateProject).toHaveBeenCalledWith({ projectId: "project-1", visibility: "project" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("asks for confirmation before applying the entire workspace scope", async () => {
    renderDialog({ kind: "project", resourceId: "project-1", currentScope: "private" });
    fireEvent.click(screen.getByRole("radio", { name: /Entire workspace/ }));
    fireEvent.click(screen.getByRole("button", { name: /Save as Entire workspace/ }));
    expect(await screen.findByText(/Apply to all project resources/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Apply sharing/ }));
    await waitFor(() => expect(mutateProject).toHaveBeenCalledWith({ projectId: "project-1", visibility: "workspace" }));
  });

  it("notes that project members also see a repository in a project", async () => {
    const onOpenChange = vi.fn();
    data.projectMembers = [{ member_id: "guest-1", name: "Guest", email: "guest@example.com" }];
    renderDialog(
      { kind: "repo", resourceId: "https://github.com/acme/app.git", projectId: "project-1", currentScope: "private" },
      onOpenChange,
    );
    fireEvent.click(screen.getByRole("radio", { name: /Specific people/ }));
    expect(await screen.findByText("1 members of its project can also see it.")).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("checkbox", { name: /JEFF/ }));
    await waitFor(() => expect(addShare).toHaveBeenCalledWith("jeff"));
    expect(data.shareResources).toContainEqual({ kind: "repo", url: "https://github.com/acme/app.git" });
    fireEvent.click(screen.getByRole("button", { name: /Save as Specific people/ }));
    await waitFor(() => expect(mutateRepo).toHaveBeenCalledWith({ url: "https://github.com/acme/app.git", visibility: "project" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("keeps the sharing entry inert for guests", () => {
    const onClick = vi.fn();
    render(
      <I18nProvider locale="en" resources={resources}>
        <GuestReadOnlyScope isGuest>
          <WriteAction>
            <ShareScopeTrigger scope="private" onClick={onClick} />
          </WriteAction>
        </GuestReadOnlyScope>
      </I18nProvider>,
    );
    fireEvent.click(screen.getByTestId("write-action"));
    expect(onClick).not.toHaveBeenCalled();
    expect(screen.getByTestId("write-action")).toHaveAttribute("aria-disabled", "true");
  });
});
