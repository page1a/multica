// Wiring, accessibility and the named regressions for the members roster.
// The tier ladder itself — which tiers are offered, which are blocked and
// what each change costs or grants — is the canonical business of
// packages/core/workspace/member-roles.test.ts and is NOT re-run here.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import type { MemberWithUser } from "@multica/core/types";

const updateMember = vi.hoisted(() => vi.fn());
const listMembers = vi.hoisted(() => vi.fn());
const listInvitations = vi.hoisted(() => vi.fn());
const listShareLinks = vi.hoisted(() => vi.fn());
const toastSuccess = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: {
      updateMember,
      listMembers,
      listInvitations,
      listShareLinks,
      // ActorAvatar resolves avatar URLs through the client's base url.
      getBaseUrl: () => "https://api.example.test",
    },
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
    // ActorAvatar builds member links from this; without a route provider
    // the real hook throws and takes the whole roster down with it.
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector: (s: { user: { id: string } }) => unknown) =>
      selector({ user: { id: "user-owner" } }),
    { getState: () => ({ user: { id: "user-owner" } }) },
  ),
}));

vi.mock("@multica/core/billing", () => ({
  usePreviewWorkspaceSeatPurchase: () => ({ mutateAsync: vi.fn(), isPending: false }),
  usePurchaseWorkspaceSeats: () => ({ mutateAsync: vi.fn(), isPending: false }),
  workspaceSubscriptionSummaryOptions: () => ({
    queryKey: ["billing", "summary"],
    queryFn: async () => null,
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: toastSuccess, error: vi.fn() },
}));

import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { MembersTab } from "./members-tab";

/** Minimal adapter so the roster's avatars can render their profile links.
 *  Views tests never mock next/* or react-router-dom — the adapter IS the
 *  seam the platform layers plug into. */
const TEST_NAVIGATION: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/settings",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path: string) => `https://app.example.test${path}`,
};

/** `role` widens to `string` so a test can stage a tier this build does not
 *  know — exactly what the lenient response schema lets through. */
function member(
  over: Partial<Omit<MemberWithUser, "role">> &
    Pick<MemberWithUser, "id"> & { role?: string },
): MemberWithUser {
  return {
    workspace_id: "ws-1",
    user_id: `user-${over.id}`,
    role: "member",
    created_at: "2026-01-01T00:00:00Z",
    name: "Nobody",
    email: "nobody@example.test",
    avatar_url: null,
    ...over,
  } as MemberWithUser;
}

const OWNER = member({
  id: "m-owner",
  user_id: "user-owner",
  role: "owner",
  name: "Ada Owner",
  email: "ada@example.test",
});
const TEAMMATE = member({
  id: "m-teammate",
  user_id: "user-teammate",
  role: "member",
  name: "Bo Member",
  email: "bo@example.test",
});

/** Pass `null` to leave `listMembers` on whatever the test already staged —
 *  the loading case needs a request that never settles. */
function renderTab(members: MemberWithUser[] | null = null) {
  if (members) roster = members;
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // Nested inside the element, not passed as `wrapper`: renderWithI18n owns
  // the wrapper slot, and a second one would blank every translated label.
  const result = renderWithI18n(
    <QueryClientProvider client={qc}>
      <NavigationProvider value={TEST_NAVIGATION}>
        <MembersTab />
      </NavigationProvider>
    </QueryClientProvider>,
  );
  return { ...result, qc };
}

/** The tier picker for one row, found by its per-member accessible name. */
function tierPicker(name: string): HTMLElement {
  return screen.getByRole("combobox", { name: new RegExp(`Tier for ${name}`) });
}

/** Roster the fake server owns, so a refetch after a write sees the write —
 *  a static mock would silently undo every optimistic patch and make the
 *  rollback assertions meaningless. */
let roster: MemberWithUser[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  roster = [OWNER, TEAMMATE];
  listMembers.mockImplementation(async () => roster);
  listInvitations.mockResolvedValue([]);
  listShareLinks.mockResolvedValue([]);
  updateMember.mockImplementation(async (_ws: string, id: string, data: { role: string }) => {
    roster = roster.map((m) => (m.id === id ? member({ ...m, role: data.role }) : m));
    return roster.find((m) => m.id === id)!;
  });
});

