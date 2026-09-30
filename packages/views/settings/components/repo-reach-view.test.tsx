import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import type { RepoReach } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ settings: () => "/acme/settings" }),
}));
vi.mock("../../navigation", () => ({ useNavigation: () => ({ push: vi.fn() }) }));

import { RepoReachControls, reachTone } from "./repo-reach-view";

const base: RepoReach = {
  repo_url: "https://github.com/acme/api",
  key: "github.com/acme/api",
  provider: "github",
  mode: "none",
  state: "disconnected",
  account_login: "",
  link_id: null,
  last_lookup: { ok: null, at: "", error: "" },
  webhook: "",
  projects: [],
  can_configure: true,
  hint: "",
  next_action: null,
};

describe("reachTone", () => {
  it("follows the server's mode, with an expired token the one red connected repo", () => {
    expect(reachTone({ ...base, mode: "app" })).toBe("ok");
    expect(reachTone({ ...base, mode: "token" })).toBe("ok");
    expect(reachTone({ ...base, mode: "token", next_action: { kind: "replace_token" } })).toBe("bad");
    expect(reachTone({ ...base, mode: "cli" })).toBe("warn");
    expect(reachTone(base)).toBe("bad");
  });
});

describe("RepoReachControls", () => {
  it("lists the projects the server attached, or a dash when there are none", () => {
    const { unmount } = renderWithI18n(
      <RepoReachControls
        reach={{
          ...base,
          mode: "app",
          account_login: "acme",
          projects: [
            { id: "p1", title: "Billing" },
            { id: "p2", title: "Web" },
          ],
        }}
      />,
    );
    expect(screen.getByText("Billing · Web")).toBeInTheDocument();
    expect(screen.getByText("GitHub App · acme")).toBeInTheDocument();
    unmount();

    renderWithI18n(<RepoReachControls reach={base} />);
    expect(screen.getByText("—")).toBeInTheDocument();
  });
});
