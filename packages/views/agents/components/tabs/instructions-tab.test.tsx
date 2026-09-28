// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { configStore } from "@multica/core/config";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent } from "@multica/core/types";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
// The conversation-starter editor previews the chat empty state, so it reads the
// chat namespace for the built-in defaults it renders when nothing is set.
import enChat from "../../../locales/en/chat.json";
import { NavigationProvider } from "../../../navigation";
import type { NavigationAdapter } from "../../../navigation";
import type { InheritedPromptState } from "../../specialization";
import { InstructionsTab } from "./instructions-tab";

const TEST_RESOURCES = {
  en: { common: enCommon, agents: enAgents, chat: enChat },
};
const persistedPrompt = {
  label: "Review a PR",
  prompt: "Review the open pull request.",
};
const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Reviewer",
  description: "",
  instructions: "Review carefully.",
  conversation_starters: [persistedPrompt],
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-08-24T00:00:00Z",
  updated_at: "2026-08-24T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function tab(
  agent: Agent,
  onSave = vi.fn().mockResolvedValue(undefined),
  extra: {
    inheritedPromptState?: InheritedPromptState;
    onRetryInheritedPrompt?: () => void;
  } = {},
) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <InstructionsTab agent={agent} onSave={onSave} {...extra} />
    </I18nProvider>
  );
}

describe("InstructionsTab persisted-state synchronization", () => {
  beforeEach(() => {
    configStore.getState().setAgentConversationStartersSupported(true);
  });

  afterEach(() => {
    act(() => {
      configStore.getState().setAgentConversationStartersSupported(false);
    });
  });

  it("preserves an unsaved prompt across an equivalent agent-list refetch", async () => {
    const user = userEvent.setup();
    const { rerender } = render(tab(baseAgent));
    const label = screen.getByLabelText("Suggestion 1 label");
    await user.clear(label);
    await user.type(label, "Inspect the patch");

    rerender(
      tab({
        ...baseAgent,
        conversation_starters: [{ ...persistedPrompt }],
      }),
    );

    expect(screen.getByLabelText("Suggestion 1 label")).toHaveValue(
      "Inspect the patch",
    );
  });

  it("preserves dirty local state when persisted contents change", async () => {
    const user = userEvent.setup();
    const { rerender } = render(tab(baseAgent));
    const label = screen.getByLabelText("Suggestion 1 label");
    await user.clear(label);
    await user.type(label, "Inspect the patch");

    rerender(
      tab({
        ...baseAgent,
        conversation_starters: [
          { label: "Server-side change", prompt: "A different prompt." },
        ],
      }),
    );

    expect(screen.getByLabelText("Suggestion 1 label")).toHaveValue(
      "Inspect the patch",
    );
  });

  it("preserves submitted prompt edits when an optimistic update rolls back", async () => {
    let rejectSave!: (reason?: unknown) => void;
    const onSave = vi.fn(
      () =>
        new Promise<void>((_, reject) => {
          rejectSave = reject;
        }),
    );
    const user = userEvent.setup();
    const { rerender } = render(tab(baseAgent, onSave));
    const label = screen.getByLabelText("Suggestion 1 label");
    const prompt = screen.getByLabelText("Suggestion 1 prompt");
    await user.clear(label);
    await user.type(label, "Inspect the patch");
    await user.clear(prompt);
    await user.type(prompt, "Inspect the patch for correctness.");
    await user.click(screen.getByRole("button", { name: "Save" }));

    const optimisticPrompt = {
      label: "Inspect the patch",
      prompt: "Inspect the patch for correctness.",
    };
    rerender(
      tab(
        {
          ...baseAgent,
          conversation_starters: [optimisticPrompt],
        },
        onSave,
      ),
    );
    rerender(
      tab(
        {
          ...baseAgent,
          conversation_starters: [{ ...persistedPrompt }],
        },
        onSave,
      ),
    );
    await act(async () => rejectSave(new Error("Update failed")));

    expect(screen.getByLabelText("Suggestion 1 label")).toHaveValue(
      "Inspect the patch",
    );
    expect(screen.getByLabelText("Suggestion 1 prompt")).toHaveValue(
      "Inspect the patch for correctness.",
    );
  });

  it("omits conversation starters from settings writes to an older backend", async () => {
    configStore.getState().setAgentConversationStartersSupported(false);
    const onSave = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(tab(baseAgent, onSave));

    expect(
      screen.queryByText("Conversation starters"),
    ).not.toBeInTheDocument();
    const instructions = screen.getByLabelText("System prompt");
    await user.clear(instructions);
    await user.type(instructions, "Updated instructions.");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      instructions: "Updated instructions.",
    });
  });
});

