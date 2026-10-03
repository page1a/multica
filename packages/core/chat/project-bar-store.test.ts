// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { useChatProjectBarStore } from "./project-bar-store";

describe("useChatProjectBarStore", () => {
  beforeEach(() => {
    localStorage.clear();
    useChatProjectBarStore.setState({ byUser: {} });
  });

  it("removes migrated ids for one user without touching another user's legacy pins", () => {
    useChatProjectBarStore.setState({ byUser: { "user-a": ["p1", "p2"], "user-b": ["p9"] } });
    useChatProjectBarStore.getState().remove("user-a", ["p2"]);

    expect(useChatProjectBarStore.getState().byUser["user-a"]).toEqual(["p1"]);
    expect(useChatProjectBarStore.getState().byUser["user-b"]).toEqual(["p9"]);
  });

  it("forgets a user when all of their legacy pins are migrated", () => {
    useChatProjectBarStore.setState({ byUser: { "user-a": ["p1"] } });
    useChatProjectBarStore.getState().remove("user-a", ["p1"]);
    expect(useChatProjectBarStore.getState().byUser["user-a"]).toBeUndefined();
  });
});
