import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import enSquads from "../../locales/en/squads.json";

const api = vi.hoisted(() => ({
  getSquad: vi.fn(),
  listSquadMembers: vi.fn(),
  getSquadMemberStatus: vi.fn(),
  listAgents: vi.fn(),
  listMembers: vi.fn(),
  updateSquad: vi.fn(),
}));
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }));

vi.mock("@multica/core/api", () => ({ api }));
vi.mock("sonner", () => ({ toast }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
  useWorkspacePaths: () => ({ squads: () => "/acme/squads", newAgent: () => "/acme/agents/new" }),
}));
vi.mock("@multica/core/auth", () => {
  const state = { user: { id: "u-creator" } };
  return { useAuthStore: (selector: (s: typeof state) => unknown) => selector(state) };
});
vi.mock("../../navigation", () => ({
  useNavigation: () => ({ pathname: "/acme/squads/sq-1", push: vi.fn() }),
  AppLink: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));
vi.mock("../../layout/breadcrumb-header", () => ({
  BreadcrumbHeader: ({ leaf }: { leaf: React.ReactNode }) => <header>{leaf}</header>,
}));
vi.mock("../../layout/page-header", () => ({ PageHeader: () => null }));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../../common/avatar-upload-control", () => ({ AvatarUploadControl: () => null }));
vi.mock("../../editor/content-editor", () => ({ ContentEditor: () => null }));

import { SquadDetailPage } from "./squad-detail-page";

const squad = {
  id: "sq-1",
  workspace_id: "ws-1",
  name: "Core",
  description: "",
  instructions: "",
  avatar_url: null,
  leader_id: "a-1",
  creator_id: "u-creator",
  created_at: "2026-01-01",
  updated_at: "2026-01-01",
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <SquadDetailPage />
    </QueryClientProvider>,
  );
}

describe("SquadDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.getSquad.mockResolvedValue(squad);
    api.listSquadMembers.mockResolvedValue([]);
    api.getSquadMemberStatus.mockResolvedValue({ members: [] });
    api.listAgents.mockResolvedValue([]);
    api.listMembers.mockResolvedValue([{ user_id: "u-creator", role: "member", name: "Creator", email: "c@example.com" }]);
  });

  it("says why when the server refuses a description change", async () => {
    api.updateSquad.mockRejectedValue(new Error("only the squad creator can change this"));
    renderPage();

    fireEvent.click(await screen.findByText(enSquads.description_dialog.placeholder_empty));
    fireEvent.change(screen.getByPlaceholderText(enSquads.description_dialog.responsibility_placeholder), {
      target: { value: "Ships the core" },
    });
    fireEvent.click(screen.getByRole("button", { name: enSquads.description_dialog.save }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("only the squad creator can change this"));
    // The draft stays in front of the user.
    expect(screen.getByDisplayValue("Ships the core")).toBeInTheDocument();
  });

  it("reports a refused rename once", async () => {
    api.updateSquad.mockRejectedValue(new Error("only the squad creator can change this"));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Core" }));
    fireEvent.change(screen.getByPlaceholderText(enSquads.name_editor.placeholder), { target: { value: "Platform" } });
    fireEvent.click(screen.getByRole("button", { name: enSquads.name_editor.save }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("only the squad creator can change this"));
    expect(toast.error).toHaveBeenCalledTimes(1);
  });
});
