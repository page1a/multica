// Wiring, accessibility and the named regressions for the routing section.
// The four-state matrix and every malformed-settings case are the canonical
// business of packages/core/workspace/routing-settings.test.ts and are NOT
// re-run through a DOM mount here.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const updateWorkspace = vi.hoisted(() => vi.fn());
const getRoutingHealth = vi.hoisted(() => vi.fn());
const checkRoutingHealth = vi.hoisted(() => vi.fn());
const listRoutingModels = vi.hoisted(() => vi.fn());
const listAgents = vi.hoisted(() => vi.fn());
const member = vi.hoisted(() => ({ role: "owner" as "owner" | "admin" | "member" }));
const workspace = vi.hoisted(() => ({
  current: {
    id: "ws-1",
    name: "Acme",
    slug: "acme",
    settings: {} as Record<string, unknown>,
  },
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: {
      updateWorkspace,
      getRoutingHealth,
      checkRoutingHealth,
      listRoutingModels,
      listAgents,
    },
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => ({
  paths: (await importOriginal<typeof import("@multica/core/paths")>()).paths,
  useCurrentWorkspace: () => workspace.current,
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: member.role, isLoading: false }),
}));

import { parseRoutingHealth } from "@multica/core/workspace/routing-health";
import { DEFAULT_ROUTING_POLICY_PROMPT } from "@multica/core/workspace/routing-policy-prompt";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { RoutingTab } from "./routing-tab";

function render() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // The query provider is nested INSIDE the element rather than passed as
  // `wrapper`: renderWithI18n supplies its own wrapper, and a second one here
  // would replace it, leaving every label an empty string.
  const result = renderWithI18n(
    <QueryClientProvider client={qc}>
      <RoutingTab />
    </QueryClientProvider>,
  );
  return { ...result, qc };
}

/**
 * Wait until the health report has actually landed in the cache.
 *
 * Necessary because the pre-fetch render and the "unreadable report" render
 * are deliberately identical — asserting before the query settles would pass
 * on the empty render whatever the server said.
 */
async function healthSettled(qc: QueryClient) {
  await waitFor(() =>
    expect(qc.getQueryData(workspaceKeys.routingHealth("ws-1"))).toBeDefined(),
  );
}

const HEALTHY = {
  state: "enabled" as const,
  usable: true,
  reason: "",
  retry_after_seconds: 0,
  last_success_at: Math.floor(Date.now() / 1000) - 120,
  last_failure_at: 0,
  model: "gpt-5.6-luna",
  threshold: 0.7,
  gateway_host: "api.openai.com",
  gateway_default_model: "gpt-5.6-mini",
  gateway_configured: true,
  gateway_scope: "deployment" as const,
  gateway_protocol: "openai" as const,
  gateway_key_set: false,
  workspace_key_storable: true,
};

beforeEach(() => {
  updateWorkspace.mockReset();
  getRoutingHealth.mockReset();
  getRoutingHealth.mockResolvedValue(HEALTHY);
  checkRoutingHealth.mockReset();
  checkRoutingHealth.mockResolvedValue(HEALTHY);
  listRoutingModels.mockReset();
  listRoutingModels.mockResolvedValue({ models: [] });
  listAgents.mockReset();
  listAgents.mockResolvedValue([]);
  updateWorkspace.mockImplementation(async (_id: string, body: { settings?: unknown }) => ({
    ...workspace.current,
    settings: body.settings,
  }));
  member.role = "owner";
  workspace.current = { id: "ws-1", name: "Acme", slug: "acme", settings: {} };
});

// Exact names: each role card has its own model, URL and key fields, and a
// loose pattern would match the analysis card's too.
const JUDGE_MODEL = /^(judge model|判断模型)$/i;
const ANALYSIS_MODEL = /^(analysis model|分析模型)$/i;
const JUDGE_KEY = /^api key$/i;

