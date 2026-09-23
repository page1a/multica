// @vitest-environment jsdom

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  Attachment,
  Issue,
  IssueDraftPayload,
  IssuePriority,
  IssueStatus,
} from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { IssueDraftPreviewPanel } from "./issue-draft-preview-panel";

/**
 * The preview panel is the write side of an alignment: what it shows is what
 * "confirm and create" will hand the server, and what it holds when someone
 * presses save is what the server will store. These pin the two ways that
 * contract broke in the DENE-317 acceptance run — a generate that never came
 * back to the editor, and a picker that kept writing to a draft the server had
 * already retired.
 */

const mocks = vi.hoisted(() => ({
  // What the real picker's empty-selection seed does: it calls `onSelect`
  // whenever it is asked to show no runtime. The alignment page's selection is
  // server state, so that is the whole window after a confirm.
  seedRuntimeId: "rt-2" as string,
}));

vi.mock("../../agents/components/runtime-picker", () => ({
  RuntimePicker: ({ onSelect }: { onSelect: (id: string) => void }) => {
    // No dependency array: this is the shape that made the real picker seed on
    // every render of a parent whose callback identity changed.
    useEffect(() => {
      if (mocks.seedRuntimeId) onSelect(mocks.seedRuntimeId);
    });
    return <div data-testid="runtime-picker" />;
  },
}));

// The confirmed group's rows are links to issues; the path helper needs a
// workspace-scoped route this suite does not mount.
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));

vi.mock("../components/pickers/status-picker", () => ({
  StatusPicker: ({ onUpdate }: { onUpdate: (u: { status: IssueStatus | null }) => void }) => (
    <button type="button" onClick={() => onUpdate({ status: "todo" })}>
      status-picker
    </button>
  ),
}));

vi.mock("../components/pickers/priority-picker", () => ({
  PriorityPicker: ({
    onUpdate,
  }: {
    onUpdate: (u: { priority: IssuePriority | null }) => void;
  }) => (
    <button type="button" onClick={() => onUpdate({ priority: "high" })}>
      priority-picker
    </button>
  ),
}));

// The project picker is a real component with its own suite; here it is reduced
// to the only two things this suite asserts about it — the value it shows and
// the update it hands back.
vi.mock("../../projects/components/project-picker", () => ({
  ProjectPicker: ({
    projectId,
    onUpdate,
  }: {
    projectId: string | null;
    onUpdate: (u: { project_id?: string | null }) => void;
  }) => (
    <button
      type="button"
      data-testid="project-picker"
      data-project-id={projectId ?? "none"}
      onClick={() => onUpdate({ project_id: "proj-9" })}
    >
      project-picker
    </button>
  ),
}));

// The stage and assignee pickers are real components mounted on the issue
// surfaces; here they are reduced to the only thing this suite asserts about
// them — the update they hand back, and which row it belongs to.
vi.mock("../components/pickers/stage-picker", () => ({
  StagePicker: ({
    stage,
    onUpdate,
  }: {
    stage: number | null;
    onUpdate: (u: { stage: number | null }) => void;
  }) => (
    <button type="button" onClick={() => onUpdate({ stage: 2 })}>
      stage-picker:{String(stage)}
    </button>
  ),
}));

vi.mock("../components/pickers/assignee-picker", () => ({
  AssigneePicker: ({
    assigneeId,
    onUpdate,
  }: {
    assigneeId: string | null;
    onUpdate: (u: { assignee_type?: string | null; assignee_id?: string | null }) => void;
  }) => (
    <>
      <button
        type="button"
        onClick={() => onUpdate({ assignee_type: "agent", assignee_id: "ag-9" })}
      >
        assignee-picker:{String(assigneeId)}
      </button>
      <button
        type="button"
        aria-label={`clear-assignee:${String(assigneeId)}`}
        onClick={() => onUpdate({ assignee_type: null, assignee_id: null })}
      >
        clear-assignee
      </button>
    </>
  ),
}));

// The record's and the confirmed group's rows are links; a plain anchor is what
// this suite can assert on without a platform navigation adapter.
vi.mock("../../navigation", () => ({
  AppLink: ({
    href,
    children,
    ...rest
  }: {
    href: string;
    children: React.ReactNode;
  } & React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
  useNavigation: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => path,
  }),
  useBackOrReplace: () => vi.fn(),
}));

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

const STORED: IssueDraftPayload = {
  title: "",
  description: "收件箱只能逐条标记已读",
  status: "",
  priority: "",
};

/** What the carrier's block turns that into, once folded in. */
const MERGED: IssueDraftPayload = {
  title: "收件箱支持批量标记已读",
  description: "## 问题\n\n收件箱目前只能逐条标记已读。",
  status: "",
  priority: "",
};

type PanelProps = React.ComponentProps<typeof IssueDraftPreviewPanel>;

/**
 * The two shapes the conversation can produce, as the transcript reports them:
 * a screenshot someone dropped in, and an HTML prototype the carrier uploaded.
 * `markdown_url` is the durable URL a body embeds; `download_url` is the
 * click-time one this response also carries.
 */
