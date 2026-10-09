import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../../test/i18n";
import { DispatchProjectsField } from "./dispatch-projects-field";

const projects = [
  { id: "p-ipa", title: "IPA-input", icon: null },
  { id: "p-game", title: "game", icon: null },
];

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: projects, isSuccess: true }),
}));

describe("DispatchProjectsField", () => {
  afterEach(() => cleanup());

  it("reads all projects when the limit is empty", () => {
    renderWithI18n(<DispatchProjectsField wsId="ws-1" value={[]} canEdit onChange={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Projects" }).textContent).toContain("All projects");
  });

  it("shows the limited project and adds another on click", async () => {
    const onChange = vi.fn(async () => {});
    renderWithI18n(<DispatchProjectsField wsId="ws-1" value={["p-ipa"]} canEdit onChange={onChange} />);
    const trigger = screen.getByRole("button", { name: "Projects" });
    expect(trigger.textContent).toContain("IPA-input");
    await userEvent.click(trigger);
    await userEvent.click(await screen.findByText("game"));
    expect(onChange).toHaveBeenCalledWith(["p-ipa", "p-game"]);
  });

  it("lifts the limit from the all-projects row", async () => {
    const onChange = vi.fn(async () => {});
    renderWithI18n(<DispatchProjectsField wsId="ws-1" value={["p-ipa"]} canEdit onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "Projects" }));
    await userEvent.click(await screen.findByText("All projects"));
    expect(onChange).toHaveBeenCalledWith([]);
  });

  it("does not open when the seat cannot be edited", async () => {
    renderWithI18n(<DispatchProjectsField wsId="ws-1" value={[]} canEdit={false} onChange={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Projects" })).toHaveProperty("disabled", true);
  });
});