function masterSwitch() {
  return screen.getByRole("switch", { name: /enable automatic dispatch|启用自动选派/i });
}

function chip() {
  return document.querySelector("[data-state]") as HTMLElement | null;
}

describe("RoutingTab", () => {
  it("shows the off state for a workspace that has never configured routing", () => {
    render();
    expect(chip()?.getAttribute("data-state")).toBe("off");
  });

  // The regression this state exists for: somebody flips the switch, walks
  // away, and believes routing is working while the product is unchanged.
  it("shows incomplete — not enabled — when the switch is on with no model", () => {
    workspace.current.settings = { routing: { enabled: true, model: "" } };
    render();
    expect(chip()?.getAttribute("data-state")).toBe("incomplete");
  });

  it("shows enabled once a model is chosen", () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.8 },
    };
    render();
    expect(chip()?.getAttribute("data-state")).toBe("enabled");
  });

  it("fills an empty model from the saved gateway catalog", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "", base_url: "https://gw.example/v1" },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      gateway_scope: "workspace",
      gateway_host: "gw.example",
      gateway_key_set: true,
    });
    listRoutingModels.mockResolvedValue({ models: ["gateway-model", "backup-model"] });

    render();

    await waitFor(() =>
      expect(screen.getByLabelText(JUDGE_MODEL)).toHaveValue(
        "gateway-model",
      ),
    );
    expect(listRoutingModels).toHaveBeenCalledWith("ws-1");
  });

  // The protocol line, not the host line. A workspace that configured Jev saw
  // only "api.typesafe.ai" and a model id, which does not answer the question
  // it had: is this actually a System One model deciding, or a chat model being
  // asked to imitate one? The two mean different things for the confidence the
  // threshold gates on.
  it("names a System One endpoint as the one making the call", async () => {
    workspace.current.settings = {
      routing: {
        enabled: true,
        model: "jev-latest",
        base_url: "https://api.typesafe.ai",
      },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      model: "jev-latest",
      gateway_scope: "workspace",
      gateway_host: "api.typesafe.ai",
      gateway_protocol: "systemone",
      gateway_key_set: true,
    });

    const { qc } = render();
    await healthSettled(qc);

    // The System One explanation paragraph was dropped from the copy; the page
    // still names the workspace's own endpoint as the one receiving the call.
    expect(
      screen.getByText(
        "The model id is sent to this workspace's own endpoint: api.typesafe.ai.",
      ),
    ).toBeInTheDocument();
  });

  it("says nothing about System One on an ordinary chat gateway", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna" },
    };

    const { qc } = render();
    await healthSettled(qc);

    expect(
      screen.queryByText(/measured rather than self-reported/i),
    ).not.toBeInTheDocument();
  });

  it("saves the stored fields under the routing key and leaves the rest of settings alone", async () => {
    workspace.current.settings = { theme: "dark" };
    render();

    // A never-configured workspace starts on the analysis model alone.
    const model = screen.getByLabelText(ANALYSIS_MODEL);
    await userEvent.type(model, "gpt-5.6-luna");

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: Record<string, unknown> },
    ];
    expect(body.settings.theme).toBe("dark");
    // No api_key: the key is write-only and is sent ONLY when somebody typed
    // one. If an ordinary save carried `api_key: ""`, it would clear the
    // stored key every time anybody edited the model.
    expect(body.settings.routing).toEqual({
      enabled: false,
      model: "",
      confidence_threshold: 0.6,
      stale_review_hours: 24,
      base_url: "",
      policy_prompt: "",
      usage_priority: true,
      allow_upshift: false,
      judge_enabled: false,
      analysis: {
        enabled: true,
        model: "gpt-5.6-luna",
        base_url: "",
        source: "api_gateway",
      },
    });
  });

  it("switches each model role on and off on its own", async () => {
    render();
    // The judge model box is greyed out until its role is on.
    expect(screen.getByLabelText(JUDGE_MODEL)).toBeDisabled();
    await userEvent.click(
      screen.getByRole("switch", { name: /use the judge model|启用判断模型/i }),
    );
    expect(screen.getByLabelText(JUDGE_MODEL)).not.toBeDisabled();
    await userEvent.click(
      screen.getByRole("switch", { name: /use the analysis model|启用分析模型/i }),
    );
    expect(document.querySelector("[data-mode]")?.getAttribute("data-mode")).toBe("judge");

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: { judge_enabled: boolean; analysis: { enabled: boolean } } } },
    ];
    expect(body.settings.routing.judge_enabled).toBe(true);
    expect(body.settings.routing.analysis.enabled).toBe(false);
  });

  it("saves the analysis key into the analysis block only", async () => {
    render();
    await userEvent.type(screen.getByLabelText(/^analysis api key$/i), "sk-analysis");
    const saves = screen.getAllByRole("button", { name: /save key|保存 key/i });
    await userEvent.click(saves[0]!);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> & { analysis: Record<string, unknown> } } },
    ];
    expect(body.settings.routing.analysis.api_key).toBe("sk-analysis");
    expect("api_key" in body.settings.routing).toBe(false);
  });

  it("switching routing on writes enabled without inventing a model", async () => {
    render();
    await userEvent.click(masterSwitch());

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: { enabled: boolean; model: string } } },
    ];
    expect(body.settings.routing.enabled).toBe(true);
    expect(body.settings.routing.model).toBe("");
  });

  // The key box is write-only by construction: the server strips the stored
  // key from every response, so there is nothing for this field to render
  // back. These three tests are the whole contract of that box.
  it("never renders a stored key back into the field", async () => {
    getRoutingHealth.mockResolvedValue({ ...HEALTHY, gateway_key_set: true });
    const { qc } = render();
    await healthSettled(qc);
    const key = screen.getByLabelText(JUDGE_KEY) as HTMLInputElement;
    expect(key.value).toBe("");
    expect(key.type).toBe("password");
    // Not even a mask. The placeholder says a key exists; it does not stand
    // in for one, so nothing here can be mistaken for an editable value.
    expect(key.placeholder).not.toMatch(/[*•]/);
  });

  it("sends the typed key only when the save button is pressed", async () => {
    render();
    const key = screen.getByLabelText(JUDGE_KEY);
    await userEvent.type(key, "sk-live-abc");
    // Typing alone must not write: auto-save would store half-typed keys.
    expect(updateWorkspace).not.toHaveBeenCalled();

    // The judge card's button is the second: the analysis card comes first.
    await userEvent.click(screen.getAllByRole("button", { name: /save key|保存 key/i })[1]!);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect(body.settings.routing.api_key).toBe("sk-live-abc");
    // And the box is emptied, so a credential is not left sitting in the DOM.
    await waitFor(() => expect((key as HTMLInputElement).value).toBe(""));
  });

  it("saves the endpoint url with the ordinary fields", async () => {
    render();
    await userEvent.type(
      screen.getByLabelText(/^(endpoint url|端点 url)$/i),
      "https://gw.example/v1",
    );
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect(body.settings.routing.base_url).toBe("https://gw.example/v1");
    expect("api_key" in body.settings.routing).toBe(false);
  });

  it("disables the key field on a deployment that cannot store one", async () => {
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      workspace_key_storable: false,
    });
    const { qc } = render();
    await healthSettled(qc);
    await waitFor(() =>
      expect(screen.getByLabelText(JUDGE_KEY)).toBeDisabled(),
    );
  });

  it("is read-only for a plain member", async () => {
    member.role = "member";
    render();
    // Base UI's switch marks the disabled state with data-disabled rather
    // than the native attribute, so assert the behaviour too: a member who
    // clicks it must not produce a write.
    expect(masterSwitch()).toHaveAttribute("data-disabled");
    expect(screen.getByLabelText(JUDGE_MODEL)).toBeDisabled();

    await userEvent.click(masterSwitch());
    await new Promise((resolve) => setTimeout(resolve, 800));
    expect(updateWorkspace).not.toHaveBeenCalled();
  });

  it("greys out the threshold while routing is off so it cannot look active", () => {
    render();
    expect(screen.getByLabelText(/confidence threshold|置信度阈值/i)).toBeDisabled();
  });

  // The regression the fourth state exists for: a configured workspace whose
  // model is rejected or cooling down. Routing keeps that off every ticket by
  // design, so a green chip here would leave the failure visible nowhere at
  // all outside the server log (DENE-633 review, F2).
  it("shows ineffective, with the reason, when the server reports the model unusable", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "ineffective",
      usable: false,
      reason: "the routing model rejected our credentials (401)",
      retry_after_seconds: 240,
      last_failure_at: Math.floor(Date.now() / 1000),
    });
    render();

    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("ineffective"));
    expect(screen.getByText(/rejected our credentials \(401\)/)).toBeTruthy();
  });

  it("dates the connection while healthy", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    render();
    expect(await screen.findByText(/self-checked|自检/)).toBeTruthy();
    expect(getRoutingHealth).toHaveBeenCalledWith("ws-1");
  });

  // The box holds a bare model id, so "which model is this, on whose
  // endpoint?" has to be answerable from the section itself. The endpoint is
  // deployment config and cannot be edited here — which is why naming it is
  // the whole point (canonical parsing matrix:
  // packages/core/workspace/routing-health.test.ts).
  it("names the endpoint the model id is sent to", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    const { qc } = render();
    await healthSettled(qc);
    expect(await screen.findByText(/api\.openai\.com/)).toBeTruthy();
    expect(screen.getByText(/gpt-5\.6-mini/)).toBeTruthy();
  });

  // The single most common reason routing silently does nothing, and the one
  // a workspace admin cannot fix from this screen.
  it("warns when the deployment has no internal LLM at all", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      gateway_host: "",
      gateway_default_model: "",
      gateway_configured: false,
    });
    const { qc } = render();
    await healthSettled(qc);
    expect(await screen.findByText(/No endpoint is available/)).toBeTruthy();
    expect(screen.queryByText(/MULTICA_LLM_BASE_URL/)).toBeNull();
  });

  it("re-checks on demand and adopts the fresh report", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "ineffective",
      usable: false,
      reason: "the routing model is rate limiting us (429)",
      retry_after_seconds: 120,
    });
    render();
    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("ineffective"));

    await userEvent.click(screen.getByRole("button", { name: /re-check|重新自检/i }));
    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("enabled"));
  });

  it("offers no re-check button to a plain member", async () => {
    member.role = "member";
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    render();
    await waitFor(() => expect(getRoutingHealth).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: /re-check|重新自检/i })).toBeNull();
  });

  // The regression that the first real acceptance run would have hit: the
  // health report cached a moment ago still describes the switched-off
  // workspace, and "not usable" there means "routing is off", not "the model
  // is broken" (DENE-633 review, N1).
  it("does not report a fault while health still describes the settings being replaced", async () => {
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "off",
      usable: false,
      last_success_at: 0,
    });
    const { qc } = render();
    await healthSettled(qc);

    await userEvent.click(masterSwitch());
    await userEvent.type(
      screen.getByLabelText(ANALYSIS_MODEL),
      "gpt-5.6-luna",
    );

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    expect(chip()?.getAttribute("data-state")).toBe("enabled");
  });

  it("re-reads health after a save so the chip follows what was just written", async () => {
    render();
    await waitFor(() => expect(getRoutingHealth).toHaveBeenCalledTimes(1));

    await userEvent.click(masterSwitch());

    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    // Without the invalidation the settings the server derives health from
    // have changed but the cached report has not, and the chip describes the
    // previous configuration until the next poll.
    await waitFor(() => expect(getRoutingHealth).toHaveBeenCalledTimes(2));
  });

  // A response this client cannot read means it does not know — which is
  // neither a fault to report nor a connection to claim. Fed through the real
  // fallback so the test moves if that fallback does.
  it("neither reports a fault nor claims a connection for an unreadable report", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue(parseRoutingHealth({ garbage: true }));
    const { qc } = render();

    await healthSettled(qc);
    expect(chip()?.getAttribute("data-state")).toBe("enabled");
    expect(screen.getByText(/No call made since|还没有发起过调用/)).toBeTruthy();
    expect(screen.queryByText(/Connected ·|已连通 ·/)).toBeNull();
  });

  // Switching off must not keep a red chip about a model the workspace is no
  // longer using: the draft decides whether routing is on at all, and health
  // only splits enabled from ineffective.
  it("returns to off immediately when the switch is turned off", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna", confidence_threshold: 0.7 },
    };
    getRoutingHealth.mockResolvedValue({
      ...HEALTHY,
      state: "ineffective",
      usable: false,
      reason: "unreachable",
    });
    render();
    await waitFor(() => expect(chip()?.getAttribute("data-state")).toBe("ineffective"));

    await userEvent.click(masterSwitch());
    expect(chip()?.getAttribute("data-state")).toBe("off");
  });

  it("shows the default routing prompt, saves an edit, restores the default, and keeps the draft when save fails", async () => {
    const user = userEvent.setup();
    render();
    const box = screen.getByLabelText(/routing prompt/i);
    expect(box).toHaveValue(DEFAULT_ROUTING_POLICY_PROMPT);

    fireEvent.change(box, { target: { value: "Use the weak tier for copy edits." } });
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const saved = updateWorkspace.mock.calls.at(-1)?.[1] as {
      settings: { routing: { policy_prompt: string } };
    };
    expect(saved.settings.routing.policy_prompt).toBe("Use the weak tier for copy edits.");

    updateWorkspace.mockClear();
    await user.click(screen.getByRole("button", { name: /restore the default prompt/i }));
    expect(box).toHaveValue(DEFAULT_ROUTING_POLICY_PROMPT);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const restored = updateWorkspace.mock.calls.at(-1)?.[1] as {
      settings: { routing: { policy_prompt: string } };
    };
    expect(restored.settings.routing.policy_prompt).toBe("");

    updateWorkspace.mockReset();
    updateWorkspace.mockRejectedValue(new Error("offline"));
    fireEvent.change(box, { target: { value: "Keep this draft." } });
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    expect(box).toHaveValue("Keep this draft.");
  });
});