function attachmentRecord(overrides: Partial<Attachment>): Attachment {
  return {
    id: "att-1",
    workspace_id: "ws-1",
    issue_id: null,
    comment_id: null,
    chat_session_id: "sess-1",
    chat_message_id: "m1",
    uploader_type: "member",
    uploader_id: "user-1",
    filename: "prototype.png",
    url: "/uploads/prototype.png",
    download_url: "/api/attachments/att-1/download",
    markdown_url: "/api/attachments/att-1/download",
    content_type: "image/png",
    size_bytes: 2048,
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const PROTOTYPE = attachmentRecord({});
const SPEC = attachmentRecord({
  id: "att-2",
  filename: "spec.html",
  content_type: "text/html",
  url: "/uploads/spec.html",
  download_url: "/api/attachments/att-2/download",
  markdown_url: "/api/attachments/att-2/download",
});

function renderPanel(overrides: Partial<PanelProps> = {}) {
  const props: PanelProps = {
    draft: STORED,
    stage: "aligning",
    canConfirm: false,
    saving: false,
    saved: false,
    confirming: false,
    abandoning: false,
    runtime: null,
    runtimes: [],
    runtimesLoading: false,
    members: [],
    currentUserId: null,
    switchingRuntime: false,
    pending: false,
    onDirtyChange: vi.fn(),
    onSave: vi.fn().mockResolvedValue(true),
    onGenerate: vi.fn().mockResolvedValue(MERGED),
    onConfirm: vi.fn().mockResolvedValue(true),
    onAbandon: vi.fn().mockResolvedValue(true),
    onSwitchRuntime: vi.fn().mockResolvedValue("rt-2"),
    ...overrides,
  };
  const element = (next: PanelProps) => (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <IssueDraftPreviewPanel {...next} />
    </I18nProvider>
  );
  const utils = render(element(props));
  return {
    ...utils,
    props,
    /** Re-render with a changed prop, as the page does when the server answers. */
    rerenderWith: (next: Partial<PanelProps>) =>
      utils.rerender(element({ ...props, ...next })),
  };
}

function titleInput(): HTMLInputElement {
  return screen.getByLabelText("Title") as HTMLInputElement;
}

function descriptionInput(): HTMLTextAreaElement {
  return screen.getByLabelText("Description") as HTMLTextAreaElement;
}

beforeEach(() => {
  mocks.seedRuntimeId = "rt-2";
});
afterEach(() => cleanup());

describe("IssueDraftPreviewPanel generate", () => {
  it("shows the carrier's folded draft instead of the values it replaced", async () => {
    const { props, rerenderWith } = renderPanel();
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });

    fireEvent.click(screen.getByRole("button", { name: "Generate preview" }));
    await waitFor(() => expect(props.onGenerate).toHaveBeenCalledTimes(1));
    // The user's own edit is part of what they asked to have folded in.
    expect(props.onGenerate).toHaveBeenCalledWith({
      ...STORED,
      title: "手打的标题",
    });

    // The save response is what the page re-renders with.
    rerenderWith({ draft: MERGED });
    await waitFor(() => expect(titleInput().value).toBe(MERGED.title));
    expect(descriptionInput().value).toBe(MERGED.description);
  });

  it("saves what was generated, not the pre-generate values", async () => {
    // The data-loss shape from the acceptance run: the panel kept showing the
    // old title and the seed description, so the next save wrote them straight
    // back over the freshly generated draft and dropped it out of `ready`.
    const onSave = vi.fn().mockResolvedValue(true);
    const { props, rerenderWith } = renderPanel({ onSave });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate preview" }));
    await waitFor(() => expect(titleInput().value).toBe(MERGED.title));

    // The save response is what the page re-renders with; the editor must be
    // clean against it, not still holding the pre-generate values.
    rerenderWith({ draft: MERGED });
    await waitFor(() => expect(props.onDirtyChange).toHaveBeenLastCalledWith(false));

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith(MERGED, "draft");
  });

  it("keeps a ready draft ready when the edited preview is saved", async () => {
    // The save button refines the words; it is not a way to un-converge. The
    // server reads an omitted status as `draft`, so a save over a generated
    // preview used to close the confirm gate again (DENE-319).
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({ stage: "ready", draft: { ...STORED, title: "T" }, onSave });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave.mock.calls[0]?.[1]).toBe("ready");
  });

  it("keeps the edits when the generate wrote nothing", async () => {
    // No title anywhere yet is the one case generate cannot fix; the editor
    // must hold on to what the user typed rather than clear itself.
    const { props } = renderPanel({
      onGenerate: vi.fn().mockResolvedValue(null),
    });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate preview" }));
    await waitFor(() => expect(props.onGenerate).toHaveBeenCalledTimes(1));
    expect(titleInput().value).toBe("手打的标题");
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(true);
  });
  it("offers generate before there is a title to save", async () => {
    // The carrier's block is the only place a title comes from before someone
    // types one, so a generate gated on the title deadlocks the step that
    // supplies it.
    renderPanel();
    expect(screen.getByRole("button", { name: "Generate preview" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Save draft" })).toBeDisabled();
  });
});

describe("IssueDraftPreviewPanel server revisions", () => {
  it("adopts a reply the carrier folded in while the editor was untouched", () => {
    // The acceptance run's starting state: the panel mounted on the seed draft
    // (no title, the user's own words), the carrier's proposal was folded into
    // the server draft behind it, and the panel stayed on the seed — reporting
    // an unsaved edit the user never made, which is what blocked every later
    // fold and let the next save overwrite the generated draft.
    const { props, rerenderWith } = renderPanel();
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(false);

    rerenderWith({ draft: MERGED });
    expect(titleInput().value).toBe(MERGED.title);
    expect(descriptionInput().value).toBe(MERGED.description);
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it("keeps a real edit when a server revision lands under it", () => {
    const onDirtyChange = vi.fn();
    const { rerenderWith } = renderPanel({ onDirtyChange });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    rerenderWith({ draft: MERGED });
    expect(titleInput().value).toBe("手打的标题");
    expect(descriptionInput().value).toBe(STORED.description);
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });

  it("is clean again once the server has stored what was on screen", async () => {
    const onSave = vi.fn().mockResolvedValue(true);
    const { props, rerenderWith } = renderPanel({ onSave });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));

    rerenderWith({ draft: { ...STORED, title: "手打的标题" } });
    await waitFor(() => expect(props.onDirtyChange).toHaveBeenLastCalledWith(false));
  });
});

describe("IssueDraftPreviewPanel runtime seeding", () => {
  it("does not write a seeded runtime to a draft the server no longer has", () => {
    // `draft === null` is the window after a confirm: the row is gone from the
    // unfinished list while the page is still on screen. The picker still seeds
    // an empty selection, and that seed must not become a rebind request per
    // render against a retired draft.
    const { props } = renderPanel({ draft: null, stage: "creating" });
    expect(screen.getByTestId("runtime-picker")).toBeTruthy();
    expect(props.onSwitchRuntime).not.toHaveBeenCalled();
  });

  it("still applies a picker selection while the draft is live", () => {
    const { props } = renderPanel({ runtime: null });
    expect(props.onSwitchRuntime).toHaveBeenCalledWith("rt-2");
  });

  it("ignores the seed once the draft is created", () => {
    const { props } = renderPanel({ stage: "created" });
    expect(props.onSwitchRuntime).not.toHaveBeenCalled();
  });
});

/**
 * The group half of the panel (DENE-411): what confirming will create, and what
 * it will start.
 *
 * The load-bearing fact is that a confirm can enqueue several agents at once,
 * and only the stage decides how many. A preview that showed the group without
 * saying which rows run would be asking the user to approve something they
 * cannot see; so the assertions here are about the counts and the per-row
 * badges, not about the rows existing.
 */
const GROUP: IssueDraftPayload = {
  title: "收件箱支持批量标记已读",
  description: "d",
  status: "",
  priority: "",
  children: [
    {
      key: "c1",
      title: "后端接口",
      description: "",
      status: "todo",
      priority: "none",
      stage: 1,
      assignee_type: "agent",
      assignee_id: "ag-1",
      assignee_hint: "backend",
    },
    {
      key: "c2",
      title: "前端页面",
      description: "",
      status: "backlog",
      priority: "none",
      stage: 2,
      assignee_type: "agent",
      assignee_id: "ag-2",
      assignee_hint: "frontend",
    },
  ],
};

function childTitleInput(n: number): HTMLInputElement {
  return screen.getByLabelText(`Sub-issue ${n} title`) as HTMLInputElement;
}

/**
 * The two shapes the parent line can take, and the difference is not cosmetic
 * (DENE-755). `PARENT_COORDINATES` is a group whose parent carries an agent or
 * squad: the stage barrier wakes it when a stage closes, so it really does
 * coordinate a chain. `PARENT_UNWOKEN` is a group where nobody would be woken —
 * an unassigned parent, or one held by a person — and saying only "it
 * coordinates" there reads as a chain that is closed when the later stages will
 * in fact sit in Backlog forever.
 */
const PARENT_COORDINATES =
  "The parent coordinates this group, so it starts no work of its own at confirm.";
const PARENT_UNWOKEN =
  "The parent coordinates this group, but no agent or squad holds it: when a stage closes, nobody is woken to promote the next one — it stays in Backlog until a person promotes it.";
const PARENT_UNASSIGNED_NOTICE =
  "Unassigned — you can still confirm and assign it later.";

function savedPayload(onSave: ReturnType<typeof vi.fn>): IssueDraftPayload {
  return onSave.mock.calls[0]?.[0] as IssueDraftPayload;
}

describe("IssueDraftPreviewPanel single-issue drafts", () => {
  it("shows no group section for a draft with no sub-issues", () => {
    // The old shape is a group of one, not a legacy branch: no heading, no
    // empty list, no counts that would read as something missing.
    renderPanel({ draft: { ...STORED, title: "T" } });
    expect(screen.queryByText("Sub-issues")).toBeNull();
    expect(screen.queryByText("Parent issue")).toBeNull();
    expect(screen.queryByText(/Confirming creates/)).toBeNull();
  });
});

describe("IssueDraftPreviewPanel group", () => {
  it("lists the whole group and says which rows start on confirm", () => {
    renderPanel({ draft: GROUP, stage: "ready", canConfirm: true });

    expect(screen.getByText("Sub-issues")).toBeTruthy();
    expect(screen.getByText("Parent issue")).toBeTruthy();
    expect(childTitleInput(1).value).toBe("后端接口");
    expect(childTitleInput(2).value).toBe("前端页面");
    expect(screen.getByText("Suggested: backend")).toBeTruthy();

    // Exactly one row starts an agent; the other is parked and the panel says
    // so, because a confirm that starts two agents is not what this preview
    // should imply.
    expect(screen.getAllByText("Runs immediately")).toHaveLength(1);
    expect(screen.getAllByText("Waits for its stage")).toHaveLength(1);

    expect(screen.getByText("Confirming creates 3 issues.")).toBeTruthy();
    expect(screen.getByText("Starting right away: 1.")).toBeTruthy();
    expect(
      screen.getByText("Created in Backlog, waiting for their stage: 1."),
    ).toBeTruthy();
    // And the parent, which is created but starts nothing. This fixture's parent
    // is UNASSIGNED, so the line may not stop at "it coordinates": with no agent
    // or squad on the parent, `notifyParentOfChildDone` wakes nobody and every
    // later stage stays parked. The coordination-only line belongs to a parent
    // something can actually be woken on.
    expect(screen.getByText(PARENT_UNWOKEN)).toBeTruthy();
    expect(screen.queryByText(PARENT_COORDINATES)).toBeNull();
  });

  it("writes the deleted sub-issue out of the payload it saves", () => {
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({ draft: GROUP, stage: "ready", onSave });

    fireEvent.click(screen.getByRole("button", { name: "Remove sub-issue 2" }));
    expect(screen.queryByText("前端页面")).toBeNull();
    expect(screen.getByText("Confirming creates 2 issues.")).toBeTruthy();
    expect(
      screen.queryByText("Created in Backlog, waiting for their stage: 1."),
    ).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    return waitFor(() => expect(onSave).toHaveBeenCalledTimes(1)).then(() => {
      const sent = savedPayload(onSave);
      expect(sent.children?.map((child) => child.key)).toEqual(["c1"]);
      expect(sent.children?.[0]?.title).toBe("后端接口");
    });
  });

  it("re-derives the status and the counts when a stage is edited", () => {
    // The user moves the only sub-issue to stage 2: nothing runs on confirm,
    // and the summary has to stop promising that something will.
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({
      draft: { ...STORED, title: "T", children: [GROUP.children![0]!] },
      stage: "ready",
      onSave,
    });
    expect(screen.getByText("Starting right away: 1.")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "stage-picker:1" }));
    expect(screen.queryByText("Starting right away: 1.")).toBeNull();
    expect(screen.getByText("Waits for its stage")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    return waitFor(() => expect(onSave).toHaveBeenCalledTimes(1)).then(() => {
      // The stage is what the confirm dispatches by, so the derived status has
      // to be in the payload the server stores — the confirm sends a revision,
      // not a payload, and cannot derive anything itself.
      expect(savedPayload(onSave).children?.[0]?.status).toBe("backlog");
    });
  });

  it("takes an assignee the user picked for a sub-issue", () => {
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({ draft: GROUP, stage: "ready", onSave });

    fireEvent.click(screen.getByRole("button", { name: "assignee-picker:ag-2" }));
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    return waitFor(() => expect(onSave).toHaveBeenCalledTimes(1)).then(() => {
      const sent = savedPayload(onSave);
      expect(sent.children?.[1]?.assignee_type).toBe("agent");
      expect(sent.children?.[1]?.assignee_id).toBe("ag-9");
      // The row that was not touched keeps what it had.
      expect(sent.children?.[0]?.assignee_id).toBe("ag-1");
    });
  });

  it("counts a row with no agent as starting nothing", () => {
    // An unassigned sub-issue is created and enqueues nothing; calling it
    // "runs immediately" would tell the user an agent had been paged.
    renderPanel({
      draft: {
        ...STORED,
        title: "T",
        children: [{ ...GROUP.children![0]!, assignee_type: null, assignee_id: null }],
      },
      stage: "ready",
    });
    expect(screen.getByText("Unassigned, won't auto-start")).toBeTruthy();
    expect(screen.queryByText(/Starting right away/)).toBeNull();
  });

  it("will not confirm a group the user has edited but not saved", async () => {
    // The confirm sends a revision, not a payload: it creates what the SERVER
    // holds. Offering it right after a row was deleted would create that row
    // anyway and start its agent, directly under a count that just said it
    // would not — the one thing this preview exists to prevent.
    const onConfirm = vi.fn().mockResolvedValue(true);
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({
      draft: GROUP,
      stage: "ready",
      canConfirm: true,
      onConfirm,
      onSave,
    });

    const confirm = screen.getByRole("button", { name: /Confirm and create/ });
    expect(confirm).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: "Remove sub-issue 2" }));
    expect(screen.getByText("Confirming creates 2 issues.")).toBeTruthy();
    expect(confirm).toBeDisabled();
    expect(
      screen.getByText(
        "Save your edits first — confirming creates the saved draft, not what is on screen.",
      ),
    ).toBeTruthy();

    // Saving is what makes the two agree, and the button comes back.
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(confirm).toBeEnabled());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("lists the group the confirm created, by identifier", () => {
    renderPanel({
      draft: GROUP,
      stage: "created",
      createdIssues: [
        { id: "i1", identifier: "MUL-1", title: "Parent", status: "todo" },
        { id: "i2", identifier: "MUL-2", title: "Child", status: "backlog" },
      ],
    });
    expect(screen.getByText("2 issues created")).toBeTruthy();
    expect(screen.getByRole("link", { name: /MUL-1/ })).toHaveAttribute(
      "href",
      "/acme/issues/i1",
    );
    expect(screen.getByRole("link", { name: /MUL-2/ })).toBeTruthy();
  });

  it("falls back to the parent alone when the confirm reported no group", () => {
    // A backend that predates groups answers with `issue_id` only. One link is
    // the honest rendering of that, not an error.
    renderPanel({
      draft: GROUP,
      stage: "created",
      producedIssueId: "i1",
      createdIssues: null,
    });
    expect(screen.queryByText(/issues created/)).toBeNull();
    expect(screen.getByRole("link", { name: /Open the issue/ })).toHaveAttribute(
      "href",
      "/acme/issues/i1",
    );
  });

  it("names the seat a confirm could not apply", () => {
    // The seat the user saw beside the row is not the seat the issue got. This
    // footer is the only place that difference is ever visible, and the fix
    // (assigning it) happens on the issue, which the row already links to.
    renderPanel({
      draft: GROUP,
      stage: "created",
      createdIssues: [
        { id: "i1", identifier: "MUL-1", title: "Parent", status: "todo" },
        { id: "i2", identifier: "MUL-2", title: "Child", status: "backlog" },
      ],
      assignmentWarnings: [
        {
          key: "c1",
          title: "Child",
          reason: "cannot assign to archived agent",
        },
      ],
    });
    expect(
      screen.getByText(
        "“Child” could not be assigned, so it was created unassigned.",
      ),
    ).toBeTruthy();
  });

  it("counts several dropped seats instead of listing them all", () => {
    renderPanel({
      draft: GROUP,
      stage: "created",
      createdIssues: [
        { id: "i1", identifier: "MUL-1", title: "Parent", status: "todo" },
      ],
      assignmentWarnings: [
        { key: "c1", title: "Child one", reason: "archived agent" },
        { key: "c2", title: "Child two", reason: "archived agent" },
      ],
    });
    expect(
      screen.getByText(
        "2 issues could not be assigned, so they were created unassigned.",
      ),
    ).toBeTruthy();
  });

  it("says nothing about seats when every assignment landed", () => {
    // A warning about nothing is noise on the one screen where the user is
    // checking what the confirm actually created.
    renderPanel({
      draft: GROUP,
      stage: "created",
      createdIssues: [
        { id: "i1", identifier: "MUL-1", title: "Parent", status: "todo" },
      ],
      assignmentWarnings: [],
    });
    expect(screen.queryByText(/could not be assigned/)).toBeNull();
  });
});

