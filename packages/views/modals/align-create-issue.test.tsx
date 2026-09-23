import { forwardRef, useImperativeHandle, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftPayload, RuntimeDevice } from "@multica/core/types";
import enCommon from "../locales/en/common.json";
import enIssues from "../locales/en/issues.json";
import enModals from "../locales/en/modals.json";
import enEditor from "../locales/en/editor.json";
import enProjects from "../locales/en/projects.json";
import enAgents from "../locales/en/agents.json";
import { AlignCreatePanel } from "./align-create-issue";

/**
 * The alignment face's contract is narrow on purpose: open the conversation,
 * hand the request over, leave. It must never create an issue — and it must
 * navigate even when the first turn fails, because a draft the user cannot see
 * is a draft they will create again.
 *
 * DENE-370 moved this face INSIDE the create-issue dialog. What that adds is
 * the shared input: the alignment request lives in the create draft's own
 * `align` slot, and its attachments ride the same pool "New issue" uses.
 */

interface CreateSessionInput {
  runtime_id: string;
  model?: string;
  thinking_level?: string;
  capabilities?: string[];
  draft?: Partial<IssueDraftPayload>;
}

const mocks = vi.hoisted(() => ({
  drafts: [] as unknown[],
  runtimes: [] as unknown[],
  createIssueDraftSession: vi.fn(),
  sendChatMessage: vi.fn(),
  listIssueDrafts: vi.fn(),
  listRuntimes: vi.fn(),
  listMembers: vi.fn(),
  initiateListModels: vi.fn(),
  getListModelsResult: vi.fn(),
  push: vi.fn(),
  close: vi.fn(),
  currentUserId: "user-1" as string | null,
  setAlign: vi.fn(),
  setShared: vi.fn(),
  setActiveMode: vi.fn(),
}));

vi.mock("@multica/core/api", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      createIssueDraftSession: mocks.createIssueDraftSession,
      sendChatMessage: mocks.sendChatMessage,
      listIssueDrafts: mocks.listIssueDrafts,
      listRuntimes: mocks.listRuntimes,
      listMembers: mocks.listMembers,
      initiateListModels: mocks.initiateListModels,
      getListModelsResult: mocks.getListModelsResult,
    },
  };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } | null }) => unknown) =>
    selector({
      user: mocks.currentUserId ? { id: mocks.currentUserId } : null,
    }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issues: () => "/acme/issues",
    newIssueDraft: (id: string) => `/acme/issues/new/${id}`,
    runtimes: () => "/acme/runtimes",
  }),
}));

// The upload pool is the SHARED create-dialog pool, so this face owns no
// uploads of its own — it renders what the draft store holds. Mocked in the
// Zustand callable-store shape the repo's tests use, with the writers recorded
// so a spec can prove a body edit lands in the align slot.
const draftStore = {
  draft: {
    shared: {
      projectId: undefined as string | undefined,
      priority: "none" as const,
      dueDate: null as string | null,
      attachments: [] as unknown[],
    },
    manual: { title: "", description: "" },
    agent: { prompt: "" },
    align: { request: "" } as {
      request: string;
      capabilities?: string[];
    },
    activeMode: "manual" as string,
  },
  setAlign: mocks.setAlign,
  setShared: mocks.setShared,
  setActiveMode: mocks.setActiveMode,
};

// The real store is a zustand subscription, so a write made through a picker
// re-renders the face that reads it. The mock has to do the same or a spec
// could never observe the picker's own update: it would assert against the
// render that preceded the click.
const draftStoreListeners = new Set<() => void>();
let draftStoreRevision = 0;
function subscribeDraftStore(listener: () => void) {
  draftStoreListeners.add(listener);
  return () => {
    draftStoreListeners.delete(listener);
  };
}
function getDraftStoreRevision() {
  return draftStoreRevision;
}
function writeDraftStore(next: typeof draftStore.draft) {
  draftStore.draft = next;
  draftStoreRevision += 1;
  draftStoreListeners.forEach((listener) => listener());
}

