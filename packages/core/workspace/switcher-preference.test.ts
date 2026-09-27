import { beforeEach, describe, expect, it } from "vitest";
import {
  arrangeWorkspaces,
  useWorkspaceSwitcherPreferenceStore,
} from "./switcher-preference";

const ws = (...ids: string[]) => ids.map((id) => ({ id }));
const ids = (list: { id: string }[]) => list.map((w) => w.id);

describe("arrangeWorkspaces", () => {
  it("keeps server order when nothing is arranged", () => {
    const { pinned, rest } = arrangeWorkspaces(ws("a", "b", "c"));
    expect(ids(pinned)).toEqual([]);
    expect(ids(rest)).toEqual(["a", "b", "c"]);
  });

  it("puts pinned first and follows the person's order in both groups", () => {
    const { pinned, rest } = arrangeWorkspaces(ws("a", "b", "c", "d"), {
      order: ["d", "c", "b", "a"],
      pinned: ["b", "d"],
    });
    expect(ids(pinned)).toEqual(["d", "b"]);
    expect(ids(rest)).toEqual(["c", "a"]);
  });

  it("appends a newly joined workspace at the end without reshuffling", () => {
    const { rest } = arrangeWorkspaces(ws("new", "a", "b"), {
      order: ["b", "a"],
      pinned: [],
    });
    expect(ids(rest)).toEqual(["b", "a", "new"]);
  });

  it("ignores workspaces the person has left", () => {
    const { pinned, rest } = arrangeWorkspaces(ws("a"), {
      order: ["gone", "a"],
      pinned: ["gone"],
    });
    expect(ids(pinned)).toEqual([]);
    expect(ids(rest)).toEqual(["a"]);
  });
});

describe("useWorkspaceSwitcherPreferenceStore", () => {
  beforeEach(() => {
    useWorkspaceSwitcherPreferenceStore.setState({ byUser: {} });
  });

  it("pins without moving anything, and unpins back into place", () => {
    const store = useWorkspaceSwitcherPreferenceStore.getState();
    store.togglePinned("u1", ["a", "b", "c"], "c");
    let pref = useWorkspaceSwitcherPreferenceStore.getState().byUser.u1!;
    expect(pref).toEqual({ order: ["a", "b", "c"], pinned: ["c"] });
    expect(ids(arrangeWorkspaces(ws("a", "b", "c"), pref).pinned)).toEqual(["c"]);

    store.togglePinned("u1", ["a", "b", "c"], "c");
    pref = useWorkspaceSwitcherPreferenceStore.getState().byUser.u1!;
    expect(ids(arrangeWorkspaces(ws("a", "b", "c"), pref).rest)).toEqual(["a", "b", "c"]);
  });

  it("keeps each person's arrangement separate", () => {
    const store = useWorkspaceSwitcherPreferenceStore.getState();
    store.setArrangement("u1", ["b"], ["a"]);
    store.setArrangement("u2", [], ["a", "b"]);
    const { byUser } = useWorkspaceSwitcherPreferenceStore.getState();
    expect(byUser.u1).toEqual({ order: ["b", "a"], pinned: ["b"] });
    expect(byUser.u2).toEqual({ order: ["a", "b"], pinned: [] });
  });
});