/**
 * DENE-755: whether the parent line may promise a chain. The server wakes the
 * parent's assignee when a stage closes, but `notifyParentOfChildDone` skips a
 * parent held by a person (MUL-2538) and there is no seat at all to wake on an
 * unassigned one. In those two shapes every stage after the first stays in
 * Backlog with no comment and no inbox row, so the panel has to say so — the
 * coordination sentence alone reads as "this advances itself", which is the one
 * thing that is not true.
 */
describe("IssueDraftPreviewPanel parent wakeup", () => {
  it("says nobody will be woken when the parent is unassigned", () => {
    renderPanel({ draft: GROUP, stage: "ready", canConfirm: true });
    expect(screen.getByText(PARENT_UNWOKEN)).toBeTruthy();
    expect(screen.queryByText(PARENT_COORDINATES)).toBeNull();
  });

  it("says nobody will be woken when the parent is held by a person", () => {
    renderPanel({
      draft: { ...GROUP, assignee_type: "member", assignee_id: "u1" },
      stage: "ready",
      canConfirm: true,
    });
    expect(screen.getByText(PARENT_UNWOKEN)).toBeTruthy();
    expect(screen.queryByText(PARENT_COORDINATES)).toBeNull();
    // A parent with a real assignee is not "unassigned", whatever else the
    // footer has to say about it.
    expect(screen.queryByText(PARENT_UNASSIGNED_NOTICE)).toBeNull();
  });

  it("keeps the coordination line when an agent holds the parent", () => {
    renderPanel({
      draft: { ...GROUP, assignee_type: "agent", assignee_id: "ag-parent" },
      stage: "ready",
      canConfirm: true,
    });
    expect(screen.getByText(PARENT_COORDINATES)).toBeTruthy();
    expect(screen.queryByText(PARENT_UNWOKEN)).toBeNull();
    expect(screen.queryByText(PARENT_UNASSIGNED_NOTICE)).toBeNull();
  });

  it("keeps the coordination line when a squad holds the parent", () => {
    renderPanel({
      draft: { ...GROUP, assignee_type: "squad", assignee_id: "sq-parent" },
      stage: "ready",
      canConfirm: true,
    });
    expect(screen.getByText(PARENT_COORDINATES)).toBeTruthy();
    expect(screen.queryByText(PARENT_UNWOKEN)).toBeNull();
  });
});

