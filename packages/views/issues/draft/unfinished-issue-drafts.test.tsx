import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftSummary } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import {
  UnfinishedIssueDraftsBanner,
  issueDraftPreview,
  issueDraftTitle,
} from "./unfinished-issue-drafts";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

/**
 * The drafts list is the ONLY route back into an alignment conversation — its
 * carrier is a `kind='system'` agent, so it is absent from every chat list. A
 * row whose title or preview renders the carrier's wire format is a row nobody
 * can recognise, which is the same as no list at all.
 *
 * Since DENE-443 the list is also where an alignment is GIVEN UP on, which is
 * why the one-draft shortcut is gone: it used to resume the only row directly,
 * and that is exactly the case where the discard action has nowhere else to
 * live.
 */

const mocks = vi.hoisted(() => ({ abandonIssueDraft: vi.fn() }));

vi.mock("@multica/core/api", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: { abandonIssueDraft: mocks.abandonIssueDraft },
  };
});

function summary(overrides: Partial<IssueDraftSummary>): IssueDraftSummary {
  return {
    chat_session_id: "sess-1",
    workspace_id: "ws-1",
    status: "draft",
    revision: 1,
    draft: { title: "", description: "", status: "", priority: "" },
    issue_id: null,
    policy: { key: "question", version: "1", guided: true },
    capabilities: { keys: [], version: "" },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "Align a new issue",
    runtime_id: "rt-1",
    last_message_content: "",
    last_message_role: "",
    last_message_at: "",
    ...overrides,
  };
}

describe("issueDraftTitle", () => {
  it("uses the draft's own title, not the constant chat session title", () => {
    // Every carrier session is titled "Align a new issue"; a list of those is
    // unreadable, so the structured title is the only candidate worth showing.
    expect(
      issueDraftTitle(summary({ draft: { title: "Dark mode", description: "", status: "", priority: "" } })),
    ).toBe("Dark mode");
    expect(issueDraftTitle(summary({}))).toBe("");
  });
});

describe("issueDraftPreview", () => {
  it("decodes the user's own turn out of the wire envelope", () => {
    const content =
      'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"add dark mode","current_draft":{"title":"","description":"","status":"","priority":""}}';
    expect(
      issueDraftPreview(summary({ last_message_role: "user", last_message_content: content })),
    ).toBe("add dark mode");
  });

  it("strips the carrier's draft block from an assistant turn", () => {
    expect(
      issueDraftPreview(
        summary({
          last_message_role: "assistant",
          last_message_content:
            'Which surfaces?\n<issue_draft>{"title":"Dark mode"}</issue_draft>',
        }),
      ),
    ).toBe("Which surfaces?");
  });
});

describe("UnfinishedIssueDraftsBanner", () => {
  const one = summary({
    chat_session_id: "sess-1",
    draft: { title: "Dark mode", description: "", status: "", priority: "" },
  });

  beforeEach(() => {
    mocks.abandonIssueDraft.mockReset();
  });

  function renderBanner(
    drafts: IssueDraftSummary[],
    onResume: (id: string) => void = () => {},
  ) {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    return render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <UnfinishedIssueDraftsBanner wsId="ws-1" drafts={drafts} onResume={onResume} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  }

  it("renders nothing when there is no unfinished draft", () => {
    renderBanner([]);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("names the draft by its structured title", () => {
    renderBanner([one]);
    expect(screen.getByRole("button").textContent).toContain("1 unfinished alignment");
  });

  it("opens the list even for a single draft instead of resuming it", async () => {
    // The shortcut this replaces was the only way to reach a lone draft, so it
    // also swallowed the only way to discard one.
    const onResume = vi.fn();
    renderBanner([one], onResume);

    await userEvent.click(screen.getByRole("button"));

    expect(onResume).not.toHaveBeenCalled();
    expect(await screen.findByText("Unfinished alignments")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Discard "Dark mode"/ })).toBeTruthy();
  });

  it("resumes the row the user clicks", async () => {
    const onResume = vi.fn();
    renderBanner([one], onResume);

    await userEvent.click(screen.getByRole("button"));
    await userEvent.click(await screen.findByText("Dark mode"));

    expect(onResume).toHaveBeenCalledWith("sess-1");
  });

  it("abandons the row only after the confirm is accepted", async () => {
    mocks.abandonIssueDraft.mockResolvedValue({ ...one, status: "abandoned" });
    const onResume = vi.fn();
    renderBanner([one], onResume);

    await userEvent.click(screen.getByRole("button"));
    await userEvent.click(await screen.findByRole("button", { name: /Discard "Dark mode"/ }));

    // The confirm says what the server actually does — archive, not delete.
    expect(await screen.findByText(/archived as abandoned/)).toBeTruthy();
    expect(mocks.abandonIssueDraft).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(mocks.abandonIssueDraft).not.toHaveBeenCalled();

    await userEvent.click(await screen.findByRole("button", { name: /Discard "Dark mode"/ }));
    await userEvent.click(await screen.findByRole("button", { name: /^Discard$/ }));

    await waitFor(() => expect(mocks.abandonIssueDraft).toHaveBeenCalledWith("sess-1"));
    // Opening a row is not a side effect of discarding it.
    expect(onResume).not.toHaveBeenCalled();
  });

  it("states the server's own reason when the abandon is refused", async () => {
    // Same rule DENE-422 set for the entry: a 4xx message is written for the
    // reader, so it is shown verbatim rather than collapsed into one sentence.
    mocks.abandonIssueDraft.mockRejectedValue(
      new ApiError("this alignment was already confirmed", 409, "Conflict"),
    );
    renderBanner([one]);

    await userEvent.click(screen.getByRole("button"));
    await userEvent.click(await screen.findByRole("button", { name: /Discard "Dark mode"/ }));
    await userEvent.click(await screen.findByRole("button", { name: /^Discard$/ }));

    expect(
      await screen.findByText("this alignment was already confirmed"),
    ).toBeTruthy();
    // The confirm stays up: a closed dialog would report success.
    expect(screen.getByText(/archived as abandoned/)).toBeTruthy();
  });
});
