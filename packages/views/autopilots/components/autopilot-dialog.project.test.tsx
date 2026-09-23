import { useImperativeHandle, useRef, useState } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AutopilotExecutionMode, AutopilotTrigger } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

// Regression cover for MUL-6681 (GH #7550): the dialog rendered the Project
// section only for create_issue autopilots and sent `project_id: null` for
// run_only ones. A run_only autopilot could therefore never be bound to a
// project from the UI, and editing anything else on one — a title, a cron —
// silently cleared a binding made via the CLI. That binding is not cosmetic:
// it is the only project context a run_only task has, so losing it drops the
// run out of its project's worktree and into a bare workdir.

const mockCreateAutopilot = vi.hoisted(() => vi.fn());
const mockUpdateAutopilot = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-test" }));
vi.mock("@multica/core/paths", () => ({ useCurrentWorkspace: () => ({ name: "Acme" }) }));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: (wsId: string) => ({
    queryKey: ["agents", wsId],
    queryFn: async () => [
      {
        id: "agent-1",
        name: "Scout",
        description: "Researches things",
        archived_at: null,
        runtime_id: "runtime-1",
      },
    ],
  }),
  squadListOptions: (wsId: string) => ({
    queryKey: ["squads", wsId],
    queryFn: async () => [],
  }),
}));

vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: (wsId: string) => ({
    queryKey: ["projects", wsId],
    queryFn: async () => [{ id: "proj-1", title: "Fleet", icon: null }],
  }),
}));

vi.mock("@multica/core/autopilots/queries", () => ({
  cronPreviewOptions: (wsId: string, expr: string, tz: string) => ({
    queryKey: ["cron-preview", wsId, expr, tz],
    queryFn: async () => ({ next_runs: ["2126-07-14T01:00:00Z"] }),
    retry: false,
  }),
}));

vi.mock("@multica/core/autopilots/mutations", () => ({
  useCreateAutopilot: () => ({ mutateAsync: mockCreateAutopilot }),
  useCreateAutopilotTrigger: () => ({ mutateAsync: vi.fn().mockResolvedValue({ id: "trg-new" }) }),
  useUpdateAutopilot: () => ({ mutateAsync: mockUpdateAutopilot }),
  useUpdateAutopilotTrigger: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

vi.mock("../../editor", () => ({
  TitleEditor: ({ ref, defaultValue, placeholder, onChange, onSubmit }: any) => {
    const [value, setValue] = useState(defaultValue ?? "");
    const inputRef = useRef<HTMLInputElement>(null);
    useImperativeHandle(ref, () => ({
      getText: () => value,
      focus: () => inputRef.current?.focus(),
      focusAtCoords: () => inputRef.current?.focus(),
    }));
    return (
      <input
        ref={inputRef}
        aria-label="title"
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          setValue(e.target.value);
          onChange?.(e.target.value);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") onSubmit?.();
        }}
      />
    );
  },
  ContentEditor: ({ placeholder }: any) => <textarea aria-label="runbook" placeholder={placeholder} />,
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => <span data-testid="actor-avatar">{actorId}</span>,
}));

vi.mock("./subscriber-multi-select", () => ({
  SubscriberMultiSelect: () => <div data-testid="subscriber-multi-select" />,
}));

// Stand-in picker: renders the dialog's own trigger (so the selected project
// title is asserted against the real markup) plus a button that reports a
// selection, which is how the create-mode test binds a project.
vi.mock("../../projects/components/project-picker", () => ({
  ProjectPicker: ({
    triggerRender,
    onUpdate,
  }: {
    triggerRender: React.ReactElement;
    onUpdate: (updates: { project_id: string | null }) => void;
  }) => (
    <div>
      {triggerRender}
      <button type="button" onClick={() => onUpdate({ project_id: "proj-1" })}>
        pick Fleet
      </button>
    </div>
  ),
}));

vi.mock("./pickers/timezone-picker", () => ({
  TimezonePicker: ({ value }: { value: string }) => <div data-testid="timezone-picker">{value}</div>,
}));

import { AutopilotDialog } from "./autopilot-dialog";

const AUTOPILOT_ID = "ap-1";

const scheduleTrigger: AutopilotTrigger = {
  id: "trg-1",
  autopilot_id: AUTOPILOT_ID,
  kind: "schedule",
  enabled: true,
  cron_expression: "TZ=Asia/Shanghai 30 8 * * *",
  timezone: "Asia/Shanghai",
  next_run_at: null,
  webhook_token: null,
  label: null,
  last_fired_at: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderEditDialog(
  mode: AutopilotExecutionMode,
  projectId: string | null,
  triggers: AutopilotTrigger[] = [],
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <AutopilotDialog
        mode="edit"
        open
        onOpenChange={vi.fn()}
        autopilotId={AUTOPILOT_ID}
        initial={{
          title: "Push fleet repo to GitHub",
          description: "",
          project_id: projectId,
          assignee_type: "agent",
          assignee_id: "agent-1",
          execution_mode: mode,
          subscriber_user_ids: [],
        }}
        triggers={triggers}
        collaborators={[]}
        canManageAccess={false}
      />
    </QueryClientProvider>,
  );
}

function renderCreateDialog() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <AutopilotDialog
        mode="create"
        open
        onOpenChange={vi.fn()}
        initial={{
          assignee_type: "agent",
          assignee_id: "agent-1",
          execution_mode: "run_only",
        }}
      />
    </QueryClientProvider>,
  );
}