/**
 * DENE-423: the project. The carrier is never told which one to use — it has no
 * project list and is not asked to guess — so if this panel does not offer it,
 * nothing downstream can. The three things pinned here are the three a user
 * would be misled by: whether the choice is saved, whether a later reply
 * reverts it, and whether a sub-issue appears to have one of its own.
 */
describe("IssueDraftPreviewPanel project", () => {
  it("saves the project the user picks, and reports the edit as unsaved", () => {
    const onSave = vi.fn().mockResolvedValue(true);
    const { props } = renderPanel({
      draft: { ...STORED, title: "T" },
      stage: "ready",
      onSave,
    });
    expect(screen.getByTestId("project-picker")).toHaveAttribute(
      "data-project-id",
      "none",
    );

    fireEvent.click(screen.getByTestId("project-picker"));

    // The project is an ordinary edit: the confirm reads the SAVED draft, so an
    // edit that did not mark itself dirty would be silently not-what-gets-created.
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(true);

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    return waitFor(() => expect(onSave).toHaveBeenCalledTimes(1)).then(() => {
      expect(savedPayload(onSave).project_id).toBe("proj-9");
    });
  });

  it("keeps a project edit when a carrier reply lands under it", () => {
    // The reply block cannot mention the project (`parseIssueDraftBlock` accepts
    // only the fields the carrier owns), so folding one in must not read as
    // "no project" and revert the choice.
    const onDirtyChange = vi.fn();
    const { rerenderWith } = renderPanel({
      draft: { ...STORED, title: "T" },
      onDirtyChange,
    });

    fireEvent.click(screen.getByTestId("project-picker"));
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    rerenderWith({ draft: { ...MERGED, project_id: "p-from-server" } });
    expect(screen.getByTestId("project-picker")).toHaveAttribute(
      "data-project-id",
      "proj-9",
    );
    expect(titleInput().value).toBe("T");
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });

  it("offers no project control on a sub-issue row", () => {
    // A sub-issue's project is backfilled from the parent inside the create
    // transaction, so a control on the row would be a choice the confirm
    // silently overwrites. The panel says the inheritance out loud instead.
    renderPanel({ draft: GROUP, stage: "ready" });

    expect(screen.getAllByTestId("project-picker")).toHaveLength(1);
    expect(childTitleInput(1)).toBeTruthy();
    expect(
      screen.getByText("Sub-issues are filed under the parent's project."),
    ).toBeTruthy();
  });
});

