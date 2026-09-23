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
const previewData = { project_id: "project-1", visibility: "private", affected_count: 8, previously_private_count: 3 };

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "me" } }) }));
vi.mock("@multica/core/projects", () => ({
  projectMembersOptions: (_wsId: string, projectId: string) => ({ queryKey: ["project-members", projectId], queryFn: () => [] }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["workspace-members"], queryFn: () => [{ user_id: "guest-1", role: "guest", name: "Guest", email: "guest@example.com" }] }),
}));
vi.mock("@multica/core/visibility", () => ({
  useSetIssueVisibility: () => ({ isPending: false, mutateAsync: mutateIssue }),
  useSetRepoVisibility: () => ({ isPending: false, mutateAsync: mutateRepo }),
  useSetProjectVisibility: () => ({ isPending: false, mutateAsync: mutateProject }),
  useProjectVisibilityPreview: (_projectId: string, enabled: boolean) => ({
    isLoading: false,
    data: enabled ? previewData : undefined,
  }),
}));
vi.mock("./actor-avatar", () => ({ ActorAvatar: () => <span data-testid="avatar" /> }));

const resources = { en: { common: enCommon, layout: enLayout } };

function renderDialog(target: Parameters<typeof ShareScopeDialog>[0]["target"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={resources}>
        <ShareScopeDialog open onOpenChange={vi.fn()} target={target} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("ShareScopeDialog", () => {
  beforeEach(() => {
    mutateIssue.mockReset().mockResolvedValue({ visibility: "workspace", audience_size: 4 });
    mutateRepo.mockReset().mockResolvedValue({ visibility: "workspace", audience_size: 4 });
    mutateProject.mockReset().mockResolvedValue({ visibility: "project", audience_size: 2 });
  });

  it("disables project members when the resource is not in a project", () => {
    renderDialog({ kind: "issue", resourceId: "issue-1", currentScope: "private" });
    const projectRadio = screen.getByRole("radio", { name: /Project members/ });
    expect(projectRadio).toBeDisabled();
    expect(screen.getByText(/Add this resource to a project first/)).toBeInTheDocument();
  });

  it("asks for confirmation before applying a project-wide scope", async () => {
    const onOpenChange = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en" resources={resources}>
          <ShareScopeDialog
            open
            onOpenChange={onOpenChange}
            target={{ kind: "project", resourceId: "project-1", currentScope: "private" }}
          />
        </I18nProvider>
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("radio", { name: /Project members/ }));
    fireEvent.click(screen.getByRole("button", { name: /Save as Project members/ }));
    expect(await screen.findByText(/Apply to all project resources/)).toBeInTheDocument();
    expect(screen.getByText(/8 resources, including 3/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Apply sharing/ }));
    await waitFor(() => expect(mutateProject).toHaveBeenCalledWith({ projectId: "project-1", visibility: "project" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("asks for confirmation before applying the entire workspace scope", async () => {
    const onOpenChange = vi.fn();
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <I18nProvider locale="en" resources={resources}>
          <ShareScopeDialog
            open
            onOpenChange={onOpenChange}
            target={{ kind: "project", resourceId: "project-1", currentScope: "private" }}
          />
        </I18nProvider>
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("radio", { name: /Entire workspace/ }));
    fireEvent.click(screen.getByRole("button", { name: /Save as Entire workspace/ }));
    expect(await screen.findByText(/Apply to all project resources/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Apply sharing/ }));
    await waitFor(() => expect(mutateProject).toHaveBeenCalledWith({ projectId: "project-1", visibility: "workspace" }));
  });

  it("allows a repository in a project to use project-member sharing", async () => {
    const onOpenChange = vi.fn();
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <I18nProvider locale="en" resources={resources}>
          <ShareScopeDialog
            open
            onOpenChange={onOpenChange}
            target={{ kind: "repo", resourceId: "https://github.com/acme/app.git", projectId: "project-1", currentScope: "private" }}
          />
        </I18nProvider>
      </QueryClientProvider>,
    );
    const projectRadio = screen.getByRole("radio", { name: /Project members/ });
    expect(projectRadio).not.toBeDisabled();
    fireEvent.click(projectRadio);
    fireEvent.click(screen.getByRole("button", { name: /Save as Project members/ }));
    await waitFor(() => expect(mutateRepo).toHaveBeenCalledWith({ url: "https://github.com/acme/app.git", visibility: "project" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("wires the project-member management entry", async () => {
    const onManageMembers = vi.fn();
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <I18nProvider locale="en" resources={resources}>
          <ShareScopeDialog
            open
            onOpenChange={vi.fn()}
            target={{ kind: "project", resourceId: "project-1", currentScope: "private" }}
            onManageMembers={onManageMembers}
          />
        </I18nProvider>
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("radio", { name: /Project members/ }));
    fireEvent.click(await screen.findByRole("button", { name: /Manage project members/ }));
    expect(onManageMembers).toHaveBeenCalledTimes(1);
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