const saveButton = () => screen.getByRole("button", { name: "Save" });

describe("AutopilotDialog project section", () => {
  beforeEach(() => {
    mockCreateAutopilot.mockReset().mockResolvedValue({ id: AUTOPILOT_ID });
    mockUpdateAutopilot.mockReset().mockResolvedValue({ id: AUTOPILOT_ID });
  });

  it("offers the project picker to a run_only autopilot", async () => {
    renderEditDialog("run_only", "proj-1");

    expect(screen.getByText("Project")).toBeInTheDocument();
    // The bound project reads back instead of the empty-state label.
    expect(await screen.findByText("Fleet")).toBeInTheDocument();
    expect(screen.queryByText("No project")).not.toBeInTheDocument();
  });

  it("says the project also decides where runs execute", () => {
    renderEditDialog("run_only", null);

    expect(
      screen.getByText("Also sets where runs execute — the project's repository or local directory"),
    ).toBeInTheDocument();
  });

  it("keeps a run_only autopilot's project on save instead of clearing it", async () => {
    const user = userEvent.setup();
    renderEditDialog("run_only", "proj-1");

    await user.type(screen.getByLabelText("title"), " v2");
    await user.click(saveButton());

    await waitFor(() => expect(mockUpdateAutopilot).toHaveBeenCalledTimes(1));
    expect(mockUpdateAutopilot.mock.calls[0]?.[0]).toMatchObject({
      id: AUTOPILOT_ID,
      execution_mode: "run_only",
      project_id: "proj-1",
    });
  });

  it("keeps a create_issue autopilot's project on save", async () => {
    const user = userEvent.setup();
    renderEditDialog("create_issue", "proj-1");

    await user.click(saveButton());

    await waitFor(() => expect(mockUpdateAutopilot).toHaveBeenCalledTimes(1));
    expect(mockUpdateAutopilot.mock.calls[0]?.[0]).toMatchObject({ project_id: "proj-1" });
  });

  it("binds a project chosen while creating a run_only autopilot", async () => {
    const user = userEvent.setup();
    renderCreateDialog();

    await user.type(screen.getByLabelText("title"), "Push fleet repo to GitHub");
    await user.click(screen.getByRole("button", { name: "pick Fleet" }));
    await user.click(screen.getByRole("button", { name: "Create autopilot" }));

    await waitFor(() => expect(mockCreateAutopilot).toHaveBeenCalledTimes(1));
    expect(mockCreateAutopilot.mock.calls[0]?.[0]).toMatchObject({
      execution_mode: "run_only",
      project_id: "proj-1",
    });
  });

  it("asks before saving a schedule with no project, and does not save until confirmed", async () => {
    const user = userEvent.setup();
    renderEditDialog("create_issue", null, [scheduleTrigger]);

    await user.click(saveButton());

    expect(await screen.findByText("This plan has no project")).toBeInTheDocument();
    expect(mockUpdateAutopilot).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Continue without a project" }));

    await waitFor(() => expect(mockUpdateAutopilot).toHaveBeenCalledTimes(1));
    expect(mockUpdateAutopilot.mock.calls[0]?.[0]).toMatchObject({ project_id: null });
  });

  it("returns to the project picker without saving when the empty-project confirm is cancelled", async () => {
    const user = userEvent.setup();
    renderEditDialog("create_issue", null, [scheduleTrigger]);

    await user.type(screen.getByLabelText("title"), " kept");
    await user.click(saveButton());
    await user.click(await screen.findByRole("button", { name: "Choose a project" }));

    expect(mockUpdateAutopilot).not.toHaveBeenCalled();
    expect(screen.getByLabelText("title")).toHaveValue("Push fleet repo to GitHub kept");
    expect(screen.queryByText("This plan has no project")).not.toBeInTheDocument();
  });

  it("asks again after a confirmed save fails", async () => {
    const user = userEvent.setup();
    mockUpdateAutopilot.mockRejectedValueOnce(new Error("nope"));
    renderEditDialog("create_issue", null, [scheduleTrigger]);

    await user.click(saveButton());
    await user.click(await screen.findByRole("button", { name: "Continue without a project" }));
    await waitFor(() => expect(mockUpdateAutopilot).toHaveBeenCalledTimes(1));

    await user.click(saveButton());
    expect(await screen.findByText("This plan has no project")).toBeInTheDocument();
    expect(mockUpdateAutopilot).toHaveBeenCalledTimes(1);
  });
});