/**
 * DENE-421: the parent's own assignee. Sub-issue rows have had a picker since
 * DENE-411; the root had none, so a group could only ever be created with an
 * unassigned parent. Same reasoning as the project — the carrier has no
 * workspace roster to resolve a name against, so if the panel does not offer
 * it, nothing downstream can.
 */
describe("IssueDraftPreviewPanel parent assignee", () => {
  it("saves the assignee the user picks for the parent, and reports the edit", () => {
    const onSave = vi.fn().mockResolvedValue(true);
    const { props } = renderPanel({
      draft: { ...STORED, title: "T" },
      stage: "ready",
      onSave,
    });

    fireEvent.click(screen.getByRole("button", { name: "assignee-picker:null" }));
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(true);

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    return waitFor(() => expect(onSave).toHaveBeenCalledTimes(1)).then(() => {
      const sent = savedPayload(onSave);
      expect(sent.assignee_type).toBe("agent");
      expect(sent.assignee_id).toBe("ag-9");
    });
  });

  it("keeps a parent assignee edit when a carrier reply lands under it", () => {
    // Identical hazard to the project's: the carrier's block cannot carry an
    // assignee, so folding a reply in must not read as "unassigned" and revert.
    const onDirtyChange = vi.fn();
    const { rerenderWith } = renderPanel({
      draft: { ...STORED, title: "T" },
      onDirtyChange,
    });

    fireEvent.click(screen.getByRole("button", { name: "assignee-picker:null" }));
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    rerenderWith({ draft: MERGED });
    expect(screen.getByRole("button", { name: "assignee-picker:ag-9" })).toBeTruthy();
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });
});