describe("MembersTab roster states", () => {
  it("shows placeholder rows while the roster is still loading", async () => {
    listMembers.mockReturnValue(new Promise(() => {}));
    renderTab();

    expect(screen.getByRole("status", { name: /Loading members/i })).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: /Tier for/ })).toBeNull();
  });

  it("offers the invite entry when nobody but you is on the roster", async () => {
    renderTab([OWNER]);

    await screen.findByText("No members yet");
    await userEvent.click(screen.getByRole("button", { name: /Invite a member/i }));
    // The prompt's only job is to land the person in the invite field.
    expect(screen.getByRole("textbox", { name: /user@company\.com/ })).toHaveFocus();
  });

  it("drops the prompt once someone else is on the roster", async () => {
    renderTab();

    await screen.findByText("Bo Member");
    expect(screen.queryByText("No members yet")).toBeNull();
  });

  it("renders one tier picker per editable row and none for yourself", async () => {
    renderTab();

    await screen.findByText("Bo Member");
    expect(tierPicker("Bo Member")).toBeInTheDocument();
    // An owner demoting themselves is the classic way to lock a workspace;
    // the server refuses it, so the row never offers it.
    expect(screen.queryByRole("combobox", { name: /Tier for Ada Owner/ })).toBeNull();
  });

  it("shows a tier this build does not know without inventing one for it", async () => {
    renderTab([OWNER, member({ id: "m-x", role: "superadmin", name: "Cy Future" })]);

    await screen.findByText("Cy Future");
    expect(screen.getByText("Unknown tier")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: /Tier for Cy Future/ })).toBeNull();
  });
});

describe("MembersTab tier changes", () => {
  it("updates the row and names what the change affects", async () => {
    // Hold the write open so the assertion below can only pass on the
    // optimistic patch, not on a refetch that already landed.
    let releaseWrite: () => void = () => {};
    const written = new Promise<void>((resolve) => {
      releaseWrite = resolve;
    });
    const serverWrite = updateMember.getMockImplementation()!;
    updateMember.mockImplementation(async (...args: unknown[]) => {
      await written;
      return serverWrite(...args);
    });

    renderTab();
    await screen.findByText("Bo Member");

    await userEvent.click(tierPicker("Bo Member"));
    await userEvent.click(await screen.findByRole("option", { name: /Admin/ }));

    await waitFor(() =>
      expect(updateMember).toHaveBeenCalledWith("ws-1", "m-teammate", { role: "admin" }),
    );
    // Row shows the new tier while the write is still in flight.
    await waitFor(() => expect(tierPicker("Bo Member")).toHaveTextContent("Admin"));
    releaseWrite();
    // And the person who made the change is told what it opened, which is
    // the whole point of the confirmation.
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled());
    const [title, opts] = toastSuccess.mock.calls.at(-1)!;
    expect(title).toMatch(/Bo Member is now Admin/);
    expect(opts.description).toMatch(/invite members and change tiers/);
  });

  it("rolls the row back and explains the failure in place", async () => {
    updateMember.mockRejectedValue(new Error("seat limit reached"));
    renderTab();
    await screen.findByText("Bo Member");

    await userEvent.click(tierPicker("Bo Member"));
    await userEvent.click(await screen.findByRole("option", { name: /Admin/ }));

    await waitFor(() => expect(screen.getByText("seat limit reached")).toBeInTheDocument());
    // The optimistic patch must not survive the rejection.
    expect(tierPicker("Bo Member")).toHaveTextContent("Member");
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("lets a member be moved down to guest", async () => {
    // Guest is released: the server rejects every guest write in one layer
    // (DENE-697), so the picker no longer holds the tier back.
    renderTab();
    await screen.findByText("Bo Member");

    await userEvent.click(tierPicker("Bo Member"));
    await userEvent.click(await screen.findByRole("option", { name: /Guest/ }));

    await waitFor(() =>
      expect(updateMember).toHaveBeenCalledWith("ws-1", "m-teammate", { role: "guest" }),
    );
  });
});