describe("RoutingTab seat order switches", () => {
  it("writes usage priority and upshift into the routing block", async () => {
    workspace.current.settings = {
      routing: { enabled: true, model: "gpt-5.6-luna" },
    };
    render();
    const usage = screen.getByRole("switch", { name: "Prefer ample usage" });
    const upshift = screen.getByRole("switch", { name: "Allow one tier up" });
    expect(usage).toHaveAttribute("data-checked");
    expect(upshift).not.toHaveAttribute("data-checked");

    await userEvent.click(upshift);
    await waitFor(() => expect(updateWorkspace).toHaveBeenCalled());
    const [, body] = updateWorkspace.mock.calls.at(-1) as [
      string,
      { settings: { routing: Record<string, unknown> } },
    ];
    expect(body.settings.routing.usage_priority).toBe(true);
    expect(body.settings.routing.allow_upshift).toBe(true);
  });

  it("greys out upshift while usage priority is off", () => {
    workspace.current.settings = {
      routing: {
        enabled: true,
        model: "gpt-5.6-luna",
        usage_priority: false,
        allow_upshift: true,
      },
    };
    render();
    const upshift = screen.getByRole("switch", { name: "Allow one tier up" });
    expect(upshift).toHaveAttribute("data-disabled");
    expect(upshift).not.toHaveAttribute("data-checked");
  });
});