/**
 * DENE-415: a continuation round. The same conversation reopens on a group that
 * already exists, and the next confirm adds to it instead of adopting it whole.
 * The two things this suite pins are the two a person would be misled by: which
 * rows are already real, and what the confirm in front of them will create.
 */
describe("IssueDraftPreviewPanel continuation round", () => {
  const adopted = (id: string, identifier: string, title: string): Issue =>
    ({
      id,
      identifier,
      title,
      status: "todo",
      stage: null,
    }) as unknown as Issue;

  it("separates what exists from what this round adds", () => {
    renderPanel({
      draft: {
        ...GROUP,
        children: [
          ...(GROUP.children ?? []),
          {
            key: "c3",
            title: "文档更新",
            description: "",
            status: "",
            priority: "",
            stage: 1,
            assignee_type: "agent",
            assignee_id: "ag-3",
            assignee_hint: null,
          },
        ],
      },
      round: 2,
      continuation: true,
      // The root ("") and c1/c2 own issues; c3 is the new one.
      builtKeys: new Set(["", "c1", "c2"]),
      builtChildren: [
        adopted("i1", "MUL-1", "后端接口"),
        adopted("i2", "MUL-2", "前端页面"),
      ],
    });

    expect(screen.getByText("Round 2")).toBeTruthy();
    expect(screen.getByText("Already created")).toBeTruthy();
    expect(screen.getByRole("link", { name: /MUL-1/ })).toHaveAttribute(
      "href",
      "/acme/issues/i1",
    );
    expect(screen.getByRole("link", { name: /MUL-2/ })).toHaveAttribute(
      "href",
      "/acme/issues/i2",
    );

    // Only the new row is editable, and it is the only one in the editable
    // list: an adopted row's fields are never written back, so offering them
    // for edit would promise a write the confirm does not perform. The parent
    // is adopted too, which is why its fields are locked on this round.
    expect(screen.getByDisplayValue("文档更新")).toBeTruthy();
    expect(screen.queryByDisplayValue("后端接口")).toBeNull();
    expect(screen.queryByDisplayValue("前端页面")).toBeNull();

    // The numbers under the button are about this round.
    expect(titleInput()).toBeDisabled();

    // The parent's status/priority pickers take no `disabled` prop, so the row
    // has to be inert as well as dimmed: dimming alone leaves their triggers in
    // the tab order, and a keyboard user would be able to change a field this
    // round's confirm silently drops.
    const parentFields = screen.getByText("Status").closest("div")?.parentElement;
    expect(parentFields).toHaveAttribute("inert");

    expect(screen.getByText("Confirming creates 1 new issue(s).")).toBeTruthy();
    expect(screen.getByText("Starting right away: 1.")).toBeTruthy();
    // The parent is adopted too, so it is part of what this round keeps.
    expect(screen.getByText("Kept as they are: 3.")).toBeTruthy();
    expect(screen.queryByText("Confirming creates 3 issues.")).toBeNull();
  });

  it("says so when the round adds nothing", () => {
    renderPanel({
      draft: GROUP,
      round: 3,
      continuation: true,
      builtKeys: new Set(["", "c1", "c2"]),
      builtChildren: [
        adopted("i1", "MUL-1", "后端接口"),
        adopted("i2", "MUL-2", "前端页面"),
      ],
    });
    expect(
      screen.getByText(
        "Confirming creates nothing new: every sub-issue here already exists.",
      ),
    ).toBeTruthy();
    expect(
      screen.getByText("Nothing new yet — keep talking and the added work appears here."),
    ).toBeTruthy();
  });

  it("counts every node of a first round as being created", () => {
    // Without a group behind it the panel is the DENE-411 one: nothing is
    // adopted, and every row is a create.
    renderPanel({ draft: GROUP, round: 1, continuation: false });
    expect(screen.getByText("Confirming creates 3 issues.")).toBeTruthy();
    expect(screen.queryByText("Already created")).toBeNull();
    // ...and the parent fields stay reachable, which is also what proves the
    // continuation suite's `inert` assertion is reading a live attribute.
    expect(
      screen.getByText("Status").closest("div")?.parentElement,
    ).not.toHaveAttribute("inert");
  });
});