// The "customize" link in a chat's empty state lands here with ?focus=. The
// tab must bring the editor into view and then clear the param, so a refresh
// or a later tab switch does not replay the flash.
describe("InstructionsTab conversation-starters deep link", () => {
  // Mirrors FOCUS_FLASH_MS in instructions-tab.tsx.
  const FLASH_MS = 1600;

  beforeEach(() => {
    configStore.getState().setAgentConversationStartersSupported(true);
  });

  afterEach(() => {
    act(() => {
      configStore.getState().setAgentConversationStartersSupported(false);
    });
  });

  // A fresh adapter object per render — what the web platform actually hands
  // down, and the condition the flash-timer regression below depends on.
  const adapter = (search: string, replace = vi.fn()): NavigationAdapter => ({
    push: vi.fn(),
    replace,
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(search),
    hash: "",
    getShareableUrl: (path: string) => `https://app.test${path}`,
  });

  function renderWithSearch(search: string) {
    const replace = vi.fn();
    const scrollIntoView = vi.fn();
    // jsdom has no layout, so the method does not exist at all.
    Element.prototype.scrollIntoView = scrollIntoView;
    const tree = (value: NavigationAdapter, agent: Agent) => (
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <NavigationProvider value={value}>
          <InstructionsTab agent={agent} onSave={vi.fn()} />
        </NavigationProvider>
      </I18nProvider>
    );
    const { rerender } = render(tree(adapter(search, replace), baseAgent));
    return {
      replace,
      scrollIntoView,
      /**
       * Re-render as the platform does: a new adapter object every time, and
       * optionally a different agent for an in-place navigation between two
       * agent pages on the same route.
       */
      settleUrl: (next: string, agent: Agent = baseAgent) =>
        rerender(tree(adapter(next, replace), agent)),
    };
  }

  const ringed = () =>
    document.querySelector<HTMLElement>("[class*='ring-brand']");

  it("scrolls to the editor and drops the focus param, keeping the view", () => {
    const { replace, scrollIntoView } = renderWithSearch(
      "view=instructions&focus=conversation_starters",
    );

    expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
    expect(replace).toHaveBeenCalledWith(
      "/acme/agents/agent-1?view=instructions",
    );
  });

  // Regression: the flash timer must not hang off the effect's cleanup. The
  // navigation adapter is not referentially stable, so React would run that
  // cleanup on the very next render and cancel the timeout, leaving the ring
  // on the editor permanently.
  it("ends the flash instead of ringing the editor forever", () => {
    vi.useFakeTimers();
    try {
      const { settleUrl } = renderWithSearch(
        "view=instructions&focus=conversation_starters",
      );
      expect(ringed()).not.toBeNull();

      // The stripped URL arrives on a new adapter object, re-running the
      // effect. A flash timer owned by that effect's cleanup dies here.
      act(() => settleUrl("view=instructions"));
      act(() => {
        vi.advanceTimersByTime(FLASH_MS + 400);
      });

      expect(ringed()).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  // Regression: the guard against re-firing before the stripped URL lands must
  // not become a one-shot-per-mount latch. A chat window left open beside this
  // page can send the very same link again, and it has to land again.
  it("focuses again when the link is clicked a second time", () => {
    const { replace, scrollIntoView, settleUrl } = renderWithSearch(
      "view=instructions&focus=conversation_starters",
    );
    expect(scrollIntoView).toHaveBeenCalledTimes(1);

    act(() => settleUrl("view=instructions"));
    act(() => settleUrl("view=instructions&focus=conversation_starters"));

    expect(scrollIntoView).toHaveBeenCalledTimes(2);
    expect(replace).toHaveBeenCalledTimes(2);
    expect(ringed()).not.toBeNull();
  });

  // Regression: the same guard is keyed by agent, so navigating between two
  // agent pages on this route does not swallow the second agent's link.
  it("focuses a link aimed at a different agent", () => {
    const other: Agent = { ...baseAgent, id: "agent-2" };
    const { scrollIntoView, settleUrl } = renderWithSearch(
      "view=instructions&focus=conversation_starters",
    );
    expect(scrollIntoView).toHaveBeenCalledTimes(1);

    // The URL still carries focus — it is the NEXT agent's deep link.
    act(() => settleUrl("view=instructions&focus=conversation_starters", other));

    expect(scrollIntoView).toHaveBeenCalledTimes(2);
  });

  it("ignores an ordinary visit", () => {
    const { replace, scrollIntoView } = renderWithSearch("view=instructions");

    expect(scrollIntoView).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
  });
});

// DENE-304: a specialisation's prompt has two halves, and the tab has to show
// which half is whose. The inherited half is read-only (it is edited on the
// base role, and the server never accepts an override), and the effective
// prompt preview is the composed string the daemon is actually handed.
describe("InstructionsTab inheritance", () => {
  const specializationAgent: Agent = {
    ...baseAgent,
    id: "agent-spec",
    name: "Nightly Reviewer",
    instructions: "Only review the diff.",
    parent_agent_id: "agent-base",
    parent_agent_name: "Base Reviewer",
    inherited_instructions: "Review carefully.",
  };

  it("shows the base role's prompt as a read-only block", () => {
    render(tab(specializationAgent));

    const block = screen.getByTestId("agent-inherited-prompt");
    expect(block).toHaveTextContent("Base role prompt");
    expect(block).toHaveTextContent(
      /edit it on "Base Reviewer" to update every specialization/i,
    );
    // The editable field is the ADDITIONAL half, not the whole prompt.
    expect(screen.getByLabelText(/Additional instructions/i)).toHaveValue(
      "Only review the diff.",
    );
  });

  it("previews the composed prompt the runtime will receive", async () => {
    const user = userEvent.setup();
    render(tab(specializationAgent));

    const preview = screen.getByTestId("agent-effective-prompt");
    await user.click(
      within(preview).getByRole("button", { name: /^Show$/i }),
    );

    // Compared as raw text: `toHaveTextContent` collapses whitespace, and the
    // blank line between the halves is the point.
    expect(screen.getByTestId("agent-effective-prompt-text").textContent).toBe(
      "Review carefully.\n\nOnly review the diff.",
    );
  });

  it("keeps the preview in step with an unsaved edit", async () => {
    const user = userEvent.setup();
    render(tab(specializationAgent));

    const editor = screen.getByLabelText(/Additional instructions/i);
    await user.clear(editor);
    await user.type(editor, "New rules");

    const preview = screen.getByTestId("agent-effective-prompt");
    await user.click(
      within(preview).getByRole("button", { name: /^Show$/i }),
    );

    // What is previewed is what would run — not the last saved value.
    expect(screen.getByTestId("agent-effective-prompt-text").textContent).toBe(
      "Review carefully.\n\nNew rules",
    );
  });

  it("does not invent a parent half when the base role's prompt is unavailable", () => {
    render(
      tab({
        ...specializationAgent,
        inherited_instructions: "",
      }),
    );

    expect(screen.getByTestId("agent-inherited-prompt")).toHaveTextContent(
      /has no prompt, or you don't have access to it/i,
    );
  });

  // DENE-384: an absent `inherited_instructions` used to be stated as a fact
  // about the base role even when the read that carries it had failed, which
  // read as a business conclusion and misled a real acceptance pass.
  it("reports a failed parent read as a read failure, with a retry", async () => {
    const user = userEvent.setup();
    const onRetryInheritedPrompt = vi.fn();
    render(
      tab(
        { ...specializationAgent, inherited_instructions: "" },
        vi.fn().mockResolvedValue(undefined),
        { inheritedPromptState: "failed", onRetryInheritedPrompt },
      ),
    );

    const block = screen.getByTestId("agent-inherited-prompt");
    expect(block).not.toHaveTextContent(
      /has no prompt, or you don't have access to it/i,
    );
    expect(block).toHaveTextContent(/couldn't load the base role's prompt/i);

    await user.click(screen.getByTestId("agent-inherited-prompt-retry"));
    expect(onRetryInheritedPrompt).toHaveBeenCalledTimes(1);
  });

  it("shows the composed preview only once the parent half is known", async () => {
    const user = userEvent.setup();
    render(
      tab(
        { ...specializationAgent, inherited_instructions: "" },
        vi.fn().mockResolvedValue(undefined),
        { inheritedPromptState: "loading" },
      ),
    );

    const block = screen.getByTestId("agent-inherited-prompt");
    expect(block).not.toHaveTextContent(
      /has no prompt, or you don't have access to it/i,
    );
    expect(block).toHaveTextContent(/loading the base role's prompt/i);

    // "Exactly what the runtime receives" would be a lie while the parent
    // half is still unknown — the composed text is withheld until it lands.
    const preview = screen.getByTestId("agent-effective-prompt");
    await user.click(within(preview).getByRole("button", { name: /^Show$/i }));
    expect(
      screen.queryByTestId("agent-effective-prompt-text"),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("agent-effective-prompt-unknown")).toHaveTextContent(
      /loading the base role's prompt/i,
    );
  });

  it("tells a base role which specialisations a prompt edit reaches", () => {
    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <InstructionsTab
          agent={{ ...baseAgent, name: "Base Reviewer" }}
          onSave={vi.fn().mockResolvedValue(undefined)}
          childAgents={[
            { ...baseAgent, id: "agent-spec", name: "Nightly Reviewer" },
          ]}
        />
      </I18nProvider>,
    );

    const hint = screen.getByTestId("agent-specialization-children");
    expect(hint).toHaveTextContent(
      /also changes every specialization of "Base Reviewer"/i,
    );
    expect(hint).toHaveTextContent("Nightly Reviewer");
  });

  it("stays a plain prompt editor for a base role with no specialisations", () => {
    render(tab({ ...baseAgent, name: "Base Reviewer" }));

    expect(
      screen.queryByTestId("agent-inherited-prompt"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("agent-effective-prompt"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("agent-specialization-children"),
    ).not.toBeInTheDocument();
  });
});