vi.mock("@multica/core/issues/stores", () => ({
  useIssueDraftStore: Object.assign(
    (selector?: (state: typeof draftStore) => unknown) => {
      useSyncExternalStore(subscribeDraftStore, getDraftStoreRevision);
      return selector ? selector(draftStore) : draftStore;
    },
    { getState: () => draftStore },
  ),
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({
    push: mocks.push,
    replace: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => path,
  }),
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

// The project picker is a real component with its own suite; this face's
// contract is the WIRING around it — which draft slot the choice lands in, and
// what the first turn carries.
vi.mock("../projects/components/project-picker", () => ({
  ProjectPicker: ({
    projectId,
    onUpdate,
  }: {
    projectId: string | null;
    onUpdate: (updates: { project_id?: string | null }) => void;
  }) => (
    <button
      type="button"
      data-testid="project-picker"
      data-project-id={projectId ?? "none"}
      onClick={() => onUpdate({ project_id: "proj-1" })}
    >
      Choose project
    </button>
  ),
}));

// Pasting happens through the editor's own upload path, which the coordinator
// owns; the spec drives a file in through the footer button instead.
vi.mock("@multica/ui/components/common/file-upload-button", () => ({
  FileUploadButton: ({ onSelect }: { onSelect: (file: File) => void }) => (
    <button type="button" onClick={() => onSelect(new File(["test"], "test.txt"))}>
      Upload file
    </button>
  ),
}));

// The real editor is a Tiptap surface with its own suite; this face's contract
// is the WIRING around it — which draft slot the body lands in, which uploads
// it renders, and what the first turn carries. The mock keeps the real upload
// gate and the real handle shape (getMarkdown / uploadFile / hasActiveUploads)
// so the submit gate under test is production code, not a stub.
let mockUploadIdSeq = 0;

// Whether the mocked editor's underlying instance is "created". The real
// ContentEditor builds Tiptap in a passive effect, so its imperative handle
// exists for a commit before content can be inserted; a spec flips this to
// reproduce that window.
let editorLive = true;

vi.mock("../editor", async () => {
  const uploadGate = await vi.importActual<
    typeof import("../editor/use-upload-gate")
  >("../editor/use-upload-gate");

  const ContentEditor = forwardRef(
    (
      {
        defaultValue,
        onUpdate,
        onSubmit,
        onUploadFile,
        onUploadingChange,
        placeholder,
        attachments,
      }: {
        defaultValue?: string;
        onUpdate?: (md: string) => void;
        onSubmit?: () => void;
        onUploadFile?: (file: File, uploadId: string) => Promise<unknown>;
        onUploadingChange?: (uploading: boolean) => void;
        placeholder?: string;
        attachments?: unknown[];
      },
      ref: React.Ref<unknown>,
    ) => {
      const valueRef = useRef(defaultValue ?? "");
      const [value, setValue] = useState(defaultValue ?? "");
      const inFlightRef = useRef(0);
      useImperativeHandle(ref, () => ({
        getMarkdown: () => valueRef.current,
        clearContent: () => {
          valueRef.current = "";
          setValue("");
        },
        focus: () => {},
        focusAtCoords: () => {},
        focusAtTextAnchor: () => {},
        uploadFile: async (file: File) => {
          inFlightRef.current += 1;
          if (inFlightRef.current === 1) onUploadingChange?.(true);
          try {
            return await onUploadFile?.(file, `mock-upload-${++mockUploadIdSeq}`);
          } finally {
            inFlightRef.current -= 1;
            if (inFlightRef.current === 0) onUploadingChange?.(false);
          }
        },
        hasActiveUploads: () => inFlightRef.current > 0,
        // Mirrors the real handle: appends parsed markdown to the live
        // document and reports whether it landed. `editorLive` is what makes
        // the "handle exists, Tiptap does not yet" window testable — the real
        // instance is created in a passive effect, and a seed inserted in that
        // window is silently dropped.
        insertMarkdownAtEnd: (markdown: string) => {
          if (!editorLive) return false;
          const next = valueRef.current
            ? `${valueRef.current}\n\n${markdown}`
            : markdown;
          valueRef.current = next;
          setValue(next);
          onUpdate?.(next);
          return true;
        },
        insertUploadPlaceholder: () => true,
        settleUploadPlaceholder: () => false,
      }));
      return (
        <textarea
          value={value}
          placeholder={placeholder}
          data-attachments-count={attachments?.length ?? 0}
          onChange={(e) => {
            valueRef.current = e.target.value;
            setValue(e.target.value);
            onUpdate?.(e.target.value);
          }}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") onSubmit?.();
          }}
        />
      );
    },
  );
  ContentEditor.displayName = "ContentEditor";

  return {
    ...uploadGate,
    useFileDropZone: () => ({ isDragOver: false, dropZoneProps: {} }),
    FileDropOverlay: () => null,
    ContentEditor,
  };
});

// The face is a PANEL: it only ever renders inside the shell's Dialog root, so
// the sr-only title is stubbed here the way the sibling panel suites do.
vi.mock("@multica/ui/components/ui/dialog", () => ({
  DialogTitle: ({ children, className }: { children: ReactNode; className?: string }) => (
    <div className={className}>{children}</div>
  ),
}));

// The unfinished-drafts entries have their own suite; here the face only has
// to route them.
vi.mock("../issues/draft/unfinished-issue-drafts", () => ({
  UnfinishedIssueDraftsBanner: ({
    drafts,
    onResume,
  }: {
    drafts: unknown[];
    onResume: (id: string) => void;
  }) =>
    drafts.length > 0 ? (
      <button type="button" onClick={() => onResume("sess-old")}>
        resume-unfinished
      </button>
    ) : null,
}));

const TEST_RESOURCES = {
  en: {
    common: enCommon,
    issues: enIssues,
    modals: enModals,
    editor: enEditor,
    projects: enProjects,
    agents: enAgents,
  },
};

const ONLINE_RUNTIME = {
  id: "rt-1",
  name: "Local",
  status: "online",
  owner_id: "user-1",
  provider: "claude",
  device_info: "host-a",
} as unknown as RuntimeDevice;

// A second online machine, so the toolbar picker has something to choose
// BETWEEN — the whole point of DENE-443's second item.
const SECOND_ONLINE_RUNTIME = {
  id: "rt-2",
  name: "Codex on laptop",
  status: "online",
  owner_id: "user-1",
  provider: "codex",
  device_info: "host-b",
} as unknown as RuntimeDevice;

function renderPanel(props: {
  onClose?: () => void;
  onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
  parentIssueId?: string;
} = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AlignCreatePanel
          onClose={props.onClose ?? mocks.close}
          onSwitchMode={props.onSwitchMode}
          parentIssueId={props.parentIssueId}
        />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

/** The catalog the configuration panel reads for the selected machine. One
 *  model carries an effort dial and one does not, so "the chips follow the
 *  model" is observable rather than assumed. */
const MODEL_CATALOG = {
  supported: true,
  models: [
    {
      id: "claude-opus-4-6",
      label: "claude-opus-4-6",
      provider: "claude",
      thinking: {
        supported_levels: [
          { value: "low", label: "Low" },
          { value: "high", label: "High" },
        ],
      },
    },
    {
      id: "claude-haiku-4-6",
      label: "claude-haiku-4-6",
      provider: "claude",
    },
  ],
  unavailable_models: [],
};

function submitButton() {
  return screen.getByRole("button", { name: "Start aligning" });
}

/** Opens the composite configuration panel from its toolbar pill. The pill's
 *  accessible name is the summary line, which changes with the selection — so
 *  this finds it by its stable label instead. */
async function openConfigPanel() {
  await userEvent.click(
    await screen.findByRole("button", { name: "Alignment configuration" }),
  );
}

function editor() {
  return screen.getByPlaceholderText("What do you want done?");
}

async function typeRequest(text: string) {
  await userEvent.type(editor(), text);
  await waitFor(() => expect(submitButton()).toBeEnabled());
}

function uploadedPool(filename = "x.png", url = "https://cdn/x.png") {
  return [
    {
      clientUploadId: "c-1",
      status: "uploaded",
      filename,
      size: 9,
      attachment: {
        id: "att-1",
        workspace_id: "ws-1",
        issue_id: null,
        comment_id: null,
        chat_session_id: null,
        chat_message_id: null,
        uploader_type: "member",
        uploader_id: "user-1",
        filename,
        url,
        download_url: url,
        markdown_url: url,
        content_type: "image/png",
        size_bytes: 9,
        created_at: "2026-01-01T00:00:00Z",
      },
    },
  ];
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.currentUserId = "user-1";
  mocks.drafts = [];
  mocks.runtimes = [ONLINE_RUNTIME];
  mocks.listIssueDrafts.mockImplementation(() => Promise.resolve(mocks.drafts));
  mocks.listRuntimes.mockImplementation(() => Promise.resolve(mocks.runtimes));
  mocks.listMembers.mockImplementation(() => Promise.resolve([]));
  // The server's cached-catalog shape: `completed` on the first call, so
  // `resolveRuntimeModels` answers without its 500ms poll — a real first-time
  // discovery takes that path, and every client-side test that opened the panel
  // would otherwise pay a wall-clock sleep per machine.
  mocks.initiateListModels.mockResolvedValue({
    id: "discovery-1",
    status: "completed",
    models: MODEL_CATALOG.models,
    unavailable_models: [],
    supported: true,
    cached: false,
  });
  mocks.getListModelsResult.mockResolvedValue({
    id: "discovery-1",
    status: "completed",
    models: MODEL_CATALOG.models,
    unavailable_models: [],
    supported: true,
    cached: false,
  });
  mocks.createIssueDraftSession.mockResolvedValue({
    session_id: "sess-new",
    agent_id: "agent-1",
    runtime_id: "rt-1",
    draft: {
      chat_session_id: "sess-new",
      workspace_id: "ws-1",
      status: "draft",
      revision: 1,
      draft: { title: "", description: "add dark mode", status: "", priority: "" },
      created_at: "",
      updated_at: "",
    },
  });
  mocks.sendChatMessage.mockResolvedValue({ message_id: "msg-1", task_id: "task-1" });
  draftStore.draft.shared.attachments = [];
  draftStore.draft.shared.projectId = undefined;
  draftStore.draft.align.request = "";
  // A real write, not just a recorded call: the panel derives what it will send
  // from the draft slot, so a mock that only counted calls would leave it
  // rendering — and submitting — the set from before the click.
  draftStore.draft.align.capabilities = undefined;
  // A started alignment remembers its capability set (DENE-691); without this
  // every test after the first submit would open on a previous test's boxes.
  localStorage.removeItem("multica_alignment_capabilities:user-1");
  editorLive = true;
  mocks.setAlign.mockImplementation(
    (patch: { request?: string; capabilities?: string[] }) => {
      writeDraftStore({
        ...draftStore.draft,
        align: { ...draftStore.draft.align, ...patch },
      });
    },
  );
  mocks.setShared.mockImplementation((patch: { projectId?: string }) => {
    writeDraftStore({
      ...draftStore.draft,
      shared: { ...draftStore.draft.shared, ...patch },
    });
  });
});

describe("AlignCreatePanel", () => {
  /**
   * DENE-452: an alignment opened from a comment is seeded with that comment,
   * and the seed lands AFTER this panel mounts — the source-context preview is
   * a request of its own. `ContentEditor` is uncontrolled (`defaultValue` is
   * read once), so a seed written to the align slot on a later commit is
   * invisible unless the panel pushes it into the live document.
   *
   * This is the real timing, not the synchronous one: a spec that puts the
   * quote in the store BEFORE the first render passes against an editor that
   * can never show a late seed, which is exactly how this shipped broken.
   */
  it("shows a comment seed that arrives after it mounted", async () => {
    renderPanel();
    expect(editor()).toHaveValue("");

    // The preview resolves and the shell writes the quote into the align slot.
    act(() => {
      writeDraftStore({
        ...draftStore.draft,
        align: { request: "MUL-9\n\n> the toggle flickers" },
      });
    });

    await waitFor(() =>
      expect(editor()).toHaveValue("MUL-9\n\n> the toggle flickers"),
    );
    // And it is submittable: the seed counts as content, so the button that was
    // disabled over an empty box goes live once the runtime list settles too.
    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("leaves a request the user typed while the preview was in flight", async () => {
    renderPanel();
    await typeRequest("my own sentence");

    act(() => {
      writeDraftStore({
        ...draftStore.draft,
        align: { request: "MUL-9\n\n> the toggle flickers" },
      });
    });

    // Their sentence stands. A seed that appended itself here would be the form
    // arguing with whoever is filling it in.
    await waitFor(() => expect(editor()).toHaveValue("my own sentence"));
  });

  it("retries the seed when the editor is not live yet", async () => {
    // Reproduces the commit where the imperative handle exists but Tiptap does
    // not: the first insert is a no-op, and dropping the quote there would lose
    // it for good.
    editorLive = false;
    renderPanel();

    act(() => {
      writeDraftStore({
        ...draftStore.draft,
        align: { request: "MUL-9\n\n> the toggle flickers" },
      });
    });
    await waitFor(() => expect(editor()).toHaveValue(""));

    editorLive = true;
    await waitFor(() =>
      expect(editor()).toHaveValue("MUL-9\n\n> the toggle flickers"),
    );
  });

  it("opens a conversation for the request and creates no issue", async () => {
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-1");
    // The idea is stored in the draft before anything is sent, so a lost first
    // turn costs the turn and not the request.
    expect(input.draft?.description).toBe("add dark mode");

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    // No attachments referenced → the third argument stays absent rather than
    // arriving as an empty list.
    expect(mocks.sendChatMessage.mock.calls[0]![2]).toBeUndefined();
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
    expect(mocks.close).toHaveBeenCalled();
  });

  it("runs the alignment on the machine picked in the configuration panel", async () => {
    // Which CLI the alignment runs on used to be decided silently and could
    // only be changed after the fact, on the detail page (DENE-443).
    mocks.runtimes = [ONLINE_RUNTIME, SECOND_ONLINE_RUNTIME];
    renderPanel();
    await typeRequest("add dark mode");

    // Seeded with the machine the face would have chosen on its own, so the
    // pill never starts unset, and nothing is created until submit.
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
    await openConfigPanel();
    await userEvent.click(await screen.findByText("Codex on laptop"));

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-2");
    // Changing the machine clears the model and the effort: both belong to the
    // machine, and a value inherited from the previous one is a choice the user
    // never made (and one the new runtime may not serve).
    expect(input.model).toBeUndefined();
    expect(input.thinking_level).toBeUndefined();
    // A floating popover plus a model query is slower than this suite's other
    // cases under a full parallel run; the budget is explicitly the panel's.
  }, 15_000);

  /**
   * The panel is one decision in one place (DENE-514): the machine, the model
   * it will be asked for, the reasoning effort, and which built-in alignment
   * methods run. The last three could not be chosen anywhere before — the model
   * and the effort are read off the carrier agent row at creation, and the
   * methods are assembled into the carrier's prompt there.
   */
  it("sends the model, effort, and capability set the panel is showing", async () => {
    mocks.runtimes = [ONLINE_RUNTIME];
    renderPanel();
    await typeRequest("add dark mode");

    await openConfigPanel();
    // The catalog is per machine and per model; the level chips come from the
    // chosen model, so the model is picked first. Discovery is a poll, hence the
    // explicit timeout.
    await userEvent.click(
      await screen.findByText("claude-opus-4-6", {}, { timeout: 3000 }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "High" }, { timeout: 3000 }),
    );
    // The panel opens on the built-in default — all three — so two clicks turn
    // two of them OFF. Any subset is legitimate, and this one proves the
    // changes travel in the registry's order rather than the click's.
    await userEvent.click(
      screen.getByRole("checkbox", { name: /Decision map/ }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: /See the screen first/ }),
    );

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.model).toBe("claude-opus-4-6");
    expect(input.thinking_level).toBe("high");
    // Registry order, not click order: the server composes the prompt in this
    // order, so the recorded set has to be a function of the set itself.
    expect(input.capabilities).toEqual(["grill"]);
  }, 15_000);

  /**
   * Every box off is a request the server accepts — it is the picker's own
   * "none of them" — and it must travel as an empty array. Omitting the field
   * would mean "the server's built-in default", the opposite of what an emptied
   * picker just asked for.
   */
  it("sends an empty capability list when every box is unchecked", async () => {
    renderPanel();
    await typeRequest("add dark mode");

    await openConfigPanel();
    for (const name of [/Decision map/, /Requirement interview/, /See the screen first/]) {
      await userEvent.click(screen.getByRole("checkbox", { name }));
    }

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.capabilities).toEqual([]);
  }, 15_000);

  it("starts from the built-in capability set, not from an empty one", async () => {
    // The default is the whole offered list, which is also the server's own
    // default: a client that has never opened the panel still asks for the
    // methods, because "built in" is what the product means by them.
    renderPanel();
    await typeRequest("add dark mode");

    await openConfigPanel();
    for (const name of [/Decision map/, /Requirement interview/, /See the screen first/]) {
      expect(screen.getByRole("checkbox", { name })).toBeChecked();
    }
  }, 15_000);

  // DENE-691. The storage format and its fallbacks are covered in
  // packages/core/issue-drafts/capability-preference.test.ts; this keeps the
  // wiring — what the boxes open on, and that only a started alignment counts.
  it("opens on the combination this user last used and remembers the next one", async () => {
    localStorage.setItem("multica_alignment_capabilities:user-1", JSON.stringify(["grill"]));
    try {
      renderPanel();
      await typeRequest("add dark mode");
      await openConfigPanel();
      expect(screen.getByRole("checkbox", { name: /Requirement interview/ })).toBeChecked();
      expect(screen.getByRole("checkbox", { name: /Decision map/ })).not.toBeChecked();

      // Ticking a box is not "using" it: nothing is remembered until the start.
      await userEvent.click(screen.getByRole("checkbox", { name: /Decision map/ }));
      expect(localStorage.getItem("multica_alignment_capabilities:user-1")).toBe(JSON.stringify(["grill"]));

      await userEvent.click(submitButton());
      await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
      await waitFor(() => {
        const stored = JSON.parse(localStorage.getItem("multica_alignment_capabilities:user-1") ?? "[]") as string[];
        expect(stored).toHaveLength(2);
        expect(stored).toContain("grill");
      });
    } finally {
      localStorage.removeItem("multica_alignment_capabilities:user-1");
    }
  }, 15_000);

  it("holds the start button on a placeholder until the signed-in user is known", async () => {
    mocks.currentUserId = null;
    renderPanel();
    await userEvent.type(editor(), "add dark mode");

    expect(screen.getByRole("status")).toHaveTextContent(
      "Loading your last alignment methods…",
    );
    expect(submitButton()).toBeDisabled();

    await openConfigPanel();
    expect(screen.queryByRole("checkbox", { name: /Requirement interview/ })).toBeNull();
  }, 15_000);

  it("shows the system default and one notice when the record cannot be read, and still starts", async () => {
    localStorage.setItem("multica_alignment_capabilities:user-1", "{");
    renderPanel();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Couldn't load your last alignment methods",
    );

    await typeRequest("add dark mode");
    await openConfigPanel();
    for (const name of [/Decision map/, /Requirement interview/, /See the screen first/]) {
      expect(screen.getByRole("checkbox", { name })).toBeChecked();
    }

    await userEvent.click(submitButton());
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
  }, 15_000);

  it("still starts when remembering the combination fails", async () => {
    const original = localStorage.setItem.bind(localStorage);
    vi.spyOn(localStorage, "setItem").mockImplementation((key, value) => {
      if (String(key).startsWith("multica_alignment_capabilities")) {
        throw new Error("quota");
      }
      original(key, value);
    });
    try {
      renderPanel();
      await typeRequest("add dark mode");
      await userEvent.click(submitButton());
      await waitFor(() =>
        expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
      );
    } finally {
      vi.restoreAllMocks();
    }
  }, 15_000);

  it("turns a capability back on after every box was cleared", async () => {
    // The empty state must not be sticky: a set that can only shrink would make
    // "none of them" a one-way door out of the product's own methods.
    renderPanel();
    await typeRequest("add dark mode");

    await openConfigPanel();
    for (const name of [/Decision map/, /Requirement interview/, /See the screen first/]) {
      await userEvent.click(screen.getByRole("checkbox", { name }));
    }
    await userEvent.click(
      screen.getByRole("checkbox", { name: /See the screen first/ }),
    );

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.capabilities).toEqual(["grill-frontend-look"]);
  }, 15_000);

  it("still opens the conversation when the first turn could not be sent", async () => {
    mocks.sendChatMessage.mockRejectedValue(new Error("network down"));
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    // The draft exists and holds the request; staying here would only invite a
    // second, duplicate draft.
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
  });

  /**
   * DENE-422: a lost first turn must reach the page that can resend it. This
   * panel closes on navigation and the conversation opens with an empty
   * transcript, so without the handoff the user sees a blank alignment and has
   * no way to know their request never arrived.
   */
  it("hands a lost first turn to the conversation it navigates to", async () => {
    mocks.sendChatMessage.mockRejectedValue(
      new ApiError("runtime_unusable", 409, "Conflict"),
    );
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-new"),
    );
    // The page reads this slot and says the turn was lost; the draft id is what
    // keeps the sentence on the conversation it belongs to.
    expect(mocks.setAlign).toHaveBeenCalledWith({ seedFailedDraftId: "sess-new" });
  });

  /**
   * The exits of `CreateIssueDraftSession` say different things — a runtime that
   * went offline, a private runtime, an oversized draft — and all of them are
   * 4xx sentences written for the reader. Collapsing them into one generic line
   * is what made DENE-366 unreportable from a screenshot.
   */
  it("shows the server's own reason when the entry is refused", async () => {
    mocks.createIssueDraftSession.mockRejectedValue(
      new ApiError("runtime must be online to start an issue draft session", 409, "Conflict"),
    );
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(
      "runtime must be online to start an issue draft session",
    );
    expect(alert).not.toHaveTextContent("Could not start the alignment conversation.");
    // A refused create leaves nothing to open, so the face stays put.
    expect(mocks.push).not.toHaveBeenCalled();
  });

  /**
   * A 5xx message is Go error chains and table names (MUL-6472) and must never
   * be rendered. The status code is the part that is safe, and it is what makes
   * a screenshot actionable.
   */
  it("states the status for a server error without leaking its message", async () => {
    mocks.createIssueDraftSession.mockRejectedValue(
      new ApiError(
        'pq: relation "issue_draft_sessions" does not exist',
        500,
        "Internal Server Error",
      ),
    );
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(
      "Could not start the alignment conversation. Server error (HTTP 500).",
    );
    expect(alert).not.toHaveTextContent("issue_draft_sessions");
  });

  /**
   * CLAUDE.md's malformed-response contract. `parseWithFallback` degrades an
   * unreadable create-session body to an empty session whose draft exists on the
   * server, so "the session was not created" would be false AND expensive: the
   * user creates a second, orphaned draft. Drift is reported as drift.
   */
  it("reports an unreadable create-session response as drift, not a missing session", async () => {
    mocks.createIssueDraftSession.mockResolvedValue({ session_id: "" });
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(
      "The server's response was not recognized. The client or the server may need an update.",
    );
    expect(alert).not.toHaveTextContent("Could not start the alignment conversation.");
    // The draft was created but this client never learned its id, so there is
    // nowhere to navigate to — and nothing is sent.
    expect(mocks.push).not.toHaveBeenCalled();
    expect(mocks.sendChatMessage).not.toHaveBeenCalled();
  });

  it("refuses to start without a request", async () => {
    renderPanel();
    // Typing a request first is what proves the auto-selected runtime is in
    // place; clearing it again leaves the request as the only gate, which is
    // the one this test is about.
    await typeRequest("x");
    await userEvent.clear(editor());
    await waitFor(() => expect(submitButton()).toBeDisabled());
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
  });

  /**
   * DENE-367: the entry face asks for one thing — what to align on. The runtime
   * is chosen for the user, and the picker moves to the alignment page's
   * preview. What must not move is the choice itself.
   */
  it("starts on a request alone, with no runtime picker on the face", async () => {
    renderPanel();
    await typeRequest("add dark mode");

    // Enabled by the request alone: the runtime was seeded without a click.
    expect(submitButton()).toBeEnabled();
    expect(screen.queryByTestId("runtime-picker")).toBeNull();
  });

  it("asks for a runtime only when there is none to run on", async () => {
    mocks.runtimes = [];
    renderPanel();

    expect(
      await screen.findByText(
        "No online runtime is available, so the alignment agent cannot run.",
      ),
    ).toBeTruthy();
    // A link, not a button: it navigates to the machines list.
    expect(screen.getByRole("link", { name: "Connect a runtime" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start aligning" })).toBeNull();
  });

  /**
   * With no picker on this face, the seed is the only chance to land on a
   * machine that can actually run. The list arrives ordered by creation, so an
   * older offline machine of the user's own sorts ahead of a usable online one
   * — and seeding that would state the alignment cannot start while a machine
   * that could run it sits one entry away, with nothing to click.
   */
  it("seeds an online runtime over an older offline one the user owns", async () => {
    mocks.runtimes = [
      { ...ONLINE_RUNTIME, id: "rt-old", name: "Old laptop", status: "offline" },
      { ...ONLINE_RUNTIME, id: "rt-new", name: "Desk" },
    ];
    renderPanel();
    await typeRequest("add dark mode");
    expect(screen.queryByText("Old laptop is offline, so the alignment cannot start.")).toBeNull();

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-new");
  });

  it("prefers the user's own machine among the online ones", async () => {
    mocks.runtimes = [
      {
        ...ONLINE_RUNTIME,
        id: "rt-shared",
        name: "Shared",
        owner_id: "user-2",
        visibility: "public",
      } as unknown as RuntimeDevice,
      { ...ONLINE_RUNTIME, id: "rt-mine", name: "Mine" },
    ];
    renderPanel();
    await typeRequest("add dark mode");

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.runtime_id).toBe("rt-mine");
  });

  it("names the offline runtime instead of only disabling submit", async () => {
    mocks.runtimes = [{ ...ONLINE_RUNTIME, status: "offline" }];
    renderPanel();

    expect(
      await screen.findByText("Local is offline, so the alignment cannot start."),
    ).toBeTruthy();
    await userEvent.type(editor(), "add dark mode");
    expect(submitButton()).toBeDisabled();
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
  });

  it("routes an unfinished draft to its conversation", async () => {
    // The row carries a live status: the banner counts only the alignments with
    // a next turn in them (DENE-371), so a fixture without one is filtered out
    // before it ever reaches the banner.
    mocks.drafts = [{ chat_session_id: "sess-old", status: "draft" }];
    renderPanel();
    await userEvent.click(await screen.findByRole("button", { name: "resume-unfinished" }));
    expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-old");
    expect(mocks.close).toHaveBeenCalled();
  });

  /**
   * DENE-370: the body is the create draft's `align` slot. A request written on
   * this face must land there (and in no other face's slot), and a request that
   * is already there is what the editor opens on — which is how the manual
   * face's assist-init reaches this one.
   */
  it("opens on the request already in the align draft slot", async () => {
    draftStore.draft.align.request = "carried from the manual face";
    renderPanel();

    expect(editor()).toHaveValue("carried from the manual face");
    // A body that is already there is submittable on its own, without a click
    // anywhere else on the face.
    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("writes edits into the align slot so a mode switch cannot lose them", async () => {
    renderPanel();
    await typeRequest("add dark mode");

    await waitFor(() => expect(mocks.setAlign).toHaveBeenCalled());
    expect(mocks.setAlign).toHaveBeenLastCalledWith({ request: "add dark mode" });
    // And the other faces' bodies are untouched.
    expect(draftStore.draft.manual.description).toBe("");
    expect(draftStore.draft.agent.prompt).toBe("");
  });

  it("marks the draft as being edited on the alignment face", () => {
    renderPanel();
    expect(mocks.setActiveMode).toHaveBeenCalledWith("align");
  });

  /**
   * The whole point of folding this face into the create dialog: a file
   * uploaded on the manual face is still in the shared pool here, and the ids
   * whose link the request references ride the first turn (DENE-369's
   * transport) instead of being attached to nothing.
   */
  it("sends the shared pool's referenced attachments with the first turn", async () => {
    draftStore.draft.shared.attachments = uploadedPool();
    draftStore.draft.align.request = "look at ![shot](https://cdn/x.png)";
    renderPanel();

    // The pool the manual face fills is the pool this face renders.
    expect(editor().getAttribute("data-attachments-count")).toBe("1");

    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage.mock.calls[0]![2]).toEqual(["att-1"]);
  });

  it("leaves unreferenced pool entries out of the first turn", async () => {
    // A file uploaded on the manual face whose link was removed from the
    // alignment request must not ride along.
    draftStore.draft.shared.attachments = uploadedPool();
    draftStore.draft.align.request = "no files in here";
    renderPanel();

    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage.mock.calls[0]![2]).toBeUndefined();
  });

  it("offers a way back to the new-issue face", async () => {
    const onSwitchMode = vi.fn();
    renderPanel({ onSwitchMode });

    await userEvent.click(screen.getByRole("button", { name: /Switch to New issue/i }));
    expect(onSwitchMode).toHaveBeenCalledWith(null);
  });

  /**
   * DENE-423: the project is optional and the face does not ask for it up
   * front — but when one is chosen, the draft is created holding it, so the
   * whole group the conversation settles on is filed under that project rather
   * than under nothing. It rides the SHARED slot, like the attachment pool, so
   * the manual face and this one are choosing the same field.
   */
  it("files the conversation under the project picked on this face", async () => {
    renderPanel();
    await typeRequest("add dark mode");

    await userEvent.click(screen.getByTestId("project-picker"));
    expect(mocks.setShared).toHaveBeenCalledWith({ projectId: "proj-1" });
    expect(screen.getByTestId("project-picker")).toHaveAttribute(
      "data-project-id",
      "proj-1",
    );

    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.draft?.project_id).toBe("proj-1");
  });

  it("files the conversation under the issue it was started from", async () => {
    /**
     * DENE-452: an alignment started from an existing issue is filed beneath
     * it. The parent is written into the draft at creation because the confirm
     * builds the group out of the STORED payload — a field that only lived in
     * this panel would never reach the server, and the conversation would found
     * a second top-level issue instead of the children of the one the person
     * was looking at.
     */
    renderPanel({ parentIssueId: "issue-7" });
    await typeRequest("this needs breaking down");
    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.draft?.parent_issue_id).toBe("issue-7");
  });

  it("leaves the parent absent when the alignment founds its own top-level issue", async () => {
    // Absent rather than null, so the stored payload reads exactly as it did
    // before mid-flight alignment existed.
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.draft).not.toHaveProperty("parent_issue_id");
  });

  /**
   * The other half of the same contract: a project the manual face already
   * committed — including one an opener seeded from a project page — is what
   * this face opens on, with no second click, because both faces read the one
   * shared slot.
   */
  it("opens on the project the shared slot already holds", async () => {
    draftStore.draft.shared.projectId = "proj-9";
    renderPanel();

    expect(screen.getByTestId("project-picker")).toHaveAttribute(
      "data-project-id",
      "proj-9",
    );

    await typeRequest("add dark mode");
    await userEvent.click(submitButton());
    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    expect(input.draft?.project_id).toBe("proj-9");
  });

  it("leaves the project out of the draft when none was chosen", async () => {
    renderPanel();
    await typeRequest("add dark mode");
    await userEvent.click(submitButton());

    await waitFor(() => expect(mocks.createIssueDraftSession).toHaveBeenCalledTimes(1));
    const input = mocks.createIssueDraftSession.mock.calls[0]![0] as CreateSessionInput;
    // Absent, not an empty string: "no project" is the field being missing.
    expect(input.draft).not.toHaveProperty("project_id");
  });

  it("blocks submit while a shared attachment is still uploading", async () => {
    draftStore.draft.shared.attachments = [
      { clientUploadId: "c-flight", status: "uploading", filename: "mid.png", size: 9 },
    ];
    draftStore.draft.align.request = "add dark mode";
    renderPanel();

    // The coordinator-owned placeholder widens the gate, so the button cannot
    // serialize a request whose image has not landed yet.
    await waitFor(() => expect(submitButton()).toBeDisabled());
    fireEvent.click(submitButton());
    expect(mocks.createIssueDraftSession).not.toHaveBeenCalled();
  });
});