describe("IssueDraftPreviewPanel carried files", () => {
  it("shows what the confirm hands to the parent task", () => {
    // Both shapes are listed the same way; only the image gets a thumbnail,
    // because a prototype is looked at and a spec is opened.
    renderPanel({ attachments: [PROTOTYPE, SPEC] });
    expect(screen.getByText("Reference files")).toBeTruthy();
    expect(
      screen.getByText(
        "After you confirm, these 2 files belong to the parent task. Sub-issues link them instead of uploading a copy.",
      ),
    ).toBeTruthy();
    expect(
      screen.getByText("prototype.png").closest("a"),
    ).toHaveAttribute("href", "/api/attachments/att-1/download");
    expect(screen.getByText("spec.html").closest("a")).toHaveAttribute(
      "href",
      "/api/attachments/att-2/download",
    );
  });

  it("counts one file in the singular", () => {
    // The hint names the files by number, so it inflects with them: a lone
    // prototype must not read as "these 1 files".
    renderPanel({ attachments: [PROTOTYPE] });
    expect(
      screen.getByText(
        "After you confirm, this file belongs to the parent task. Sub-issues link it instead of uploading a copy.",
      ),
    ).toBeTruthy();
  });

  it("falls back to the click-time URL when the server sends no durable one", () => {
    // `markdown_url` is additive: an older backend omits it, and a row whose
    // href resolved to the empty string would be a dead link.
    renderPanel({
      attachments: [{ ...PROTOTYPE, markdown_url: "" }],
    });
    expect(screen.getByText("prototype.png").closest("a")).toHaveAttribute(
      "href",
      "/api/attachments/att-1/download",
    );
  });

  it("still names a file the server described with an id alone", () => {
    // `AttachmentSchema` validates the id and passes the rest through, so a
    // response carrying nothing else must still render: a name and no link,
    // never an anchor back at the page it is already on.
    renderPanel({
      attachments: [{ id: "att-9" } as Attachment],
    });
    expect(screen.getByText("Reference files")).toBeTruthy();
    expect(screen.getByText("att-9").closest("a")).toBeNull();
  });

  it("adds nothing at all when the alignment produced no files", () => {
    // No heading, no empty-state sentence, no row: a section about nothing is
    // what the project and assignee pickers do not have either.
    const { container } = renderPanel({ attachments: [] });
    expect(screen.queryByText("Reference files")).toBeNull();
    expect(screen.queryByText(/parent task/)).toBeNull();
    expect(screen.queryByRole("listitem")).toBeNull();
    expect(container.querySelectorAll("img")).toHaveLength(0);
  });

  it("adds nothing when the caller never mentions files", () => {
    // An older caller that does not know about the prop is the same statement
    // as an alignment with no uploads.
    renderPanel();
    expect(screen.queryByText("Reference files")).toBeNull();
  });
});

// The pure fill rules (once per row, never over a picked assignee) are covered
// in packages/core/issue-drafts/assignee-suggestions.test.ts. This suite keeps
// the wiring: a suggestion has to reach the SERVER draft, because the confirm
// creates what the server holds (DENE-691).
describe("assignee suggestions", () => {
  const READY: IssueDraftPayload = {
    title: "Ship it",
    description: "",
    status: "",
    priority: "",
    children: [{ key: "c1", title: "backend", description: "", status: "todo", priority: "" }],
  };
  const SEAT = { assignee_type: "agent" as const, assignee_id: "agent-1", name: "孙悟空", tier: "strong" };

  it("saves the suggested seats into the draft instead of only showing them", async () => {
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({ stage: "ready", draft: READY, onSave, assigneeSuggestions: [SEAT, null] });

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    const [saved, status] = onSave.mock.calls[0] as [IssueDraftPayload, string];
    expect(status).toBe("ready");
    expect(saved.assignee_id).toBe("agent-1");
    // Routing had no confident seat for the child: it stays unassigned.
    expect(saved.children?.[0]?.assignee_id ?? null).toBeNull();
  });

  it("does not touch the draft while a turn is running", async () => {
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({ stage: "ready", draft: READY, onSave, pending: true, assigneeSuggestions: [SEAT, SEAT] });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(onSave).not.toHaveBeenCalled();
  });

  it("keeps confirmation available and explains unavailable suggestions", () => {
    renderPanel({
      stage: "ready",
      draft: READY,
      canConfirm: true,
      assigneeSuggestionsError: true,
    });

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Suggestions are unavailable",
    );
    expect(screen.getAllByText(/Unassigned/).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByRole("button", { name: "Confirm and create" })).toBeEnabled();
  });

  it("shows the loading state while suggestions are being resolved", () => {
    renderPanel({ stage: "ready", draft: READY, assigneeSuggestionsLoading: true });
    expect(screen.getByRole("status")).toHaveTextContent("Finding suggested assignees");
  });

  it("persists a cleared child as unassigned when the same suggestion returns", async () => {
    const onSave = vi.fn().mockResolvedValue(true);
    const { rerenderWith } = renderPanel({ stage: "ready", draft: READY, onSave, assigneeSuggestions: [SEAT, SEAT] });

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    const clearButtons = screen.getAllByRole("button", { name: "clear-assignee:agent-1" });
    fireEvent.click(clearButtons[1]!);

    // A refetched answer is a new array, but the panel's offered-row memory
    // must keep the child empty after the user cleared it.
    const suggestionsAgain = [{ ...SEAT }, { ...SEAT }];
    rerenderWith({ assigneeSuggestions: suggestionsAgain });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2));
    const [saved] = onSave.mock.calls[1] as [IssueDraftPayload, string];
    expect(saved.children?.[0]?.assignee_id ?? null).toBeNull();
    expect(saved.children?.[0]?.assignee_type ?? null).toBeNull();
  });
});
